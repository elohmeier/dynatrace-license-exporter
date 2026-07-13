package collector

import (
	"testing"

	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

func TestCombinedCollectorRegistersAllLicenseCollectors(t *testing.T) {
	cfg := config.Default()
	cfg.URL = "https://tenant.example.invalid"

	combined := Combine(
		New(cfg, nil, nil, nil),
		NewClusterLicenseExporter(cfg, nil, nil),
		NewContributorExporter(cfg, nil, nil),
	)

	registry := prometheus.NewRegistry()
	if err := registry.Register(combined); err != nil {
		t.Fatalf("register all license collectors: %v", err)
	}
}
