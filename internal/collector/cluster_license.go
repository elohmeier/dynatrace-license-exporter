package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/elohmeier/dynatrace-license-exporter/internal/dynatrace"
	"github.com/prometheus/client_golang/prometheus"
)

const clusterLicenseCollector = "cluster_license"

// ClusterLicenseClient is the cluster API operation used by the license
// summary collector.
type ClusterLicenseClient interface {
	ClusterLicense(ctx context.Context) (dynatrace.ClusterLicense, error)
}

type clusterLicenseSnapshot struct {
	LastBillingTime time.Time
	ExpirationTime  time.Time
	Products        []licenseProductSnapshot
}

type licenseProductSnapshot struct {
	Name        string
	Quota       float64
	Usage       float64
	Remaining   float64
	UsageRatio  float64
	UsageStatus string
}

// ClusterLicenseExporter independently caches the current cluster-wide
// contract quota and billed usage summary.
type ClusterLicenseExporter struct {
	cfg    config.Config
	client ClusterLicenseClient
	logger *slog.Logger
	now    func() time.Time

	refreshMu       sync.Mutex
	mu              sync.RWMutex
	startOnce       sync.Once
	stopOnce        sync.Once
	schedulerCancel context.CancelFunc
	snapshot        *clusterLicenseSnapshot
	state           state
	desc            clusterLicenseDescriptors
	labelValues     []string
}

type clusterLicenseDescriptors struct {
	collectorUp     *prometheus.Desc
	refreshTotal    *prometheus.Desc
	refreshErrors   *prometheus.Desc
	refreshDuration *prometheus.Desc
	lastAttempt     *prometheus.Desc
	lastSuccess     *prometheus.Desc
	cacheAge        *prometheus.Desc
	cacheStale      *prometheus.Desc
	quota           *prometheus.Desc
	billedUsage     *prometheus.Desc
	remaining       *prometheus.Desc
	usageRatio      *prometheus.Desc
	usageStatus     *prometheus.Desc
	lastBilling     *prometheus.Desc
	expiration      *prometheus.Desc
}

// ClusterLicenseStatus is returned by the cluster-license debug endpoint. It
// intentionally contains no account, environment, contact, or license-key
// data.
type ClusterLicenseStatus struct {
	Ready                    bool    `json:"ready"`
	Collector                string  `json:"collector"`
	LastAttemptUnix          int64   `json:"last_attempt_unix,omitempty"`
	LastSuccessUnix          int64   `json:"last_success_unix,omitempty"`
	LastDurationSeconds      float64 `json:"last_duration_seconds"`
	CacheAgeSeconds          float64 `json:"cache_age_seconds"`
	MaxStaleSeconds          float64 `json:"max_stale_seconds"`
	Stale                    bool    `json:"stale"`
	Attempts                 uint64  `json:"attempts"`
	Errors                   uint64  `json:"errors"`
	LastError                string  `json:"last_error,omitempty"`
	LastBillingTimestampUnix int64   `json:"last_billing_timestamp_unix,omitempty"`
	LicenseExpirationUnix    int64   `json:"license_expiration_unix,omitempty"`
	ProductCount             int     `json:"product_count"`
}

// NewClusterLicenseExporter creates the independent cluster license summary
// collector.
func NewClusterLicenseExporter(cfg config.Config, client ClusterLicenseClient, logger *slog.Logger) *ClusterLicenseExporter {
	labelKeys := cfg.LabelKeys()
	labelValues := make([]string, len(labelKeys))
	for i, key := range labelKeys {
		labelValues[i] = cfg.Labels[key]
	}
	labels := func(dynamic ...string) []string {
		return append(append([]string{}, labelKeys...), dynamic...)
	}
	return &ClusterLicenseExporter{
		cfg:         cfg,
		client:      client,
		logger:      logger,
		now:         time.Now,
		labelValues: labelValues,
		desc: clusterLicenseDescriptors{
			collectorUp:     prometheus.NewDesc(namespace+"_collector_up", "Whether the named collector succeeded during its last refresh.", labels("collector"), nil),
			refreshTotal:    prometheus.NewDesc(namespace+"_refresh_total", "Total collector refresh attempts.", labels("collector"), nil),
			refreshErrors:   prometheus.NewDesc(namespace+"_refresh_errors_total", "Total failed collector refresh attempts.", labels("collector"), nil),
			refreshDuration: prometheus.NewDesc(namespace+"_refresh_duration_seconds", "Duration of the last collector refresh.", labels("collector"), nil),
			lastAttempt:     prometheus.NewDesc(namespace+"_cache_last_attempt_timestamp_seconds", "Unix timestamp of the last cache refresh attempt.", labels("collector"), nil),
			lastSuccess:     prometheus.NewDesc(namespace+"_cache_last_success_timestamp_seconds", "Unix timestamp of the last successful cache refresh.", labels("collector"), nil),
			cacheAge:        prometheus.NewDesc(namespace+"_cache_age_seconds", "Age of the last successful in-memory snapshot.", labels("collector"), nil),
			cacheStale:      prometheus.NewDesc(namespace+"_cache_stale", "Whether the in-memory snapshot is missing or stale.", labels("collector"), nil),
			quota:           prometheus.NewDesc(namespace+"_license_quota", "Contract quota reported by the Dynatrace cluster license API.", labels("product"), nil),
			billedUsage:     prometheus.NewDesc(namespace+"_license_billed_usage", "Billed usage reported by the Dynatrace cluster license API.", labels("product"), nil),
			remaining:       prometheus.NewDesc(namespace+"_license_remaining", "Remaining contract quota reported by the Dynatrace cluster license API.", labels("product"), nil),
			usageRatio:      prometheus.NewDesc(namespace+"_license_usage_ratio", "Fraction of the contract quota used according to the Dynatrace cluster license API.", labels("product"), nil),
			usageStatus:     prometheus.NewDesc(namespace+"_license_usage_status_info", "Current Dynatrace quota usage status for a licensed product.", labels("product", "status"), nil),
			lastBilling:     prometheus.NewDesc(namespace+"_license_last_billing_timestamp_seconds", "Unix timestamp of the last cluster license billing update.", labels(), nil),
			expiration:      prometheus.NewDesc(namespace+"_license_expiration_timestamp_seconds", "Unix timestamp when the current cluster license expires.", labels(), nil),
		},
	}
}

// Start launches an immediate license-summary refresh and its independent
// scheduler.
func (e *ClusterLicenseExporter) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		e.schedulerCancel = cancel
		go e.schedulerLoop(runCtx)
	})
}

// Stop cancels the cluster-license scheduler.
func (e *ClusterLicenseExporter) Stop() {
	e.stopOnce.Do(func() {
		if e.schedulerCancel != nil {
			e.schedulerCancel()
		}
	})
}

func (e *ClusterLicenseExporter) schedulerLoop(ctx context.Context) {
	e.refreshWithLog(ctx)
	ticker := time.NewTicker(e.cfg.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.refreshWithLog(ctx)
		}
	}
}

func (e *ClusterLicenseExporter) refreshWithLog(ctx context.Context) {
	if err := e.RefreshOnce(ctx); err != nil && e.logger != nil {
		e.logger.Error("cluster license refresh failed", "collector", clusterLicenseCollector, "err", err)
	}
}

// RefreshOnce atomically replaces the cluster license summary after a
// successful request and validation.
func (e *ClusterLicenseExporter) RefreshOnce(ctx context.Context) error {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, e.cfg.RefreshTimeout)
	defer cancel()

	started := e.now().UTC()
	e.mu.Lock()
	e.state.Attempts++
	e.state.LastAttempt = started
	e.mu.Unlock()

	license, err := e.client.ClusterLicense(ctx)
	var snapshot *clusterLicenseSnapshot
	if err == nil {
		snapshot, err = newClusterLicenseSnapshot(license)
	}
	finished := e.now().UTC()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.LastDuration = finished.Sub(started)
	if err != nil {
		e.state.Errors++
		e.state.LastError = err.Error()
		e.state.LastAttemptOK = false
		return err
	}
	e.snapshot = snapshot
	e.state.LastSuccess = finished
	e.state.LastError = ""
	e.state.LastAttemptOK = true
	return nil
}

func newClusterLicenseSnapshot(license dynatrace.ClusterLicense) (*clusterLicenseSnapshot, error) {
	products := []licenseProductSnapshot{
		productSnapshot("host_units", license.UsageOfHostUnits),
		productSnapshot("dem_units", license.UsageOfDEMUnits),
		productSnapshot("ddu_units", license.UsageOfDDUUnits),
	}
	for _, product := range products {
		for name, value := range map[string]float64{
			"quota": product.Quota, "usage": product.Usage, "remaining": product.Remaining, "usage ratio": product.UsageRatio,
		} {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("cluster license %s %s is not finite", product.Name, name)
			}
		}
	}
	return &clusterLicenseSnapshot{
		LastBillingTime: license.LastBillingTime,
		ExpirationTime:  license.LicenseExpirationTime,
		Products:        products,
	}, nil
}

func productSnapshot(name string, usage dynatrace.LicenseUsage) licenseProductSnapshot {
	return licenseProductSnapshot{
		Name:        name,
		Quota:       usage.Quota,
		Usage:       usage.Usage,
		Remaining:   usage.Remaining,
		UsageRatio:  usage.UsagePercent / 100,
		UsageStatus: usage.UsageStatus,
	}
}

// Status returns a point-in-time cache status without sensitive license data.
func (e *ClusterLicenseExporter) Status(now time.Time) ClusterLicenseStatus {
	e.mu.RLock()
	st := e.state
	snapshot := e.snapshot
	e.mu.RUnlock()
	status := ClusterLicenseStatus{
		Collector:           clusterLicenseCollector,
		LastAttemptUnix:     unixIntOrZero(st.LastAttempt),
		LastSuccessUnix:     unixIntOrZero(st.LastSuccess),
		LastDurationSeconds: st.LastDuration.Seconds(),
		MaxStaleSeconds:     e.cfg.MaxStale.Seconds(),
		Attempts:            st.Attempts,
		Errors:              st.Errors,
		LastError:           st.LastError,
		Stale:               true,
	}
	if !st.LastSuccess.IsZero() {
		status.CacheAgeSeconds = max(0, now.Sub(st.LastSuccess).Seconds())
		status.Stale = status.CacheAgeSeconds > e.cfg.MaxStale.Seconds()
	}
	if snapshot != nil {
		status.LastBillingTimestampUnix = unixIntOrZero(snapshot.LastBillingTime)
		status.LicenseExpirationUnix = unixIntOrZero(snapshot.ExpirationTime)
		status.ProductCount = len(snapshot.Products)
	}
	status.Ready = snapshot != nil && !status.Stale
	return status
}

// DebugCacheHandler returns cluster-license cache state without account,
// environment, contact, or license-key data.
func (e *ClusterLicenseExporter) DebugCacheHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(e.Status(e.now().UTC()))
}

// Describe implements prometheus.Collector.
func (e *ClusterLicenseExporter) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range e.allDescriptors() {
		ch <- desc
	}
}

// Collect implements prometheus.Collector.
func (e *ClusterLicenseExporter) Collect(ch chan<- prometheus.Metric) {
	now := e.now().UTC()
	e.mu.RLock()
	st := e.state
	snapshot := e.snapshot
	e.mu.RUnlock()

	up := 0.0
	if st.LastAttemptOK {
		up = 1
	}
	age, stale := 0.0, 1.0
	if !st.LastSuccess.IsZero() {
		age = max(0, now.Sub(st.LastSuccess).Seconds())
		if age <= e.cfg.MaxStale.Seconds() {
			stale = 0
		}
	}
	e.emit(ch, e.desc.collectorUp, prometheus.GaugeValue, up, clusterLicenseCollector)
	e.emit(ch, e.desc.refreshTotal, prometheus.CounterValue, float64(st.Attempts), clusterLicenseCollector)
	e.emit(ch, e.desc.refreshErrors, prometheus.CounterValue, float64(st.Errors), clusterLicenseCollector)
	e.emit(ch, e.desc.refreshDuration, prometheus.GaugeValue, st.LastDuration.Seconds(), clusterLicenseCollector)
	e.emit(ch, e.desc.lastAttempt, prometheus.GaugeValue, unixOrZero(st.LastAttempt), clusterLicenseCollector)
	e.emit(ch, e.desc.lastSuccess, prometheus.GaugeValue, unixOrZero(st.LastSuccess), clusterLicenseCollector)
	e.emit(ch, e.desc.cacheAge, prometheus.GaugeValue, age, clusterLicenseCollector)
	e.emit(ch, e.desc.cacheStale, prometheus.GaugeValue, stale, clusterLicenseCollector)
	if snapshot == nil {
		return
	}
	if !snapshot.LastBillingTime.IsZero() {
		e.emit(ch, e.desc.lastBilling, prometheus.GaugeValue, float64(snapshot.LastBillingTime.Unix()))
	}
	if !snapshot.ExpirationTime.IsZero() {
		e.emit(ch, e.desc.expiration, prometheus.GaugeValue, float64(snapshot.ExpirationTime.Unix()))
	}
	for _, product := range snapshot.Products {
		e.emit(ch, e.desc.quota, prometheus.GaugeValue, product.Quota, product.Name)
		e.emit(ch, e.desc.billedUsage, prometheus.GaugeValue, product.Usage, product.Name)
		e.emit(ch, e.desc.remaining, prometheus.GaugeValue, product.Remaining, product.Name)
		e.emit(ch, e.desc.usageRatio, prometheus.GaugeValue, product.UsageRatio, product.Name)
		e.emit(ch, e.desc.usageStatus, prometheus.GaugeValue, 1, product.Name, product.UsageStatus)
	}
}

func (e *ClusterLicenseExporter) emit(ch chan<- prometheus.Metric, desc *prometheus.Desc, valueType prometheus.ValueType, value float64, labels ...string) {
	values := append(append([]string{}, e.labelValues...), labels...)
	ch <- prometheus.MustNewConstMetric(desc, valueType, value, values...)
}

func (e *ClusterLicenseExporter) allDescriptors() []*prometheus.Desc {
	return []*prometheus.Desc{
		e.desc.collectorUp, e.desc.refreshTotal, e.desc.refreshErrors, e.desc.refreshDuration,
		e.desc.lastAttempt, e.desc.lastSuccess, e.desc.cacheAge, e.desc.cacheStale,
		e.desc.quota, e.desc.billedUsage, e.desc.remaining, e.desc.usageRatio,
		e.desc.usageStatus, e.desc.lastBilling, e.desc.expiration,
	}
}

var _ prometheus.Collector = (*ClusterLicenseExporter)(nil)
