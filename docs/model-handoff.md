# Model inspection and artifact handoff

lazymlflow can inspect model metadata, trace run relationships, and export the
original artifact bytes with a SHA-256 manifest. These operations do not load
models, import artifact Python, unpickle checkpoints, install dependencies, or
change the tracking server or registry.

```sh
lazymlflow models list --all
lazymlflow models versions classifier --all
lazymlflow models related RUN_ID --json
lazymlflow models inspect models:/classifier@champion --json
lazymlflow models export models:/classifier/7 --dest ./model-bundle
lazymlflow models verify ./model-bundle --manifest ./reviewed-model.json --json
```

`list` enumerates Registered Models; `versions` lists a named model's versions.
Both support `--limit`, `--page-token`, and `--all`; `list` also accepts the
server's `--filter`. These operations are read-only. `related` preserves a run's
MLflow 3 Logged Model inputs and outputs, including output step zero. The same
model may appear in both roles. Per-model resolution errors preserve the
available relationships and make the command return a failure.

## Selecting a source

| Source | Meaning |
| --- | --- |
| `runs:/RUN_ID/ARTIFACT_PATH` | A run artifact directory or a raw file |
| `models:/NAME/VERSION` | A numeric Registered Model version |
| `models:/NAME@ALIAS` | An alias, resolved once to a numeric version |
| `models:/m-MODEL_ID` | A Logged Model, introduced in MLflow 3 |

Registered Models, numeric versions, and Logged Models are distinct entities.
A single run can produce several Logged Models and consume models produced by
other runs. A Registered Model version may refer to a Logged Model, a run, or
another supported artifact repository. Inspection retains these relationships;
it does not replace a model's identity with its run name.

Aliases are mutable. An export records both the requested alias and the numeric
version resolved at the beginning, then downloads that exact version. The
registry's download-URI endpoint determines the storage location; a version's
historical `source` string is not assumed to be the download location.

Version numbers and Logged Model IDs identify the selected object, but do not
make the underlying storage immutable. File hashes in a reviewed manifest
identify the bytes. Legacy stages such as `Production`, implicit `latest`,
Databricks/Unity Catalog, and separate tracking/registry servers are outside
this interface. An older server without a required model endpoint reports an
unsupported operation rather than an empty model list.

## What inspection establishes

Inspection reads server metadata, the selected artifact root listing, and a
bounded `MLmodel` YAML document when present. It reports declared flavors,
signature, declared artifact/code references with presence checks, environment
references, and dependency declarations. Environment
inspection follows at most eight safe relative files, at most 1 MiB per file
and 4 MiB in total; external URLs and installer actions are not followed.
YAML aliases, anchors, custom tags, duplicate keys, excessive nesting, and
oversized documents are rejected. For direct storage, listing sizes are checked
before the official CLI downloads a metadata file, and bytes are checked again
before parsing; an inaccurate provider size can still cause a larger transfer.

`runtime_validation: "not_run"` is always explicit. A declared `python_function`
flavor is a **candidate** for MLflow serving, not a successful runtime test.
Missing `MLmodel` is normal for a custom checkpoint or raw artifact; it does not
make the bytes invalid. Code/serialized-file annotations explain which visible
files may require execution by a later consumer. Inspection never does so.

Proxied artifact access uses Go and the current target's authenticated/SSH
connection. Direct local or supported cloud repositories use the official
MLflow artifact CLI in the configured environment. A remote `file://` source
is rejected because the server's filesystem is not the client's filesystem.
Downloading uses the resolved storage URI, not the `models:/` wrapper, so the
MLflow client does not add a `registered_model_meta` file to the payload.

## Bundle and Git/CI contract

`--dest` is the exact output directory:

```text
model-bundle/
  payload/                  original files, unchanged
  manifest.json             lazymlflow-model-bundle/v1 receipt
```

The manifest stores the requested and resolved reference, target source key,
resolution/export times, and a sorted inventory of relative paths, byte sizes,
and SHA-256 hashes. It excludes credentials, machine-absolute storage paths,
signed URL query strings, and arbitrary remote tags/configuration. A raw file
is placed inside `payload/` under its original filename. Export stages the
complete bundle beside the destination; failure or cancellation preserves an
existing destination. Replacement requires `--overwrite`.

For Git-based delivery:

1. Inspect and export a chosen model. Review its metadata and file inventory;
   run the model-specific validation required by your project.
2. Save a reviewed `manifest.json` in your consumer repository. Committing it
   is the user's normal Git operation; lazymlflow does not write Git history.
3. In CI, use its exact `resolved_uri` rather than a mutable alias, export a
   fresh bundle, and run `models verify` against the independently committed
   manifest.
4. Pass verified `payload/` to the existing build or deployment pipeline.

`verify` is offline and does not load target configuration. It checks the exact
regular-file set, sizes, and SHA-256 hashes in `payload/`; changed, missing, or
additional files fail. Symlinks and non-regular files fail. It uses the supplied
expected manifest, never the new bundle's own receipt, and ignores observational
timestamps. `--json` reports `valid`, counts, and differences; mismatch returns
exit status 1. A manifest supplied from the same untrusted bundle only establishes
self-consistency; review/commit an independent copy for CI.

The manifest is a byte-delivery contract, not a signature, training-provenance
attestation, promotion approval, runtime compatibility test, or deployment
receipt. Native/embedded consumers can keep their own weight conversion,
ordered-feature contracts, golden-vector checks, release locks, and deployment
journal. Python/Hugging Face consumers still validate code, dependencies, model
format, and hardware with their existing tools. No project-specific model
format, private catalog, or trading-runtime integration is built into lazymlflow.

## Serving feasibility study

Serving is an explicit synthetic experiment, not a lazymlflow product command:

```sh
uv run --no-project scripts/serving_feasibility.py --json
```

The script pins MLflow 3.16.1, scikit-learn 1.7.2, and pandas 2.3.3. It first
compares local prediction with a separate `mlflow models predict --env-manager uv`
invocation that recreates the recorded environment under a temporary
`MLFLOW_ENV_ROOT`. It then creates
three small models and starts one temporary loopback HTTP server at a time:

| Synthetic case | Contract checked |
| --- | --- |
| scikit-learn regression | Standard MLflow flavor returns the expected prediction |
| Stateless `PythonModel` | Custom PyFunc returns a structured prediction |
| Explicit recurrent state | Input carries `hidden`; output carries `next_hidden`; repeated identical requests agree |

Observed on 2026-09-21 with Python 3.12.10: all three cases passed. The
regression and stateless model returned approximately 6; recurrent steps returned
`(prediction=2, next_hidden=1)` then `(prediction=3, next_hidden=1.5)`. Repeating
the same recurrent request returned the same first result. Local prediction and
prediction in the separately recreated uv environment both returned approximately
6. The HTTP server rejected missing input fields, non-numeric values for the
recorded numeric signature, and inconsistent input shape. HTTP serving itself
used `--env-manager local`; the separate uv test established environment recreation
for one synthetic scikit-learn fixture.

Readiness waits are bounded at 60 seconds, the environment recreation subprocess
is bounded at 240 seconds, HTTP calls time out, and process groups/temp
files are cleaned up on success or failure. The explicit-state case demonstrates
caller-owned state between requests; it does not implement stream identity,
worker affinity, concurrent state updates, backpressure, batching, or reset
policy. This is no latency/throughput benchmark and does not evaluate private
models, hardware serving, production reliability, or parity with a native
runtime. Those require a separate serving design.

## Verification and upstream contracts

```sh
go test ./internal/models ./internal/mlflow ./internal/cli
uv run --no-project --with mlflow==3.16.1 python scripts/integration_models.py
uv run --no-project scripts/serving_feasibility.py --json
```

The handoff integration uses only synthetic artifacts and a disposable SQLite
tracking server. Its intentionally invalid pickle verifies that inspection and
export do not deserialize model objects. It exercises run, logged-model,
registered-version and alias references, relationships, direct local and proxy
transport, exact bytes, and independent-manifest tamper detection.

Sources: [MLflow model/tracking concepts](https://mlflow.org/docs/latest/ml/tracking/),
[Registry REST API](https://mlflow.org/docs/latest/api_reference/rest-api.html),
[MLflow 3.16.1 Logged Model wire contract](https://github.com/mlflow/mlflow/blob/v3.16.1/mlflow/protos/service.proto),
[Registry artifact resolution and wrapper metadata](https://github.com/mlflow/mlflow/blob/v3.16.1/mlflow/store/artifact/models_artifact_repo.py).
Logged Model routes are marked `PUBLIC_UNDOCUMENTED` in the pinned protobuf;
compatibility is covered by versioned fixtures and the real 3.16.1 integration.
