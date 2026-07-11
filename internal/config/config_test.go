package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFromLookupEnv(t *testing.T) {
	values := map[string]string{
		"DYNATRACE_URL":                   "https://tenant.example.invalid",
		"DYNATRACE_ENVIRONMENT_NAMES":     "id-one=Production,id-two=Testing",
		"DYNATRACE_LABELS":                "site=west,stage=test",
		"DYNATRACE_INCLUDE_HOSTS":         "false",
		"DYNATRACE_REFRESH_INTERVAL":      "30m",
		"DYNATRACE_MAX_DOWNLOAD_BYTES":    "12345",
		"DYNATRACE_MAX_ARCHIVE_DOCUMENTS": "42",
		"DYNATRACE_ENVIRONMENTS_FILE":     "/example/environments.json",
		"DYNATRACE_ENTITY_TAG_KEYS":       "team, application",
		"DYNATRACE_CONTRIBUTOR_LIMIT":     "25",
		"DYNATRACE_ENTITY_PARALLELISM":    "3",
	}
	cfg, err := FromLookupEnv(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.URL != values["DYNATRACE_URL"] || cfg.IncludeHosts || cfg.RefreshInterval != 30*time.Minute {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.EnvironmentNames["id-two"] != "Testing" || cfg.Labels["site"] != "west" {
		t.Fatalf("unexpected mappings: %+v %+v", cfg.EnvironmentNames, cfg.Labels)
	}
	if cfg.MaxDownloadBytes != 12345 || cfg.MaxArchiveDocuments != 42 {
		t.Fatalf("unexpected limits: %+v", cfg)
	}
	if cfg.EnvironmentsFile != "/example/environments.json" || cfg.ContributorLimit != 25 || cfg.EntityParallelism != 3 || len(cfg.EntityTagKeys) != 2 {
		t.Fatalf("unexpected contributor config: %+v", cfg)
	}
}

func TestLoadEnvironmentsAndTokens(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "environments.json")
	raw := `{"environments":[{"id":"environment-one","name":"Example","token_file":"` + tokenPath + `"},{"id":"environment-two","token_env":"EXAMPLE_ENV_TOKEN"}]}`
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EXAMPLE_ENV_TOKEN", "environment-token")
	cfg := Default()
	cfg.EnvironmentsFile = configPath
	environments, err := cfg.LoadEnvironments()
	if err != nil {
		t.Fatal(err)
	}
	if len(environments) != 2 || environments[1].Name != "environment-two" {
		t.Fatalf("environments = %+v", environments)
	}
	if token, err := environments[0].Token(); err != nil || token != "file-token" {
		t.Fatalf("file token = %q, %v", token, err)
	}
	if token, err := environments[1].Token(); err != nil || token != "environment-token" {
		t.Fatalf("env token = %q, %v", token, err)
	}
}

func TestLoadEnvironmentsValidation(t *testing.T) {
	for _, raw := range []string{
		`{"environments":[{"name":"missing id","token_env":"TOKEN"}]}`,
		`{"environments":[{"id":"same","token_env":"TOKEN"},{"id":"same","token_env":"TOKEN"}]}`,
		`{"environments":[{"id":"missing-token"}]}`,
		`not-json`,
	} {
		path := filepath.Join(t.TempDir(), "environments.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Default()
		cfg.EnvironmentsFile = path
		if _, err := cfg.LoadEnvironments(); err == nil {
			t.Fatalf("LoadEnvironments(%s) unexpectedly succeeded", raw)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "URL") {
		t.Fatalf("missing URL error = %v", err)
	}
	cfg.URL = "https://tenant.example.invalid"
	cfg.Labels["environment"] = "collision"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved label error = %v", err)
	}
	delete(cfg.Labels, "environment")
	cfg.BillingLookback = cfg.SettlementDelay
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "lookback") {
		t.Fatalf("lookback error = %v", err)
	}
}

func TestParseAssignments(t *testing.T) {
	got, err := ParseAssignments("one=first, two = second")
	if err != nil || got["two"] != "second" {
		t.Fatalf("ParseAssignments = %v, %v", got, err)
	}
	if _, err := ParseAssignments("invalid"); err == nil {
		t.Fatal("invalid assignment unexpectedly succeeded")
	}
}

func TestTokenSources(t *testing.T) {
	t.Setenv("DYNATRACE_TOKEN", "fallback-token")
	t.Setenv("DYNATRACE_CLUSTER_TOKEN", "cluster-token")
	cfg := Default()
	if got, err := cfg.Token(); err != nil || got != "cluster-token" {
		t.Fatalf("cluster token = %q, %v", got, err)
	}
	t.Setenv("DYNATRACE_CLUSTER_TOKEN", "")
	if got, err := cfg.Token(); err != nil || got != "fallback-token" {
		t.Fatalf("fallback token = %q, %v", got, err)
	}
	t.Setenv("DYNATRACE_TOKEN", "")
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.ClusterTokenFile = path
	if got, err := cfg.Token(); err != nil || got != "file-token" {
		t.Fatalf("file token = %q, %v", got, err)
	}
}
