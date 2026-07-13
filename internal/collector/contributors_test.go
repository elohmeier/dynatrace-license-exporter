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
	mu                sync.Mutex
	err               error
	entityErr         error
	relationshipErr   error
	clusterErr        error
	queryCount        int
	entityCount       int
	relationshipCount int
	clusterCount      int
	calls             chan struct{}
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
	case strings.Contains(selector, "ddu.metrics.byEntity"):
		return []dynatrace.MetricDatum{{Dimensions: map[string]string{
			"dt.entity.monitored_entity": "ENVIRONMENT-EXAMPLE", "dt.entity.monitored_entity.name": "environment.example.invalid",
		}, Value: 8}}, nil
	case strings.Contains(selector, "ddu.traces.byEntity"):
		return []dynatrace.MetricDatum{{Dimensions: map[string]string{
			"dt.entity.monitored_entity": "SERVICE-EXAMPLE", "dt.entity.monitored_entity.name": "service.example.invalid",
		}, Value: 25}}, nil
	case strings.Contains(selector, "ddu.events.byEntity"):
		return []dynatrace.MetricDatum{
			{Dimensions: map[string]string{
				"dt.entity.monitored_entity": "CLOUD_APPLICATION-EXAMPLE", "dt.entity.monitored_entity.name": "workload.example.invalid",
			}, Value: 4},
			{Dimensions: map[string]string{
				"dt.entity.monitored_entity.name": "...",
			}, Value: 3},
		}, nil
	case strings.Contains(selector, "ddu.metrics.total"):
		return []dynatrace.MetricDatum{{Value: 10}}, nil
	case strings.Contains(selector, "ddu.log.total"):
		return nil, nil
	case strings.Contains(selector, "ddu.traces.total"):
		return []dynatrace.MetricDatum{{Value: 25}}, nil
	case strings.Contains(selector, "ddu.events.total"):
		return []dynatrace.MetricDatum{{Value: 10}}, nil
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
	entity := &dynatrace.Entity{
		EntityID: entityID, DisplayName: strings.ToLower(entityID) + ".example.invalid",
		Tags:            []dynatrace.EntityTag{{Key: "team", Value: "example"}, {Key: "ignored", Value: "private"}},
		ManagementZones: []dynatrace.EntityZone{{ID: "zone-example", Name: "Example Zone"}},
		Properties:      make(map[string]any),
		ToRelationships: make(map[string][]dynatrace.EntityReference),
	}
	switch entityID {
	case "HOST-EXAMPLE":
		entity.Type = "HOST"
		entity.DisplayName = "host.example.invalid"
		entity.Properties["monitoringMode"] = "FULL_STACK"
	case "ENVIRONMENT-EXAMPLE":
		entity.Type = "ENVIRONMENT"
		entity.DisplayName = "environment.example.invalid"
	case "SERVICE-EXAMPLE":
		entity.Type = "SERVICE"
		entity.DisplayName = "service.example.invalid"
		entity.Properties["serviceDetectionAttributes"] = map[string]any{"k8s.namespace.name": "example-namespace"}
	case "CLOUD_APPLICATION-EXAMPLE":
		entity.Type = "CLOUD_APPLICATION"
		entity.DisplayName = "workload.example.invalid"
	default:
		entity.Type = "EXAMPLE"
	}
	return entity, nil
}

func (f *fakeContributorClient) KubernetesRelationships(_ context.Context, _ string, entityType string, _ []string) ([]dynatrace.Entity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relationshipCount++
	if f.relationshipErr != nil {
		return nil, f.relationshipErr
	}
	switch entityType {
	case "SERVICE":
		return []dynatrace.Entity{{
			EntityID: "SERVICE-EXAMPLE", Type: "SERVICE",
			ToRelationships: map[string][]dynatrace.EntityReference{
				"isNamespaceOfService": {{ID: "CLOUD_APPLICATION_NAMESPACE-EXAMPLE", Type: "CLOUD_APPLICATION_NAMESPACE"}},
			},
		}}, nil
	case "CLOUD_APPLICATION":
		return []dynatrace.Entity{{
			EntityID: "CLOUD_APPLICATION-EXAMPLE", Type: "CLOUD_APPLICATION",
			ToRelationships: map[string][]dynatrace.EntityReference{
				"isNamespaceOfCa": {{ID: "CLOUD_APPLICATION_NAMESPACE-EXAMPLE", Type: "CLOUD_APPLICATION_NAMESPACE"}},
				"isClusterOfCa":   {{ID: "KUBERNETES_CLUSTER-EXAMPLE", Type: "KUBERNETES_CLUSTER"}},
			},
		}}, nil
	case "CLOUD_APPLICATION_NAMESPACE":
		return []dynatrace.Entity{{
			EntityID: "CLOUD_APPLICATION_NAMESPACE-EXAMPLE", Type: "CLOUD_APPLICATION_NAMESPACE",
			DisplayName: "namespace.example.invalid",
			ToRelationships: map[string][]dynatrace.EntityReference{
				"isClusterOfNamespace": {{ID: "KUBERNETES_CLUSTER-EXAMPLE", Type: "KUBERNETES_CLUSTER"}},
			},
		}}, nil
	default:
		return nil, nil
	}
}

func (f *fakeContributorClient) KubernetesClusters(_ context.Context, _ string, _ []string) ([]dynatrace.Entity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clusterCount++
	if f.clusterErr != nil {
		return nil, f.clusterErr
	}
	return []dynatrace.Entity{{
		EntityID: "KUBERNETES_CLUSTER-EXAMPLE", Type: "KUBERNETES_CLUSTER", DisplayName: "cluster.example.invalid",
		Properties: map[string]any{"kubernetesDistribution": "OPENSHIFT"},
	}}, nil
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
	if !status.Ready || status.ContributorCount != 5 || status.EntityCount != 4 || status.Errors != 0 {
		t.Fatalf("status = %+v", status)
	}
	client.mu.Lock()
	queryCount, entityCount, relationshipCount, clusterCount := client.queryCount, client.entityCount, client.relationshipCount, client.clusterCount
	client.mu.Unlock()
	if queryCount != len(contributorQuerySpecs(cfg.ContributorLimit)) || entityCount != 4 || relationshipCount != 4 || clusterCount != 1 {
		t.Fatalf("queries=%d entities=%d relationships=%d clusters=%d", queryCount, entityCount, relationshipCount, clusterCount)
	}

	expected := `
# HELP dynatrace_entity_attribute_info Selected platform attribute on a contributor entity.
# TYPE dynatrace_entity_attribute_info gauge
dynatrace_entity_attribute_info{attribute="monitoring_mode",entity_id="HOST-EXAMPLE",environment="Example",environment_id="environment-example",site="test",value="FULL_STACK"} 1
dynatrace_entity_attribute_info{attribute="kubernetes_namespace",entity_id="SERVICE-EXAMPLE",environment="Example",environment_id="environment-example",site="test",value="example-namespace"} 1
# HELP dynatrace_entity_kubernetes_cluster_info Kubernetes cluster related to a contributor entity.
# TYPE dynatrace_entity_kubernetes_cluster_info gauge
dynatrace_entity_kubernetes_cluster_info{entity_id="CLOUD_APPLICATION-EXAMPLE",environment="Example",environment_id="environment-example",kubernetes_cluster="cluster.example.invalid",kubernetes_cluster_entity_id="KUBERNETES_CLUSTER-EXAMPLE",kubernetes_distribution="OPENSHIFT",site="test"} 1
dynatrace_entity_kubernetes_cluster_info{entity_id="SERVICE-EXAMPLE",environment="Example",environment_id="environment-example",kubernetes_cluster="cluster.example.invalid",kubernetes_cluster_entity_id="KUBERNETES_CLUSTER-EXAMPLE",kubernetes_distribution="OPENSHIFT",site="test"} 1
# HELP dynatrace_entity_kubernetes_namespace_info Kubernetes namespace related to a contributor entity.
# TYPE dynatrace_entity_kubernetes_namespace_info gauge
dynatrace_entity_kubernetes_namespace_info{entity_id="CLOUD_APPLICATION-EXAMPLE",environment="Example",environment_id="environment-example",kubernetes_namespace="namespace.example.invalid",kubernetes_namespace_entity_id="CLOUD_APPLICATION_NAMESPACE-EXAMPLE",site="test"} 1
dynatrace_entity_kubernetes_namespace_info{entity_id="SERVICE-EXAMPLE",environment="Example",environment_id="environment-example",kubernetes_namespace="namespace.example.invalid",kubernetes_namespace_entity_id="CLOUD_APPLICATION_NAMESPACE-EXAMPLE",site="test"} 1
# HELP dynatrace_entity_tag_info Allow-listed tag on a contributor entity.
# TYPE dynatrace_entity_tag_info gauge
dynatrace_entity_tag_info{entity_id="CLOUD_APPLICATION-EXAMPLE",environment="Example",environment_id="environment-example",key="team",site="test",value="example"} 1
dynatrace_entity_tag_info{entity_id="ENVIRONMENT-EXAMPLE",environment="Example",environment_id="environment-example",key="team",site="test",value="example"} 1
dynatrace_entity_tag_info{entity_id="HOST-EXAMPLE",environment="Example",environment_id="environment-example",key="team",site="test",value="example"} 1
dynatrace_entity_tag_info{entity_id="SERVICE-EXAMPLE",environment="Example",environment_id="environment-example",key="team",site="test",value="example"} 1
# HELP dynatrace_license_contributor_davis_data_units Billed Davis data units attributed by Dynatrace to top monitored entities or the explicit unattributed bucket for the rolling contributor window.
# TYPE dynatrace_license_contributor_davis_data_units gauge
dynatrace_license_contributor_davis_data_units{dimension_id="CLOUD_APPLICATION-EXAMPLE",dimension_name="workload.example.invalid",dimension_type="entity",environment="Example",environment_id="environment-example",pool="events",site="test"} 4
dynatrace_license_contributor_davis_data_units{dimension_id="ENVIRONMENT-EXAMPLE",dimension_name="environment.example.invalid",dimension_type="entity",environment="Example",environment_id="environment-example",pool="metrics",site="test"} 8
dynatrace_license_contributor_davis_data_units{dimension_id="SERVICE-EXAMPLE",dimension_name="service.example.invalid",dimension_type="entity",environment="Example",environment_id="environment-example",pool="traces",site="test"} 25
dynatrace_license_contributor_davis_data_units{dimension_id="unattributed",dimension_name="unattributed",dimension_type="unattributed",environment="Example",environment_id="environment-example",pool="events",site="test"} 3
# HELP dynatrace_license_contributor_davis_data_units_coverage_ratio Fraction of the rolling billed pool total represented by exported top entity and unattributed contributor rows.
# TYPE dynatrace_license_contributor_davis_data_units_coverage_ratio gauge
dynatrace_license_contributor_davis_data_units_coverage_ratio{environment="Example",environment_id="environment-example",pool="events",site="test"} 0.7
dynatrace_license_contributor_davis_data_units_coverage_ratio{environment="Example",environment_id="environment-example",pool="log",site="test"} 1
dynatrace_license_contributor_davis_data_units_coverage_ratio{environment="Example",environment_id="environment-example",pool="metrics",site="test"} 0.8
dynatrace_license_contributor_davis_data_units_coverage_ratio{environment="Example",environment_id="environment-example",pool="traces",site="test"} 1
# HELP dynatrace_license_contributor_window_davis_data_units Total billed Davis data units returned by Dynatrace for the rolling contributor window.
# TYPE dynatrace_license_contributor_window_davis_data_units gauge
dynatrace_license_contributor_window_davis_data_units{environment="Example",environment_id="environment-example",pool="events",site="test"} 10
dynatrace_license_contributor_window_davis_data_units{environment="Example",environment_id="environment-example",pool="log",site="test"} 0
dynatrace_license_contributor_window_davis_data_units{environment="Example",environment_id="environment-example",pool="metrics",site="test"} 10
dynatrace_license_contributor_window_davis_data_units{environment="Example",environment_id="environment-example",pool="traces",site="test"} 25
# HELP dynatrace_license_contributor_host_units Host-unit usage returned by Dynatrace for the rolling contributor window.
# TYPE dynatrace_license_contributor_host_units gauge
dynatrace_license_contributor_host_units{entity_id="HOST-EXAMPLE",entity_name="host.example.invalid",environment="Example",environment_id="environment-example",monitoring_mode="full_stack",site="test"} 42
# HELP dynatrace_license_reported_metric_davis_data_units Raw metric Davis data units reported by Dynatrace before host-unit included DDUs are deducted; this is not billed consumption.
# TYPE dynatrace_license_reported_metric_davis_data_units gauge
dynatrace_license_reported_metric_davis_data_units{environment="Example",environment_id="environment-example",metric_key="custom.metric.example",site="test"} 12.5
`
	if err := testutil.CollectAndCompare(exporter, strings.NewReader(expected),
		"dynatrace_entity_attribute_info", "dynatrace_entity_kubernetes_cluster_info",
		"dynatrace_entity_kubernetes_namespace_info", "dynatrace_entity_tag_info",
		"dynatrace_license_contributor_davis_data_units", "dynatrace_license_contributor_davis_data_units_coverage_ratio",
		"dynatrace_license_contributor_window_davis_data_units", "dynatrace_license_contributor_host_units",
		"dynatrace_license_reported_metric_davis_data_units",
	); err != nil {
		t.Fatal(err)
	}

	client.mu.Lock()
	client.err = errors.New("temporary query failure")
	client.mu.Unlock()
	if err := exporter.RefreshOnce(context.Background()); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	if status := exporter.Status(now); !status.Ready || status.Errors != 1 || status.ContributorCount != 5 {
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
	if status.ContributorCount != 5 || status.EntityCount != 0 || status.Errors != 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestContributorKubernetesParentFailureIsNonFatal(t *testing.T) {
	tests := []struct {
		name   string
		client *fakeContributorClient
	}{
		{name: "relationships", client: &fakeContributorClient{relationshipErr: errors.New("relationship metadata unavailable")}},
		{name: "clusters", client: &fakeContributorClient{clusterErr: errors.New("cluster metadata unavailable")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.URL = "https://tenant.example.invalid"
			target := ContributorTarget{Environment: config.Environment{ID: "environment-example", Name: "Example"}, Client: tt.client}
			exporter := NewContributorExporter(cfg, []ContributorTarget{target}, nil)
			if err := exporter.RefreshOnce(context.Background()); err != nil {
				t.Fatalf("Kubernetes parent enrichment should be optional: %v", err)
			}
			if status := exporter.Status(time.Now()); !status.Ready || status.EntityCount != 4 || status.Errors != 0 {
				t.Fatalf("status = %+v", status)
			}
		})
	}
}

func TestContributorDimension(t *testing.T) {
	tests := []struct {
		name              string
		spec              querySpec
		id                string
		displayName       string
		wantDimensionType string
		wantID            string
		wantName          string
	}{
		{
			name: "entity", spec: querySpec{Family: "ddu", DimensionType: "entity", Entity: true},
			id: "SERVICE-EXAMPLE", displayName: "service.example.invalid",
			wantDimensionType: "entity", wantID: "SERVICE-EXAMPLE", wantName: "service.example.invalid",
		},
		{
			name: "missing entity", spec: querySpec{Family: "ddu", DimensionType: "entity", Entity: true},
			displayName: "...", wantDimensionType: unattributedDimension,
			wantID: unattributedDimension, wantName: unattributedDimension,
		},
		{
			name: "name fallback", spec: querySpec{Family: "host_units", DimensionType: "host", Entity: true},
			id: "HOST-EXAMPLE", wantDimensionType: "host", wantID: "HOST-EXAMPLE", wantName: "HOST-EXAMPLE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dimensionType, id, name := contributorDimension(tt.spec, tt.id, tt.displayName)
			if dimensionType != tt.wantDimensionType || id != tt.wantID || name != tt.wantName {
				t.Fatalf("contributorDimension() = %q, %q, %q; want %q, %q, %q", dimensionType, id, name, tt.wantDimensionType, tt.wantID, tt.wantName)
			}
		})
	}
}

func TestCoverageRatio(t *testing.T) {
	tests := []struct {
		name           string
		covered, total float64
		want           float64
	}{
		{name: "partial", covered: 7, total: 10, want: 0.7},
		{name: "complete", covered: 10, total: 10, want: 1},
		{name: "rounding overflow", covered: 10.0001, total: 10, want: 1},
		{name: "empty pool", covered: 0, total: 0, want: 1},
		{name: "invalid positive coverage", covered: 1, total: 0, want: 0},
		{name: "negative coverage", covered: -1, total: 10, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := coverageRatio(tt.covered, tt.total); got != tt.want {
				t.Fatalf("coverageRatio(%v, %v) = %v, want %v", tt.covered, tt.total, got, tt.want)
			}
		})
	}
}

func TestContributorQuerySpecsUseActionableDDUViews(t *testing.T) {
	var rawMetrics, billedEntities, totals int
	for _, spec := range contributorQuerySpecs(17) {
		if strings.Contains(spec.Selector, "byDescription") {
			t.Fatalf("non-actionable description query retained: %s", spec.Selector)
		}
		if strings.Contains(spec.Selector, "limit(17)") && spec.Family == "reported_metric_ddu" {
			rawMetrics++
		}
		if spec.Family == "ddu" && spec.DimensionType == "entity" {
			billedEntities++
		}
		if spec.Family == "ddu_total" {
			totals++
		}
	}
	if rawMetrics != 1 || billedEntities != len(dduPools) || totals != len(dduPools) {
		t.Fatalf("raw metrics=%d billed entity views=%d totals=%d", rawMetrics, billedEntities, totals)
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
	billing := New(cfg, nil, nil, nil)
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
