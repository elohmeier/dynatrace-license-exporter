package dynatrace

import "github.com/prometheus/client_golang/prometheus"

// Metrics instruments requests made to the Dynatrace API.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics creates API request metrics under namespace.
func NewMetrics(namespace string) *Metrics {
	return &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "api",
			Name:      "requests_total",
			Help:      "Total Dynatrace API requests made by the exporter.",
		}, []string{"endpoint", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: "api",
			Name:      "request_duration_seconds",
			Help:      "Dynatrace API request duration by endpoint.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"endpoint"}),
	}
}

// Collectors returns all API instrumentation collectors.
func (m *Metrics) Collectors() []prometheus.Collector {
	if m == nil {
		return nil
	}
	return []prometheus.Collector{m.requests, m.duration}
}

func (m *Metrics) observe(endpoint, code string, seconds float64) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(endpoint, code).Inc()
	m.duration.WithLabelValues(endpoint).Observe(seconds)
}
