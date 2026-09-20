# Verification record

Verified on 2026-09-20. Automated fixtures use disposable configuration, stores,
credentials and artifact destinations; authorized read-only checks against an
existing server are called out in the v0.2 record below.

## v0.2 verification

The v0.2 checks used disposable configuration and XDG data directories. An
existing remote MLflow 3.6.0 server was also queried read-only to check its real
dataset/parameter conventions; personal visibility changes stayed in the
temporary local state database.

- macOS arm64 and native Linux arm64, **Go 1.25.0**: full vet, race tests, build,
  and actual PTY checks passed. The Linux test container and temporary toolchain
  files were removed afterward.
- Real PTY: SGR mouse click/wheel/drag, pane resize, numeric focus shortcuts, zoom,
  mouse capture toggle, column checkbox selection, parameter grouping and SQLite
  persistence, plus the existing comparison/history/artifact/target-switch flows.
- Shared target forms: standalone and embedded geometry, field focus, literal
  text input, review/save/edit/cancel semantics, disabled-mouse behavior, stale
  click rejection and terminal restoration.
- Real MLflow 3.6.0: native dataset inputs; the exact `learning rate` parameter;
  optimizer + learning-rate grouping; per-experiment isolation; saved TUI view
  restoration; local hide/archive/restore. Raw queries and remote run lifecycle
  remained unchanged. Nested runs use fixtures because the sampled active
  experiments have no parent-run tags.
- SSH: real remote version/experiment queries, browser access, and direct versus
  SSH artifact downloads with identical bytes. Unit tests cover private CA/TLS
  names, prefix boundaries, host/origin validation, authentication precedence,
  redirects, replacement, cancellation and process cleanup.
- Authorized remote deployment configuration: effective allowed-host defaults
  preserved, explicit Tailscale endpoints appended, and missing-curl healthcheck
  replaced by Python urllib. Only MLflow was recreated; database and object-store
  containers remained running with unchanged IDs. Host allow/reject and healthy
  container state were verified. The remote config has a separate backup and was
  not committed by this implementation session.
- Local state: absent-store reads, schema setup/version checks, failed writes,
  concurrent updates, bounded locks, cancellation, source isolation and restored
  views are covered by tests. Data queries apply personal views only with
  `--use-view`; JSON distinguishes complete results from locally sorted pages.

Performance measurements are local observations, not universal latency targets.
On an Apple M4 with 10,000 loaded runs, cached rendering measured approximately
0.324 ms / 125 KB per frame versus 27.7 ms / 37 MB before caching. Precomputed
sort keys reduced metric sorting from about 50.8 ms to 2.54 ms, and dataset
sorting from 179.4 ms to 6.87 ms. Reproduce with:

```sh
go test ./internal/core -run '^$' -bench BenchmarkSortRuns -benchmem
go test ./internal/tui -run '^$' -bench BenchmarkDashboardLoadedRuns -benchmem
```

## Automated checks and terminal behavior

- macOS arm64: `go vet ./...`, `go test -race ./...`, and executable build passed.
- Real PTY: arrow/Vim navigation, literal text input, submitted server filters,
  two-run comparisons and histories, artifact download, target switching while
  a refresh is pending, and normal exit passed.
- PTY sizes: 120×36, 80×24, 38×12, 12×4 and 140×42. Terminal echo/canonical mode
  and alternate-screen restoration were asserted.
- Shared CLI/dashboard target form: real CLI PTY creation and review/save passed;
  deterministic tests cover embedded cancellation, runtime fields, advanced
  settings, failed saves and duplicate IDs.
- Model tests cover late responses, per-target state, stable selection after
  refresh, Unicode widths, missing/nonfinite metrics, repeated steps, close
  floating-point differences, duplicate run names and local-server replacement.
- Linux arm64 with Go 1.25.0: all package race tests, native binary build and
  the complete real PTY smoke passed in an isolated container. The container
  was removed afterward. Linux arm64 and amd64 cross-builds also passed.

## Real MLflow and artifact integrations

`scripts/integration_mlflow.py` passed with **MLflow 2.22.0, 3.12.0 and 3.16.1**.
For each version, both SQLite and FileStore exercised experiment pagination,
run sorting/filtering, cross-experiment comparisons, complete metric histories,
artifact listing and recursive downloads. Downloaded bytes matched; existing
destinations were protected, missing databases were not created, and the
SQLite SHA-256 remained unchanged.

The managed `uvx` MLflow 3.16.1 path and explicitly selected Python environments
were also exercised. Unit tests verify cancellation and process-descendant
cleanup without relying on Python startup timing.

`scripts/integration_s3.py` passed with **MLflow 3.16.1 and real MinIO
RELEASE.2025-04-22T22-12-26Z**, built from the official source tag for macOS.
Tests covered root/nested listing, Unicode filenames, binary data, single files,
directories and whole-run downloads through both Go HTTP proxy and the official
MLflow CLI's direct S3 access. The proxy target deliberately named a nonexistent
Python executable to verify it needed no Python runtime. Test servers and data
were cleaned up.

The repository CI repeats Go/race/PTY checks on macOS and Ubuntu, the three
MLflow versions on Ubuntu, and a Docker-backed MinIO integration on Ubuntu.
These workflows are configured; no remote CI run or release was published by
this implementation session.

## Boundaries

Databricks-specific behavior, direct PostgreSQL/MySQL access, registry/tracing
features and cross-target comparisons remain outside this version. Other artifact providers
may work with a configured MLflow environment but were not tested. Browser and
clipboard actions depend on the OS's available desktop commands.
