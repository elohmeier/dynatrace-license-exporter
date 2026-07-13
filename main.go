package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/elohmeier/dynatrace-license-exporter/internal/collector"
	"github.com/elohmeier/dynatrace-license-exporter/internal/config"
	"github.com/elohmeier/dynatrace-license-exporter/internal/dynatrace"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	app     = "dynatrace-license-exporter"
	version = "dev"
	build   = "none"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.FromEnv()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	var (
		labels           string
		environmentNames string
		entityTagKeys    string
		showVersion      bool
		debug            bool
	)
	flags := flag.NewFlagSet(app, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cfg.URL, "url", cfg.URL, "Dynatrace Managed base URL")
	flags.StringVar(&cfg.ConnectAddress, "connect-address", cfg.ConnectAddress, "Optional host:port to dial while preserving URL Host and TLS SNI")
	flags.StringVar(&cfg.ClusterTokenFile, "cluster-token-file", cfg.ClusterTokenFile, "File containing the Dynatrace cluster API token")
	flags.StringVar(&cfg.EnvironmentsFile, "environments-file", cfg.EnvironmentsFile, "JSON file describing optional environment API clients")
	flags.StringVar(&cfg.CAFile, "ca-file", cfg.CAFile, "Custom CA certificate bundle")
	flags.StringVar(&labels, "labels", "", "Additional Prometheus labels as key=value pairs")
	flags.StringVar(&environmentNames, "environment-names", "", "Environment display names as uuid=name pairs")
	flags.StringVar(&entityTagKeys, "entity-tag-keys", "", "Comma-separated entity tag keys to export")
	flags.BoolVar(&cfg.IncludeHosts, "include-hosts", cfg.IncludeHosts, "Export per-host billing metrics")
	flags.BoolVar(&cfg.InsecureSkipVerify, "ignore-cert", cfg.InsecureSkipVerify, "Disable TLS certificate verification")
	flags.IntVar(&cfg.BindPort, "bind-port", cfg.BindPort, "HTTP listen port")
	flags.DurationVar(&cfg.RequestTimeout, "request-timeout", cfg.RequestTimeout, "Dynatrace HTTP request timeout")
	flags.DurationVar(&cfg.RefreshInterval, "refresh-interval", cfg.RefreshInterval, "Background refresh interval")
	flags.DurationVar(&cfg.RefreshTimeout, "refresh-timeout", cfg.RefreshTimeout, "Maximum duration of one refresh")
	flags.DurationVar(&cfg.BillingLookback, "billing-lookback", cfg.BillingLookback, "Overlapping billing archive query window")
	flags.DurationVar(&cfg.SettlementDelay, "settlement-delay", cfg.SettlementDelay, "Minimum age of an interval before export")
	flags.DurationVar(&cfg.MaxStale, "max-stale", cfg.MaxStale, "Maximum age of the last successful cache refresh")
	flags.Int64Var(&cfg.MaxDownloadBytes, "max-download-bytes", cfg.MaxDownloadBytes, "Maximum compressed API response size")
	flags.Int64Var(&cfg.MaxNestedArchiveBytes, "max-nested-archive-bytes", cfg.MaxNestedArchiveBytes, "Maximum expanded nested ZIP size")
	flags.Int64Var(&cfg.MaxJSONDocumentBytes, "max-json-document-bytes", cfg.MaxJSONDocumentBytes, "Maximum expanded billing JSON document size")
	flags.IntVar(&cfg.MaxArchiveDocuments, "max-archive-documents", cfg.MaxArchiveDocuments, "Maximum billing documents per archive")
	flags.DurationVar(&cfg.ContributorLookback, "contributor-lookback", cfg.ContributorLookback, "Rolling Metrics API contributor window")
	flags.DurationVar(&cfg.ContributorRefreshInterval, "contributor-refresh-interval", cfg.ContributorRefreshInterval, "Contributor background refresh interval")
	flags.DurationVar(&cfg.ContributorRefreshTimeout, "contributor-refresh-timeout", cfg.ContributorRefreshTimeout, "Maximum duration of one contributor refresh")
	flags.DurationVar(&cfg.ContributorMaxStale, "contributor-max-stale", cfg.ContributorMaxStale, "Maximum age of contributor cache data")
	flags.IntVar(&cfg.ContributorLimit, "contributor-limit", cfg.ContributorLimit, "Top contributors retained per billing query")
	flags.IntVar(&cfg.EntityParallelism, "entity-parallelism", cfg.EntityParallelism, "Maximum concurrent entity metadata requests")
	flags.BoolVar(&showVersion, "version", false, "Print version and exit")
	flags.BoolVar(&debug, "debug", false, "Enable debug logging")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if showVersion {
		_, _ = fmt.Fprintf(stdout, "%s %s (%s)\n", app, version, build)
		return 0
	}
	if labels != "" {
		parsed, err := config.ParseAssignments(labels)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "invalid labels: %v\n", err)
			return 1
		}
		for key, value := range parsed {
			cfg.Labels[key] = value
		}
	}
	if environmentNames != "" {
		parsed, err := config.ParseAssignments(environmentNames)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "invalid environment names: %v\n", err)
			return 1
		}
		for key, value := range parsed {
			cfg.EnvironmentNames[key] = value
		}
	}
	if entityTagKeys != "" {
		cfg.EntityTagKeys = config.ParseCSV(entityTagKeys)
	}
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 1
	}
	token, err := cfg.Token()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "token configuration: %v\n", err)
		return 1
	}
	environments, err := cfg.LoadEnvironments()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "environment configuration: %v\n", err)
		return 1
	}
	for _, environment := range environments {
		cfg.EnvironmentNames[environment.ID] = environment.Name
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: level})).With(
		"app", app,
		"version", version,
		"build", build,
	)
	apiMetrics := dynatrace.NewMetrics("dynatrace")
	client, err := dynatrace.NewClient(dynatrace.Config{
		BaseURL:            cfg.URL,
		Token:              token,
		ConnectAddress:     cfg.ConnectAddress,
		Timeout:            cfg.RequestTimeout,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		CAFile:             cfg.CAFile,
		UserAgent:          app + "/" + version,
		MaxDownloadBytes:   cfg.MaxDownloadBytes,
		Metrics:            apiMetrics,
	})
	if err != nil {
		logger.Error("failed to create Dynatrace client", "err", err)
		return 1
	}
	var contributorExporter *collector.ContributorExporter
	var hostTargets []collector.HostTarget
	if len(environments) > 0 {
		targets := make([]collector.ContributorTarget, 0, len(environments))
		hostTargets = make([]collector.HostTarget, 0, len(environments))
		for _, environment := range environments {
			environmentToken, err := environment.Token()
			if err != nil {
				logger.Error("failed to resolve environment token", "environment_id", environment.ID, "err", err)
				return 1
			}
			environmentClient, err := dynatrace.NewClient(dynatrace.Config{
				BaseURL: cfg.URL, Token: environmentToken, ConnectAddress: cfg.ConnectAddress,
				Timeout: cfg.RequestTimeout, InsecureSkipVerify: cfg.InsecureSkipVerify, CAFile: cfg.CAFile,
				UserAgent: app + "/" + version, MaxDownloadBytes: cfg.MaxDownloadBytes, Metrics: apiMetrics,
			})
			if err != nil {
				logger.Error("failed to create environment client", "environment_id", environment.ID, "err", err)
				return 1
			}
			targets = append(targets, collector.ContributorTarget{Environment: environment, Client: environmentClient})
			hostTargets = append(hostTargets, collector.HostTarget{Environment: environment, Client: environmentClient})
		}
		contributorExporter = collector.NewContributorExporter(cfg, targets, logger)
	}

	exporter := collector.New(cfg, client, hostTargets, logger)
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	registry.MustRegister(apiMetrics.Collectors()...)
	if contributorExporter != nil {
		registry.MustRegister(collector.Combine(exporter, contributorExporter))
	} else {
		registry.MustRegister(exporter)
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	exporter.Start(appCtx)
	defer exporter.Stop()
	if contributorExporter != nil {
		contributorExporter.Start(appCtx)
		defer contributorExporter.Stop()
	}

	server := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.BindPort),
		Handler:           newMux(registry, exporter, contributorExporter),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting HTTP server", "addr", server.Addr, "include_hosts", cfg.IncludeHosts, "environment_api_clients", len(environments))
		errCh <- server.ListenAndServe()
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	select {
	case sig := <-sigCh:
		logger.Info("shutdown requested", "signal", sig.String())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "err", err)
			return 1
		}
	}

	appCancel()
	exporter.Stop()
	if contributorExporter != nil {
		contributorExporter.Stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("HTTP server shutdown failed", "err", err)
		return 1
	}
	return 0
}

func newMux(registry *prometheus.Registry, exporter *collector.Exporter, contributors *collector.ContributorExporter) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	health := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK\n"))
	}
	mux.HandleFunc("/health", health)
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/readyz", exporter.ReadyHandler)
	mux.HandleFunc("/debug/cache", exporter.DebugCacheHandler)
	if contributors != nil {
		mux.HandleFunc("/debug/contributors", contributors.DebugCacheHandler)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(app + "\n\n/metrics\n/healthz\n/readyz\n/debug/cache\n/debug/contributors\n"))
	})
	return mux
}
