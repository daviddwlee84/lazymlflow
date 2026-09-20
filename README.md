# lazymlflow

A keyboard-driven Go CLI/TUI for browsing MLflow experiments, comparing runs,
and downloading artifacts. Remote tracking uses the public REST API. Local
`mlruns` and SQLite targets run an owned, temporary **official MLflow server**;
lazymlflow does not implement MLflow's storage format or database schema.

## Build and run

Requires Go 1.25 or newer. macOS and Linux are the primary supported platforms.

```sh
go build -o bin/lazymlflow ./cmd/lazymlflow
./bin/lazymlflow --help
./bin/lazymlflow --tracking-uri https://mlflow.example.com
```

Install from a checkout with `go install ./cmd/lazymlflow`. The main-package
install path is `github.com/daviddwlee84/lazymlflow/cmd/lazymlflow`; no published
release is required to build this checkout. Build a versioned binary with
`go build -ldflags '-X main.version=0.1.0' -o bin/lazymlflow ./cmd/lazymlflow`.

Remote metadata and proxied artifacts need only the Go binary. Local stores and
direct cloud artifact access require either [uv](https://docs.astral.sh/uv/)
(`uvx`) or an existing Python environment containing MLflow. The default uv
runtime pins MLflow **3.16.1**; it prepares dependencies on first use. Specify a
matching existing environment for an older database; it is never modified or
upgraded by lazymlflow.

## Targets

```sh
lazymlflow targets add lab --uri https://mlflow.example.com
lazymlflow targets add legacy --uri ./mlruns --python ./.venv
lazymlflow targets add local --uri sqlite:///mlflow.db --python ./.venv/bin/python
lazymlflow targets default lab
lazymlflow targets test local
lazymlflow --target lab
```

`targets add` without business arguments opens a form in a terminal.
Use `targets add --help` for environment, authentication, and artifact options.
Explicit `--interactive` opens a prefilled add/edit form. Partial command-line
arguments without `--interactive` produce a usage error instead of a prompt.
The CLI and dashboard share the same form; Ctrl+O reveals advanced runtime,
artifact and authentication settings before the final review.

Configuration is `$XDG_CONFIG_HOME/lazymlflow/config.toml`, falling back to
`~/.config/lazymlflow/config.toml` on macOS/Linux. `--config` selects another
file. Targets have stable IDs independent of their display names:

```toml
default_target = "lab"

[[targets]]
id = "lab"
name = "Research server"
tracking_uri = "https://mlflow.example.com/tracking"
web_url = "https://mlflow.example.com/tracking"
token_env = "LAB_MLFLOW_TOKEN"

[[targets]]
id = "local"
tracking_uri = "sqlite:////absolute/project/mlflow.db"
working_dir = "/absolute/project"
python = "/absolute/project/.venv/bin/python"

[[targets]]
id = "legacy"
tracking_uri = "file:///absolute/project/mlruns"
mlflow_version = "3.12.0"

[tui]
metric_columns = ["loss", "accuracy"]
parameter_columns = ["learning_rate"]
refresh_seconds = 0
```

Selection precedence: explicit `--target` or `--tracking-uri` (mutually
exclusive), `LAZYMLFLOW_TARGET`, `MLFLOW_TRACKING_URI`, saved default, first
configured target. An explicitly selected broken target never falls back.
Temporary URI overrides are not saved. Relative paths entered through `targets
add` are made absolute; relative paths in handwritten configuration resolve
against its directory, or explicit `working_dir`.

Credentials use environment references: `token_env`, `username_env`,
`password_env`; a configured Basic username/password takes precedence over
token. `ca_file` supplies a private CA. Ambient `MLFLOW_TRACKING_*` credentials
are honored for temporary URI targets, and are isolated from named profiles.
Provider variables such as `AWS_PROFILE` and `MLFLOW_S3_ENDPOINT_URL` work as
with MLflow. Optional `env` maps child-variable names to environment-variable
references, and `extra_packages = ["boto3"]` adds packages to a uv runtime.
Never store credential values in target URLs.

`config show --json` displays redacted effective configuration. `doctor` tests
the selected connection and reports the actual version; `doctor --offline`
only inspects configuration and available tools.

## TUI

Run `lazymlflow` in a terminal. Experiments, runs and the selected run's details
stay visible at wider sizes; a narrow terminal shows the active pane. Each
target retains its own selections, filters and comparison basket for the
session.

| Key | Action |
| --- | --- |
| Arrows / `j k`, `gg` / `G` | Navigate |
| Tab / Shift+Tab | Change pane |
| `h l` | Change pane, or parent/enter in artifacts |
| `t` | Target picker; `a` add and `e` edit inside picker |
| `/` | Search already loaded rows; Enter accepts, Esc clears |
| `f` | Submit a server-side MLflow filter |
| `s` | Set server-side sort order |
| `n` | Load next page |
| `r` | Refresh; previous rows survive a failed refresh |
| Space / `c` | Select runs / open comparison |
| `x` | Compare only differences |
| `m` | Choose a metric and load history |
| `v` | Run columns, or toggle comparison table/chart |
| `a` in chart | Switch step / elapsed-time axis |
| `[` / `]` | Change details tab |
| `d` / `D` / Ctrl+X | Download selection / current directory / cancel download |
| `o` / `y` | Open selected resource in browser / copy full ID or path |
| `?` / `:` | Context help / action menu |
| Esc / `q` | Back / quit |

Text fields own printable keys; typing `q`, `j` or `/` never activates a
navigation shortcut. The help/footer describe the actions valid in the current
context. A `*` in the pane title marks focus even without color. Colors are
optional (`NO_COLOR`); Nerd Fonts are not required.

Comparison works across experiments **within one target**. Tables show latest
values returned by MLflow, not the best historical value. Missing metrics are
`—`; NaN/infinities stay distinct. History retains repeated steps and plots by
step or elapsed time. Display sampling does not change the underlying metric
values. A selected metric's direction is never assumed to mean better/worse.

## CLI

```sh
lazymlflow experiments list --all --json
lazymlflow runs list 1 --filter 'metrics.loss < 0.4' \
  --order-by 'metrics.loss ASC' --metrics loss,accuracy --all
lazymlflow runs get RUN_ID --json
lazymlflow runs compare RUN_A RUN_B --differences --json
lazymlflow metrics history RUN_ID loss --json
lazymlflow artifacts ls RUN_ID
lazymlflow artifacts download RUN_ID model --dest ./downloaded-model
lazymlflow open RUN_ID
lazymlflow open --experiment 1 --print
lazymlflow completion zsh
```

`--limit` controls server page size; default is 100. List JSON contains the
server continuation token. Use `--page-token` or `--all` to continue; sorting
and MLflow filters apply on the server across the entire query. Parameters and
tags follow MLflow's string semantics. Filter grammar is MLflow's SQL subset,
not general SQL (for example, `AND` is supported but `OR` is not).

`--json` prints data only on stdout and structured errors on stderr. No TTY or
JSON invocation prompts. Exit codes: 0 success, 1 operation failure, 2
usage/configuration error, 130 cancellation. A bare non-TTY invocation prints
help. Browser-opening `open` is interactive in effect; for a finite remote URL
use `open --print --json`.

## Local servers and artifacts

Local stores must already exist. SQLite is opened read-only, including the
registry connection: incompatible schemas fail with a runtime-selection hint.
lazymlflow never runs database migrations. Legacy FileStore uses MLflow's
compatibility setting; the official runtime may create its `.trash`/`models`
administrative directories. The app exposes no tracking write commands; the
original MLflow web UI has its own controls.

A local server listens only on `127.0.0.1`, with one worker. The TUI starts it
in the background on first use and reuses it until exit. CLI queries keep it
alive through the complete query/download, then stop it. Local `open` stays in
the foreground until Ctrl+C so the browser's URL remains usable. Existing
remote/Compose servers are never stopped. No daemon or persistent background
server is installed.

Artifact routing uses the run's actual `artifact_uri`. HTTP proxy downloads use
Go; direct repositories use the official MLflow artifact CLI in the selected
environment. An existing server-side `file://` location cannot be read from a
remote computer: configure artifact proxying or use a local target with access
to the store. For a local database containing `mlflow-artifacts:` URIs, set
`artifacts_destination` to the **original** server's artifact destination.
Starting a server does not relocate or rewrite recorded artifact URIs.

`--dest` names the exact output file/directory, not its parent. Transfers stage
beside the destination and publish only on success. Existing destinations need
explicit `--overwrite`; canceled/failed transfers preserve them. Local files,
proxy artifacts and S3/MinIO are the first-version provider scope; other MLflow
providers can be supplied through a configured environment. Dataset/logged-model
comparison, registry management and Databricks-specific behavior are outside
this version.

## Development and verification

```sh
go vet ./...
go test -race ./...
go build -o bin/lazymlflow ./cmd/lazymlflow
python3 scripts/pty_smoke.py --binary ./bin/lazymlflow
uv run --no-project --with mlflow==3.16.1 python scripts/integration_mlflow.py
uv run --no-project --with mlflow==3.16.1 --with boto3 python scripts/integration_s3.py
```

The real-MLflow test creates disposable file/SQLite stores, verifies query and
download behavior, and checks that SQLite bytes remain unchanged. Run it with
MLflow 2.22.0, 3.12.0 or 3.16.1 in isolated environments. The PTY test uses only
disposable HTTP fixtures and temporary configuration. The optional S3 test needs
Docker and starts a temporary MinIO container to exercise proxy and direct
artifact downloads with identical data.

See [the verification record](docs/verification.md) for tested versions,
platforms, scenarios and first-version boundaries.
