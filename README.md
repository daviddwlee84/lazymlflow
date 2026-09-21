# lazymlflow

A keyboard-driven Go CLI/TUI for browsing MLflow experiments, comparing runs,
and downloading artifacts, with mouse navigation, resizable panes, per-experiment
views, nested runs, dataset/feature inspection, local Markdown notes, and
evidence-based summaries and reusable prompts. Remote tracking uses the public REST API. Local
`mlruns` and SQLite targets run an owned, temporary **official MLflow server**;
lazymlflow does not implement MLflow's storage format or database schema.

Tagged [GitHub releases](https://github.com/daviddwlee84/lazymlflow/releases) provide macOS/Linux amd64/arm64 archives, SHA-256 checksums, and Bash/Zsh completions. Verify the matching archive against `checksums.txt` before installing it. Source release archives and Go module downloads omit conversation records and agent plans while retaining build resources. See [RELEASING.md](RELEASING.md).

Optional workflows recommend and create persistent MLflow servers, prepare
training environments, inspect registered/logged models, and export model
artifacts with manifests that CI can verify. Browsing remains read-only;
server provisioning is an explicit, separate operation.

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
`go build -ldflags '-X main.version=v0.1.0' -o bin/lazymlflow ./cmd/lazymlflow`.

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
lazymlflow targets add remote-ssh --uri http://127.0.0.1:8000 --ssh-host my-ssh-alias
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

An optional `ssh_host` uses an existing OpenSSH alias. The tracking URI is
interpreted from that host, so `http://127.0.0.1:8000` means its loopback service.
lazymlflow owns the forwarding connection and a scoped loopback proxy, shared by
REST, artifact commands and browser access. HTTPS retains the upstream hostname
and CA checks. Configure a working `ssh my-ssh-alias` login first: the application
uses noninteractive authentication, requires an already trusted host key, and
does not alter SSH configuration or reuse/stop your control-master connections.

MLflow 3.5+ may reject direct requests with an **Invalid Host header** error.
The server administrator can add the exact IP/hostname and port to
`MLFLOW_SERVER_ALLOWED_HOSTS` or `--allowed-hosts`, preserving the existing
allowlist. An SSH target also works with a server that accepts only loopback
access. The application does not automatically modify remote server settings.

`config show --json` displays redacted effective configuration. `doctor` tests
the selected connection and reports the actual version; `doctor --offline`
only inspects configuration and available tools.

### Training with a target

```sh
lazymlflow targets env lab --shell sh > mlflow-env.sh
. ./mlflow-env.sh
python train.py

# Or keep the environment scoped to one process:
lazymlflow targets exec lab -- python train.py
lazymlflow targets env lab --json
```

Shell output uses credential **references**, never resolved passwords or tokens.
Set the referenced variables in your shell before sourcing it. JSON separates
literal values, environment references and variables to clear. `sh` output works
with sh, bash and zsh. Unrelated provider settings remain inherited; selected
target settings override conflicting MLflow connection settings.

Named Basic-auth profiles require both username/password references to resolve
to nonempty values. The MLflow SDK can also read `~/.mlflow/credentials`;
environment output documents that precedence, and `targets exec` rejects a
conflicting saved Basic pair before starting a named token/no-auth command.
Temporary `--tracking-uri` commands retain the SDK's ambient credential behavior.

For SSH targets, use `targets exec`: it keeps the owned tunnel alive until the
training command exits, forwards terminal input/signals, and preserves the
child's exit code. Existing file/SQLite targets are readonly browsing adapters;
create a persistent HTTP server below for training. In the dashboard, `E` previews
and copies the selected target's environment or its SSH execution command.

## Persistent server setup

```sh
lazymlflow server recommend --remote-training --json
lazymlflow server init                         # terminal wizard
lazymlflow server init personal --dir ./mlflow-server --register-target
lazymlflow server up --dir ./mlflow-server
lazymlflow server status --dir ./mlflow-server
lazymlflow server logs --dir ./mlflow-server
lazymlflow server down --dir ./mlflow-server    # preserves data volumes
```

The dashboard uses the same wizard through `t`, then `s` (or Ctrl+N). Choose the
training setup, adjust storage/access, review, and optionally start the stack or
save a target. Back preserves the draft; nothing is created before submission.
Generation works without Docker. Lifecycle commands require Docker and a
generated stack directory. The persistent services continue after lazymlflow exits.

| Need | Recommended setup |
| --- | --- |
| One person on one machine | SQLite and proxied local artifacts |
| Shared or remote training | PostgreSQL and proxied server-local artifacts |
| Existing mounted NAS | Local PostgreSQL and proxied NAS artifacts |
| Existing S3 service | PostgreSQL and the existing endpoint/bucket |
| Managed S3 with no preference | RustFS; SeaweedFS is also supported |

LAN access defaults to HTTPS and native MLflow users/roles. TLS and authentication
are independent options; an explicit `--tls off` or `--auth off` is supported for
trusted environments. Native permissions default to `NO_PERMISSIONS`; use the
MLflow Admin UI to create members and grant access. The bootstrap password is
stored in a private file, and restarting preserves existing passwords and grants.

Training hosts connect to the tracking endpoint; artifact proxying means they
normally need neither NAS mounts nor S3 credentials. A NAS filesystem is an
artifact directory, not a generic replacement for object-store/database disks.
Generated services use pinned images and persistent volumes; moving existing
experiments, changing providers and database upgrades are separate operations.
See [self-hosting](docs/self-hosting.md) for Compose profiles, CA trust, storage
requirements, optional security settings and backup boundaries.

## Models and reproducible artifact handoff

```sh
lazymlflow models list
lazymlflow models versions example
lazymlflow models related RUN_ID
lazymlflow models inspect models:/example@candidate --json
lazymlflow models export models:/example/3 --dest ./model-bundle
lazymlflow models verify ./model-bundle --manifest ./reviewed-manifest.json
```

Sources can be run artifact paths (`runs:/RUN_ID/model`), registered model
versions, aliases, or MLflow 3 Logged Model IDs (`models:/m-...`). An alias is
resolved once; manifests record the selected numeric version. Inspection reads
bounded metadata, flavors, signatures and environment references without loading
weights, importing model code or installing its dependencies.

`export` preserves original files in `payload/` and writes a separate manifest
with source identity and each file's size/SHA-256. Review and commit that manifest
if desired; CI can export from its resolved source and `verify` against the
committed manifest. Verification catches changed, missing and extra files,
including when a remote URI has been overwritten. It does not establish model
quality, runtime compatibility or production deployment.

In the TUI, `O` opens registered models, `C` shows models related to the selected
run, and Ctrl+O accepts an explicit source. Enter opens versions/inspection;
`e` reviews and exports a bundle, `b` returns to the model list, and Esc returns
to the original browser context.

Complete MLflow packages with a `python_function` flavor are candidates for
generic serving. Raw weights still need their model architecture, preprocessing
and inference code. [Model handoff and serving](docs/model-handoff.md) covers
the Git/CI workflow and the bounded synthetic serving study; no production model
serving or deployment controller is included.

## TUI

Run `lazymlflow` in a terminal. Experiments, runs and the selected run's details
stay visible at wider sizes; a narrow terminal shows the active pane. Each
target retains its own selections, filters and comparison basket for the
session. Pane proportions, mouse preference, experiment-specific views and
local visibility choices persist across launches.

| Key | Action |
| --- | --- |
| Arrows / `j k`, `gg` / `G` | Navigate |
| Tab / Shift+Tab | Change pane |
| `1` / `2` / `3` | Focus experiments / runs / details |
| `z` / `i` | Zoom focused pane / show full experiment information |
| Ctrl+W, then `h j k l` | Resize panes; `0` resets, Enter/Esc finishes |
| `M` | Toggle mouse capture (`--mouse=false` disables it at launch) |
| `h l` | Change pane, or parent/enter in artifacts |
| `t` | Target picker; `a` add and `e` edit inside picker |
| Ctrl+N / `t`, then `s` | Set up a persistent MLflow server |
| `E` / `O` / `C` | Target environment / registered models / selected run's models |
| `/` | Search already loaded rows; Enter accepts, Esc clears |
| `f` | Submit a server-side MLflow filter |
| `s` | Searchable sort picker; Enter sets primary, Space adds secondary |
| `b` | Parameter groups, nested/flat mode and default expansion |
| `L` / `V` | Layout / local visibility menus |
| `H` / `X` / `U` | Hide / archive / restore the selected item locally |
| `n` | Load next page |
| `A` / Ctrl+X | Load all matching pages / cancel background collection |
| `r` | Refresh; previous rows survive a failed refresh |
| Space / `c` | Select runs / open comparison |
| `x` | Compare only differences |
| `m` | Choose a metric and load history |
| `v` | Searchable column checkboxes, or toggle comparison table/chart |
| `a` in chart | Switch step / elapsed-time axis |
| `[` / `]` | Change details tab |
| `P` | Inspect a parent run that is outside the loaded results |
| `B` / `N` / `S` | Dataset workspace / local journal / summary and prompt |
| `d` / `D` / Ctrl+X | Download selection / current directory / cancel download |
| `o` / `y` | Open selected resource in browser / copy full ID or path |
| `?` / `:` | Context help / action menu |
| Esc / `q` | Back / quit |

Text fields own printable keys; typing `q`, `j` or `/` never activates a
navigation shortcut. The help/footer describe the actions valid in the current
context. In Runs, `[`/`]` pan additional columns while keeping Run name visible.
Mouse clicks select rows and activate the visible controls, the wheel scrolls
the hovered pane, and dragging a pane divider or column edge resizes it. Menus
consume mouse events so clicks cannot activate rows underneath. A `*` in the
pane title marks focus even without color. Colors are
optional (`NO_COLOR`); Nerd Fonts are not required.

Comparison works across experiments **within one target**. Tables show latest
values returned by MLflow, not the best historical value. Missing metrics are
`—`; NaN/infinities stay distinct. History retains repeated steps and plots by
step or elapsed time. Display sampling does not change the underlying metric
values. A selected metric's direction is never assumed to mean better/worse.

## Columns, sorting and grouping

The Columns menu groups attributes, native dataset inputs, parameters, metrics
and tags. Search the actual logged keys (including spaces such as `learning
rate`), press Enter/Tab to leave the search field, then Space to toggle a column.
Use `<`/`>` to reorder, `[`/`]` to adjust width and `n` for numeric interpretation.
Run name remains pinned; dataset columns can be restricted to an input context.
The Datasets detail tab shows every input, including its name, digest, context
and source. Multiple inputs are preserved rather than silently selecting one.

Every experiment remembers its own columns, widths, multi-column sort,
grouping, expansion, filter and visibility. Defaults still come from `[tui]`;
there is no assumption that unrelated experiments use the same parameter keys.

Sorting immediately previews the loaded rows. Supported metric/parameter/tag/
attribute ordering also fetches the server-sorted page in the background, while
navigation remains usable. Parameters/tags use MLflow string semantics unless
numeric interpretation is explicitly selected. Dataset, duration and numeric
parameter sorts are local and labeled **loaded rows**; `A` loads all matching
pages, with Ctrl+X cancellation. Missing values and NaNs are not replaced by
zero. Sorting ends with a stable run-ID tie-break.

The Group by menu can combine multiple exact parameter keys. Groups use raw
logged values, distinguish missing from empty, and show loaded counts; runs
inside each group use the selected sort. Groups do not claim aggregate metrics
or complete membership while pages remain unloaded.

Auto mode switches to a tree when a `mlflow.parentRunId` tag is present. `h/l`
collapses/expands nodes; default expansion is one level and can be changed to
collapsed or all. A parent missing from a page, excluded by a filter or locally
hidden becomes a context row without hiding its children. `P` inspects it.
Flat, tree and parameter-group modes are alternatives: flat shows the global
ranking, while a tree sorts siblings and parameter groups sort their members.

## Local preferences and visibility

Personal state is separate from every MLflow backend:
`$XDG_DATA_HOME/lazymlflow/state.db`, falling back to
`~/.local/share/lazymlflow/state.db`. It uses pure-Go SQLite (no CGo/Python),
transactional versioned setup and bounded lock waits. It contains preferences
and visibility entries plus local journal notes, not a replica of runs, metrics or
credentials. Local schema version 2 adds notes transactionally while preserving
version-1 views and visibility; it does not change any MLflow database.

Hidden/archived items are excluded from the normal view; `V` shows hidden,
archived or all items and `U` restores them. These actions never modify MLflow
lifecycle/tags or other users' web views. Hiding a parent does not hide its
children; hiding an experiment does not rewrite its runs. Source identity uses
the configured target/origin, not a temporary SSH/local-server port.

```sh
lazymlflow view set 11 --column dataset:name --column 'param:learning rate' \
  --column metric:valid_loss --sort 'metric:valid_loss ASC'
lazymlflow view set 11 --group-by optimizer --group-by 'learning rate'
lazymlflow view show 11 --json
lazymlflow view hide --run RUN_ID
lazymlflow view archive --experiment 11
lazymlflow view restore --experiment 11
lazymlflow view reset 11
lazymlflow runs list 11 --use-view --json
```

These local view commands work without connecting to MLflow. Ordinary CLI
queries retain their raw server behavior; `--use-view` explicitly applies
personal settings. Saved run views require one explicit experiment. Local
sorting never silently loads all pages: use `--all` for a complete matching set.

## Detailed inspection and metric histories

Metrics, Parameters and Tags are searchable, sortable tables. In the detail
pane, `/` searches the current table, `s` sorts, Enter opens the complete value
or metric curve, and `Y` copies that value (`y` still copies the run ID).
Selection, search and scroll survive switching tabs. Use `z` for more space.

The Metrics tab starts with model metrics; `e` cycles model/system/all. `v`
switches between table and dashboard, `p` overlays up to four selected metrics,
`m` picks a metric, and `a` switches step/elapsed-time axes. In an expanded
curve, Left/Right or `h/l` moves the sample cursor. First/last finite values,
minimum, maximum, sample counts, non-finite counts, step and timestamp ranges
are derived from the full history; server latest stays separate. Repeated steps
remain distinct and non-finite values create gaps. Curves use Braille with an
ASCII fallback (`u`). `R` toggles automatic metric refresh; it is off by default.
It uses `tui.refresh_seconds`, or five seconds when enabled without a configured
interval. Completed runs receive one final refresh; automatic mode stays enabled
and resumes when another running run is selected.
Background history loading is bounded, cancelable and isolated by source/run/key;
a failed refresh retains the prior usable history with its error.

The Datasets tab shows every logged input. `{` / `}` changes input, `/` searches
features, Space expands nested fields, Enter opens complete field details, and
`v` switches table/raw JSON. Tabular columns/leaves, logged profile row counts,
and tensor rank/shape/known dimensions are reported separately; a parameter such
as `feature_dim` is a logged parameter, not proof of the dataset's schema.
Malformed or missing schemas remain inspectable as raw data. Dataset files are
not opened to infer unlogged values.

## Dataset workspace

`B` opens Datasets → Related runs → Schema/Metadata/Notes within the current
target. It initially uses loaded runs and labels that scope. `A` explicitly scans
all accessible experiments/pages; Ctrl+X cancels while retaining partial results.
Progress, errors, completeness and lifecycle scope stay visible. `V` changes
active/all/deleted lifecycle; `H` includes locally hidden/archived relationships.
`/` searches the focused pane, `h/l` folds dataset names, Enter inspects a variant
or opens a related run, and `[`/`]` changes detail tabs. The usual pane focus,
zoom, resize and mouse controls remain available; `B` or Esc returns.

Variants combine the configured source, dataset name/digest, source type,
canonical source and schema. The same name alone does not establish identity.
Per-run context/profile are preserved as relationships, so training/test uses
of the same variant remain visible. A catalog is a metadata view, not a new
dataset registry or persistent copy of remote datasets.

```sh
lazymlflow datasets list --all --json
lazymlflow datasets list --experiment 11 --view all
lazymlflow datasets schema RUN_ID --index 1 --json
lazymlflow datasets runs RUN_ID --index 1 --experiment 11
```

Dataset input indexes are one-based. `datasets runs` uses the selected input as
a seed and finds the exact variant across the selected experiments (the whole
target by default). JSON includes scan scope/completeness and partial errors.

## Local journal

`N` opens notes for the selected experiment, run or dataset. Use `c` to compose,
`e` to edit, `/` to search, `y` to copy, `d` to soft-delete, `D` to include deleted
notes, and `u` to restore. The multiline editor saves with Ctrl+S. Ctrl+E hands
its draft to `$VISUAL`, then `$EDITOR`, then `vi`; returning does not save until
Ctrl+S. Unsaved changes require an explicit save/discard choice. Conflicting
revisions retain the draft rather than overwrite a newer note.

```sh
lazymlflow notes add --run RUN_ID --file - < observation.md
lazymlflow notes list --run RUN_ID --json
lazymlflow notes edit NOTE_ID --run RUN_ID --revision 1 --body 'Updated finding'
lazymlflow notes delete NOTE_ID --run RUN_ID --revision 2
lazymlflow notes restore NOTE_ID --run RUN_ID --revision 3
lazymlflow notes add --experiment 11 --body 'Experiment-level observation'
lazymlflow notes list --dataset-run RUN_ID --index 1
```

`--dataset DATASET_ID` uses the identity from `datasets list/schema` without
connecting; `--dataset-run` resolves it from a live run. Ordinary run/experiment
notes also work without connecting. Notes stay in local `state.db`, scoped to
source and entity identity; they do not modify MLflow tags. Editing, deleting
and restoring require the current revision from `notes list --json`.

## Summaries and reusable prompts

`S` collects a summary for the selected run/experiment, or the comparison's run
set. In the preview, `p` switches Markdown/prompt, `y` copies, `e` exports to a
new file, and `n` saves the report through the local note editor. Existing export
files are preserved. Ctrl+X cancels collection; nothing launches an agent.

```sh
lazymlflow runs summary RUN_ID --metric loss --history sampled > run.md
lazymlflow runs summary RUN_A RUN_B --history full --json > comparison.json
lazymlflow experiments summary 11 --filter 'metrics.loss < 0.4' \
  --detail-limit 20 > experiment.md
lazymlflow prompt list --json
lazymlflow prompt render run-summary RUN_ID > prompt.md
lazymlflow prompt render compare-runs RUN_A RUN_B --metric loss
lazymlflow prompt render experiment-summary 11 --all-details
```

Summaries default to Markdown; `--json` returns version-1 evidence. A snapshot
contains the configured origin without target authentication settings, complete
latest metrics/parameters/tags and logged dataset schema/profile, selected
histories, root artifact entries and active local notes. Artifact contents are
not fetched. `--metric` is repeatable and preserves exact keys. Automatic history
selection excludes `system/` unless `--include-system` is set; explicitly chosen
system keys are honored. `--history none|sampled|full` defaults to sampled with
up to 200 retained points per metric. Statistics always use the full retrieved
history and sampling/completeness are explicit.

Experiment summaries scan **all matching metadata pages**, then retrieve details
for the first 20 runs in server order (start time descending by default).
`--filter`, `--order-by`, `--view`, `--detail-limit` and `--all-details` control
that scope; metadata-only runs are not presented as if their histories were
inspected. Required metadata failures fail the command; unavailable optional
histories/artifacts/notes remain explicit warnings. Collection uses at most four
concurrent I/O operations and responds to cancellation.

The report and prompt render the same captured context. Prompts separate observed
facts, inference and unknowns and treat all logged strings/local notes as data.
They do not assume metric direction, convergence, causality or model quality.
With `--json`, prompt rendering returns the recipe ID, context version and text;
rendering does not contact a model provider or execute the generated prompt.

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
lazymlflow never runs MLflow database migrations. Legacy FileStore uses MLflow's
compatibility setting; the official runtime may create its `.trash`/`models`
administrative directories. The app exposes no tracking write commands; the
original MLflow web UI has its own controls.

A local server listens only on `127.0.0.1`, with one worker. The TUI starts it
in the background on first use and reuses it until exit. CLI queries keep it
alive through the complete query/download, then stop it. Local `open` stays in
the foreground until Ctrl+C so the browser's URL remains usable. SSH `open`
has the same foreground lifetime; `--print` rejects ephemeral SSH URLs. Existing
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
providers can be supplied through a configured environment. Model Registry,
GenAI tracing, Databricks-specific behavior and cross-target comparisons remain
outside this version.

## Migration research boundary

No tracking-store migration or cross-server import command is implemented.
Local preferences/notes remain keyed by source plus entity identity; changing a
server does not automatically merge or reassign journals.

As reviewed on 2026-09-20:

- MLflow's official [`migrate-filestore`](https://mlflow.org/docs/latest/self-hosting/migrate-from-file-store/)
  requires MLflow 3.10+ and migrates FileStore metadata to an empty SQLite target.
  It preserves existing IDs/timestamps; artifacts keep their original URIs and
  are not copied. This is separate from merging two tracking servers.
- [User-specified run IDs, issue #12780](https://github.com/mlflow/mlflow/issues/12780)
  remains an open request. Server-to-server export/import creates destination IDs;
  any future journal transfer would need an explicit source-to-destination mapping.
- A complete PostgreSQL dump/restore into an empty compatible deployment can
  preserve stored identifiers—an engineering inference from PostgreSQL's
  [logical backup semantics](https://www.postgresql.org/docs/current/backup-dump.html),
  not a supported merge strategy. Artifact storage is a separate transfer concern.
- [`mlflow-export-import`](https://github.com/mlflow/mlflow-export-import)
  supports server transfers, but its model-input import issue
  [#250](https://github.com/mlflow/mlflow-export-import/issues/250) and proposed fix
  [#251](https://github.com/mlflow/mlflow-export-import/pull/251) were still open.
  No migration or fix from those projects was applied here.

## Development and verification

```sh
go vet ./...
go test -race ./...
go build -o bin/lazymlflow ./cmd/lazymlflow
python3 scripts/pty_smoke.py --binary ./bin/lazymlflow
python3 scripts/pty_inspection.py --binary ./bin/lazymlflow
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
