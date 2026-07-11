package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var validLabelName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config is the complete exporter configuration after environment and flag parsing.
type Config struct {
	URL                        string
	ConnectAddress             string
	ClusterTokenFile           string
	EnvironmentsFile           string
	EnvironmentNames           map[string]string
	EntityTagKeys              []string
	Labels                     map[string]string
	IncludeHosts               bool
	BindPort                   int
	RequestTimeout             time.Duration
	RefreshInterval            time.Duration
	RefreshTimeout             time.Duration
	BillingLookback            time.Duration
	SettlementDelay            time.Duration
	MaxStale                   time.Duration
	MaxDownloadBytes           int64
	MaxNestedArchiveBytes      int64
	MaxJSONDocumentBytes       int64
	MaxArchiveDocuments        int
	ContributorLookback        time.Duration
	ContributorRefreshInterval time.Duration
	ContributorRefreshTimeout  time.Duration
	ContributorMaxStale        time.Duration
	ContributorLimit           int
	EntityParallelism          int
	InsecureSkipVerify         bool
	CAFile                     string
}

// Default returns conservative defaults for hourly billing collection.
func Default() Config {
	return Config{
		EnvironmentNames:           make(map[string]string),
		Labels:                     make(map[string]string),
		IncludeHosts:               true,
		BindPort:                   9721,
		RequestTimeout:             2 * time.Minute,
		RefreshInterval:            time.Hour,
		RefreshTimeout:             10 * time.Minute,
		BillingLookback:            6 * time.Hour,
		SettlementDelay:            2*time.Hour + 5*time.Minute,
		MaxStale:                   3 * time.Hour,
		MaxDownloadBytes:           64 << 20,
		MaxNestedArchiveBytes:      128 << 20,
		MaxJSONDocumentBytes:       8 << 20,
		MaxArchiveDocuments:        1000,
		ContributorLookback:        7 * 24 * time.Hour,
		ContributorRefreshInterval: 6 * time.Hour,
		ContributorRefreshTimeout:  10 * time.Minute,
		ContributorMaxStale:        18 * time.Hour,
		ContributorLimit:           100,
		EntityParallelism:          5,
	}
}

// FromEnv applies DYNATRACE_* environment variables to the defaults.
func FromEnv() (Config, error) {
	return FromLookupEnv(os.LookupEnv)
}

// FromLookupEnv is FromEnv with an injectable environment for tests.
func FromLookupEnv(lookup func(string) (string, bool)) (Config, error) {
	cfg := Default()
	value := func(key string) string {
		v, _ := lookup(key)
		return strings.TrimSpace(v)
	}
	cfg.URL = value("DYNATRACE_URL")
	cfg.ConnectAddress = value("DYNATRACE_CONNECT_ADDRESS")
	cfg.ClusterTokenFile = value("DYNATRACE_CLUSTER_TOKEN_FILE")
	cfg.EnvironmentsFile = value("DYNATRACE_ENVIRONMENTS_FILE")
	cfg.CAFile = value("DYNATRACE_CA_FILE")
	cfg.EntityTagKeys = ParseCSV(value("DYNATRACE_ENTITY_TAG_KEYS"))
	var err error
	if raw := value("DYNATRACE_ENVIRONMENT_NAMES"); raw != "" {
		cfg.EnvironmentNames, err = ParseAssignments(raw)
		if err != nil {
			return cfg, fmt.Errorf("DYNATRACE_ENVIRONMENT_NAMES: %w", err)
		}
	}
	if raw := value("DYNATRACE_LABELS"); raw != "" {
		cfg.Labels, err = ParseAssignments(raw)
		if err != nil {
			return cfg, fmt.Errorf("DYNATRACE_LABELS: %w", err)
		}
	}
	if raw := value("DYNATRACE_INCLUDE_HOSTS"); raw != "" {
		cfg.IncludeHosts, err = strconv.ParseBool(raw)
		if err != nil {
			return cfg, fmt.Errorf("DYNATRACE_INCLUDE_HOSTS must be a boolean: %w", err)
		}
	}
	if raw := value("DYNATRACE_IGNORE_CERT"); raw != "" {
		cfg.InsecureSkipVerify, err = strconv.ParseBool(raw)
		if err != nil {
			return cfg, fmt.Errorf("DYNATRACE_IGNORE_CERT must be a boolean: %w", err)
		}
	}
	if err := applyInt(value("DYNATRACE_BIND_PORT"), &cfg.BindPort, "DYNATRACE_BIND_PORT"); err != nil {
		return cfg, err
	}
	if err := applyInt(value("DYNATRACE_MAX_ARCHIVE_DOCUMENTS"), &cfg.MaxArchiveDocuments, "DYNATRACE_MAX_ARCHIVE_DOCUMENTS"); err != nil {
		return cfg, err
	}
	if err := applyInt(value("DYNATRACE_CONTRIBUTOR_LIMIT"), &cfg.ContributorLimit, "DYNATRACE_CONTRIBUTOR_LIMIT"); err != nil {
		return cfg, err
	}
	if err := applyInt(value("DYNATRACE_ENTITY_PARALLELISM"), &cfg.EntityParallelism, "DYNATRACE_ENTITY_PARALLELISM"); err != nil {
		return cfg, err
	}
	for _, item := range []struct {
		key    string
		target *int64
	}{
		{"DYNATRACE_MAX_DOWNLOAD_BYTES", &cfg.MaxDownloadBytes},
		{"DYNATRACE_MAX_NESTED_ARCHIVE_BYTES", &cfg.MaxNestedArchiveBytes},
		{"DYNATRACE_MAX_JSON_DOCUMENT_BYTES", &cfg.MaxJSONDocumentBytes},
	} {
		if err := applyInt64(value(item.key), item.target, item.key); err != nil {
			return cfg, err
		}
	}
	for _, item := range []struct {
		key    string
		target *time.Duration
	}{
		{"DYNATRACE_REQUEST_TIMEOUT", &cfg.RequestTimeout},
		{"DYNATRACE_REFRESH_INTERVAL", &cfg.RefreshInterval},
		{"DYNATRACE_REFRESH_TIMEOUT", &cfg.RefreshTimeout},
		{"DYNATRACE_BILLING_LOOKBACK", &cfg.BillingLookback},
		{"DYNATRACE_SETTLEMENT_DELAY", &cfg.SettlementDelay},
		{"DYNATRACE_MAX_STALE", &cfg.MaxStale},
		{"DYNATRACE_CONTRIBUTOR_LOOKBACK", &cfg.ContributorLookback},
		{"DYNATRACE_CONTRIBUTOR_REFRESH_INTERVAL", &cfg.ContributorRefreshInterval},
		{"DYNATRACE_CONTRIBUTOR_REFRESH_TIMEOUT", &cfg.ContributorRefreshTimeout},
		{"DYNATRACE_CONTRIBUTOR_MAX_STALE", &cfg.ContributorMaxStale},
	} {
		if err := applyDuration(value(item.key), item.target, item.key); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// Validate checks values that must remain coherent after CLI overrides.
func (c Config) Validate() error {
	if strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf("Dynatrace URL is required")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("Dynatrace URL must be an absolute http or https URL")
	}
	if c.BindPort < 1 || c.BindPort > 65535 {
		return fmt.Errorf("bind port must be between 1 and 65535")
	}
	for name, value := range map[string]time.Duration{
		"request timeout":              c.RequestTimeout,
		"refresh interval":             c.RefreshInterval,
		"refresh timeout":              c.RefreshTimeout,
		"billing lookback":             c.BillingLookback,
		"max stale":                    c.MaxStale,
		"contributor lookback":         c.ContributorLookback,
		"contributor refresh interval": c.ContributorRefreshInterval,
		"contributor refresh timeout":  c.ContributorRefreshTimeout,
		"contributor max stale":        c.ContributorMaxStale,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be greater than zero", name)
		}
	}
	if c.SettlementDelay < 0 {
		return fmt.Errorf("settlement delay must not be negative")
	}
	if c.BillingLookback <= c.SettlementDelay {
		return fmt.Errorf("billing lookback must be greater than settlement delay")
	}
	if c.MaxDownloadBytes <= 0 || c.MaxNestedArchiveBytes <= 0 || c.MaxJSONDocumentBytes <= 0 || c.MaxArchiveDocuments <= 0 {
		return fmt.Errorf("archive size and document limits must be greater than zero")
	}
	if c.ContributorLimit <= 0 || c.ContributorLimit > 1000 {
		return fmt.Errorf("contributor limit must be between 1 and 1000")
	}
	if c.EntityParallelism <= 0 || c.EntityParallelism > 50 {
		return fmt.Errorf("entity parallelism must be between 1 and 50")
	}
	return ValidateLabels(c.Labels, ReservedLabelNames())
}

// Environment configures one environment-level Metrics and Entity API client.
type Environment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TokenFile string `json:"token_file,omitempty"`
	TokenEnv  string `json:"token_env,omitempty"`
}

type environmentFile struct {
	Environments []Environment `json:"environments"`
}

// LoadEnvironments loads generic contributor targets from the configured JSON file.
func (c Config) LoadEnvironments() ([]Environment, error) {
	if c.EnvironmentsFile == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(c.EnvironmentsFile)
	if err != nil {
		return nil, fmt.Errorf("read environments file: %w", err)
	}
	var file environmentFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("decode environments file: %w", err)
	}
	seen := make(map[string]bool)
	for i := range file.Environments {
		env := &file.Environments[i]
		env.ID = strings.TrimSpace(env.ID)
		env.Name = strings.TrimSpace(env.Name)
		env.TokenFile = strings.TrimSpace(env.TokenFile)
		env.TokenEnv = strings.TrimSpace(env.TokenEnv)
		if env.ID == "" {
			return nil, fmt.Errorf("environment %d has no id", i)
		}
		if seen[env.ID] {
			return nil, fmt.Errorf("duplicate environment id %q", env.ID)
		}
		seen[env.ID] = true
		if env.Name == "" {
			env.Name = env.ID
		}
		if env.TokenFile == "" && env.TokenEnv == "" {
			return nil, fmt.Errorf("environment %q needs token_file or token_env", env.ID)
		}
	}
	return file.Environments, nil
}

// Token resolves an environment API token without accepting inline secrets in JSON.
func (e Environment) Token() (string, error) {
	if e.TokenEnv != "" {
		if token := strings.TrimSpace(os.Getenv(e.TokenEnv)); token != "" {
			return token, nil
		}
	}
	if e.TokenFile != "" {
		raw, err := os.ReadFile(e.TokenFile)
		if err != nil {
			return "", fmt.Errorf("read token file for environment %q: %w", e.ID, err)
		}
		if token := strings.TrimSpace(string(raw)); token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("no token available for environment %q", e.ID)
}

// Token reads a token from DYNATRACE_CLUSTER_TOKEN, DYNATRACE_TOKEN, or a configured file.
func (c Config) Token() (string, error) {
	if token := strings.TrimSpace(os.Getenv("DYNATRACE_CLUSTER_TOKEN")); token != "" {
		return token, nil
	}
	if token := strings.TrimSpace(os.Getenv("DYNATRACE_TOKEN")); token != "" {
		return token, nil
	}
	if c.ClusterTokenFile == "" {
		return "", fmt.Errorf("set DYNATRACE_CLUSTER_TOKEN or DYNATRACE_CLUSTER_TOKEN_FILE")
	}
	raw, err := os.ReadFile(c.ClusterTokenFile)
	if err != nil {
		return "", fmt.Errorf("read cluster token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("cluster token file is empty")
	}
	return token, nil
}

// ParseAssignments parses comma-separated key=value mappings.
func ParseAssignments(raw string) (map[string]string, error) {
	result := make(map[string]string)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("assignment %q must be key=value", part)
		}
		result[key] = value
	}
	return result, nil
}

// ParseCSV parses a comma-separated string into trimmed non-empty values.
func ParseCSV(raw string) []string {
	var values []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// LabelKeys returns stable sorted custom label names.
func (c Config) LabelKeys() []string {
	keys := make([]string, 0, len(c.Labels))
	for key := range c.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ReservedLabelNames returns exporter-owned labels custom labels may not shadow.
func ReservedLabelNames() []string {
	return []string{
		"attribute", "collector", "dimension_id", "dimension_name", "dimension_type", "entity_id", "entity_name", "entity_type",
		"environment", "environment_id", "has_containers", "host", "host_category", "host_id", "key", "kind", "location",
		"management_zone", "monitor_type", "monitoring_mode", "paas", "pool", "premium_log_analytics", "source", "test_id", "value",
	}
}

// ValidateLabels enforces Prometheus naming and prevents collisions.
func ValidateLabels(labels map[string]string, reserved []string) error {
	reservedSet := make(map[string]bool, len(reserved))
	for _, label := range reserved {
		reservedSet[label] = true
	}
	for key := range labels {
		if !validLabelName.MatchString(key) || strings.HasPrefix(key, "__") {
			return fmt.Errorf("invalid Prometheus label name %q", key)
		}
		if reservedSet[key] {
			return fmt.Errorf("custom label %q is reserved by the exporter", key)
		}
	}
	return nil
}

func applyDuration(raw string, target *time.Duration, name string) error {
	if raw == "" {
		return nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s must be a duration: %w", name, err)
	}
	*target = value
	return nil
}

func applyInt(raw string, target *int, name string) error {
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%s must be an integer: %w", name, err)
	}
	*target = value
	return nil
}

func applyInt64(raw string, target *int64, name string) error {
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("%s must be an integer: %w", name, err)
	}
	*target = value
	return nil
}
