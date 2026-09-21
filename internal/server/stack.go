package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type InitOptions struct{ AdminPasswordEnv string }

type Stack struct {
	Dir         string            `json:"directory"`
	Spec        ServerSpec        `json:"spec"`
	Project     string            `json:"project"`
	TrackingURI string            `json:"tracking_uri"`
	Files       []string          `json:"files"`
	CreatedAt   string            `json:"created_at"`
	Digests     map[string]string `json:"file_digests"`
	NASIdentity string            `json:"nas_mount_identity,omitempty"`
}

func (s *Stack) AdminPasswordPath() string { return filepath.Join(s.Dir, "secrets", "admin_password") }
func (s *Stack) CAPath() string            { return filepath.Join(s.Dir, "ca.crt") }

// Target makes only a connection record; neither connecting nor removing this
// target starts/stops the persistent stack. Credential references carry no values.
func (s *Stack) Target() core.Target {
	t := core.Target{ID: s.Spec.ID, Name: s.Spec.ID, TrackingURI: s.TrackingURI}
	if s.Spec.Auth == "native" {
		t.UsernameEnv, t.PasswordEnv = "MLFLOW_TRACKING_USERNAME", "MLFLOW_TRACKING_PASSWORD"
	}
	if s.Spec.TLS == "internal" {
		t.CAFile = s.CAPath()
	}
	return t
}

// Init atomically creates a new private directory. It never merges with or
// replaces an existing directory, and does not contact Docker or tracking APIs.
func Init(ctx context.Context, dir string, requested ServerSpec, options ...InitOptions) (*Stack, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	spec, err := Normalize(requested)
	if err != nil {
		return nil, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(dir); err == nil {
		return nil, fmt.Errorf("destination already exists: %s; generate into a new directory", dir)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := checkArtifactPath(ctx, spec, false); err != nil {
		return nil, err
	}
	secrets := map[string][]byte{}
	if spec.Backend == "postgres" {
		value, e := randomSecret(32)
		if e != nil {
			return nil, e
		}
		secrets["postgres_password"] = []byte(value)
	}
	if spec.Auth == "native" {
		value := ""
		if len(options) > 0 && options[0].AdminPasswordEnv != "" {
			key := options[0].AdminPasswordEnv
			if !envPattern.MatchString(key) {
				return nil, fmt.Errorf("admin-password-env must name an environment variable")
			}
			value = os.Getenv(key)
			if value == "" {
				return nil, fmt.Errorf("admin password environment variable %s is empty or unset", key)
			}
		}
		if value == "" {
			value, err = randomSecret(32)
			if err != nil {
				return nil, err
			}
		}
		if utf8.RuneCountInString(value) < 12 || value == "password1234" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("admin password must be at least 12 characters and cannot contain line breaks or NUL")
		}
		secrets["admin_password"] = []byte(value)
		value, err = randomSecret(32)
		if err != nil {
			return nil, err
		}
		secrets["csrf_secret"] = []byte(value)
	}
	if isS3(spec.Artifacts) {
		if spec.Artifacts == "s3" {
			for file, key := range map[string]string{"s3_access_key": spec.S3AccessKeyEnv, "s3_secret_key": spec.S3SecretKeyEnv} {
				v := os.Getenv(key)
				if v == "" {
					return nil, fmt.Errorf("S3 credential environment variable %s is empty or unset", key)
				}
				if strings.ContainsAny(v, "\x00\r\n") {
					return nil, fmt.Errorf("S3 credential %s contains a line break or NUL", key)
				}
				secrets[file] = []byte(v)
			}
		} else {
			for _, key := range []string{"s3_access_key", "s3_secret_key", "storage_admin_password"} {
				v, e := randomSecret(24)
				if e != nil {
					return nil, e
				}
				secrets[key] = []byte(v)
			}
		}
	}
	if spec.TLS == "provided" {
		cert, e := os.ReadFile(spec.CertFile)
		if e != nil {
			return nil, fmt.Errorf("read TLS certificate: %w", e)
		}
		key, e := os.ReadFile(spec.KeyFile)
		if e != nil {
			return nil, fmt.Errorf("read TLS key: %w", e)
		}
		if _, e = tls.X509KeyPair(cert, key); e != nil {
			return nil, fmt.Errorf("TLS certificate/key pair is invalid: %w", e)
		}
		secrets["tls_cert.pem"], secrets["tls_key.pem"] = cert, key
	}
	// raw env files are consumed by Compose, not interpolated by a shell. Docker
	// file-secret bind permissions are unreliable for arbitrary non-root UIDs.
	serverEnv := ""
	for _, pair := range [][2]string{{"POSTGRES_PASSWORD", "postgres_password"}, {"AWS_ACCESS_KEY_ID", "s3_access_key"}, {"AWS_SECRET_ACCESS_KEY", "s3_secret_key"}, {"MLFLOW_FLASK_SERVER_SECRET_KEY", "csrf_secret"}} {
		if v, ok := secrets[pair[1]]; ok {
			serverEnv += pair[0] + "=" + string(v) + "\n"
		}
	}
	secrets["server.env"] = []byte(serverEnv)
	if spec.Backend == "postgres" {
		secrets["postgres.env"] = []byte("POSTGRES_PASSWORD=" + string(secrets["postgres_password"]) + "\n")
	}
	if spec.Auth == "native" {
		secrets["bootstrap.env"] = []byte("LAZYMLFLOW_BOOTSTRAP_PASSWORD=" + string(secrets["admin_password"]) + "\n")
	}
	if spec.Artifacts == "rustfs" {
		secrets["storage.env"] = []byte("RUSTFS_ACCESS_KEY=" + string(secrets["s3_access_key"]) + "\nRUSTFS_SECRET_KEY=" + string(secrets["s3_secret_key"]) + "\n")
	}
	if spec.Artifacts == "seaweedfs" {
		secrets["storage.env"] = []byte("AWS_ACCESS_KEY_ID=" + string(secrets["s3_access_key"]) + "\nAWS_SECRET_ACCESS_KEY=" + string(secrets["s3_secret_key"]) + "\nWEED_ADMIN_PASSWORD=" + string(secrets["storage_admin_password"]) + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".lazymlflow-new-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	suffix, err := randomSecret(6)
	if err != nil {
		return nil, err
	}
	stack := &Stack{Dir: dir, Spec: spec, Project: "lazymlflow-" + strings.ReplaceAll(spec.ID, "_", "-") + "-" + suffix, TrackingURI: spec.TrackingURI(), CreatedAt: time.Now().UTC().Format(time.RFC3339), Digests: map[string]string{}}
	if spec.Artifacts == "nas" {
		stack.NASIdentity, err = mountIdentity(ctx, spec.NASMount)
		if err != nil {
			return nil, err
		}
	}
	files, err := render(stack)
	if err != nil {
		return nil, err
	}
	for name, data := range files {
		if err := writeExclusive(filepath.Join(staging, name), data, 0600); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		stack.Digests[name] = hex.EncodeToString(sum[:])
		stack.Files = append(stack.Files, name)
	}
	for name, data := range secrets {
		if err := writeExclusive(filepath.Join(staging, "secrets", name), data, 0600); err != nil {
			return nil, err
		}
		stack.Files = append(stack.Files, filepath.ToSlash(filepath.Join("secrets", name)))
	}
	sortStrings(stack.Files)
	data, err := json.MarshalIndent(stack, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = writeExclusive(filepath.Join(staging, "stack.json"), append(data, '\n'), 0600); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// Reserve the destination before moving files so Rename can never replace a
	// directory created by another process between validation and publication.
	if err = os.Mkdir(dir, 0700); err != nil {
		return nil, fmt.Errorf("reserve stack directory: %w", err)
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == "stack.json" {
			continue
		}
		if err = os.Rename(filepath.Join(staging, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			return nil, fmt.Errorf("publishing stack failed; incomplete directory retained at %s: %w", dir, err)
		}
	}
	if err = os.Rename(filepath.Join(staging, "stack.json"), filepath.Join(dir, "stack.json")); err != nil {
		return nil, fmt.Errorf("publishing stack receipt failed; incomplete directory retained at %s: %w", dir, err)
	}
	return stack, nil
}

func Load(dir string) (*Stack, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("stack directory must be a real directory")
	}
	data, err := os.ReadFile(filepath.Join(abs, "stack.json"))
	if err != nil {
		return nil, fmt.Errorf("not a generated lazymlflow stack: %w", err)
	}
	var s Stack
	if err = json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid stack manifest: %w", err)
	}
	norm, err := Normalize(s.Spec)
	if err != nil {
		return nil, err
	}
	s.Spec = norm
	// Helpers and private TLS files use relative binds, so a stack can be copied
	// to another host. Explicit artifact/NAS paths remain that host's responsibility.
	s.Dir = abs
	if !regexpProject(s.Project, s.Spec.ID) {
		return nil, fmt.Errorf("invalid generated Compose project identity")
	}
	if s.TrackingURI != s.Spec.TrackingURI() {
		return nil, fmt.Errorf("tracking URI does not match generated spec")
	}
	for name, want := range s.Digests {
		if !safeRelative(name) {
			return nil, fmt.Errorf("invalid generated file path")
		}
		p := filepath.Join(abs, name)
		info, e := os.Lstat(p)
		if e != nil {
			return nil, e
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("generated file %s is not a regular file", name)
		}
		contents, e := os.ReadFile(p)
		if e != nil {
			return nil, e
		}
		sum := sha256.Sum256(contents)
		if hex.EncodeToString(sum[:]) != want {
			return nil, fmt.Errorf("generated file %s was modified; lifecycle refuses changed layouts (generate a new stack, migrate separately)", name)
		}
	}
	if len(s.Digests) == 0 || s.Digests["compose.yaml"] == "" {
		return nil, fmt.Errorf("stack manifest has no generated Compose receipt")
	}
	return &s, nil
}

func randomSecret(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func writeExclusive(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
func safeRelative(s string) bool {
	return s != "" && !filepath.IsAbs(s) && filepath.Clean(s) == s && s != ".." && !strings.HasPrefix(s, ".."+string(filepath.Separator))
}
func regexpProject(project, id string) bool {
	prefix := "lazymlflow-" + strings.ReplaceAll(id, "_", "-") + "-"
	if !strings.HasPrefix(project, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(project, prefix)
	_, err := hex.DecodeString(suffix)
	return len(suffix) == 12 && err == nil
}
func isS3(kind string) bool { return kind == "s3" || kind == "rustfs" || kind == "seaweedfs" }
