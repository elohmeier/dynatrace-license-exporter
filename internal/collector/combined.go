package collector

import "github.com/prometheus/client_golang/prometheus"

// CombinedCollector registers collectors that intentionally share self-metric
// descriptors as one Prometheus collector.
type CombinedCollector struct {
	collectors []prometheus.Collector
}

// Combine returns one collector that forwards Describe and Collect.
func Combine(collectors ...prometheus.Collector) *CombinedCollector {
	return &CombinedCollector{collectors: append([]prometheus.Collector(nil), collectors...)}
}

// Describe implements prometheus.Collector.
func (c *CombinedCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, collector := range c.collectors {
		collector.Describe(ch)
	}
}

// Collect implements prometheus.Collector.
func (c *CombinedCollector) Collect(ch chan<- prometheus.Metric) {
	for _, collector := range c.collectors {
		collector.Collect(ch)
	}
}

var _ prometheus.Collector = (*CombinedCollector)(nil)
