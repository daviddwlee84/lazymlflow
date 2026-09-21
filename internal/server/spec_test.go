package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeLayouts(t *testing.T) {
	for _, artifact := range []string{"local", "s3", "rustfs", "seaweedfs"} {
		t.Run(artifact, func(t *testing.T) {
			s := DefaultSpec()
			s.ID = "team"
			s.Backend = "postgres"
			s.Artifacts = artifact
			if _, err := Normalize(s); err != nil {
				t.Fatal(err)
			}
		})
	}
	s := DefaultSpec()
	s.ID = "team"
	s.Access = "lan"
	s.TLS = "internal"
	s.Auth = "native"
	s.BindAddress = "0.0.0.0"
	s.Hostname = "mlflow.example.internal"
	s.Port = 8443
	got, err := Normalize(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.TrackingURI() != "https://mlflow.example.internal:8443" {
		t.Fatal(got.TrackingURI())
	}
	s.TLS, s.Auth = "off", "off"
	if _, err := Normalize(s); err != nil {
		t.Fatalf("explicit opt-outs rejected: %v", err)
	}
	for _, mutate := range []func(*ServerSpec){func(s *ServerSpec) { s.ID = "../escape" }, func(s *ServerSpec) { s.Hostname = "safe\nreverse_proxy evil" }, func(s *ServerSpec) { s.Artifacts = "minio" }, func(s *ServerSpec) { s.Artifacts = "s3"; s.S3Endpoint = "https://key:secret@host" }, func(s *ServerSpec) { s.Access = "loopback"; s.BindAddress = "0.0.0.0" }} {
		bad := s
		mutate(&bad)
		if _, err := Normalize(bad); err == nil {
			t.Fatalf("invalid spec accepted: %+v", bad)
		}
	}
}

func TestRecommendationDoesNotRequireObjectStorageForRemoteClients(t *testing.T) {
	r := Recommend(Requirements{RemoteTraining: true, ExistingNAS: "/mnt/nas/artifacts"})
	if r.Spec.Backend != "postgres" || r.Spec.Artifacts != "nas" || r.Spec.Auth != "native" || r.Spec.TLS != "internal" {
		t.Fatalf("unexpected recommendation: %+v", r)
	}
	r = Recommend(Requirements{NeedObjectStorage: true, Provider: "seaweedfs"})
	if r.Spec.Artifacts != "seaweedfs" {
		t.Fatal(r.Spec.Artifacts)
	}
}

func TestInitPrivateAndImmutable(t *testing.T) {
	s := DefaultSpec()
	s.ID = "private"
	s.Auth = "native"
	s.Backend = "postgres"
	s.Artifacts = "rustfs"
	t.Setenv("BOOTSTRAP_TEST", "secret$with#characters'12345")
	dir := filepath.Join(t.TempDir(), "new")
	stack, err := Init(context.Background(), dir, s, InitOptions{AdminPasswordEnv: "BOOTSTRAP_TEST"})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := os.ReadFile(stack.AdminPasswordPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"compose.yaml", "stack.json", "Dockerfile", "README.md"} {
		data, _ := os.ReadFile(filepath.Join(dir, file))
		if strings.Contains(string(data), string(secret)) {
			t.Fatalf("secret leaked into %s", file)
		}
	}
	for _, file := range []string{"secrets/admin_password", "secrets/server.env", "secrets/bootstrap.env", "stack.json"} {
		stat, err := os.Stat(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if stat.Mode().Perm() != 0600 {
			t.Fatalf("%s mode %o", file, stat.Mode().Perm())
		}
	}
	compose := readCompose(t, dir)
	services := compose["services"].(map[string]any)
	ml := services["mlflow"].(map[string]any)
	data, _ := json.Marshal(ml)
	if strings.Contains(string(data), "bootstrap.env") || strings.Contains(string(data), "admin_password") {
		t.Fatal("ordinary server received bootstrap secret")
	}
	for _, name := range []string{"postgres", "storage"} {
		if _, ok := services[name].(map[string]any)["ports"]; ok {
			t.Fatalf("%s exposes a host port", name)
		}
	}
	if _, err := Init(context.Background(), dir, s); err == nil {
		t.Fatal("overwrote existing directory")
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "compose.yaml"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n")
	_ = f.Close()
	if _, err := Load(dir); err == nil {
		t.Fatal("modified compose accepted")
	}
}

func TestNASMissingMountFailsBeforeWritingStack(t *testing.T) {
	s := DefaultSpec()
	s.ID = "nas"
	s.Backend = "postgres"
	s.Artifacts = "nas"
	s.NASMount = t.TempDir()
	s.ArtifactPath = s.NASMount
	dir := filepath.Join(t.TempDir(), "stack")
	if _, err := Init(context.Background(), dir, s); err == nil || !strings.Contains(err.Error(), "mount") {
		t.Fatalf("expected mount error: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("created stack despite missing mount")
	}
}

func TestNASSymlinkEscapeFailsBeforeProbe(t *testing.T) {
	s := DefaultSpec()
	s.ID = "nas"
	s.Backend = "postgres"
	s.Artifacts = "nas"
	s.NASMount = t.TempDir()
	s.ArtifactPath = filepath.Join(s.NASMount, "escaped")
	if err := os.Symlink(t.TempDir(), s.ArtifactPath); err != nil {
		t.Skip(err)
	}
	if err := checkArtifactPath(context.Background(), s, true); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected symlink containment failure before mount/write probe: %v", err)
	}
}

func TestIPv6AndLiteralDollarRendering(t *testing.T) {
	s := DefaultSpec()
	s.ID = "ipv6"
	s.TLS = "internal"
	s.Hostname = "::1"
	s.BindAddress = "::1"
	s.AdminUsername = "user$literal"
	s.Auth = "native"
	s.ArtifactPath = filepath.Join(t.TempDir(), "$literal")
	if err := os.Mkdir(s.ArtifactPath, 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "stack")
	if _, err := Init(context.Background(), dir, s); err != nil {
		t.Fatal(err)
	}
	caddy, _ := os.ReadFile(filepath.Join(dir, "Caddyfile"))
	if !strings.Contains(string(caddy), "https://[::1]:443") {
		t.Fatalf("invalid IPv6 Caddy site: %s", caddy)
	}
	compose, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if !strings.Contains(string(compose), "user$$literal") || !strings.Contains(string(compose), "/$$literal") {
		t.Fatal("literal Compose interpolation was not escaped")
	}
}

func TestBootstrapPasswordCountsCharacters(t *testing.T) {
	t.Setenv("SHORT_UNICODE_PASSWORD", "短密碼測")
	s := DefaultSpec()
	s.ID = "password"
	s.Auth = "native"
	if _, err := Init(context.Background(), filepath.Join(t.TempDir(), "stack"), s, InitOptions{AdminPasswordEnv: "SHORT_UNICODE_PASSWORD"}); err == nil {
		t.Fatal("four Unicode characters were accepted as a 12-character password")
	}
}

func TestPrintedShellArgumentIsLiteral(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip(err)
	}
	value := "a'b$(printf expanded)`printf expanded` with spaces"
	out, err := exec.Command("sh", "-c", "printf '%s' "+quoteShell(value)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != value {
		t.Fatalf("printed argument executed shell syntax: %q", out)
	}
}

func TestGeneratedStackCanMoveToAnotherHost(t *testing.T) {
	s := DefaultSpec()
	s.ID = "copy"
	s.TLS = "internal"
	s.Hostname = "localhost"
	dir := filepath.Join(t.TempDir(), "first")
	stack, err := Init(context.Background(), dir, s)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if strings.Contains(string(data), dir) {
		t.Fatal("generated helper bind is not portable")
	}
	moved := filepath.Join(filepath.Dir(dir), "moved")
	if err = os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(moved)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Dir != moved || loaded.Project != stack.Project {
		t.Fatal("identity not preserved")
	}
}

func TestProviderContractAndExternalCredentials(t *testing.T) {
	for _, kind := range []string{"s3", "rustfs", "seaweedfs"} {
		t.Run(kind, func(t *testing.T) {
			s := DefaultSpec()
			s.ID = "test"
			s.Artifacts = kind
			t.Setenv("AWS_ACCESS_KEY_ID", "test-access")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
			dir := filepath.Join(t.TempDir(), "stack")
			_, err := Init(context.Background(), dir, s)
			if err != nil {
				t.Fatal(err)
			}
			c := readCompose(t, dir)
			ml := c["services"].(map[string]any)["mlflow"].(map[string]any)
			env := ml["environment"].(map[string]any)
			if env["MLFLOW_BOTO_CLIENT_ADDRESSING_STYLE"] != "path" || env["MLFLOW_ENABLE_PROXY_MULTIPART_UPLOAD"] != "false" {
				t.Fatal(env)
			}
			if kind == "s3" {
				if _, ok := c["services"].(map[string]any)["storage"]; ok {
					t.Fatal("external S3 created storage service")
				}
			}
		})
	}
}

func readCompose(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err = json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
