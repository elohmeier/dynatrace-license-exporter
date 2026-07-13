package dynatrace

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestClusterLicenseRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != clusterLicensePath {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Api-Token synthetic-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("accept = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
  "licenseExpirationTime":"2030-12-31T23:59:59Z",
  "lastBillingTime":"2030-01-02T03:00:00Z",
  "usageOfHostUnits":{"quota":100,"usage":75,"usagePercent":75,"remaining":25,"remainingPercent":25,"usageStatus":"USING_QUOTA"},
  "usageOfDemUnits":{"quota":2000,"usage":1000,"usagePercent":50,"remaining":1000,"remainingPercent":50,"usageStatus":"USING_QUOTA"},
  "usageOfDduUnits":{"quota":3000,"usage":750,"usagePercent":25,"remaining":2250,"remainingPercent":75,"usageStatus":"USING_QUOTA"}
}`)
	}))
	defer server.Close()

	metrics := NewMetrics("test")
	client, err := NewClient(Config{BaseURL: server.URL, Token: "synthetic-token", Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ClusterLicense(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.UsageOfHostUnits.Quota != 100 || got.UsageOfDEMUnits.Remaining != 1000 || got.UsageOfDDUUnits.UsagePercent != 25 {
		t.Fatalf("cluster license = %+v", got)
	}
	wantBilling := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
	if !got.LastBillingTime.Equal(wantBilling) {
		t.Fatalf("last billing time = %s", got.LastBillingTime)
	}
	if got := testutil.ToFloat64(metrics.requests.WithLabelValues("cluster_license", "200")); got != 1 {
		t.Fatalf("request counter = %v, want 1", got)
	}
}
