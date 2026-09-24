# Verification record

Verification records through 2026-09-21. Automated fixtures use disposable configuration, stores,
credentials and artifact destinations; authorized read-only checks against an
existing server are called out separately below.

## Server setup, training environments and model handoff — 2026-09-21

These checks use synthetic models, disposable tracking data and isolated
configuration. They do not deploy or modify an existing tracking server.

- macOS arm64: full `go vet ./...`, `go test -race ./...`, build, and the three
  PTY scripts passed. Focused reruns cover the final model-list cancellation,
  empty-list mouse, failed-setup draft recovery and narrow review scrolling fixes.
- Native Linux arm64 with Go **1.25.0**: full vet/race/build and all three PTY
  scripts passed in a cached test container. Network was disabled; source,
  modules and the checksum-verified Go toolchain were mounted read-only. A final
  focused serverform/TUI race run and setup/model PTY run passed after UI changes.
  The task container and temporary Linux toolchain/build cache were removed.
- `pty_setup_models.py`: standalone and embedded setup, review/Back/cancel,
  narrow resizing, literal input, unchanged target default, secret-reference
  environment copy, registry/version inspection, bundle hashes, related models
  and explicit URI input passed. A fake Docker executable proves this generation
  test never starts services. Existing browser and inspection PTY scripts passed.
- Target environment tests cover self/cyclic references, quoting, stale settings,
  secret-free JSON/shell output, local-store rejection and SSH proxy environment.
  Attached-child tests use a real controlling PTY to verify input, foreground
  ownership, terminal restoration, Ctrl+C and process-tree cleanup; ordinary
  child exit codes are preserved.
- SDK authentication checks cover nonempty named Basic credentials, alternate
  auth-selector isolation, and detection of a saved SDK Basic credential pair
  that would override a named token/no-auth profile. The new command reports
  that conflict before starting a child and never prints the credential values.
  Temporary URI invocations retain ambient SDK behavior.
- Real Docker Engine **29.4.0**: all eight generated-stack profiles passed:
  SQLite, SQLite/native auth/internal TLS, PostgreSQL/native auth/internal TLS,
  PostgreSQL/local artifacts, RustFS, SeaweedFS, external S3 backed by a separate
  disposable RustFS fixture, and supplied TLS certificates. Checks include
  metric/artifact and Registry persistence, Unicode filenames, multipart S3,
  wrong-CA rejection, anonymous 401, unauthorized-member artifact 403, changed
  passwords surviving restart, and stable CA bytes. Deleting the initialized
  administrator makes both native-auth profiles fail with repair diagnostics;
  retaining the bootstrap password does not recreate the deleted administrator.
  All test containers, networks and volumes were removed.
- This controller's existing Docker proxy pointed to an unused local port and
  its credential helper stalled. Docker testing used a task-owned temporary
  proxy relay plus an isolated anonymous Docker configuration. Both were removed;
  the user's global Docker proxy/auth settings were not changed. Running new
  image pulls with that original configuration still requires resolving those
  environment settings; they are not a dependency of the generated templates.
- Real MLflow **3.16.1**: `integration_models.py` passed all four model source
  forms, actual Logged Model wire data, input/output relationships and step 0,
  safe metadata/dependency inspection, raw files, registry navigation, exact
  payload preservation and independent-manifest tamper rejection. An invalid
  pickle remains unread as model code. Direct local and proxied artifact paths
  passed. The existing SQLite/FileStore regression also passed with SQLite
  bytes unchanged.
- Compatibility reruns against **MLflow 2.22.0 and 3.12.0** on Python 3.12 passed
  the existing integration suite with the new downloader: queries, histories,
  recursive Unicode/binary downloads, overwrite/missing-database protection and
  unchanged SQLite SHA-256. Temporary fixtures and test binaries were removed.
- `serving_feasibility.py` passed synthetic sklearn, custom PyFunc and explicit
  recurrent-state HTTP inference. Local predict and a separate uv recreation of
  the sklearn model's recorded environment produced the same result. Missing
  inputs, wrong dtype and wrong shape were rejected. Temporary process groups
  and model files were removed. See [the study](model-handoff.md#serving-feasibility-study)
  for pinned versions and results; this does not validate a private model or a
  production inference service.

The CI workflow includes these PTY/model checks and a disposable Docker server
matrix. Configuring CI is separate from running it on a remote forge; no remote
CI run or release publication is claimed here. Actual NAS access requires an
operator-supplied mount; missing-mount and symlink-escape guards have automated
coverage. No physical NAS or live share-replacement scenario was used for this
verification.

## v0.3 inspection, journal and summaries

The following checks completed on 2026-09-20 with disposable local configuration
and XDG state directories:

- macOS arm64: full `go vet ./...`, `go test -race ./...`, and both real PTY
  scripts passed. Native Linux arm64 with **Go 1.25.0** passed full vet/race,
  native build, `pty_smoke.py` and `pty_inspection.py` in the cached test image.
  The Linux container had no external network, read-only source/module mounts,
  and isolated HOME/XDG directories; its container/toolchain snapshot was removed.
- v0.3 real PTY: expanded curves and sample cursor (keyboard/mouse), overlays,
  automatic refresh through run completion, a 146-column schema search,
  floating local note composition and external-editor handoff, cross-experiment
  dataset scan/relationships, and shared summary/prompt/export. Terminal and
  temporary-file cleanup passed along with the existing v0.2 interaction flows.
- `internal/core`, `internal/localstate`, `internal/inspection` and `internal/cli`:
  vet and race tests passed for schema/dataset contracts, local journal behavior,
  evidence collection and the CLI commands.
- Summary contexts: complete latest metrics and raw schema/profile, exact metric
  keys, model/system selection, local run/dataset/experiment notes, immutable
  copies and versioned JSON. A 1,000-point fixture verified full-history statistics
  with a 200-point retained sample, extrema and non-finite values; full/none modes
  and cancellation were also checked.
- Experiment summaries: complete multi-page metadata scans, default first-20
  details, `--all-details`, filter/order/lifecycle propagation, repeated-page
  rejection, required metadata errors and explicit optional-section warnings.
  Collection never exceeded four concurrent operations in the controlled fixture.
- Final experiment-overview regression: 27 metadata rows with only 20 detailed
  runs. Status totals, metric logged/missing/finite/non-finite coverage and latest
  ranges, parameter distinct values, and dataset-variant/context usage included
  fields present only in the final seven rows. Repeated dataset inputs did not
  inflate run counts; empty parameters remained distinct from missing values.
- Numeric history observations use complete finite history for last-minus-first,
  last-minus-minimum and last-minus-maximum, with step/timestamp references. Tests
  covered a one-point display sample, finite zero, all non-finite history and
  floating-point overflow without emitting an unsupported finite difference.
  These final report additions passed focused inspection/CLI vet and race tests.
- Markdown/prompt rendering: deterministic output for a captured snapshot,
  distinct latest/history statistics, no terminal escape sequences, safely quoted
  external strings, and explicit observed-fact/inference/unknown instructions.
  Prompt listing did not load configuration or connect; rendering did not launch
  an agent, browser or artifact download.
- Existing MLflow 3.6.0, read-only: a complete one-run experiment summary plus a
  run with 17 latest metrics and one dataset. The selected metric yielded six
  history points with zero collection warnings. JSON, Markdown and reusable
  prompt output succeeded; temporary binaries, configuration and state were removed.
- A further read-only check on the requested MLflow 3.6.0 run confirmed 146 logged
  schema columns and 18 points each for `train_loss`, `valid_loss` and `valid_corr`,
  with zero summary warnings. Its exact dataset-variant association scan completed
  across accessible experiments and returned one related use. Temporary state was
  isolated and removed.

The migration notes in [README](../README.md#migration-research-boundary) are
research, not execution evidence. No tracking-store migration, cross-server
import, remote journal write or model-provider call was performed for v0.3.
The v0.2 record below retains the earlier release's integration and performance
evidence; the v0.3 workspace checks above exercise the new inspection flows.

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
MLflow versions on Ubuntu, and a Docker-backed S3 integration on Ubuntu. That
fixture now defaults to the official RustFS 1.0.0 image pinned by digest; explicit
native or custom-container MinIO fixtures remain supported. It also checks
bounded presigned HTTP previews and rejection of implicit direct-S3 downloads.

## Boundaries

The original v0.3 boundary below predates the model and server workflows above.
Databricks-specific behavior, direct PostgreSQL/MySQL browsing, tracing
features and cross-target comparisons remain outside that browser scope. Other artifact providers
may work with a configured MLflow environment but were not tested. Browser and
clipboard actions depend on the OS's available desktop commands.
