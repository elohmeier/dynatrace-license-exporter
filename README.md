# Dynatrace License Exporter

[![CI](https://github.com/elohmeier/dynatrace-license-exporter/actions/workflows/ci.yml/badge.svg)](https://github.com/elohmeier/dynatrace-license-exporter/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/elohmeier/dynatrace-license-exporter)](https://github.com/elohmeier/dynatrace-license-exporter/releases)
[![GHCR](https://img.shields.io/badge/ghcr.io-dynatrace--license--exporter-blue)](https://github.com/elohmeier/dynatrace-license-exporter/pkgs/container/dynatrace-license-exporter)
[![Go Report Card](https://goreportcard.com/badge/github.com/elohmeier/dynatrace-license-exporter)](https://goreportcard.com/report/github.com/elohmeier/dynatrace-license-exporter)
[![Go Reference](https://pkg.go.dev/badge/github.com/elohmeier/dynatrace-license-exporter.svg)](https://pkg.go.dev/github.com/elohmeier/dynatrace-license-exporter)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Prometheus exporter for Dynatrace Managed license-consumption records. It
downloads a short, overlapping window from the cluster license API, parses the
nested billing archive, selects the newest settled hourly record, and exposes
the result from an in-memory cache.

Prometheus scrapes never call Dynatrace. A failed refresh retains the last good
snapshot; `/readyz` reports whether that snapshot is still fresh.

An optional contributor module queries environment-level billing metrics to
identify the largest HU, DEM, and DDU contributors and enriches entity-backed
rows with names, management zones, selected platform attributes, and
operator-allow-listed tags.

## Requirements

- A Dynatrace Managed cluster API URL.
- A cluster API token allowed to read
  `/api/cluster/v2/license/consumption`.
- Optional environment API tokens allowed to read metrics and entities when
  contributor collection is enabled.
- Network and TLS trust from the exporter to the Dynatrace Managed endpoint.

The exporter is read-only and only performs `GET` requests.

## Quick start

```sh
export DYNATRACE_URL=https://dynatrace.example.com
export DYNATRACE_CLUSTER_TOKEN_FILE=/run/secrets/dynatrace-cluster-token
go run .
```

The default listen address is `:9721`.

```sh
curl http://localhost:9721/metrics
curl http://localhost:9721/readyz
```

## Collection model

The cluster endpoint returns an outer ZIP containing a
`billingRecords_*.zip`, which in turn contains one JSON file per billing
interval. The exporter:

1. Queries an overlapping window, six hours by default.
2. Applies strict limits to the HTTP body, nested ZIP, JSON documents, and
   document count.
3. Selects the newest interval ending before the settlement cutoff.
4. Calculates host-unit and DEM estimates and keeps raw DDU values.
5. Atomically replaces the in-memory snapshot only after complete success.

The default refresh interval is one hour and the default settlement delay is
2 hours 5 minutes. Dynatrace Managed rejects license archive requests ending
within the latest two hours; the extra five minutes avoids boundary races.
Overlap allows corrected or delayed billing records to be picked up by a later
refresh.

### Estimation formulas

The archive supplies billing inputs, not final HU and DEM values. The exporter
applies the following formulas and marks the resulting metrics as estimates.
Review them when upgrading or changing the Dynatrace licensing model.

Full-stack host units use monitored memory (`hostMemoryBytes`, falling back to
`passMemoryLimit`):

| Memory | Estimated host units |
| --- | ---: |
| up to 1.6 GiB | 0.10 |
| up to 4 GiB | 0.25 |
| up to 8 GiB | 0.50 |
| up to 16 GiB | 1.00 |
| above 16 GiB | `ceil(memory GiB / 16)` |

Infrastructure-only usage is estimated as
`min(full-stack host units × 0.3, 1.0)`.

Synthetic DEM is estimated as `HTTP executions × 0.1` and
`browser executions × 1.0`. Unknown monitor types conservatively use one DEM
unit per execution.

RUM DEM is estimated as:

```text
visits × 0.25
+ mobile sessions × 0.25
+ session replays × 1.0
+ mobile session replays × 1.0
+ user properties × 0.01
```

## Configuration

CLI flags override environment values. Tokens are intentionally not accepted
as CLI flags because command lines may be visible to other users.

| Flag | Environment | Default | Description |
| --- | --- | --- | --- |
| `-url` | `DYNATRACE_URL` | required | Dynatrace Managed base URL, without an environment path. |
| `-connect-address` | `DYNATRACE_CONNECT_ADDRESS` | none | Optional `host:port` connection override; URL Host and TLS SNI are preserved. |
| `-cluster-token-file` | `DYNATRACE_CLUSTER_TOKEN_FILE` | none | File containing the cluster API token. |
| `-environments-file` | `DYNATRACE_ENVIRONMENTS_FILE` | none | JSON file enabling environment contributor collection. |
| none | `DYNATRACE_CLUSTER_TOKEN` | none | Cluster API token; takes precedence over the token file. |
| none | `DYNATRACE_TOKEN` | none | Fallback API token environment variable. |
| `-ca-file` | `DYNATRACE_CA_FILE` | system trust | Additional CA certificate bundle. |
| `-ignore-cert` | `DYNATRACE_IGNORE_CERT` | `false` | Disable TLS verification. Prefer a CA file. |
| `-environment-names` | `DYNATRACE_ENVIRONMENT_NAMES` | none | Comma-separated `environment-id=display-name` mappings. |
| `-labels` | `DYNATRACE_LABELS` | none | Comma-separated constant Prometheus labels. |
| `-include-hosts` | `DYNATRACE_INCLUDE_HOSTS` | `true` | Export per-host metrics. |
| `-bind-port` | `DYNATRACE_BIND_PORT` | `9721` | HTTP listen port. |
| `-request-timeout` | `DYNATRACE_REQUEST_TIMEOUT` | `2m` | Per-request HTTP timeout. |
| `-refresh-interval` | `DYNATRACE_REFRESH_INTERVAL` | `1h` | Background refresh interval. |
| `-refresh-timeout` | `DYNATRACE_REFRESH_TIMEOUT` | `10m` | Overall refresh timeout. |
| `-billing-lookback` | `DYNATRACE_BILLING_LOOKBACK` | `6h` | Overlapping archive query window. |
| `-settlement-delay` | `DYNATRACE_SETTLEMENT_DELAY` | `2h5m` | Age of the archive query end; must normally exceed Dynatrace's two-hour availability delay. |
| `-max-stale` | `DYNATRACE_MAX_STALE` | `3h` | Maximum age of a successful cache refresh. |
| `-max-download-bytes` | `DYNATRACE_MAX_DOWNLOAD_BYTES` | `67108864` | Maximum compressed API response. |
| `-max-nested-archive-bytes` | `DYNATRACE_MAX_NESTED_ARCHIVE_BYTES` | `134217728` | Maximum expanded nested ZIP. |
| `-max-json-document-bytes` | `DYNATRACE_MAX_JSON_DOCUMENT_BYTES` | `8388608` | Maximum expanded JSON document. |
| `-max-archive-documents` | `DYNATRACE_MAX_ARCHIVE_DOCUMENTS` | `1000` | Maximum JSON document count. |
| `-contributor-lookback` | `DYNATRACE_CONTRIBUTOR_LOOKBACK` | `168h` | Rolling Metrics API query window. |
| `-contributor-refresh-interval` | `DYNATRACE_CONTRIBUTOR_REFRESH_INTERVAL` | `6h` | Contributor refresh interval. |
| `-contributor-refresh-timeout` | `DYNATRACE_CONTRIBUTOR_REFRESH_TIMEOUT` | `10m` | Overall contributor refresh timeout. |
| `-contributor-max-stale` | `DYNATRACE_CONTRIBUTOR_MAX_STALE` | `18h` | Maximum age of the contributor snapshot. |
| `-contributor-limit` | `DYNATRACE_CONTRIBUTOR_LIMIT` | `100` | Top rows retained per billing query. |
| `-entity-parallelism` | `DYNATRACE_ENTITY_PARALLELISM` | `5` | Concurrent entity metadata requests. |
| `-entity-tag-keys` | `DYNATRACE_ENTITY_TAG_KEYS` | none | Comma-separated entity tag keys to export. |

Example environment-name mapping:

```sh
export DYNATRACE_ENVIRONMENT_NAMES='11111111-1111-1111-1111-111111111111=Production,22222222-2222-2222-2222-222222222222=Testing'
```

Unknown environment IDs are used as their own display names.

### Contributor environments

Contributor collection is disabled unless an environments file is configured.
The file contains no inline secrets—only token-file paths or environment
variable names:

```json
{
  "environments": [
    {
      "id": "environment-example-one",
      "name": "Example One",
      "token_file": "/run/secrets/dynatrace/environment-one-token"
    },
    {
      "id": "environment-example-two",
      "name": "Example Two",
      "token_env": "DYNATRACE_EXAMPLE_TWO_TOKEN"
    }
  ]
}
```

A complete synthetic example is available in
[`examples/environments.json`](examples/environments.json). Names from this
file are also applied to cluster billing metrics.

Entity tags are not exported by default. Explicitly allow only stable,
low-cardinality keys needed for ownership or grouping:

```sh
export DYNATRACE_ENTITY_TAG_KEYS='application,team,environment'
```

## Metrics

Billing interval and aggregate metrics:

- `dynatrace_license_period_start_timestamp_seconds`
- `dynatrace_license_period_end_timestamp_seconds`
- `dynatrace_license_period_duration_seconds`
- `dynatrace_license_data_age_seconds`
- `dynatrace_license_environment_info`
- `dynatrace_license_estimated_host_units{environment_id,environment,monitoring_mode}`
- `dynatrace_license_host_count{environment_id,environment,monitoring_mode}`
- `dynatrace_license_estimated_dem_units{environment_id,environment,source}`
- `dynatrace_license_davis_data_units{environment_id,environment,pool}`
- `dynatrace_license_rum_usage{environment_id,environment,kind}`
- `dynatrace_license_synthetic_executions{environment_id,environment,test_id,monitor_type,location}`
- `dynatrace_license_synthetic_estimated_dem_units{environment_id,environment,test_id,monitor_type}`

Optional per-host metrics:

- `dynatrace_license_host_estimated_host_units`
- `dynatrace_license_host_memory_bytes`

Exporter self-metrics:

- `dynatrace_up`
- `dynatrace_collector_up{collector}`
- `dynatrace_refresh_total{collector}`
- `dynatrace_refresh_errors_total{collector}`
- `dynatrace_refresh_duration_seconds{collector}`
- `dynatrace_cache_last_attempt_timestamp_seconds{collector}`
- `dynatrace_cache_last_success_timestamp_seconds{collector}`
- `dynatrace_cache_age_seconds{collector}`
- `dynatrace_cache_stale{collector}`
- `dynatrace_api_requests_total{endpoint,code}`
- `dynatrace_api_request_duration_seconds{endpoint}`

Optional rolling contributor metrics:

- `dynatrace_license_contributor_window_start_timestamp_seconds`
- `dynatrace_license_contributor_window_end_timestamp_seconds`
- `dynatrace_license_contributor_window_seconds`
- `dynatrace_license_contributor_host_units{environment_id,environment,monitoring_mode,entity_id,entity_name}`
- `dynatrace_license_contributor_dem_units{environment_id,environment,source,entity_id,entity_name}`
- `dynatrace_license_contributor_davis_data_units{environment_id,environment,pool,dimension_type,dimension_id,dimension_name}`
- `dynatrace_entity_info{environment_id,environment,entity_id,entity_name,entity_type}`
- `dynatrace_entity_management_zone_info{environment_id,environment,entity_id,management_zone}`
- `dynatrace_entity_tag_info{environment_id,environment,entity_id,key,value}`
- `dynatrace_entity_attribute_info{environment_id,environment,entity_id,attribute,value}`

The contributor collector runs independently from the cluster archive
collector and reports its own `collector="contributors"` cache and refresh
self-metrics. A failed contributor refresh retains its previous complete
snapshot and does not affect `/readyz`, which represents the required cluster
billing collector.

Host-unit and DEM values are explicitly named `estimated`: the archive
contains their billing inputs, while the exporter applies documented conversion
rules. DDU pool values are exported directly from the archive. All billing
values are gauges for the interval identified by the period metrics; they are
not process-lifetime counters.

## PromQL examples

Current aggregate usage by environment:

```promql
sum by (environment) (dynatrace_license_estimated_host_units)
sum by (environment) (dynatrace_license_estimated_dem_units)
sum by (environment, pool) (dynatrace_license_davis_data_units)
```

Highest current host consumers:

```promql
topk(20, dynatrace_license_host_estimated_host_units)
```

Highest contributors in the configured rolling contributor window:

```promql
topk(20, dynatrace_license_contributor_host_units)
topk(20, dynatrace_license_contributor_dem_units)
topk(20, dynatrace_license_contributor_davis_data_units)
```

Average host-unit usage over seven days:

```promql
avg_over_time(
  (sum by (environment) (dynatrace_license_estimated_host_units))[7d:1h]
)
```

## HTTP endpoints

| Path | Description |
| --- | --- |
| `/metrics` | Prometheus metrics from the cached snapshot. |
| `/health` and `/healthz` | Process liveness. |
| `/readyz` | HTTP 200 only when a non-stale snapshot exists. |
| `/debug/cache` | Cache timestamps, age, errors, and billing period; never includes credentials or payload records. |
| `/debug/contributors` | Contributor cache status when the optional module is configured. |

## Container

Release images are published to:

```sh
docker pull ghcr.io/elohmeier/dynatrace-license-exporter:latest
```

Run with a read-only secret mount:

```sh
docker run --rm -p 9721:9721 \
  -e DYNATRACE_URL=https://dynatrace.example.com \
  -e DYNATRACE_CLUSTER_TOKEN_FILE=/run/secrets/token \
  -v "$PWD/token:/run/secrets/token:ro" \
  ghcr.io/elohmeier/dynatrace-license-exporter:latest
```

Enable contributor collection with read-only configuration and token mounts:

```sh
docker run --rm -p 9721:9721 \
  -e DYNATRACE_URL=https://dynatrace.example.com \
  -e DYNATRACE_CLUSTER_TOKEN_FILE=/run/secrets/cluster-token \
  -e DYNATRACE_ENVIRONMENTS_FILE=/etc/dynatrace/environments.json \
  -v "$PWD/cluster-token:/run/secrets/cluster-token:ro" \
  -v "$PWD/environment-one-token:/run/secrets/dynatrace/environment-one-token:ro" \
  -v "$PWD/environments.json:/etc/dynatrace/environments.json:ro" \
  ghcr.io/elohmeier/dynatrace-license-exporter:latest
```

## Development

```sh
make ci
```

The CI target runs formatting and module checks, `go vet`, race-enabled tests,
an 80% overall coverage floor, and a full build.

The tests use synthetic hostnames, identifiers, and in-memory ZIP archives;
they do not require a Dynatrace server or token.

## License

MIT
