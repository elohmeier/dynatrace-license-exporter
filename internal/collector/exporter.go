package collector

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/elohmeier/dynatrace-license-exporter/internal/billing"
	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	namespace        = "dynatrace"
	billingCollector = "billing_archive"
)

// LicenseClient is the cluster API operation used by the exporter.
type LicenseClient interface {
	LicenseConsumption(ctx context.Context, start, end time.Time) ([]byte, error)
}

// Exporter refreshes billing data in the background and exposes an immutable snapshot.
type Exporter struct {
	cfg    config.Config
	client LicenseClient
	hosts  *hostNameEnricher
	logger *slog.Logger
	now    func() time.Time

	refreshMu       sync.Mutex
	mu              sync.RWMutex
	startOnce       sync.Once
	stopOnce        sync.Once
	schedulerCancel context.CancelFunc
	snapshot        *billing.Snapshot
	state           state
	desc            descriptors
	labelValues     []string
}

type state struct {
	LastAttempt   time.Time
	LastSuccess   time.Time
	LastDuration  time.Duration
	Attempts      uint64
	Errors        uint64
	LastError     string
	LastAttemptOK bool
}

// CacheStatus is returned by readiness and debug endpoints.
type CacheStatus struct {
	Ready                  bool    `json:"ready"`
	Collector              string  `json:"collector"`
	LastAttemptUnix        int64   `json:"last_attempt_unix,omitempty"`
	LastSuccessUnix        int64   `json:"last_success_unix,omitempty"`
	LastDurationSeconds    float64 `json:"last_duration_seconds"`
	CacheAgeSeconds        float64 `json:"cache_age_seconds"`
	MaxStaleSeconds        float64 `json:"max_stale_seconds"`
	Stale                  bool    `json:"stale"`
	Attempts               uint64  `json:"attempts"`
	Errors                 uint64  `json:"errors"`
	LastError              string  `json:"last_error,omitempty"`
	BillingPeriodStartUnix int64   `json:"billing_period_start_unix,omitempty"`
	BillingPeriodEndUnix   int64   `json:"billing_period_end_unix,omitempty"`
	BillingDataAgeSeconds  float64 `json:"billing_data_age_seconds,omitempty"`
	EnvironmentCount       int     `json:"environment_count"`
}

type descriptors struct {
	up                  *prometheus.Desc
	collectorUp         *prometheus.Desc
	refreshTotal        *prometheus.Desc
	refreshErrors       *prometheus.Desc
	refreshDuration     *prometheus.Desc
	lastAttempt         *prometheus.Desc
	lastSuccess         *prometheus.Desc
	cacheAge            *prometheus.Desc
	cacheStale          *prometheus.Desc
	periodStart         *prometheus.Desc
	periodEnd           *prometheus.Desc
	periodDuration      *prometheus.Desc
	billingDataAge      *prometheus.Desc
	environmentInfo     *prometheus.Desc
	estimatedHostUnits  *prometheus.Desc
	hostCount           *prometheus.Desc
	estimatedDEMUnits   *prometheus.Desc
	davisDataUnits      *prometheus.Desc
	rumUsage            *prometheus.Desc
	syntheticExecutions *prometheus.Desc
	syntheticDEMUnits   *prometheus.Desc
	hostEstimatedUnits  *prometheus.Desc
	hostMemoryBytes     *prometheus.Desc
}

// New constructs an exporter without starting its scheduler.
func New(cfg config.Config, client LicenseClient, hostTargets []HostTarget, logger *slog.Logger) *Exporter {
	labelKeys := cfg.LabelKeys()
	labelValues := make([]string, len(labelKeys))
	for i, key := range labelKeys {
		labelValues[i] = cfg.Labels[key]
	}
	labels := func(dynamic ...string) []string {
		return append(append([]string{}, labelKeys...), dynamic...)
	}
	d := descriptors{
		up:                  prometheus.NewDesc(namespace+"_up", "Whether the last Dynatrace billing refresh succeeded.", labels(), nil),
		collectorUp:         prometheus.NewDesc(namespace+"_collector_up", "Whether the named collector succeeded during its last refresh.", labels("collector"), nil),
		refreshTotal:        prometheus.NewDesc(namespace+"_refresh_total", "Total collector refresh attempts.", labels("collector"), nil),
		refreshErrors:       prometheus.NewDesc(namespace+"_refresh_errors_total", "Total failed collector refresh attempts.", labels("collector"), nil),
		refreshDuration:     prometheus.NewDesc(namespace+"_refresh_duration_seconds", "Duration of the last collector refresh.", labels("collector"), nil),
		lastAttempt:         prometheus.NewDesc(namespace+"_cache_last_attempt_timestamp_seconds", "Unix timestamp of the last cache refresh attempt.", labels("collector"), nil),
		lastSuccess:         prometheus.NewDesc(namespace+"_cache_last_success_timestamp_seconds", "Unix timestamp of the last successful cache refresh.", labels("collector"), nil),
		cacheAge:            prometheus.NewDesc(namespace+"_cache_age_seconds", "Age of the last successful in-memory snapshot.", labels("collector"), nil),
		cacheStale:          prometheus.NewDesc(namespace+"_cache_stale", "Whether the in-memory snapshot is missing or stale.", labels("collector"), nil),
		periodStart:         prometheus.NewDesc(namespace+"_license_period_start_timestamp_seconds", "Start of the exported billing interval.", labels(), nil),
		periodEnd:           prometheus.NewDesc(namespace+"_license_period_end_timestamp_seconds", "End of the exported billing interval.", labels(), nil),
		periodDuration:      prometheus.NewDesc(namespace+"_license_period_duration_seconds", "Duration of the exported billing interval.", labels(), nil),
		billingDataAge:      prometheus.NewDesc(namespace+"_license_data_age_seconds", "Age of the end of the exported billing interval.", labels(), nil),
		environmentInfo:     prometheus.NewDesc(namespace+"_license_environment_info", "Known Dynatrace billing environment.", labels("environment_id", "environment"), nil),
		estimatedHostUnits:  prometheus.NewDesc(namespace+"_license_estimated_host_units", "Estimated host units in the billing interval by monitoring mode.", labels("environment_id", "environment", "monitoring_mode"), nil),
		hostCount:           prometheus.NewDesc(namespace+"_license_host_count", "Monitored host count in the billing interval by monitoring mode.", labels("environment_id", "environment", "monitoring_mode"), nil),
		estimatedDEMUnits:   prometheus.NewDesc(namespace+"_license_estimated_dem_units", "Estimated digital experience monitoring units in the billing interval by source.", labels("environment_id", "environment", "source"), nil),
		davisDataUnits:      prometheus.NewDesc(namespace+"_license_davis_data_units", "Davis data units in the billing interval by pool.", labels("environment_id", "environment", "pool"), nil),
		rumUsage:            prometheus.NewDesc(namespace+"_license_rum_usage", "Raw real-user-monitoring billing input in the interval by kind.", labels("environment_id", "environment", "kind"), nil),
		syntheticExecutions: prometheus.NewDesc(namespace+"_license_synthetic_executions", "Synthetic monitor executions in the billing interval.", labels("environment_id", "environment", "test_id", "monitor_type", "location"), nil),
		syntheticDEMUnits:   prometheus.NewDesc(namespace+"_license_synthetic_estimated_dem_units", "Estimated DEM units for a synthetic monitor in the billing interval.", labels("environment_id", "environment", "test_id", "monitor_type"), nil),
		hostEstimatedUnits:  prometheus.NewDesc(namespace+"_license_host_estimated_host_units", "Estimated host units for one host in the billing interval.", labels("environment_id", "environment", "host_id", "host", "monitoring_mode", "host_category", "paas", "has_containers", "premium_log_analytics"), nil),
		hostMemoryBytes:     prometheus.NewDesc(namespace+"_license_host_memory_bytes", "Memory used to estimate host units for one host.", labels("environment_id", "environment", "host_id", "host", "monitoring_mode", "host_category", "paas", "has_containers", "premium_log_analytics"), nil),
	}
	return &Exporter{
		cfg:         cfg,
		client:      client,
		hosts:       newHostNameEnricher(hostTargets, logger),
		logger:      logger,
		now:         time.Now,
		desc:        d,
		labelValues: labelValues,
	}
}

// Start launches an immediate refresh followed by periodic refreshes.
func (e *Exporter) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		e.schedulerCancel = cancel
		go e.schedulerLoop(runCtx)
	})
}

// Stop cancels the background scheduler.
func (e *Exporter) Stop() {
	e.stopOnce.Do(func() {
		if e.schedulerCancel != nil {
			e.schedulerCancel()
		}
	})
}

func (e *Exporter) schedulerLoop(ctx context.Context) {
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

func (e *Exporter) refreshWithLog(ctx context.Context) {
	if err := e.RefreshOnce(ctx); err != nil && e.logger != nil {
		e.logger.Error("billing refresh failed", "collector", billingCollector, "err", err)
	}
}

// RefreshOnce retrieves and atomically installs the latest settled snapshot.
func (e *Exporter) RefreshOnce(ctx context.Context) error {
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

	queryEnd := started.Add(-e.cfg.SettlementDelay)
	queryStart := queryEnd.Add(-e.cfg.BillingLookback)
	archive, err := e.client.LicenseConsumption(ctx, queryStart, queryEnd)
	if err == nil {
		limits := billing.ParseLimits{
			MaxNestedArchiveBytes: e.cfg.MaxNestedArchiveBytes,
			MaxJSONDocumentBytes:  e.cfg.MaxJSONDocumentBytes,
			MaxDocuments:          e.cfg.MaxArchiveDocuments,
		}
		var snapshot *billing.Snapshot
		snapshot, err = billing.LatestSettledSnapshot(archive, queryEnd, e.cfg.EnvironmentNames, limits)
		if err == nil {
			if e.cfg.IncludeHosts {
				e.hosts.enrich(ctx, snapshot)
			}
			e.mu.Lock()
			e.snapshot = snapshot
			e.mu.Unlock()
		}
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
	e.state.LastSuccess = finished
	e.state.LastError = ""
	e.state.LastAttemptOK = true
	return nil
}

// Describe implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range e.allDescriptors() {
		ch <- desc
	}
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	now := e.now().UTC()
	e.mu.RLock()
	st := e.state
	snapshot := e.snapshot
	e.mu.RUnlock()

	up := 0.0
	if st.LastAttemptOK {
		up = 1
	}
	cacheAge := 0.0
	stale := 1.0
	if !st.LastSuccess.IsZero() {
		cacheAge = max(0, now.Sub(st.LastSuccess).Seconds())
		if cacheAge <= e.cfg.MaxStale.Seconds() {
			stale = 0
		}
	}
	e.emit(ch, e.desc.up, prometheus.GaugeValue, up)
	e.emit(ch, e.desc.collectorUp, prometheus.GaugeValue, up, billingCollector)
	e.emit(ch, e.desc.refreshTotal, prometheus.CounterValue, float64(st.Attempts), billingCollector)
	e.emit(ch, e.desc.refreshErrors, prometheus.CounterValue, float64(st.Errors), billingCollector)
	e.emit(ch, e.desc.refreshDuration, prometheus.GaugeValue, st.LastDuration.Seconds(), billingCollector)
	e.emit(ch, e.desc.lastAttempt, prometheus.GaugeValue, unixOrZero(st.LastAttempt), billingCollector)
	e.emit(ch, e.desc.lastSuccess, prometheus.GaugeValue, unixOrZero(st.LastSuccess), billingCollector)
	e.emit(ch, e.desc.cacheAge, prometheus.GaugeValue, cacheAge, billingCollector)
	e.emit(ch, e.desc.cacheStale, prometheus.GaugeValue, stale, billingCollector)
	if snapshot == nil {
		return
	}
	e.collectSnapshot(ch, now, snapshot)
}

func (e *Exporter) collectSnapshot(ch chan<- prometheus.Metric, now time.Time, snapshot *billing.Snapshot) {
	e.emit(ch, e.desc.periodStart, prometheus.GaugeValue, float64(snapshot.PeriodStart.Unix()))
	e.emit(ch, e.desc.periodEnd, prometheus.GaugeValue, float64(snapshot.PeriodEnd.Unix()))
	e.emit(ch, e.desc.periodDuration, prometheus.GaugeValue, snapshot.PeriodEnd.Sub(snapshot.PeriodStart).Seconds())
	e.emit(ch, e.desc.billingDataAge, prometheus.GaugeValue, max(0, now.Sub(snapshot.PeriodEnd).Seconds()))
	for _, env := range snapshot.Environments {
		e.emit(ch, e.desc.environmentInfo, prometheus.GaugeValue, 1, env.ID, env.Name)
		for _, mode := range sortedKeys(env.HostUnitsByMode) {
			e.emit(ch, e.desc.estimatedHostUnits, prometheus.GaugeValue, env.HostUnitsByMode[mode], env.ID, env.Name, mode)
		}
		for _, mode := range sortedKeys(env.HostCountByMode) {
			e.emit(ch, e.desc.hostCount, prometheus.GaugeValue, env.HostCountByMode[mode], env.ID, env.Name, mode)
		}
		for _, source := range sortedKeys(env.DEMBySource) {
			e.emit(ch, e.desc.estimatedDEMUnits, prometheus.GaugeValue, env.DEMBySource[source], env.ID, env.Name, source)
		}
		for _, pool := range sortedKeys(env.DavisDataUnitsByPool) {
			e.emit(ch, e.desc.davisDataUnits, prometheus.GaugeValue, env.DavisDataUnitsByPool[pool], env.ID, env.Name, pool)
		}
		for _, kind := range sortedKeys(env.RUMUsageByKind) {
			e.emit(ch, e.desc.rumUsage, prometheus.GaugeValue, env.RUMUsageByKind[kind], env.ID, env.Name, kind)
		}
		for _, synthetic := range env.Synthetic {
			e.emit(ch, e.desc.syntheticExecutions, prometheus.GaugeValue, synthetic.PublicExecutions, env.ID, env.Name, synthetic.TestID, synthetic.MonitorType, "public")
			e.emit(ch, e.desc.syntheticExecutions, prometheus.GaugeValue, synthetic.PrivateExecutions, env.ID, env.Name, synthetic.TestID, synthetic.MonitorType, "private")
			e.emit(ch, e.desc.syntheticDEMUnits, prometheus.GaugeValue, synthetic.EstimatedDEMUnits, env.ID, env.Name, synthetic.TestID, synthetic.MonitorType)
		}
		if e.cfg.IncludeHosts {
			for _, host := range env.Hosts {
				labels := []string{env.ID, env.Name, host.ID, host.Name, host.MonitoringMode, host.Category, strconv.FormatBool(host.PaaS), strconv.FormatBool(host.HasContainers), strconv.FormatBool(host.PremiumLogAnalytics)}
				e.emit(ch, e.desc.hostEstimatedUnits, prometheus.GaugeValue, host.EstimatedHostUnits, labels...)
				e.emit(ch, e.desc.hostMemoryBytes, prometheus.GaugeValue, float64(host.MemoryBytes), labels...)
			}
		}
	}
}

// Status returns a point-in-time cache and readiness status.
func (e *Exporter) Status(now time.Time) CacheStatus {
	e.mu.RLock()
	st := e.state
	snapshot := e.snapshot
	e.mu.RUnlock()
	status := CacheStatus{
		Collector:           billingCollector,
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
		status.BillingPeriodStartUnix = snapshot.PeriodStart.Unix()
		status.BillingPeriodEndUnix = snapshot.PeriodEnd.Unix()
		status.BillingDataAgeSeconds = max(0, now.Sub(snapshot.PeriodEnd).Seconds())
		status.EnvironmentCount = len(snapshot.Environments)
	}
	status.Ready = snapshot != nil && !status.Stale
	return status
}

// ReadyHandler returns HTTP 200 only while a non-stale snapshot is available.
func (e *Exporter) ReadyHandler(w http.ResponseWriter, _ *http.Request) {
	status := e.Status(e.now().UTC())
	w.Header().Set("Content-Type", "application/json")
	if !status.Ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(status)
}

// DebugCacheHandler returns detailed cache state without credentials or payload data.
func (e *Exporter) DebugCacheHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(e.Status(e.now().UTC()))
}

func (e *Exporter) emit(ch chan<- prometheus.Metric, desc *prometheus.Desc, valueType prometheus.ValueType, value float64, labels ...string) {
	values := append(append([]string{}, e.labelValues...), labels...)
	ch <- prometheus.MustNewConstMetric(desc, valueType, value, values...)
}

func (e *Exporter) allDescriptors() []*prometheus.Desc {
	return []*prometheus.Desc{
		e.desc.up, e.desc.collectorUp, e.desc.refreshTotal, e.desc.refreshErrors, e.desc.refreshDuration,
		e.desc.lastAttempt, e.desc.lastSuccess, e.desc.cacheAge, e.desc.cacheStale, e.desc.periodStart,
		e.desc.periodEnd, e.desc.periodDuration, e.desc.billingDataAge, e.desc.environmentInfo,
		e.desc.estimatedHostUnits, e.desc.hostCount, e.desc.estimatedDEMUnits, e.desc.davisDataUnits,
		e.desc.rumUsage, e.desc.syntheticExecutions, e.desc.syntheticDEMUnits, e.desc.hostEstimatedUnits,
		e.desc.hostMemoryBytes,
	}
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func unixOrZero(value time.Time) float64 {
	return float64(unixIntOrZero(value))
}

func unixIntOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

var _ prometheus.Collector = (*Exporter)(nil)
