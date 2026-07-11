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
		if got := r.URL.Query().Get("fields"); got != "properties,tags,managementZones" {
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
