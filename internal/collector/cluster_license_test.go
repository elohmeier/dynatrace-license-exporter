package collector

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/elohmeier/dynatrace-license-exporter/internal/dynatrace"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeClusterLicenseClient struct {
	mu      sync.Mutex
	license dynatrace.ClusterLicense
	err     error
	calls   chan struct{}
}

func (f *fakeClusterLicenseClient) ClusterLicense(context.Context) (dynatrace.ClusterLicense, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != nil {
		select {
		case f.calls <- struct{}{}:
		default:
		}
	}
	return f.license, f.err
}

func TestClusterLicenseRefreshCollectAndRetainLastGoodSnapshot(t *testing.T) {
	now := time.Date(2030, 1, 2, 4, 0, 0, 0, time.UTC)
	client := &fakeClusterLicenseClient{license: syntheticClusterLicense()}
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	cfg.Labels = map[string]string{"site": "test"}
	exporter := NewClusterLicenseExporter(cfg, client, nil)
	exporter.now = func() time.Time { return now }

	if err := exporter.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("RefreshOnce: %v", err)
	}
	status := exporter.Status(now)
	if !status.Ready || status.ProductCount != 3 || status.LastBillingTimestampUnix != client.license.LastBillingTime.Unix() {
		t.Fatalf("status = %+v", status)
	}

	expected := `
# HELP dynatrace_license_billed_usage Billed usage reported by the Dynatrace cluster license API.
# TYPE dynatrace_license_billed_usage gauge
dynatrace_license_billed_usage{product="ddu_units",site="test"} 750
dynatrace_license_billed_usage{product="dem_units",site="test"} 1000
dynatrace_license_billed_usage{product="host_units",site="test"} 75
# HELP dynatrace_license_expiration_timestamp_seconds Unix timestamp when the current cluster license expires.
# TYPE dynatrace_license_expiration_timestamp_seconds gauge
dynatrace_license_expiration_timestamp_seconds{site="test"} 1.924991999e+09
# HELP dynatrace_license_last_billing_timestamp_seconds Unix timestamp of the last cluster license billing update.
# TYPE dynatrace_license_last_billing_timestamp_seconds gauge
dynatrace_license_last_billing_timestamp_seconds{site="test"} 1.8935532e+09
# HELP dynatrace_license_quota Contract quota reported by the Dynatrace cluster license API.
# TYPE dynatrace_license_quota gauge
dynatrace_license_quota{product="ddu_units",site="test"} 3000
dynatrace_license_quota{product="dem_units",site="test"} 2000
dynatrace_license_quota{product="host_units",site="test"} 100
# HELP dynatrace_license_remaining Remaining contract quota reported by the Dynatrace cluster license API.
# TYPE dynatrace_license_remaining gauge
dynatrace_license_remaining{product="ddu_units",site="test"} 2250
dynatrace_license_remaining{product="dem_units",site="test"} 1000
dynatrace_license_remaining{product="host_units",site="test"} 25
# HELP dynatrace_license_usage_ratio Fraction of the contract quota used according to the Dynatrace cluster license API.
# TYPE dynatrace_license_usage_ratio gauge
dynatrace_license_usage_ratio{product="ddu_units",site="test"} 0.25
dynatrace_license_usage_ratio{product="dem_units",site="test"} 0.5
dynatrace_license_usage_ratio{product="host_units",site="test"} 0.75
# HELP dynatrace_license_usage_status_info Current Dynatrace quota usage status for a licensed product.
# TYPE dynatrace_license_usage_status_info gauge
dynatrace_license_usage_status_info{product="ddu_units",site="test",status="USING_QUOTA"} 1
dynatrace_license_usage_status_info{product="dem_units",site="test",status="USING_QUOTA"} 1
dynatrace_license_usage_status_info{product="host_units",site="test",status="USING_QUOTA"} 1
`
	metricNames := []string{
		"dynatrace_license_billed_usage", "dynatrace_license_expiration_timestamp_seconds",
		"dynatrace_license_last_billing_timestamp_seconds", "dynatrace_license_quota",
		"dynatrace_license_remaining", "dynatrace_license_usage_ratio",
		"dynatrace_license_usage_status_info",
	}
	if err := testutil.CollectAndCompare(exporter, strings.NewReader(expected), metricNames...); err != nil {
		t.Fatal(err)
	}

	client.mu.Lock()
	client.err = errors.New("synthetic API failure")
	client.mu.Unlock()
	if err := exporter.RefreshOnce(context.Background()); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	status = exporter.Status(now)
	if !status.Ready || status.Errors != 1 || status.LastError == "" {
		t.Fatalf("last-good snapshot was not retained: %+v", status)
	}
	if err := testutil.CollectAndCompare(exporter, strings.NewReader(expected), metricNames...); err != nil {
		t.Fatal(err)
	}
}

func TestClusterLicenseSchedulerDebugAndValidation(t *testing.T) {
	client := &fakeClusterLicenseClient{license: syntheticClusterLicense(), calls: make(chan struct{}, 2)}
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	cfg.RefreshInterval = 10 * time.Millisecond
	exporter := NewClusterLicenseExporter(cfg, client, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exporter.Start(ctx)
	defer exporter.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-client.calls:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for refresh %d", i+1)
		}
	}
	exporter.Stop()
	exporter.Stop()

	recorder := httptest.NewRecorder()
	exporter.DebugCacheHandler(recorder, httptest.NewRequest(http.MethodGet, "/debug/license", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"collector":"cluster_license"`) {
		t.Fatalf("debug response = %d %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{"account", "environment", "contact", "license_key", "licenseKey"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("debug response contains forbidden field %q: %s", forbidden, recorder.Body.String())
		}
	}

	invalid := syntheticClusterLicense()
	invalid.UsageOfHostUnits.Quota = math.NaN()
	if _, err := newClusterLicenseSnapshot(invalid); err == nil {
		t.Fatal("non-finite cluster license unexpectedly succeeded")
	}
}

func syntheticClusterLicense() dynatrace.ClusterLicense {
	return dynatrace.ClusterLicense{
		LicenseExpirationTime: time.Date(2030, 12, 31, 23, 59, 59, 0, time.UTC),
		LastBillingTime:       time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC),
		UsageOfHostUnits: dynatrace.LicenseUsage{
			Quota: 100, Usage: 75, UsagePercent: 75, Remaining: 25, RemainingPercent: 25, UsageStatus: "USING_QUOTA",
		},
		UsageOfDEMUnits: dynatrace.LicenseUsage{
			Quota: 2000, Usage: 1000, UsagePercent: 50, Remaining: 1000, RemainingPercent: 50, UsageStatus: "USING_QUOTA",
		},
		UsageOfDDUUnits: dynatrace.LicenseUsage{
			Quota: 3000, Usage: 750, UsagePercent: 25, Remaining: 2250, RemainingPercent: 75, UsageStatus: "USING_QUOTA",
		},
	}
}
