# Self-hosting an MLflow tracking server

`lazymlflow server` recommends a layout, generates a standalone Docker Compose
directory, and manages that directory's persistent services. Connection targets
remain separate: opening, closing, or removing a target never stops the server.
You can keep using an existing tracking server without generating a stack.

```sh
lazymlflow server recommend --remote-training --json
lazymlflow server init                 # guided setup in a terminal
lazymlflow server init personal --dir ./mlflow-local --start
```

Bare `server init` opens the same setup form as the dashboard's **Set up server**
action. Partial flags stay scriptable and require an ID and `--dir`; add
`--interactive` to prefill a form. `--json` and non-terminal invocations never
prompt. `--dry-run` validates a complete request without creating files or
contacting Docker. `--from-spec FILE` accepts the JSON emitted by `server spec`.

## Choose only the services you need

| Need | Suggested layout |
|---|---|
| Personal work on one machine | SQLite + local artifact volume |
| Concurrent training or a small team | PostgreSQL + local artifact volume |
| An existing, reliably mounted NAS | PostgreSQL on local disk + NAS artifacts |
| Existing AWS/S3-compatible storage | PostgreSQL + external bucket |
| A new S3 service with no provider preference | PostgreSQL + RustFS |
| SeaweedFS is preferred | PostgreSQL + SeaweedFS mini |

Remote training does not itself require S3. MLflow proxies artifact transfers,
so training clients need the tracking URL and their MLflow credentials, not
storage credentials or NAS mounts.

RustFS is the suggested managed S3 provider because it matches the current
[upstream MLflow Compose architecture](https://mlflow.org/docs/latest/self-hosting/).
It is not a claim that RustFS is more mature than alternatives: its 1.0.0 stable
release dates to 2026-09-16. SeaweedFS is an explicit alternative using the same
S3 integration. Both templates are single-host deployments, without replication
or high availability. Production capacity, backups and recovery remain operating
decisions.

The generated images are pinned by version **and multi-platform manifest digest**:

| Component | Version |
|---|---|
| MLflow | 3.16.1 |
| PostgreSQL | 16.15-bookworm |
| RustFS | 1.0.0 |
| SeaweedFS | 4.47 |
| Caddy | 2.11.4-alpine |

The custom MLflow image installs `mlflow[auth]==3.16.1`,
`psycopg2-binary==2.9.13`, and `boto3==1.43.98` during build, not at each startup.
The generated Dockerfile and Compose file record the actual pins. The templates
disable MLflow server job execution and periodic job scheduling; they provision
tracking, artifacts and native authorization, not a model-execution service.

MinIO Community is not a new-stack choice: its upstream repository is archived
and no longer maintained. Existing MinIO endpoints remain usable through the
external S3 option. RustFS and SeaweedFS use Apache-2.0; Garage and MinIO use
AGPLv3. These are license identifiers, not a determination of obligations for a
particular deployment. [MinIO status](https://github.com/minio/minio),
[RustFS](https://github.com/rustfs/rustfs),
[SeaweedFS](https://github.com/seaweedfs/seaweedfs).

## Training on another machine

```sh
lazymlflow server init team --dir ./mlflow-team \
  --backend postgres --artifacts rustfs \
  --access lan --hostname mlflow.example.internal \
  --start --register-target
```

LAN access defaults to native MLflow authentication and HTTPS with an internal
CA. The default HTTPS port is 8443; HTTP uses 8000 to avoid common local port 5000
conflicts. Set `--bind` to a particular host interface if needed. Make the
hostname resolvable from the training machines. The only published port belongs
to MLflow or its HTTPS gateway: database, S3, and storage administration ports are
not published. Host-header validation stays enabled.

TLS and authentication are independent choices. `--tls off` and `--auth off`
are supported explicit opt-outs, visible in the setup review and generated
instructions. Authentication off permits any reachable client to access the
server. Basic authentication on plain HTTP does not encrypt credentials.

For an existing certificate use `--tls provided --cert-file server.crt
--key-file server.key`. Initialization verifies that the pair matches and copies
it into private stack files. It does not change your certificate authority,
system trust store, DNS, firewall, or network mounts.

After an internal-TLS stack starts, its **public root certificate** is exported
to `<stack>/ca.crt`:

```sh
lazymlflow server ca-export --dir ./mlflow-team --output ./team-ca.crt
```

Distribute that public certificate to training clients. A Python client can use:

```sh
export MLFLOW_TRACKING_URI=https://mlflow.example.internal:8443
export MLFLOW_TRACKING_SERVER_CERT_PATH=/absolute/path/to/team-ca.crt
export MLFLOW_TRACKING_USERNAME=your-member-username
# Set MLFLOW_TRACKING_PASSWORD securely in the training environment.
```

CA export never exports the CA private key or changes a trust store. It refuses
to overwrite a different destination file. Its SHA256 fingerprint hashes the
DER certificate bytes (lowercase hexadecimal, not the PEM file text); compare
it through a trusted channel when distributing the root. Do not disable certificate
verification to work around an untrusted internal CA.

## Native users and permissions

`--auth native` creates a separate authentication database (`auth.db` for
SQLite; `mlflow_auth` alongside `mlflow` for PostgreSQL). The stable bootstrap
username defaults to `admin`; change it with `--admin-username` before creating
the stack. A random initial password is saved in `secrets/admin_password`, or
use `--admin-password-env NAME` to read a supplied password from an environment
variable. Passwords must have at least 12 characters. No secret value is printed
by lazymlflow, stored in a target, or included in a generation preview.

A one-shot initializer creates the administrator only when absent. It then
persists a bootstrap receipt. Later starts preserve passwords, role memberships
and grants, including a password changed through MLflow. If the initialized
administrator disappears or loses admin status, startup requests explicit
repair instead of recreating it. The ordinary server never receives the
bootstrap password. A persistent CSRF secret is shared across restarts.

Use MLflow's native `/admin` UI to manage members and roles. The generated policy
is `default_permission=NO_PERMISSIONS`; an administrator must grant the access a
new member needs. lazymlflow does not implement a parallel user/role database or
admin API. See [MLflow authentication](https://mlflow.org/docs/latest/self-hosting/security/basic-http-auth/)
and [RBAC](https://mlflow.org/docs/latest/self-hosting/security/role-based-access-control/).

`--register-target` adds a new connection profile using
`MLFLOW_TRACKING_USERNAME` and `MLFLOW_TRACKING_PASSWORD` references; it neither
stores their values nor changes the saved default target. Existing target IDs
are preserved. The internal-CA profile points to `<stack>/ca.crt`; that file
becomes available after the stack starts.

## NAS and external S3

```sh
lazymlflow server init nas-team --dir ./mlflow-nas \
  --backend postgres --artifacts nas \
  --nas-mount /mnt/research --artifact-path /mnt/research/mlflow-artifacts

lazymlflow server init object-team --dir ./mlflow-s3 \
  --backend postgres --artifacts s3 --s3-bucket existing-mlflow \
  --s3-endpoint https://s3.example.internal \
  --s3-access-key-env TEAM_S3_ACCESS_KEY --s3-secret-key-env TEAM_S3_SECRET_KEY
```

Leave the external endpoint blank for AWS S3 and set `--s3-region` as needed.
The referenced environment variables must exist at initialization; their values
are copied into private stack files. The external bucket must already exist.
Startup verifies access and never creates the external bucket or changes its
policy. Managed providers generate their own keys and initialize their own
bucket; endpoint readiness includes a signed bucket check, not just HTTP health.

For NAS, initialization and `up` verify an actual NFS or SMB/CIFS mount. The
recorded source/filesystem identity detects replacement by another share.
Missing mounts and missing artifact directories fail instead of silently
creating a local directory. `up` also performs a small write/read check. The
MLflow container runs as UID/GID 1000; prepare appropriate NAS permissions for
that identity. lazymlflow does not mount the share or recursively change its
ownership. PostgreSQL always keeps its data on local Docker storage in this
profile.

NAS is a direct MLflow artifact backend. Do not put a RustFS data directory on
NFS; [RustFS explicitly disallows that](https://docs.rustfs.com/en/installation/linux/prerequisites-and-service).
The SeaweedFS template also keeps its metadata and object data on local Docker
storage. A NAS with its own S3 service is an external S3 endpoint.

Direct/presigned multipart transfers are disabled: the generated storage
endpoint is internal to Compose and is not a training-client URL. Artifact
traffic consequently passes through the MLflow server, which needs adequate
network and disk bandwidth. A future deployment can separate the artifact proxy
or use directly reachable object storage, with explicit routing and access
configuration.

## Lifecycle and portability

```sh
lazymlflow server up --dir ./mlflow-team
lazymlflow server status --dir ./mlflow-team --json
lazymlflow server logs --dir ./mlflow-team --follow
lazymlflow server down --dir ./mlflow-team
lazymlflow server spec --dir ./mlflow-team --json
```

`down` stops this project and preserves every data volume. There is deliberately
no volume-deleting flag. Each generation has a unique Compose project identity;
ownership checks reject resources belonging to a different stack directory.
Remote Docker contexts are unsupported by these local lifecycle commands.
Cancelling an `up` stops waiting; services Docker already started may continue,
so inspect `status` and explicitly `down` when needed.

Generation needs no Docker daemon. Starting requires Docker Engine and Compose
2.30+ (or newer major releases). The generated `compose.yaml` uses JSON syntax,
a valid YAML 1.2 subset. Secret raw env files preserve literal dollar signs and
other password characters; they and the stack directory have private filesystem
permissions. The Docker build context excludes every credential file.

Copy the whole directory privately to another Docker host to use it without
lazymlflow. Relative helper/certificate binds remain portable; explicit NAS and
artifact paths must be prepared on the destination host. Run from that directory:

```sh
python3 preflight.py
docker compose up -d --build --wait
```

The host preflight must succeed before standalone Compose, particularly for NAS.
Copying configuration does not copy Docker volumes or migrate data. A copy retains
its project identity; on the same Docker engine, use a new `server init` for an
independent instance.

Generated configuration has an integrity receipt. The managed lifecycle rejects
changed layout/runtime files. For an edited, operator-managed Compose project,
operate it directly and take responsibility for its changes. Version upgrades,
database migrations, provider switches and moves of existing artifact data are
explicit operations outside this version. MLflow retains each existing
experiment's artifact location; changing a destination is not a migration.

Back up metadata, native-auth data and artifacts consistently, and test recovery.
Persistent volumes alone are not backups. See the
[MLflow backend-store](https://mlflow.org/docs/latest/self-hosting/architecture/backend-store/)
and [artifact-store](https://mlflow.org/docs/latest/self-hosting/architecture/artifact-store/) documentation.

## Verification

`go test ./internal/server ./internal/cli` covers validation, recommendations,
private/no-overwrite generation, template structure, lifecycle ownership,
redaction, missing NAS mounts and noninteractive command behavior.

```sh
go build -o bin/lazymlflow ./cmd/lazymlflow
python3 scripts/integration_server.py --binary ./bin/lazymlflow
```

The Docker test creates disposable unique projects and removes only their test
containers, networks, volumes and local image tags. It exercises SQLite,
PostgreSQL, RustFS, SeaweedFS, an external S3 endpoint backed by a separate
disposable RustFS fixture, SQLite/PostgreSQL native auth with internal TLS, provided TLS,
Unicode artifact roundtrips, multipart S3, registry persistence, password/CA
preservation, unauthorized artifact denial, and refusal to recreate a deleted
bootstrap administrator. A real NAS is not inferred from a temporary directory; missing
mount behavior has unit coverage, and a deployed NAS still needs its own
permissions and availability check.
