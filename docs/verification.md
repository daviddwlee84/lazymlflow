# Verification record

Verified on 2026-09-20 using disposable configuration, stores, credentials and
artifact destinations. Existing user experiment data was not used.

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
features and cross-target comparisons are outside v1. Other artifact providers
may work with a configured MLflow environment but were not tested. Browser and
clipboard actions depend on the OS's available desktop commands.
