package collector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/elohmeier/dynatrace-license-exporter/internal/dynatrace"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeContributorClient struct {
	mu          sync.Mutex
	err         error
	entityErr   error
	queryCount  int
	entityCount int
	calls       chan struct{}
}

func (f *fakeContributorClient) QueryMetric(_ context.Context, _ string, selector string, _, _ time.Time) ([]dynatrace.MetricDatum, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queryCount++
	if f.calls != nil {
		select {
		case f.calls <- struct{}{}:
		default:
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	switch {
	case strings.Contains(selector, "full_stack_monitoring"):
		return []dynatrace.MetricDatum{{Dimensions: map[string]string{
			"dt.entity.host": "HOST-EXAMPLE", "dt.entity.host.name": "host.example.invalid",
		}, Value: 42}}, nil
	case strings.Contains(selector, "ddu.metrics.byMetric"):
		return []dynatrace.MetricDatum{{Dimensions: map[string]string{"Metric Key": "custom.metric.example"}, Value: 12.5}}, nil
	case strings.Contains(selector, "ddu.traces.byEntity"):
		return []dynatrace.MetricDatum{{Dimensions: map[string]string{
			"dt.entity.monitored_entity": "SERVICE-EXAMPLE", "dt.entity.monitored_entity.name": "service.example.invalid",
		}, Value: 25}}, nil
	default:
		return nil, nil
	}
}

func (f *fakeContributorClient) Entity(_ context.Context, _ string, entityID string) (*dynatrace.Entity, error) {
	f.mu.Lock()
	f.entityCount++
	err := f.entityErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &dynatrace.Entity{
		EntityID: entityID, Type: "EXAMPLE", DisplayName: strings.ToLower(entityID) + ".example.invalid",
		Tags:            []dynatrace.EntityTag{{Key: "team", Value: "example"}, {Key: "ignored", Value: "private"}},
		ManagementZones: []dynatrace.EntityZone{{ID: "zone-example", Name: "Example Zone"}},
		Properties: map[string]any{
			"monitoringMode":             "FULL_STACK",
			"serviceDetectionAttributes": map[string]any{"k8s.namespace.name": "example-namespace"},
		},
	}, nil
}

func TestContributorRefreshCollectAndRetain(t *testing.T) {
	now := time.Date(2024, 1, 8, 0, 0, 0, 0, time.UTC)
	client := &fakeContributorClient{}
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	cfg.Labels = map[string]string{"site": "test"}
	cfg.EntityTagKeys = []string{"team"}
	target := ContributorTarget{Environment: config.Environment{ID: "environment-example", Name: "Example"}, Client: client}
	exporter := NewContributorExporter(cfg, []ContributorTarget{target}, nil)
	exporter.now = func() time.Time { return now }
	if err := exporter.RefreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := exporter.Status(now)
	if !status.Ready || status.ContributorCount != 3 || status.EntityCount != 2 || status.Errors != 0 {
		t.Fatalf("status = %+v", status)
	}
	client.mu.Lock()
	queryCount, entityCount := client.queryCount, client.entityCount
	client.mu.Unlock()
	if queryCount != len(contributorQuerySpecs(cfg.ContributorLimit)) || entityCount != 2 {
		t.Fatalf("queries=%d entities=%d", queryCount, entityCount)
	}

	expected := `
# HELP dynatrace_entity_attribute_info Selected platform attribute on a contributor entity.
# TYPE dynatrace_entity_attribute_info gauge
dynatrace_entity_attribute_info{attribute="kubernetes_namespace",entity_id="HOST-EXAMPLE",environment="Example",environment_id="environment-example",site="test",value="example-namespace"} 1
dynatrace_entity_attribute_info{attribute="monitoring_mode",entity_id="HOST-EXAMPLE",environment="Example",environment_id="environment-example",site="test",value="FULL_STACK"} 1
dynatrace_entity_attribute_info{attribute="kubernetes_namespace",entity_id="SERVICE-EXAMPLE",environment="Example",environment_id="environment-example",site="test",value="example-namespace"} 1
dynatrace_entity_attribute_info{attribute="monitoring_mode",entity_id="SERVICE-EXAMPLE",environment="Example",environment_id="environment-example",site="test",value="FULL_STACK"} 1
# HELP dynatrace_entity_tag_info Allow-listed tag on a contributor entity.
# TYPE dynatrace_entity_tag_info gauge
dynatrace_entity_tag_info{entity_id="HOST-EXAMPLE",environment="Example",environment_id="environment-example",key="team",site="test",value="example"} 1
dynatrace_entity_tag_info{entity_id="SERVICE-EXAMPLE",environment="Example",environment_id="environment-example",key="team",site="test",value="example"} 1
# HELP dynatrace_license_contributor_davis_data_units Davis data units returned by Dynatrace for the rolling contributor window.
# TYPE dynatrace_license_contributor_davis_data_units gauge
dynatrace_license_contributor_davis_data_units{dimension_id="custom.metric.example",dimension_name="custom.metric.example",dimension_type="metric_key",environment="Example",environment_id="environment-example",pool="metrics",site="test"} 12.5
dynatrace_license_contributor_davis_data_units{dimension_id="SERVICE-EXAMPLE",dimension_name="service.example.invalid",dimension_type="entity",environment="Example",environment_id="environment-example",pool="traces",site="test"} 25
# HELP dynatrace_license_contributor_host_units Host-unit usage returned by Dynatrace for the rolling contributor window.
# TYPE dynatrace_license_contributor_host_units gauge
dynatrace_license_contributor_host_units{entity_id="HOST-EXAMPLE",entity_name="host.example.invalid",environment="Example",environment_id="environment-example",monitoring_mode="full_stack",site="test"} 42
`
	if err := testutil.CollectAndCompare(exporter, strings.NewReader(expected),
		"dynatrace_entity_attribute_info", "dynatrace_entity_tag_info",
		"dynatrace_license_contributor_davis_data_units", "dynatrace_license_contributor_host_units",
	); err != nil {
		t.Fatal(err)
	}

	client.mu.Lock()
	client.err = errors.New("temporary query failure")
	client.mu.Unlock()
	if err := exporter.RefreshOnce(context.Background()); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	if status := exporter.Status(now); !status.Ready || status.Errors != 1 || status.ContributorCount != 3 {
		t.Fatalf("last-good snapshot not retained: %+v", status)
	}
}

func TestContributorEntityFailureIsNonFatal(t *testing.T) {
	client := &fakeContributorClient{entityErr: errors.New("metadata unavailable")}
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	target := ContributorTarget{Environment: config.Environment{ID: "environment-example", Name: "Example"}, Client: client}
	exporter := NewContributorExporter(cfg, []ContributorTarget{target}, nil)
	if err := exporter.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("entity enrichment should be optional: %v", err)
	}
	status := exporter.Status(time.Now())
	if status.ContributorCount != 3 || status.EntityCount != 0 || status.Errors != 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestContributorSchedulerLifecycle(t *testing.T) {
	client := &fakeContributorClient{calls: make(chan struct{}, 1)}
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	cfg.ContributorRefreshInterval = 10 * time.Millisecond
	target := ContributorTarget{Environment: config.Environment{ID: "environment-example", Name: "Example"}, Client: client}
	exporter := NewContributorExporter(cfg, []ContributorTarget{target}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exporter.Start(ctx)
	select {
	case <-client.calls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for contributor scheduler")
	}
	exporter.Stop()
	exporter.Stop()
}

func TestContributorRegistryAndDebugEndpoint(t *testing.T) {
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	billing := New(cfg, nil, nil)
	contributors := NewContributorExporter(cfg, nil, nil)
	registry := prometheus.NewRegistry()
	if err := registry.Register(Combine(billing, contributors)); err != nil {
		t.Fatalf("shared self-metric descriptors rejected: %v", err)
	}
	if _, err := registry.Gather(); err != nil {
		t.Fatalf("combined collector gather failed: %v", err)
	}
	recorder := httptest.NewRecorder()
	contributors.DebugCacheHandler(recorder, httptest.NewRequest(http.MethodGet, "/debug/contributors", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"collector":"contributors"`) {
		t.Fatalf("debug response = %d %s", recorder.Code, recorder.Body.String())
	}
}
