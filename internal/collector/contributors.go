package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/elohmeier/dynatrace-license-exporter/internal/dynatrace"
	"github.com/prometheus/client_golang/prometheus"
)

const contributorsCollector = "contributors"

// ContributorClient is the environment API surface used by the collector.
type ContributorClient interface {
	QueryMetric(ctx context.Context, environmentID, selector string, from, to time.Time) ([]dynatrace.MetricDatum, error)
	Entity(ctx context.Context, environmentID, entityID string) (*dynatrace.Entity, error)
}

// ContributorTarget binds a generic environment configuration to its API client.
type ContributorTarget struct {
	Environment config.Environment
	Client      ContributorClient
}

type contributorSnapshot struct {
	WindowStart  time.Time
	WindowEnd    time.Time
	Contributors []contributor
	Entities     map[string]entityMetadata
}

type contributor struct {
	Family        string
	Subtype       string
	EnvironmentID string
	Environment   string
	DimensionType string
	DimensionID   string
	DimensionName string
	EntityID      string
	Value         float64
}

type entityMetadata struct {
	EnvironmentID   string
	Environment     string
	ID              string
	Name            string
	Type            string
	ManagementZones []string
	Tags            map[string][]string
	Attributes      map[string]string
}

type querySpec struct {
	Family        string
	Subtype       string
	DimensionType string
	DimensionKey  string
	NameKey       string
	Entity        bool
	Selector      string
}

// ContributorExporter periodically caches rolling top-contributor queries.
type ContributorExporter struct {
	cfg     config.Config
	targets []ContributorTarget
	logger  *slog.Logger
	now     func() time.Time

	refreshMu       sync.Mutex
	mu              sync.RWMutex
	startOnce       sync.Once
	stopOnce        sync.Once
	schedulerCancel context.CancelFunc
	snapshot        *contributorSnapshot
	state           state
	desc            contributorDescriptors
	labelValues     []string
}

type contributorDescriptors struct {
	collectorUp         *prometheus.Desc
	refreshTotal        *prometheus.Desc
	refreshErrors       *prometheus.Desc
	refreshDuration     *prometheus.Desc
	lastAttempt         *prometheus.Desc
	lastSuccess         *prometheus.Desc
	cacheAge            *prometheus.Desc
	cacheStale          *prometheus.Desc
	windowStart         *prometheus.Desc
	windowEnd           *prometheus.Desc
	windowDuration      *prometheus.Desc
	hostUnits           *prometheus.Desc
	demUnits            *prometheus.Desc
	davisDataUnits      *prometheus.Desc
	entityInfo          *prometheus.Desc
	entityZoneInfo      *prometheus.Desc
	entityTagInfo       *prometheus.Desc
	entityAttributeInfo *prometheus.Desc
}

// ContributorStatus is returned by the contributor debug endpoint.
type ContributorStatus struct {
	Ready               bool    `json:"ready"`
	Collector           string  `json:"collector"`
	LastAttemptUnix     int64   `json:"last_attempt_unix,omitempty"`
	LastSuccessUnix     int64   `json:"last_success_unix,omitempty"`
	LastDurationSeconds float64 `json:"last_duration_seconds"`
	CacheAgeSeconds     float64 `json:"cache_age_seconds"`
	MaxStaleSeconds     float64 `json:"max_stale_seconds"`
	Stale               bool    `json:"stale"`
	Attempts            uint64  `json:"attempts"`
	Errors              uint64  `json:"errors"`
	LastError           string  `json:"last_error,omitempty"`
	WindowStartUnix     int64   `json:"window_start_unix,omitempty"`
	WindowEndUnix       int64   `json:"window_end_unix,omitempty"`
	EnvironmentCount    int     `json:"environment_count"`
	ContributorCount    int     `json:"contributor_count"`
	EntityCount         int     `json:"entity_count"`
}

// NewContributorExporter creates the optional environment contributor collector.
func NewContributorExporter(cfg config.Config, targets []ContributorTarget, logger *slog.Logger) *ContributorExporter {
	labelKeys := cfg.LabelKeys()
	labelValues := make([]string, len(labelKeys))
	for i, key := range labelKeys {
		labelValues[i] = cfg.Labels[key]
	}
	labels := func(dynamic ...string) []string {
		return append(append([]string{}, labelKeys...), dynamic...)
	}
	return &ContributorExporter{
		cfg:         cfg,
		targets:     append([]ContributorTarget(nil), targets...),
		logger:      logger,
		now:         time.Now,
		labelValues: labelValues,
		desc: contributorDescriptors{
			collectorUp:         prometheus.NewDesc(namespace+"_collector_up", "Whether the named collector succeeded during its last refresh.", labels("collector"), nil),
			refreshTotal:        prometheus.NewDesc(namespace+"_refresh_total", "Total collector refresh attempts.", labels("collector"), nil),
			refreshErrors:       prometheus.NewDesc(namespace+"_refresh_errors_total", "Total failed collector refresh attempts.", labels("collector"), nil),
			refreshDuration:     prometheus.NewDesc(namespace+"_refresh_duration_seconds", "Duration of the last collector refresh.", labels("collector"), nil),
			lastAttempt:         prometheus.NewDesc(namespace+"_cache_last_attempt_timestamp_seconds", "Unix timestamp of the last cache refresh attempt.", labels("collector"), nil),
			lastSuccess:         prometheus.NewDesc(namespace+"_cache_last_success_timestamp_seconds", "Unix timestamp of the last successful cache refresh.", labels("collector"), nil),
			cacheAge:            prometheus.NewDesc(namespace+"_cache_age_seconds", "Age of the last successful in-memory snapshot.", labels("collector"), nil),
			cacheStale:          prometheus.NewDesc(namespace+"_cache_stale", "Whether the in-memory snapshot is missing or stale.", labels("collector"), nil),
			windowStart:         prometheus.NewDesc(namespace+"_license_contributor_window_start_timestamp_seconds", "Start of the rolling contributor query window.", labels(), nil),
			windowEnd:           prometheus.NewDesc(namespace+"_license_contributor_window_end_timestamp_seconds", "End of the rolling contributor query window.", labels(), nil),
			windowDuration:      prometheus.NewDesc(namespace+"_license_contributor_window_seconds", "Duration of the rolling contributor query window.", labels(), nil),
			hostUnits:           prometheus.NewDesc(namespace+"_license_contributor_host_units", "Host-unit usage returned by Dynatrace for the rolling contributor window.", labels("environment_id", "environment", "monitoring_mode", "entity_id", "entity_name"), nil),
			demUnits:            prometheus.NewDesc(namespace+"_license_contributor_dem_units", "DEM usage returned by Dynatrace for the rolling contributor window.", labels("environment_id", "environment", "source", "entity_id", "entity_name"), nil),
			davisDataUnits:      prometheus.NewDesc(namespace+"_license_contributor_davis_data_units", "Davis data units returned by Dynatrace for the rolling contributor window.", labels("environment_id", "environment", "pool", "dimension_type", "dimension_id", "dimension_name"), nil),
			entityInfo:          prometheus.NewDesc(namespace+"_entity_info", "Metadata for an entity referenced by contributor metrics.", labels("environment_id", "environment", "entity_id", "entity_name", "entity_type"), nil),
			entityZoneInfo:      prometheus.NewDesc(namespace+"_entity_management_zone_info", "Management-zone membership for a contributor entity.", labels("environment_id", "environment", "entity_id", "management_zone"), nil),
			entityTagInfo:       prometheus.NewDesc(namespace+"_entity_tag_info", "Allow-listed tag on a contributor entity.", labels("environment_id", "environment", "entity_id", "key", "value"), nil),
			entityAttributeInfo: prometheus.NewDesc(namespace+"_entity_attribute_info", "Selected platform attribute on a contributor entity.", labels("environment_id", "environment", "entity_id", "attribute", "value"), nil),
		},
	}
}

// Start launches an immediate contributor refresh and its independent scheduler.
func (e *ContributorExporter) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.startOnce.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		e.schedulerCancel = cancel
		go e.schedulerLoop(runCtx)
	})
}

// Stop cancels the contributor scheduler.
func (e *ContributorExporter) Stop() {
	e.stopOnce.Do(func() {
		if e.schedulerCancel != nil {
			e.schedulerCancel()
		}
	})
}

func (e *ContributorExporter) schedulerLoop(ctx context.Context) {
	e.refreshWithLog(ctx)
	ticker := time.NewTicker(e.cfg.ContributorRefreshInterval)
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

func (e *ContributorExporter) refreshWithLog(ctx context.Context) {
	if err := e.RefreshOnce(ctx); err != nil && e.logger != nil {
		e.logger.Error("contributor refresh failed", "collector", contributorsCollector, "err", err)
	}
}

// RefreshOnce atomically replaces all contributor and entity data after success.
func (e *ContributorExporter) RefreshOnce(ctx context.Context) error {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, e.cfg.ContributorRefreshTimeout)
	defer cancel()
	started := e.now().UTC()
	e.mu.Lock()
	e.state.Attempts++
	e.state.LastAttempt = started
	e.mu.Unlock()

	snapshot, err := e.buildSnapshot(ctx, started.Add(-e.cfg.ContributorLookback), started)
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

func (e *ContributorExporter) buildSnapshot(ctx context.Context, from, to time.Time) (*contributorSnapshot, error) {
	snapshot := &contributorSnapshot{WindowStart: from, WindowEnd: to, Entities: make(map[string]entityMetadata)}
	for _, target := range e.targets {
		entityIDs := make(map[string]bool)
		for _, spec := range contributorQuerySpecs(e.cfg.ContributorLimit) {
			data, err := target.Client.QueryMetric(ctx, target.Environment.ID, spec.Selector, from, to)
			if err != nil {
				return nil, fmt.Errorf("environment %q query %s: %w", target.Environment.ID, spec.DimensionType, err)
			}
			for _, datum := range data {
				id := datum.Dimensions[spec.DimensionKey]
				name := datum.Dimensions[spec.NameKey]
				if name == "" {
					name = id
				}
				row := contributor{
					Family: spec.Family, Subtype: spec.Subtype, EnvironmentID: target.Environment.ID,
					Environment: target.Environment.Name, DimensionType: spec.DimensionType,
					DimensionID: id, DimensionName: name, Value: datum.Value,
				}
				if spec.Entity && id != "" {
					row.EntityID = id
					entityIDs[id] = true
				}
				snapshot.Contributors = append(snapshot.Contributors, row)
			}
		}
		for key, metadata := range e.fetchEntities(ctx, target, entityIDs) {
			snapshot.Entities[key] = metadata
		}
	}
	sort.Slice(snapshot.Contributors, func(i, j int) bool {
		a, b := snapshot.Contributors[i], snapshot.Contributors[j]
		return a.EnvironmentID+a.Family+a.Subtype+a.DimensionType+a.DimensionID < b.EnvironmentID+b.Family+b.Subtype+b.DimensionType+b.DimensionID
	})
	return snapshot, nil
}

func (e *ContributorExporter) fetchEntities(ctx context.Context, target ContributorTarget, ids map[string]bool) map[string]entityMetadata {
	result := make(map[string]entityMetadata)
	var mu sync.Mutex
	sem := make(chan struct{}, e.cfg.EntityParallelism)
	var wg sync.WaitGroup
	for id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			entity, err := target.Client.Entity(ctx, target.Environment.ID, id)
			if err != nil {
				if e.logger != nil {
					e.logger.Warn("entity enrichment failed", "environment_id", target.Environment.ID, "entity_id", id, "err", err)
				}
				return
			}
			if entity == nil {
				return
			}
			if entity.EntityID == "" {
				entity.EntityID = id
			}
			metadata := summarizeEntity(target.Environment, *entity, e.cfg.EntityTagKeys)
			mu.Lock()
			result[target.Environment.ID+":"+id] = metadata
			mu.Unlock()
		}()
	}
	wg.Wait()
	return result
}

func summarizeEntity(environment config.Environment, entity dynatrace.Entity, tagKeys []string) entityMetadata {
	metadata := entityMetadata{
		EnvironmentID: environment.ID, Environment: environment.Name, ID: entity.EntityID,
		Name: entity.DisplayName, Type: entity.Type, Tags: make(map[string][]string), Attributes: make(map[string]string),
	}
	allowed := make(map[string]bool, len(tagKeys))
	for _, key := range tagKeys {
		allowed[key] = true
	}
	for _, tag := range entity.Tags {
		if allowed[tag.Key] {
			value := tag.Value
			if value == "" {
				value = tag.Key
			}
			metadata.Tags[tag.Key] = append(metadata.Tags[tag.Key], value)
		}
	}
	for _, zone := range entity.ManagementZones {
		if zone.Name != "" {
			metadata.ManagementZones = append(metadata.ManagementZones, zone.Name)
		}
	}
	for key, value := range selectedEntityAttributes(entity.Properties) {
		metadata.Attributes[key] = value
	}
	return metadata
}

func selectedEntityAttributes(properties map[string]any) map[string]string {
	attributes := make(map[string]string)
	copyStringProperty(attributes, "monitoring_mode", properties, "monitoringMode")
	copyStringProperty(attributes, "kubernetes_namespace", properties, "kubernetesNamespace")
	copyStringProperty(attributes, "cloud_resource_group", properties, "azureResourceGroupName")
	if serviceAttributes, ok := properties["serviceDetectionAttributes"].(map[string]any); ok && attributes["kubernetes_namespace"] == "" {
		copyStringProperty(attributes, "kubernetes_namespace", serviceAttributes, "k8s.namespace.name")
	}
	return attributes
}

func copyStringProperty(out map[string]string, attribute string, properties map[string]any, property string) {
	if value, ok := properties[property].(string); ok && value != "" {
		out[attribute] = value
	}
}

// Status returns contributor cache state without entity or dimension values.
func (e *ContributorExporter) Status(now time.Time) ContributorStatus {
	e.mu.RLock()
	st := e.state
	snapshot := e.snapshot
	e.mu.RUnlock()
	status := ContributorStatus{
		Collector: contributorsCollector, LastAttemptUnix: unixIntOrZero(st.LastAttempt), LastSuccessUnix: unixIntOrZero(st.LastSuccess),
		LastDurationSeconds: st.LastDuration.Seconds(), MaxStaleSeconds: e.cfg.ContributorMaxStale.Seconds(),
		Attempts: st.Attempts, Errors: st.Errors, LastError: st.LastError, Stale: true,
	}
	if !st.LastSuccess.IsZero() {
		status.CacheAgeSeconds = max(0, now.Sub(st.LastSuccess).Seconds())
		status.Stale = status.CacheAgeSeconds > e.cfg.ContributorMaxStale.Seconds()
	}
	if snapshot != nil {
		status.WindowStartUnix = snapshot.WindowStart.Unix()
		status.WindowEndUnix = snapshot.WindowEnd.Unix()
		status.EnvironmentCount = len(e.targets)
		status.ContributorCount = len(snapshot.Contributors)
		status.EntityCount = len(snapshot.Entities)
	}
	status.Ready = snapshot != nil && !status.Stale
	return status
}

// DebugCacheHandler returns contributor cache state without contributor values.
func (e *ContributorExporter) DebugCacheHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(e.Status(e.now().UTC()))
}

// Describe implements prometheus.Collector.
func (e *ContributorExporter) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range e.allDescriptors() {
		ch <- desc
	}
}

// Collect implements prometheus.Collector.
func (e *ContributorExporter) Collect(ch chan<- prometheus.Metric) {
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
		if age <= e.cfg.ContributorMaxStale.Seconds() {
			stale = 0
		}
	}
	e.emit(ch, e.desc.collectorUp, prometheus.GaugeValue, up, contributorsCollector)
	e.emit(ch, e.desc.refreshTotal, prometheus.CounterValue, float64(st.Attempts), contributorsCollector)
	e.emit(ch, e.desc.refreshErrors, prometheus.CounterValue, float64(st.Errors), contributorsCollector)
	e.emit(ch, e.desc.refreshDuration, prometheus.GaugeValue, st.LastDuration.Seconds(), contributorsCollector)
	e.emit(ch, e.desc.lastAttempt, prometheus.GaugeValue, unixOrZero(st.LastAttempt), contributorsCollector)
	e.emit(ch, e.desc.lastSuccess, prometheus.GaugeValue, unixOrZero(st.LastSuccess), contributorsCollector)
	e.emit(ch, e.desc.cacheAge, prometheus.GaugeValue, age, contributorsCollector)
	e.emit(ch, e.desc.cacheStale, prometheus.GaugeValue, stale, contributorsCollector)
	if snapshot == nil {
		return
	}
	e.emit(ch, e.desc.windowStart, prometheus.GaugeValue, float64(snapshot.WindowStart.Unix()))
	e.emit(ch, e.desc.windowEnd, prometheus.GaugeValue, float64(snapshot.WindowEnd.Unix()))
	e.emit(ch, e.desc.windowDuration, prometheus.GaugeValue, snapshot.WindowEnd.Sub(snapshot.WindowStart).Seconds())
	for _, row := range snapshot.Contributors {
		switch row.Family {
		case "host_units":
			e.emit(ch, e.desc.hostUnits, prometheus.GaugeValue, row.Value, row.EnvironmentID, row.Environment, row.Subtype, row.DimensionID, row.DimensionName)
		case "dem_units":
			e.emit(ch, e.desc.demUnits, prometheus.GaugeValue, row.Value, row.EnvironmentID, row.Environment, row.Subtype, row.DimensionID, row.DimensionName)
		case "ddu":
			e.emit(ch, e.desc.davisDataUnits, prometheus.GaugeValue, row.Value, row.EnvironmentID, row.Environment, row.Subtype, row.DimensionType, row.DimensionID, row.DimensionName)
		}
	}
	for _, key := range sortedKeys(snapshot.Entities) {
		entity := snapshot.Entities[key]
		e.emit(ch, e.desc.entityInfo, prometheus.GaugeValue, 1, entity.EnvironmentID, entity.Environment, entity.ID, entity.Name, entity.Type)
		for _, zone := range entity.ManagementZones {
			e.emit(ch, e.desc.entityZoneInfo, prometheus.GaugeValue, 1, entity.EnvironmentID, entity.Environment, entity.ID, zone)
		}
		for _, tagKey := range sortedKeys(entity.Tags) {
			for _, value := range entity.Tags[tagKey] {
				e.emit(ch, e.desc.entityTagInfo, prometheus.GaugeValue, 1, entity.EnvironmentID, entity.Environment, entity.ID, tagKey, value)
			}
		}
		for _, attribute := range sortedKeys(entity.Attributes) {
			e.emit(ch, e.desc.entityAttributeInfo, prometheus.GaugeValue, 1, entity.EnvironmentID, entity.Environment, entity.ID, attribute, entity.Attributes[attribute])
		}
	}
}

func (e *ContributorExporter) emit(ch chan<- prometheus.Metric, desc *prometheus.Desc, valueType prometheus.ValueType, value float64, labels ...string) {
	values := append(append([]string{}, e.labelValues...), labels...)
	ch <- prometheus.MustNewConstMetric(desc, valueType, value, values...)
}

func (e *ContributorExporter) allDescriptors() []*prometheus.Desc {
	return []*prometheus.Desc{
		e.desc.collectorUp, e.desc.refreshTotal, e.desc.refreshErrors, e.desc.refreshDuration, e.desc.lastAttempt,
		e.desc.lastSuccess, e.desc.cacheAge, e.desc.cacheStale, e.desc.windowStart, e.desc.windowEnd,
		e.desc.windowDuration, e.desc.hostUnits, e.desc.demUnits, e.desc.davisDataUnits, e.desc.entityInfo,
		e.desc.entityZoneInfo, e.desc.entityTagInfo, e.desc.entityAttributeInfo,
	}
}

func contributorQuerySpecs(limit int) []querySpec {
	withLimit := func(selector string) string {
		return fmt.Sprintf(selector, limit)
	}
	return []querySpec{
		{"host_units", "full_stack", "host", "dt.entity.host", "dt.entity.host.name", true, withLimit(`builtin:billing.full_stack_monitoring.usage_per_host:splitBy("dt.entity.host"):sum:sort(value(sum,descending)):limit(%d):names`)},
		{"host_units", "infrastructure", "host", "dt.entity.host", "dt.entity.host.name", true, withLimit(`builtin:billing.infrastructure_monitoring.usage_per_host:splitBy("dt.entity.host"):sum:sort(value(sum,descending)):limit(%d):names`)},
		{"dem_units", "browser", "synthetic_test", "dt.entity.synthetic_test", "dt.entity.synthetic_test.name", true, withLimit(`builtin:billing.synthetic.actions.usage_by_browser_monitor:splitBy("dt.entity.synthetic_test"):sum:sort(value(sum,descending)):limit(%d):names`)},
		{"dem_units", "http", "http_check", "dt.entity.http_check", "dt.entity.http_check.name", true, withLimit(`builtin:billing.synthetic.requests.usage_by_http_monitor:splitBy("dt.entity.http_check"):sum:sort(value(sum,descending)):limit(%d):names`)},
		{"dem_units", "rum_web", "application", "dt.entity.application", "dt.entity.application.name", true, withLimit(`builtin:billing.real_user_monitoring.web.session.usage_by_app:splitBy("dt.entity.application"):sum:sort(value(sum,descending)):limit(%d):names`)},
		{"ddu", "metrics", "metric_key", "Metric Key", "Metric Key", false, withLimit(`builtin:billing.ddu.metrics.byMetric:splitBy("Metric Key"):sum:sort(value(sum,descending)):limit(%d)`)},
		{"ddu", "metrics", "entity", "dt.entity.monitored_entity", "dt.entity.monitored_entity.name", true, withLimit(`builtin:billing.ddu.metrics.byEntity:splitBy("dt.entity.monitored_entity"):sum:sort(value(sum,descending)):limit(%d):names`)},
		{"ddu", "log", "entity", "dt.entity.monitored_entity", "dt.entity.monitored_entity.name", true, withLimit(`builtin:billing.ddu.log.byEntity:splitBy("dt.entity.monitored_entity"):sum:sort(value(sum,descending)):limit(%d):names`)},
		{"ddu", "log", "description", "Description", "Description", false, withLimit(`builtin:billing.ddu.log.byDescription:splitBy("Description"):sum:sort(value(sum,descending)):limit(%d)`)},
		{"ddu", "traces", "entity", "dt.entity.monitored_entity", "dt.entity.monitored_entity.name", true, withLimit(`builtin:billing.ddu.traces.byEntity:splitBy("dt.entity.monitored_entity"):sum:sort(value(sum,descending)):limit(%d):names`)},
		{"ddu", "traces", "span_kind", "Description", "Description", false, withLimit(`builtin:billing.ddu.traces.byDescription:splitBy("Description"):sum:sort(value(sum,descending)):limit(%d)`)},
		{"ddu", "events", "entity", "dt.entity.monitored_entity", "dt.entity.monitored_entity.name", true, withLimit(`builtin:billing.ddu.events.byEntity:splitBy("dt.entity.monitored_entity"):sum:sort(value(sum,descending)):limit(%d):names`)},
		{"ddu", "events", "description", "Description", "Description", false, withLimit(`builtin:billing.ddu.events.byDescription:splitBy("Description"):sum:sort(value(sum,descending)):limit(%d)`)},
	}
}

var _ prometheus.Collector = (*ContributorExporter)(nil)
