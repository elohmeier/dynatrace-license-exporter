# Repository Guidelines

## Structure

- `main.go` wires configuration, HTTP endpoints, and graceful shutdown.
- `internal/dynatrace/` contains the read-only cluster API client.
- `internal/billing/` contains archive parsing, data types, and calculations.
- `internal/collector/` contains the background scheduler and Prometheus collector.
- `examples/` contains synthetic, non-secret configuration examples.
- Tests live beside the code; fixtures must use synthetic names and identifiers.

## Development

Run these checks before committing:

```sh
make ci
```

Use `gofmt` for Go sources and table-driven tests for boundary behavior. API
tests should use `httptest`; never require a real Dynatrace endpoint.

## Commits and releases

Releases are driven from `main` by Conventional Commits and semantic-release.
Use subjects such as `fix: retain cached entity names`, `feat: enrich host
metrics`, or `chore: update release tooling`. A `fix` creates a patch release,
a `feat` creates a minor release, and a breaking-change marker (`feat!:` or a
`BREAKING CHANGE:` footer) creates a major release. Commit subjects without a
release-bearing type do not create a new version.

## Security and privacy

Never commit API tokens, internal URLs, real environment IDs, hostnames,
customer names, or billing archives. Examples and fixtures must use reserved
`.invalid` hostnames and clearly synthetic identifiers. Prefer token files and
CA bundles over command-line secrets and disabled TLS verification.

## Metrics

Avoid unbounded labels. New billing conversions must be described as estimates
unless the archive supplies the value directly. Refresh failures must preserve
the last complete snapshot and expose the failure through self-metrics.
