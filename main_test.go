package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elohmeier/dynatrace-license-exporter/internal/collector"
	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), app) {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestRunRejectsMissingURL(t *testing.T) {
	t.Setenv("DYNATRACE_URL", "")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "URL") {
		t.Fatalf("run code = %d, stderr = %q", code, stderr.String())
	}
}

func TestRunRejectsMissingEnvironmentToken(t *testing.T) {
	t.Setenv("DYNATRACE_URL", "")
	t.Setenv("DYNATRACE_CLUSTER_TOKEN", "cluster-token")
	t.Setenv("MISSING_ENVIRONMENT_TOKEN", "")
	path := filepath.Join(t.TempDir(), "environments.json")
	if err := os.WriteFile(path, []byte(`{"environments":[{"id":"environment-example","token_env":"MISSING_ENVIRONMENT_TOKEN"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-url", "https://tenant.example.invalid", "-environments-file", path}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stdout.String(), "failed to resolve environment token") {
		t.Fatalf("run code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunRejectsInvalidEnvironmentFile(t *testing.T) {
	t.Setenv("DYNATRACE_URL", "")
	t.Setenv("DYNATRACE_CLUSTER_TOKEN", "cluster-token")
	path := filepath.Join(t.TempDir(), "environments.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"-url", "https://tenant.example.invalid", "-environments-file", path}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "environment configuration") {
		t.Fatalf("run code = %d, stderr = %q", code, stderr.String())
	}
}

func TestMuxHealthAndIndex(t *testing.T) {
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"
	exporter := collector.New(cfg, nil, nil)
	mux := newMux(prometheus.NewRegistry(), exporter, nil)
	for _, path := range []string{"/health", "/healthz"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != "OK\n" {
			t.Fatalf("%s = %d %q", path, recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(recorder.Body.String(), "/metrics") {
		t.Fatalf("index = %q", recorder.Body.String())
	}
	notFound := httptest.NewRecorder()
	mux.ServeHTTP(notFound, httptest.NewRequest(http.MethodGet, "/debug/contributors", nil))
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("disabled contributor endpoint = %d", notFound.Code)
	}
}
