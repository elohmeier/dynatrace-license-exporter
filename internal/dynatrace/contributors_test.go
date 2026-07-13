package dynatrace

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueryMetricPagination(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/e/environment-example/api/v2/metrics/query" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Api-Token environment-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if r.URL.Query().Get("metricSelector") != "metric:sum" || r.URL.Query().Get("resolution") != "Inf" || r.URL.Query().Get("to") == "" {
				t.Errorf("first query = %v", r.URL.Query())
			}
			_, _ = fmt.Fprint(w, `{"nextPageKey":"page-two","result":[{"metricId":"metric","data":[{"dimensionMap":{"key":"one"},"values":[null,1.5]}]}]}`)
			return
		}
		if r.URL.Query().Get("nextPageKey") != "page-two" || len(r.URL.Query()) != 1 {
			t.Errorf("next query = %v", r.URL.Query())
		}
		_, _ = fmt.Fprint(w, `{"result":[{"metricId":"metric","data":[{"dimensionMap":{"key":"two"},"values":[2.5]}]}]}`)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Token: "environment-token", MaxDownloadBytes: 1024 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.QueryMetric(context.Background(), "environment-example", "metric:sum", time.Unix(1704067200, 0), time.Unix(1704672000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(rows) != 2 || rows[0].Value != 1.5 || rows[1].Dimensions["key"] != "two" {
		t.Fatalf("requests=%d rows=%+v", requests, rows)
	}
}

func TestEntity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/e/environment-example/api/v2/entities" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("entitySelector"); got != `entityId("HOST-EXAMPLE")` {
			t.Errorf("selector = %q", got)
		}
		if got := r.URL.Query().Get("fields"); got != contributorEntityFields {
			t.Errorf("fields = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"entities":[{"entityId":"HOST-EXAMPLE","type":"HOST","displayName":"host.example.invalid","tags":[{"key":"team","value":"example"}],"managementZones":[{"id":"zone-one","name":"Example Zone"}],"properties":{"monitoringMode":"FULL_STACK"}}]}`)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	entity, err := client.Entity(context.Background(), "environment-example", "HOST-EXAMPLE")
	if err != nil {
		t.Fatal(err)
	}
	if entity == nil || entity.DisplayName != "host.example.invalid" || entity.Properties["monitoringMode"] != "FULL_STACK" {
		t.Fatalf("entity = %+v", entity)
	}
}

func TestKubernetesRelationships(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/e/environment-example/api/v2/entities" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("entitySelector"); got != `entityId("CLOUD_APPLICATION_NAMESPACE-EXAMPLE")` {
			t.Errorf("selector = %q", got)
		}
		if got := r.URL.Query().Get("fields"); got != kubernetesRelationshipFieldsByEntityType["CLOUD_APPLICATION_NAMESPACE"] {
			t.Errorf("fields = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"entities":[{"entityId":"CLOUD_APPLICATION_NAMESPACE-EXAMPLE","type":"CLOUD_APPLICATION_NAMESPACE","displayName":"namespace.example.invalid","toRelationships":{"isClusterOfNamespace":[{"id":"KUBERNETES_CLUSTER-EXAMPLE","type":"KUBERNETES_CLUSTER"}]}}]}`)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Token: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	entities, err := client.KubernetesRelationships(context.Background(), "environment-example", "CLOUD_APPLICATION_NAMESPACE", []string{"CLOUD_APPLICATION_NAMESPACE-EXAMPLE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 || entities[0].DisplayName != "namespace.example.invalid" || len(entities[0].ToRelationships["isClusterOfNamespace"]) != 1 {
		t.Fatalf("entities = %+v", entities)
	}
	entities, err = client.KubernetesRelationships(context.Background(), "environment-example", "EXAMPLE", []string{"EXAMPLE-ONE"})
	if err != nil || len(entities) != 0 {
		t.Fatalf("unsupported type entities=%+v err=%v", entities, err)
	}
}

func TestEntitiesPagination(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/e/environment-example/api/v2/entities" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if requests == 1 {
			if got := r.URL.Query().Get("entitySelector"); got != `entityId("HOST-000000000000002A","HOST-000000000000002B")` {
				t.Errorf("selector = %q", got)
			}
			if got := r.URL.Query().Get("pageSize"); got != "100" {
				t.Errorf("page size = %q", got)
			}
			if got := r.URL.Query().Get("fields"); got != hostEntityFields {
				t.Errorf("fields = %q", got)
			}
			_, _ = fmt.Fprint(w, `{"nextPageKey":"page-two","entities":[{"entityId":"HOST-000000000000002A","type":"HOST","displayName":"host-42.example.invalid","properties":{"hostGroupName":"Synthetic Host Group","networkZone":"synthetic-zone","autoInjection":"ENABLED"},"toRelationships":{"isClusterOfHost":[{"id":"KUBERNETES_CLUSTER-0000000000000001","type":"KUBERNETES_CLUSTER"}]}}]}`)
			return
		}
		if r.URL.Query().Get("nextPageKey") != "page-two" || len(r.URL.Query()) != 1 {
			t.Errorf("next query = %v", r.URL.Query())
		}
		_, _ = fmt.Fprint(w, `{"entities":[{"entityId":"HOST-000000000000002B","type":"HOST","displayName":"host-43.example.invalid"}]}`)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Token: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	entities, err := client.Entities(context.Background(), "environment-example", []string{
		"HOST-000000000000002B", "HOST-000000000000002A", "HOST-000000000000002A",
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(entities) != 2 || entities[0].DisplayName != "host-42.example.invalid" || entities[1].DisplayName != "host-43.example.invalid" {
		t.Fatalf("requests=%d entities=%+v", requests, entities)
	}
	relationships := entities[0].ToRelationships["isClusterOfHost"]
	if len(relationships) != 1 || relationships[0].ID != "KUBERNETES_CLUSTER-0000000000000001" {
		t.Fatalf("relationships = %+v", relationships)
	}
	if entities[0].Properties["hostGroupName"] != "Synthetic Host Group" || entities[0].Properties["networkZone"] != "synthetic-zone" || entities[0].Properties["autoInjection"] != "ENABLED" {
		t.Fatalf("properties = %+v", entities[0].Properties)
	}
}

func TestKubernetesClusters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/e/environment-example/api/v2/entities" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("entitySelector"); got != `entityId("KUBERNETES_CLUSTER-0000000000000001")` {
			t.Errorf("selector = %q", got)
		}
		if got := r.URL.Query().Get("fields"); got != "properties.kubernetesDistribution" {
			t.Errorf("fields = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"entities":[{"entityId":"KUBERNETES_CLUSTER-0000000000000001","type":"KUBERNETES_CLUSTER","displayName":"Example Kubernetes Cluster","properties":{"kubernetesDistribution":"KUBERNETES"}}]}`)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Token: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	clusters, err := client.KubernetesClusters(context.Background(), "environment-example", []string{"KUBERNETES_CLUSTER-0000000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 || clusters[0].DisplayName != "Example Kubernetes Cluster" || clusters[0].Properties["kubernetesDistribution"] != "KUBERNETES" {
		t.Fatalf("clusters = %+v", clusters)
	}
}

func TestEntityIDBatches(t *testing.T) {
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("HOST-%016X", i)
	}
	batches, err := entityIDBatches(ids)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, batch := range batches {
		count += len(batch)
		if got := len(entityIDSelector(batch)); got > maxEntitySelectorLength {
			t.Fatalf("selector length = %d, want <= %d", got, maxEntitySelectorLength)
		}
	}
	if len(batches) < 2 || count != len(ids) {
		t.Fatalf("batches=%d IDs=%d, want multiple batches with %d IDs", len(batches), count, len(ids))
	}
	if _, err := entityIDBatches([]string{""}); err == nil {
		t.Fatal("empty entity ID unexpectedly succeeded")
	}
	if _, err := entityIDBatches([]string{strings.Repeat("x", maxEntitySelectorLength)}); err == nil {
		t.Fatal("oversized entity ID unexpectedly succeeded")
	}
}

func TestContributorAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid query", http.StatusBadRequest)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Token: "token"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.QueryMetric(context.Background(), "environment-example", "bad", time.Now().Add(-time.Hour), time.Now())
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("error = %v", err)
	}
}

func TestContributorAPIResponseLimitAndWindowValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"result":[]}`)
	}))
	defer server.Close()
	client, err := NewClient(Config{BaseURL: server.URL, Token: "token", MaxDownloadBytes: 5})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := client.QueryMetric(context.Background(), "environment-example", "metric", now, now); err == nil || !strings.Contains(err.Error(), "after start") {
		t.Fatalf("window error = %v", err)
	}
	if _, err := client.QueryMetric(context.Background(), "environment-example", "metric", now.Add(-time.Hour), now); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("response limit error = %v", err)
	}
}
