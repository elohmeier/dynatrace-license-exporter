package collector

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elohmeier/dynatrace-license-exporter/internal/billing"
	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/elohmeier/dynatrace-license-exporter/internal/dynatrace"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeLicenseClient struct {
	mu      sync.Mutex
	archive []byte
	err     error
	start   time.Time
	end     time.Time
	calls   chan struct{}
}

func (f *fakeLicenseClient) LicenseConsumption(_ context.Context, start, end time.Time) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.start = start
	f.end = end
	if f.calls != nil {
		select {
		case f.calls <- struct{}{}:
		default:
		}
	}
	return f.archive, f.err
}

type fakeHostEntityClient struct {
	mu       sync.Mutex
	entities []dynatrace.Entity
	err      error
	calls    int
	envID    string
	ids      []string
}

func (f *fakeHostEntityClient) Entities(_ context.Context, environmentID string, entityIDs []string) ([]dynatrace.Entity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.envID = environmentID
	f.ids = append([]string(nil), entityIDs...)
	return append([]dynatrace.Entity(nil), f.entities...), f.err
}

func TestRefreshCollectAndRetainLastGoodSnapshot(t *testing.T) {
	now := time.Date(2024, 1, 1, 3, 0, 0, 0, time.UTC)
	doc := billing.Document{
		TimeFrameStart: now.Add(-4 * time.Hour).UnixMilli(),
		TimeFrameEnd:   now.Add(-3 * time.Hour).UnixMilli(),
		EnvironmentBillingEntries: []billing.EnvironmentEntry{{
			EnvironmentUUID: "environment-example",
			HostUsages: []billing.HostUsage{{
				OSIId:           "42",
				HostName:        "archive-host-42.example.invalid",
				HostMemoryBytes: 8 * 1024 * 1024 * 1024,
			}},
			SyntheticBillingUsage: []billing.SyntheticUsage{{
				MonitorTypeID:    1,
				TestID:           "SYNTHETIC-EXAMPLE",
				PublicExecutions: 10,
			}},
			DavisDataUnits: []billing.DavisDataUnit{{Pool: "Metrics", Total: 12.5}},
		}},
	}
	client := &fakeLicenseClient{archive: archiveForDocument(t, doc)}
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	cfg.Labels = map[string]string{"site": "test"}
	cfg.EnvironmentNames = map[string]string{"environment-example": "Example"}
	hostClient := &fakeHostEntityClient{entities: []dynatrace.Entity{{
		EntityID: "HOST-000000000000002A", DisplayName: "host-42.example.invalid", Type: "HOST",
	}}}
	hostTargets := []HostTarget{{Environment: config.Environment{ID: "environment-example", Name: "Example"}, Client: hostClient}}
	exporter := New(cfg, client, hostTargets, nil)
	exporter.now = func() time.Time { return now }

	if err := exporter.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("RefreshOnce: %v", err)
	}
	wantEnd := now.Add(-cfg.SettlementDelay)
	if !client.start.Equal(wantEnd.Add(-cfg.BillingLookback)) || !client.end.Equal(wantEnd) {
		t.Fatalf("query interval = %s..%s", client.start, client.end)
	}
	hostClient.mu.Lock()
	if hostClient.calls != 1 || hostClient.envID != "environment-example" || len(hostClient.ids) != 1 || hostClient.ids[0] != "HOST-000000000000002A" {
		t.Fatalf("host lookup calls=%d environment=%q ids=%v", hostClient.calls, hostClient.envID, hostClient.ids)
	}
	hostClient.mu.Unlock()
	status := exporter.Status(now)
	if !status.Ready || status.EnvironmentCount != 1 || status.BillingPeriodEndUnix != doc.TimeFrameEnd/1000 {
		t.Fatalf("unexpected status: %+v", status)
	}
	expected := `
# HELP dynatrace_license_davis_data_units Davis data units in the billing interval by pool.
# TYPE dynatrace_license_davis_data_units gauge
dynatrace_license_davis_data_units{environment="Example",environment_id="environment-example",pool="Metrics",site="test"} 12.5
# HELP dynatrace_license_estimated_host_units Estimated host units in the billing interval by monitoring mode.
# TYPE dynatrace_license_estimated_host_units gauge
dynatrace_license_estimated_host_units{environment="Example",environment_id="environment-example",monitoring_mode="full_stack",site="test"} 0.5
dynatrace_license_estimated_host_units{environment="Example",environment_id="environment-example",monitoring_mode="infrastructure",site="test"} 0
# HELP dynatrace_license_host_estimated_host_units Estimated host units for one host in the billing interval.
# TYPE dynatrace_license_host_estimated_host_units gauge
dynatrace_license_host_estimated_host_units{environment="Example",environment_id="environment-example",has_containers="false",host="host-42.example.invalid",host_category="",host_id="HOST-000000000000002A",monitoring_mode="full_stack",paas="false",premium_log_analytics="false",site="test"} 0.5
`
	if err := testutil.CollectAndCompare(exporter, strings.NewReader(expected),
		"dynatrace_license_davis_data_units",
		"dynatrace_license_estimated_host_units",
		"dynatrace_license_host_estimated_host_units",
	); err != nil {
		t.Fatal(err)
	}

	client.err = errors.New("temporary API failure")
	if err := exporter.RefreshOnce(context.Background()); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	status = exporter.Status(now)
	if !status.Ready || status.Errors != 1 || status.LastError == "" {
		t.Fatalf("last-good snapshot was not retained: %+v", status)
	}
	if err := testutil.CollectAndCompare(exporter, strings.NewReader(expected),
		"dynatrace_license_davis_data_units",
		"dynatrace_license_estimated_host_units",
		"dynatrace_license_host_estimated_host_units",
	); err != nil {
		t.Fatal(err)
	}
}

func TestHostEnrichmentFailureRetainsNameAndPublishesFreshBilling(t *testing.T) {
	now := time.Date(2024, 1, 1, 3, 0, 0, 0, time.UTC)
	doc := billing.Document{
		TimeFrameStart: now.Add(-4 * time.Hour).UnixMilli(),
		TimeFrameEnd:   now.Add(-3 * time.Hour).UnixMilli(),
		EnvironmentBillingEntries: []billing.EnvironmentEntry{{
			EnvironmentUUID: "environment-example",
			HostUsages: []billing.HostUsage{{
				OSIId: "7", HostMemoryBytes: 8 * 1024 * 1024 * 1024,
			}},
		}},
	}
	licenseClient := &fakeLicenseClient{archive: archiveForDocument(t, doc)}
	hostClient := &fakeHostEntityClient{entities: []dynatrace.Entity{{
		EntityID: "HOST-0000000000000007", DisplayName: "host-seven.example.invalid", Type: "HOST",
	}}}
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	cfg.EnvironmentNames = map[string]string{"environment-example": "Example"}
	exporter := New(cfg, licenseClient, []HostTarget{{
		Environment: config.Environment{ID: "environment-example", Name: "Example"}, Client: hostClient,
	}}, nil)
	exporter.now = func() time.Time { return now }
	if err := exporter.RefreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	doc.EnvironmentBillingEntries[0].HostUsages[0].HostMemoryBytes = 16 * 1024 * 1024 * 1024
	licenseClient.mu.Lock()
	licenseClient.archive = archiveForDocument(t, doc)
	licenseClient.mu.Unlock()
	hostClient.mu.Lock()
	hostClient.err = errors.New("synthetic entity API failure")
	hostClient.entities = nil
	hostClient.mu.Unlock()
	if err := exporter.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("optional host enrichment failed billing refresh: %v", err)
	}
	if status := exporter.Status(now); status.Errors != 0 || !status.Ready {
		t.Fatalf("status = %+v", status)
	}

	expected := `
# HELP dynatrace_license_host_estimated_host_units Estimated host units for one host in the billing interval.
# TYPE dynatrace_license_host_estimated_host_units gauge
dynatrace_license_host_estimated_host_units{environment="Example",environment_id="environment-example",has_containers="false",host="host-seven.example.invalid",host_category="",host_id="HOST-0000000000000007",monitoring_mode="full_stack",paas="false",premium_log_analytics="false"} 1
`
	if err := testutil.CollectAndCompare(exporter, strings.NewReader(expected), "dynatrace_license_host_estimated_host_units"); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessAndDisabledHostMetrics(t *testing.T) {
	now := time.Date(2024, 1, 1, 3, 0, 0, 0, time.UTC)
	doc := billing.Document{
		TimeFrameStart: now.Add(-4 * time.Hour).UnixMilli(),
		TimeFrameEnd:   now.Add(-3 * time.Hour).UnixMilli(),
		EnvironmentBillingEntries: []billing.EnvironmentEntry{{
			EnvironmentUUID: "environment-example",
			HostUsages:      []billing.HostUsage{{OSIId: "1", HostMemoryBytes: 1024}},
		}},
	}
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	cfg.IncludeHosts = false
	hostClient := &fakeHostEntityClient{}
	exporter := New(cfg, &fakeLicenseClient{archive: archiveForDocument(t, doc)}, []HostTarget{{
		Environment: config.Environment{ID: "environment-example", Name: "Example"}, Client: hostClient,
	}}, nil)
	exporter.now = func() time.Time { return now }

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	before := httptest.NewRecorder()
	exporter.ReadyHandler(before, request)
	if before.Code != http.StatusServiceUnavailable {
		t.Fatalf("initial readiness = %d", before.Code)
	}
	if err := exporter.RefreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := httptest.NewRecorder()
	exporter.ReadyHandler(after, request)
	if after.Code != http.StatusOK {
		t.Fatalf("readiness after refresh = %d: %s", after.Code, after.Body.String())
	}
	if n := testutil.CollectAndCount(exporter, "dynatrace_license_host_estimated_host_units"); n != 0 {
		t.Fatalf("per-host metric count = %d, want 0", n)
	}
	hostClient.mu.Lock()
	if hostClient.calls != 0 {
		t.Fatalf("host entity calls = %d, want 0", hostClient.calls)
	}
	hostClient.mu.Unlock()
}

func TestSchedulerAndDebugHandler(t *testing.T) {
	now := time.Now().UTC()
	doc := billing.Document{
		TimeFrameStart: now.Add(-4 * time.Hour).UnixMilli(),
		TimeFrameEnd:   now.Add(-3 * time.Hour).UnixMilli(),
		EnvironmentBillingEntries: []billing.EnvironmentEntry{{
			EnvironmentUUID: "environment-example",
		}},
	}
	client := &fakeLicenseClient{archive: archiveForDocument(t, doc), calls: make(chan struct{}, 2)}
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	cfg.RefreshInterval = 10 * time.Millisecond
	exporter := New(cfg, client, nil, nil)
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
	exporter.DebugCacheHandler(recorder, httptest.NewRequest(http.MethodGet, "/debug/cache", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"collector":"billing_archive"`) {
		t.Fatalf("debug response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func archiveForDocument(t *testing.T, doc billing.Document) []byte {
	t.Helper()
	var nested bytes.Buffer
	nestedWriter := zip.NewWriter(&nested)
	entry, err := nestedWriter.Create("billing.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(entry).Encode(doc); err != nil {
		t.Fatal(err)
	}
	if err := nestedWriter.Close(); err != nil {
		t.Fatal(err)
	}
	var outer bytes.Buffer
	outerWriter := zip.NewWriter(&outer)
	entry, err = outerWriter.Create("billingRecords_example.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(nested.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := outerWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return outer.Bytes()
}
