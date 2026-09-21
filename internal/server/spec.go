// Package server generates and operates explicitly owned, persistent MLflow stacks.
// It is separate from target connections and never manages an existing deployment.
package server

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

const SchemaVersion = 1

// ServerSpec contains configuration, never credential values. Paths are resolved
// at generation time. A generated stack is immutable; changes require a new stack.
type ServerSpec struct {
	SchemaVersion  int    `json:"schema_version"`
	ID             string `json:"id"`
	Backend        string `json:"backend"`   // sqlite, postgres
	Artifacts      string `json:"artifacts"` // local, nas, s3, rustfs, seaweedfs
	Access         string `json:"access"`    // loopback, lan
	TLS            string `json:"tls"`       // off, internal, provided
	Auth           string `json:"auth"`      // off, native
	Hostname       string `json:"hostname,omitempty"`
	BindAddress    string `json:"bind_address"`
	Port           int    `json:"port"`
	ArtifactPath   string `json:"artifact_path,omitempty"`
	NASMount       string `json:"nas_mount,omitempty"`
	CertFile       string `json:"cert_file,omitempty"`
	KeyFile        string `json:"key_file,omitempty"`
	S3Endpoint     string `json:"s3_endpoint,omitempty"`
	S3Bucket       string `json:"s3_bucket,omitempty"`
	S3Region       string `json:"s3_region,omitempty"`
	S3AccessKeyEnv string `json:"s3_access_key_env,omitempty"`
	S3SecretKeyEnv string `json:"s3_secret_key_env,omitempty"`
	AdminUsername  string `json:"admin_username,omitempty"`
}

type Requirements struct {
	Team              bool   `json:"team"`
	RemoteTraining    bool   `json:"remote_training"`
	ExistingNAS       string `json:"existing_nas,omitempty"`
	NeedObjectStorage bool   `json:"need_object_storage"`
	S3Endpoint        string `json:"s3_endpoint,omitempty"`
	Provider          string `json:"provider,omitempty"`
}

type Recommendation struct {
	Spec    ServerSpec `json:"spec"`
	Reasons []string   `json:"reasons"`
	Caveats []string   `json:"caveats"`
	Sources []string   `json:"sources"`
}

func DefaultSpec() ServerSpec {
	return ServerSpec{SchemaVersion: SchemaVersion, Backend: "sqlite", Artifacts: "local", Access: "loopback", TLS: "off", Auth: "off", BindAddress: "127.0.0.1", Port: 8000, AdminUsername: "admin"}
}

func Recommend(r Requirements) Recommendation {
	s := DefaultSpec()
	reasons := []string{"SQLite and proxied local artifacts are sufficient for one person on one machine."}
	if r.Team || r.RemoteTraining || r.ExistingNAS != "" || r.S3Endpoint != "" || r.NeedObjectStorage || r.Provider != "" {
		s.Backend = "postgres"
		reasons = []string{"PostgreSQL supports concurrent training clients; metadata stays on local persistent storage."}
	}
	if r.RemoteTraining {
		s.Access, s.TLS, s.Auth, s.BindAddress, s.Port = "lan", "internal", "native", "0.0.0.0", 8443
		reasons = append(reasons, "Remote clients use one HTTPS endpoint with native MLflow accounts; trust the exported CA on each client.")
	}
	switch {
	case r.S3Endpoint != "":
		s.Artifacts, s.S3Endpoint = "s3", r.S3Endpoint
		s.S3Bucket, s.S3Region = "mlflow", "us-east-1"
		s.S3AccessKeyEnv, s.S3SecretKeyEnv = "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"
		reasons = append(reasons, "Use the existing S3 service; its bucket and policies remain operator managed.")
	case r.ExistingNAS != "" && !r.NeedObjectStorage && r.Provider == "":
		s.Artifacts, s.ArtifactPath, s.NASMount = "nas", r.ExistingNAS, r.ExistingNAS
		reasons = append(reasons, "A mounted NAS can hold proxied artifacts without another object-storage service.")
	case r.NeedObjectStorage || r.Provider != "":
		s.Artifacts = "rustfs"
		if r.Provider == "seaweedfs" {
			s.Artifacts = "seaweedfs"
		}
		s.S3Bucket, s.S3Region = "mlflow", "us-east-1"
		reasons = append(reasons, "RustFS follows the upstream MLflow Compose architecture; SeaweedFS is an explicit alternative with the same S3 contract.")
	}
	caveats := []string{"Generated stacks are single-host deployments, not high availability.", "Provider changes, database upgrades and existing-data migrations are separate operator tasks."}
	if s.Artifacts == "rustfs" {
		caveats = append(caveats, "RustFS 1.0.0 was released on 2026-09-16; upstream adoption does not establish production maturity.")
	}
	return Recommendation{Spec: s, Reasons: reasons, Caveats: caveats, Sources: []string{"https://mlflow.org/docs/latest/self-hosting/", "https://github.com/rustfs/rustfs/releases/tag/1.0.0", "https://github.com/seaweedfs/seaweedfs/wiki/Quick-Start-with-weed-mini"}}
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Normalize is pure: it checks the requested layout without starting services,
// looking up credentials or probing a NAS. Init and Up perform filesystem checks.
func Normalize(s ServerSpec) (ServerSpec, error) {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	if s.SchemaVersion != SchemaVersion {
		return s, fmt.Errorf("unsupported server schema version %d", s.SchemaVersion)
	}
	if !idPattern.MatchString(s.ID) {
		return s, fmt.Errorf("server ID must be 1–40 lowercase letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	if s.Backend == "" {
		s.Backend = "sqlite"
	}
	if s.Artifacts == "" {
		s.Artifacts = "local"
	}
	if s.Access == "" {
		s.Access = "loopback"
	}
	if s.TLS == "" {
		if s.Access == "lan" {
			s.TLS = "internal"
		} else {
			s.TLS = "off"
		}
	}
	if s.Auth == "" {
		if s.Access == "lan" {
			s.Auth = "native"
		} else {
			s.Auth = "off"
		}
	}
	if s.BindAddress == "" {
		if s.Access == "lan" {
			s.BindAddress = "0.0.0.0"
		} else {
			s.BindAddress = "127.0.0.1"
		}
	}
	if s.Port == 0 {
		if s.TLS != "off" {
			s.Port = 8443
		} else {
			s.Port = 8000
		}
	}
	if s.AdminUsername == "" {
		s.AdminUsername = "admin"
	}
	for name, pair := range map[string][]string{"backend": {s.Backend, "sqlite", "postgres"}, "artifacts": {s.Artifacts, "local", "nas", "s3", "rustfs", "seaweedfs"}, "access": {s.Access, "loopback", "lan"}, "tls": {s.TLS, "off", "internal", "provided"}, "auth": {s.Auth, "off", "native"}} {
		found := false
		for _, value := range pair[1:] {
			if pair[0] == value {
				found = true
			}
		}
		if !found {
			return s, fmt.Errorf("invalid %s %q (choose %s)", name, pair[0], strings.Join(pair[1:], ", "))
		}
	}
	ip := net.ParseIP(s.BindAddress)
	if ip == nil {
		return s, fmt.Errorf("bind address must be an IP address")
	}
	if s.Access == "loopback" && !ip.IsLoopback() {
		return s, fmt.Errorf("loopback access requires a loopback bind address")
	}
	if s.Port < 1 || s.Port > 65535 {
		return s, fmt.Errorf("port must be between 1 and 65535")
	}
	if s.Access == "lan" && s.Hostname == "" {
		return s, fmt.Errorf("LAN access requires --hostname (a client-reachable DNS name or IP address)")
	}
	if s.TLS != "off" {
		if s.Hostname == "" {
			if s.Access == "loopback" {
				s.Hostname = "localhost"
			} else {
				return s, fmt.Errorf("LAN HTTPS requires --hostname (a client-reachable DNS name or IP address)")
			}
		}
		if !validHostname(s.Hostname) {
			return s, fmt.Errorf("hostname must be a DNS name or IP address without a scheme, port, path or wildcard")
		}
	} else if s.Hostname != "" && !validHostname(s.Hostname) {
		return s, fmt.Errorf("invalid hostname")
	}
	if strings.TrimSpace(s.AdminUsername) != s.AdminUsername || s.AdminUsername == "" || strings.ContainsAny(s.AdminUsername, "\r\n\t:%") {
		return s, fmt.Errorf("admin username must not contain whitespace controls, colon or percent")
	}
	if s.TLS == "provided" {
		if s.CertFile == "" || s.KeyFile == "" {
			return s, fmt.Errorf("provided TLS requires both certificate and key paths")
		}
	} else if s.CertFile != "" || s.KeyFile != "" {
		return s, fmt.Errorf("certificate and key paths require provided TLS")
	}
	if s.Artifacts == "nas" {
		if s.ArtifactPath == "" || s.NASMount == "" {
			return s, fmt.Errorf("NAS artifacts require both artifact-path and nas-mount")
		}
		if s.Backend != "postgres" {
			return s, fmt.Errorf("NAS artifact profiles require PostgreSQL metadata on local storage")
		}
	} else if s.NASMount != "" {
		return s, fmt.Errorf("nas-mount is only valid for NAS artifacts")
	}
	if s.Artifacts != "nas" && s.Artifacts != "local" && s.ArtifactPath != "" {
		return s, fmt.Errorf("artifact-path is only valid for local or NAS artifacts; object storage uses its own local volume")
	}
	if s.Artifacts == "s3" || s.Artifacts == "rustfs" || s.Artifacts == "seaweedfs" {
		if s.S3Bucket == "" {
			s.S3Bucket = "mlflow"
		}
		if s.S3Region == "" {
			s.S3Region = "us-east-1"
		}
		if !bucketPattern.MatchString(s.S3Bucket) || strings.Contains(s.S3Bucket, "..") || net.ParseIP(s.S3Bucket) != nil {
			return s, fmt.Errorf("invalid S3 bucket name")
		}
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(s.S3Region) {
			return s, fmt.Errorf("invalid S3 region")
		}
		if s.Artifacts == "s3" {
			if s.S3Endpoint != "" {
				u, err := url.Parse(s.S3Endpoint)
				if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
					return s, fmt.Errorf("external S3 endpoint must be an HTTP(S) origin without credentials or a path (or empty for AWS S3)")
				}
			}
			s.S3Endpoint = strings.TrimRight(s.S3Endpoint, "/")
			if s.S3AccessKeyEnv == "" {
				s.S3AccessKeyEnv = "AWS_ACCESS_KEY_ID"
			}
			if s.S3SecretKeyEnv == "" {
				s.S3SecretKeyEnv = "AWS_SECRET_ACCESS_KEY"
			}
			if !envPattern.MatchString(s.S3AccessKeyEnv) || !envPattern.MatchString(s.S3SecretKeyEnv) {
				return s, fmt.Errorf("S3 credential references must be environment variable names")
			}
		} else {
			if s.S3Endpoint != "" || s.S3AccessKeyEnv != "" || s.S3SecretKeyEnv != "" {
				return s, fmt.Errorf("managed storage generates its own endpoint and credentials")
			}
		}
	} else if s.S3Endpoint != "" || s.S3Bucket != "" || s.S3Region != "" || s.S3AccessKeyEnv != "" || s.S3SecretKeyEnv != "" {
		return s, fmt.Errorf("S3 options require an S3 artifact provider")
	}
	for _, ptr := range []*string{&s.ArtifactPath, &s.NASMount, &s.CertFile, &s.KeyFile} {
		if *ptr != "" {
			if strings.ContainsAny(*ptr, "\x00\r\n") {
				return s, fmt.Errorf("paths cannot contain control characters")
			}
			p, err := filepath.Abs(*ptr)
			if err != nil {
				return s, err
			}
			*ptr = filepath.Clean(p)
		}
	}
	if s.Artifacts == "nas" {
		rel, err := filepath.Rel(s.NASMount, s.ArtifactPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return s, fmt.Errorf("artifact-path must be inside nas-mount")
		}
	}
	return s, nil
}

func validHostname(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	if len(s) > 253 || s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if !regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`).MatchString(part) {
			return false
		}
	}
	return true
}

func (s ServerSpec) TrackingURI() string {
	host := s.Hostname
	if host == "" {
		host = s.BindAddress
	}
	if host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	scheme := "http"
	if s.TLS != "off" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, fmt.Sprint(s.Port)))
}
