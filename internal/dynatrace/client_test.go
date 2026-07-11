package dynatrace

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestLicenseConsumptionRequest(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != licenseConsumptionPath {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("startTs") != "1704067200000" || r.URL.Query().Get("endTs") != "1704074400000" {
			t.Errorf("query = %v", r.URL.Query())
		}
		if got := r.Header.Get("Authorization"); got != "Api-Token test-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/octet-stream" {
			t.Errorf("accept = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "test-exporter/1.0.0" {
			t.Errorf("user agent = %q", got)
		}
		_, _ = io.WriteString(w, "archive")
	}))
	defer server.Close()
	metrics := NewMetrics("test")
	client, err := NewClient(Config{BaseURL: server.URL, Token: "test-token", UserAgent: "test-exporter/1.0.0", MaxDownloadBytes: 1024, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.LicenseConsumption(context.Background(), start, end)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "archive" {
		t.Fatalf("body = %q", got)
	}
	if got := testutil.ToFloat64(metrics.requests.WithLabelValues("license_consumption", "200")); got != 1 {
		t.Fatalf("request counter = %v, want 1", got)
	}
}

func TestConnectAddressPreservesHostAndSNI(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "tenant.example.invalid" {
			t.Errorf("host = %q", r.Host)
		}
		if r.TLS == nil || r.TLS.ServerName != "tenant.example.invalid" {
			t.Errorf("SNI = %q", r.TLS.ServerName)
		}
		_, _ = io.WriteString(w, "zip")
	}))
	defer server.Close()
	client, err := NewClient(Config{
		BaseURL:            "https://tenant.example.invalid",
		ConnectAddress:     server.Listener.Addr().String(),
		Token:              "test-token",
		InsecureSkipVerify: true,
		MaxDownloadBytes:   1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Hour)
	if _, err := client.LicenseConsumption(context.Background(), start, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestLicenseConsumptionLimitsAndStatus(t *testing.T) {
	t.Run("body limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "20")
			_, _ = io.WriteString(w, strings.Repeat("x", 20))
		}))
		defer server.Close()
		client, err := NewClient(Config{BaseURL: server.URL, Token: "token", MaxDownloadBytes: 10})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.LicenseConsumption(context.Background(), time.Now().Add(-time.Hour), time.Now())
		if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
			t.Fatalf("limit error = %v", err)
		}
	})
	t.Run("HTTP status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not authorized", http.StatusForbidden)
		}))
		defer server.Close()
		client, err := NewClient(Config{BaseURL: server.URL, Token: "token"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.LicenseConsumption(context.Background(), time.Now().Add(-time.Hour), time.Now())
		if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "not authorized") {
			t.Fatalf("status error = %v", err)
		}
	})
}

func TestNewClientValidation(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{BaseURL: "ftp://example.invalid", Token: "token"},
		{BaseURL: "https://example.invalid", Token: ""},
		{BaseURL: "https://example.invalid", Token: "token", ConnectAddress: "missing-port"},
	} {
		if _, err := NewClient(cfg); err == nil {
			t.Fatalf("NewClient(%+v) unexpectedly succeeded", cfg)
		}
	}
	invalidCA := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(invalidCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(Config{BaseURL: "https://example.invalid", Token: "token", CAFile: invalidCA}); err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Fatalf("invalid CA error = %v", err)
	}
}
