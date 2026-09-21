package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
)

const MLflowVersion = "3.16.1"
const MLflowImage = "ghcr.io/mlflow/mlflow:v3.16.1@sha256:6b6ec62130dd9a273b24e53e905bdc728b915fbd2dfb71f2fb7eff9a22f7fa67"
const PostgresImage = "postgres:16.15-bookworm@sha256:efedf3595f1d6f415c08568ba171029bf54052e754cc9f030e3f2412b21f3d67"
const RustFSImage = "rustfs/rustfs:1.0.0@sha256:8cc9801755448b71a786705ce76692c77e14936cccd87cf2fc31842e58f4d1ff"
const SeaweedFSImage = "chrislusf/seaweedfs:4.47@sha256:ce9e796f1fe6f06968f4c04bdaf8f678dad9c8acdfef3d244133d71bfa6bf882"
const CaddyImage = "caddy:2.11.4-alpine@sha256:de23def33b17fb5d1290b0f6c2add1d70780e52341896c00a4c8a2a2fe9d355e"

//go:embed templates/*
var templates embed.FS

func render(stack *Stack) (map[string][]byte, error) {
	s := stack.Spec
	labels := map[string]string{"io.lazymlflow.owner": stack.Project, "io.lazymlflow.server": s.ID, "io.lazymlflow.stack-directory": "${PWD}"}
	services := map[string]any{}
	volumes := map[string]any{"mlflow-data": map[string]any{"labels": labels}}
	baseEnv := map[string]string{"BACKEND_KIND": s.Backend, "ARTIFACT_KIND": s.Artifacts, "AUTH_MODE": s.Auth, "MLFLOW_AUTH_ADMIN_USERNAME": s.AdminUsername, "MLFLOW_SERVER_ALLOWED_HOSTS": "localhost:*,127.0.0.1:*,[::1]:*,mlflow:5000", "MLFLOW_ENABLE_PROXY_MULTIPART_UPLOAD": "false", "MLFLOW_ENABLE_PROXY_MULTIPART_DOWNLOAD": "false", "MLFLOW_BASIC_AUTH_FAIL_CLOSED": "true"}
	baseEnv["MLFLOW_SERVER_ENABLE_JOB_EXECUTION"] = "false"
	baseEnv["MLFLOW_SERVER_JOB_ENABLE_PERIODIC_TASKS"] = "false"
	baseEnv["MLFLOW_AUTH_ADMIN_USERNAME"] = strings.ReplaceAll(s.AdminUsername, "$", "$$")
	if s.Hostname != "" {
		host := s.Hostname
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		baseEnv["MLFLOW_SERVER_ALLOWED_HOSTS"] += "," + host + ":*"
	}
	if s.BindAddress != "0.0.0.0" && s.BindAddress != "::" {
		baseEnv["MLFLOW_SERVER_ALLOWED_HOSTS"] += "," + s.BindAddress + ":*"
	}
	if isS3(s.Artifacts) {
		baseEnv["AWS_DEFAULT_REGION"], baseEnv["S3_BUCKET"] = s.S3Region, s.S3Bucket
		baseEnv["MLFLOW_ARTIFACTS_DESTINATION"] = "s3://" + s.S3Bucket + "/artifacts"
		baseEnv["MLFLOW_BOTO_CLIENT_ADDRESSING_STYLE"] = "path"
		if s.Artifacts == "s3" {
			if s.S3Endpoint != "" {
				baseEnv["MLFLOW_S3_ENDPOINT_URL"] = strings.ReplaceAll(s.S3Endpoint, "$", "$$")
			}
		} else {
			baseEnv["MLFLOW_S3_ENDPOINT_URL"] = "http://storage:9000"
		}
	} else {
		baseEnv["MLFLOW_ARTIFACTS_DESTINATION"] = "/mlartifacts"
	}
	appVolume := []any{"mlflow-data:/data"}
	if !isS3(s.Artifacts) {
		if s.ArtifactPath != "" {
			appVolume = append(appVolume, bind(s.ArtifactPath, "/mlartifacts", false))
		} else {
			appVolume = append(appVolume, "artifacts:/mlartifacts")
			volumes["artifacts"] = map[string]any{"labels": labels}
		}
	}
	app := func(action string) map[string]any {
		return map[string]any{"image": stack.Project + "-mlflow:" + MLflowVersion, "build": map[string]any{"context": ".", "dockerfile": "Dockerfile"}, "pull_policy": "never", "command": []string{"python", "/opt/lazymlflow/runtime.py", action}, "environment": baseEnv, "env_file": []any{envFile("secrets/server.env")}, "volumes": appVolume, "labels": labels, "init": true}
	}
	if s.Backend == "postgres" {
		volumes["postgres-data"] = map[string]any{"labels": labels}
		services["postgres"] = map[string]any{"image": PostgresImage, "environment": map[string]string{"POSTGRES_USER": "mlflow", "POSTGRES_DB": "mlflow"}, "env_file": []any{envFile("secrets/postgres.env")}, "volumes": []string{"postgres-data:/var/lib/postgresql/data"}, "healthcheck": map[string]any{"test": []string{"CMD-SHELL", "pg_isready -U mlflow -d mlflow"}, "interval": "5s", "timeout": "3s", "retries": 30}, "restart": "unless-stopped", "labels": labels}
	}
	if s.Artifacts == "rustfs" || s.Artifacts == "seaweedfs" {
		volumes["storage-data"] = map[string]any{"labels": labels}
		storage := map[string]any{"volumes": []string{"storage-data:/data"}, "env_file": []any{envFile("secrets/storage.env")}, "restart": "unless-stopped", "labels": labels, "init": true}
		if s.Artifacts == "rustfs" {
			storage["image"] = RustFSImage
			storage["environment"] = map[string]string{"RUSTFS_ADDRESS": ":9000", "RUSTFS_CONSOLE_ENABLE": "false", "RUSTFS_REGION": s.S3Region}
			storage["healthcheck"] = map[string]any{"test": []string{"CMD", "curl", "--fail", "--silent", "http://127.0.0.1:9000/health"}, "interval": "5s", "timeout": "3s", "retries": 30, "start_period": "10s"}
		} else {
			storage["image"] = SeaweedFSImage
			storage["command"] = []string{"mini", "-dir=/data", "-s3.port=9000", "-webdav=false", "-admin.ui=false", "-s3.port.iceberg=0", "-s3.port.lance=0", "-master.telemetry=false", "-s3.autoCreateBucket=false", "-s3.allowDeleteBucketNotEmpty=false"}
			storage["healthcheck"] = map[string]any{"test": []string{"CMD", "curl", "--fail", "--silent", "http://127.0.0.1:9000/readyz"}, "interval": "5s", "timeout": "3s", "retries": 30, "start_period": "10s"}
		}
		services["storage"] = storage
	}
	deps := map[string]any{}
	if s.Backend == "postgres" {
		deps["postgres"] = condition("service_healthy")
	}
	if s.Auth == "native" {
		auth := app("auth-init")
		auth["env_file"] = []any{envFile("secrets/server.env"), envFile("secrets/bootstrap.env")}
		auth["restart"] = "no"
		if s.Backend == "postgres" {
			auth["depends_on"] = map[string]any{"postgres": condition("service_healthy")}
		}
		services["auth-init"] = auth
		deps["auth-init"] = condition("service_completed_successfully")
	}
	if isS3(s.Artifacts) {
		init := app("storage-init")
		init["restart"] = "no"
		d := map[string]any{}
		if s.Artifacts != "s3" {
			d["storage"] = condition("service_healthy")
		}
		if len(d) > 0 {
			init["depends_on"] = d
		}
		services["storage-init"] = init
		deps["storage-init"] = condition("service_completed_successfully")
	}
	ml := app("server")
	ml["restart"] = "unless-stopped"
	if len(deps) > 0 {
		ml["depends_on"] = deps
	}
	ml["healthcheck"] = map[string]any{"test": []string{"CMD", "python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:5000/health', timeout=3)"}, "interval": "5s", "timeout": "5s", "retries": 60, "start_period": "10s"}
	if s.TLS == "off" {
		ml["ports"] = []string{portMapping(s.BindAddress, s.Port, 5000)}
	}
	services["mlflow"] = ml
	files := map[string][]byte{}
	if s.TLS != "off" {
		volumes["caddy-data"] = map[string]any{"labels": labels}
		volumes["caddy-config"] = map[string]any{"labels": labels}
		gatewayVolumes := []any{bind("./Caddyfile", "/etc/caddy/Caddyfile", true), "caddy-data:/data", "caddy-config:/config"}
		tlsLine := "tls internal"
		if s.TLS == "provided" {
			tlsLine = "tls /etc/lazymlflow/tls_cert.pem /etc/lazymlflow/tls_key.pem"
			gatewayVolumes = append(gatewayVolumes, bind("./secrets/tls_cert.pem", "/etc/lazymlflow/tls_cert.pem", true), bind("./secrets/tls_key.pem", "/etc/lazymlflow/tls_key.pem", true))
		}
		files["Caddyfile"] = []byte(fmt.Sprintf("{\n  auto_https disable_redirects\n  admin off\n}\nhttps://%s {\n  %s\n  reverse_proxy mlflow:5000\n}\n", net.JoinHostPort(s.Hostname, "443"), tlsLine))
		services["gateway"] = map[string]any{"image": CaddyImage, "ports": []string{portMapping(s.BindAddress, s.Port, 443)}, "volumes": gatewayVolumes, "depends_on": map[string]any{"mlflow": condition("service_healthy")}, "restart": "unless-stopped", "labels": labels}
	}
	compose := map[string]any{"name": stack.Project, "services": services, "volumes": volumes, "networks": map[string]any{"default": map[string]any{"labels": labels}}}
	// JSON is a YAML 1.2 subset. Encoding structurally avoids shell/YAML injection
	// from paths and hostnames, and Docker Compose accepts this file directly.
	data, err := json.MarshalIndent(compose, "", "  ")
	if err != nil {
		return nil, err
	}
	files["compose.yaml"] = append(data, '\n')
	files["Dockerfile"] = []byte("FROM " + MLflowImage + "\nRUN pip install --no-cache-dir 'mlflow[auth]==3.16.1' 'psycopg2-binary==2.9.13' 'boto3==1.43.98'\nRUN mkdir -p /data /mlartifacts /opt/lazymlflow && chown -R 1000:1000 /data /mlartifacts\nCOPY runtime.py /opt/lazymlflow/runtime.py\nRUN chmod 0444 /opt/lazymlflow/runtime.py\nUSER 1000:1000\nWORKDIR /data\n")
	runtime, err := templates.ReadFile("templates/runtime.py")
	if err != nil {
		return nil, err
	}
	files["runtime.py"] = runtime
	preflight, err := templates.ReadFile("templates/preflight.py")
	if err != nil {
		return nil, err
	}
	files["preflight.py"] = preflight
	files[".env.example"] = []byte("# Client-side settings only. No credentials are stored in this example.\nMLFLOW_TRACKING_URI=" + stack.TrackingURI + "\n# MLFLOW_TRACKING_USERNAME=your-member-username\n# MLFLOW_TRACKING_PASSWORD=fill-securely-in-your-own-environment\n# MLFLOW_TRACKING_SERVER_CERT_PATH=/absolute/path/to/exported-ca.crt\n# Server credentials were generated privately under secrets/; never commit that directory.\n")
	files[".dockerignore"] = []byte("*\n!Dockerfile\n!runtime.py\n")
	files[".gitignore"] = []byte("secrets/\nca.crt\n*.log\n")
	files["README.md"] = []byte(generatedReadme(stack))
	return files, nil
}

func envFile(path string) map[string]string { return map[string]string{"path": path, "format": "raw"} }
func condition(s string) map[string]string  { return map[string]string{"condition": s} }
func bind(source, target string, readOnly bool) map[string]any {
	return map[string]any{"type": "bind", "source": strings.ReplaceAll(source, "$", "$$"), "target": target, "read_only": readOnly, "bind": map[string]bool{"create_host_path": false}}
}
func portMapping(host string, port, inside int) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s:%d:%d", host, port, inside)
}
func sortStrings(s []string) { sort.Strings(s) }

func generatedReadme(s *Stack) string {
	word := quoteShell(s.Dir)
	text := fmt.Sprintf("# %s MLflow server\n\nGenerated by lazymlflow. This is a single-host persistent stack, separate from client targets.\n\nTracking URL: %s\n\n```sh\nlazymlflow server up --dir %s\nlazymlflow server status --dir %s\nlazymlflow server logs --dir %s\nlazymlflow server down --dir %s\n```\n\n`down` preserves every volume. No lifecycle command migrates databases, moves artifacts, switches providers or deletes volumes. Generated configuration has an integrity receipt; generate a new stack for layout/version changes, then migrate explicitly.\n\nRequires Docker Engine and Compose 2.30+ (or Compose 5+). The Compose document uses JSON, valid YAML 1.2. The MLflow image installs pinned auth/PostgreSQL/S3 packages at build time and runs as UID/GID 1000. A bind-mounted artifact directory must be writable by that identity. No service other than the selected HTTP/HTTPS endpoint publishes a port.\n", s.Spec.ID, s.TrackingURI, word, word, word, word)
	if s.Spec.Auth == "native" {
		text += "\nNative authentication is enabled with default_permission=NO_PERMISSIONS. Bootstrap admin username: `" + s.Spec.AdminUsername + "`. Its initial password is in `secrets/admin_password` (private); ordinary server containers never receive this bootstrap secret. Initialization creates the account only if absent, never resets credentials or grants. Use the native `/admin` UI to manage members and roles. Clients use MLFLOW_TRACKING_USERNAME and MLFLOW_TRACKING_PASSWORD.\n"
	}
	if s.Spec.Auth == "off" {
		text += "\nAuthentication is explicitly off: anyone who can reach this endpoint has access.\n"
	}
	if s.Spec.TLS == "internal" {
		text += "\nTLS uses a private Caddy CA. `server up` exports its public root to `ca.crt`; `server ca-export` can copy it elsewhere. Import that public CA on training clients, or set MLFLOW_TRACKING_SERVER_CERT_PATH to its path. Never share the Caddy volume or CA private key. Do not disable certificate verification.\n"
	}
	if s.Spec.TLS == "off" {
		text += "\nTLS is explicitly off. Basic-auth credentials are not encrypted on plain HTTP.\n"
	}
	if s.Spec.Artifacts == "nas" {
		text += "\nOnly artifacts use the mounted NAS. PostgreSQL remains on a local Docker volume. `server up` verifies the configured mount and does a write/read check; a missing mount is an error. Mount credentials and UID/GID setup are operator managed. No recursive chown or mount command is performed.\n"
	}
	text += "\nArtifact transfers go through MLflow. Clients do not need S3 keys or NAS mounts. Direct/presigned multipart modes are disabled because internal storage addresses are not client endpoints. External S3 buckets are verified, never created or reconfigured.\n\nBackup both metadata/auth databases and artifacts consistently. Named volumes are persistence, not backups or high availability. Storage provider changes do not migrate existing experiment artifact locations.\n"
	if s.Spec.Artifacts == "rustfs" {
		text += "\nRustFS 1.0.0 is a recent stable release (2026-09-16); this template is not a production reliability claim. RustFS /data must not use NFS.\n"
	}
	text += "\nThe directory is portable to another Docker host. Copy it privately (including secrets/) and prepare any explicitly configured artifact/NAS paths on that host. From inside the copied directory run:\n\n```sh\npython3 preflight.py\ndocker compose up -d --build --wait\n```\n\nDo not skip the NAS preflight. Relative helper and TLS binds travel with the directory; data volumes do not. Copying a stack is not migrating its databases or artifacts. On the same Docker engine, use a new `server init` to create an independent stack; the generated project identity is retained by copies.\n"
	return text
}

func quoteShell(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
