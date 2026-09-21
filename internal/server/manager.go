package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/daviddwlee84/lazymlflow/internal/platform"
)

// Runner is injectable so generation and lifecycle boundaries can be tested
// without starting a daemon. Args are passed directly, never through a shell.
type Runner interface {
	Run(context.Context, string, []string, string) ([]byte, error)
}
type StreamRunner interface {
	Stream(context.Context, string, []string, string, io.Writer) error
}
type ExecRunner struct{}

func dockerEnv(dir string) []string {
	var env []string
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if strings.HasPrefix(key, "COMPOSE_") || key == "PWD" {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "PWD="+dir)
}
func (ExecRunner) Run(ctx context.Context, name string, args []string, dir string) ([]byte, error) {
	var output bytes.Buffer
	code, err := platform.RunAttachedInDir(ctx, append([]string{name}, args...), dockerEnv(dir), dir, nil, &output, &output)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit status %d", code)
	}
	return output.Bytes(), err
}
func (ExecRunner) Stream(ctx context.Context, name string, args []string, dir string, w io.Writer) error {
	code, err := platform.RunAttachedInDir(ctx, append([]string{name}, args...), dockerEnv(dir), dir, nil, w, w)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit status %d", code)
	}
	return err
}

type Manager struct {
	Runner       Runner
	DockerBinary string
	Timeout      time.Duration
}

func NewManager() *Manager {
	return &Manager{Runner: ExecRunner{}, DockerBinary: "docker", Timeout: 15 * time.Minute}
}
func (m *Manager) defaults() {
	if m.Runner == nil {
		m.Runner = ExecRunner{}
	}
	if m.DockerBinary == "" {
		m.DockerBinary = "docker"
	}
	if m.Timeout == 0 {
		m.Timeout = 15 * time.Minute
	}
}

type ServiceStatus struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Service  string `json:"service"`
	State    string `json:"state"`
	Health   string `json:"health,omitempty"`
	ExitCode int    `json:"exit_code"`
}
type Status struct {
	Project     string          `json:"project"`
	TrackingURI string          `json:"tracking_uri"`
	Services    []ServiceStatus `json:"services"`
}

func (m *Manager) checked(s *Stack) (*Stack, error) {
	if s == nil {
		return nil, fmt.Errorf("stack is required")
	}
	current, err := Load(s.Dir)
	if err != nil {
		return nil, err
	}
	if current.Project != s.Project {
		return nil, fmt.Errorf("stack identity changed")
	}
	m.defaults()
	return current, nil
}
func (m *Manager) preflight(ctx context.Context, s *Stack) error {
	// The generator operates on local bind paths and explicitly does not deploy
	// to arbitrary remote Docker engines or SSH hosts.
	if host := os.Getenv("DOCKER_HOST"); host != "" && !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
		return fmt.Errorf("persistent stack lifecycle requires a local Docker engine; DOCKER_HOST points to a remote engine")
	}
	if os.Getenv("DOCKER_CONTEXT") != "" || os.Getenv("DOCKER_HOST") == "" {
		data, err := m.Runner.Run(ctx, m.DockerBinary, []string{"context", "inspect", "--format", "{{json .Endpoints.docker.Host}}"}, s.Dir)
		if err != nil {
			return fmt.Errorf("inspect Docker context: %s", redact(s, string(data)))
		}
		var host string
		if err = json.Unmarshal(bytes.TrimSpace(data), &host); err != nil {
			return fmt.Errorf("Docker context returned an invalid engine address")
		}
		if !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
			return fmt.Errorf("persistent stack lifecycle requires a local Docker context, got a remote engine")
		}
	}
	data, err := m.Runner.Run(ctx, m.DockerBinary, []string{"compose", "version", "--short"}, s.Dir)
	if err != nil {
		return fmt.Errorf("Docker Compose is required: %s", redact(s, string(data)))
	}
	version := strings.TrimPrefix(strings.TrimSpace(string(data)), "v")
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return fmt.Errorf("unrecognized Docker Compose version %q", version)
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	if major < 2 || (major == 2 && minor < 30) {
		return fmt.Errorf("Docker Compose 2.30+ is required for private raw env files (found %s)", version)
	}
	data, err = m.Runner.Run(ctx, m.DockerBinary, []string{"ps", "--all", "--filter", "label=com.docker.compose.project=" + s.Project, "--format", `{{.ID}}\t{{.Label "io.lazymlflow.owner"}}\t{{.Label "io.lazymlflow.stack-directory"}}`}, s.Dir)
	if err != nil {
		return fmt.Errorf("inspect existing Compose resources: %s", redact(s, string(data)))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 || parts[1] != s.Project || parts[2] != s.Dir {
			return fmt.Errorf("Compose project already has resources owned by another stack directory; initialize a new stack to clone it")
		}
	}
	data, err = m.Runner.Run(ctx, m.DockerBinary, []string{"volume", "ls", "--filter", "name=^" + s.Project + "_", "--format", "{{.Name}}"}, s.Dir)
	if err != nil {
		return fmt.Errorf("inspect existing stack volumes: %s", redact(s, string(data)))
	}
	names := strings.Fields(string(data))
	if len(names) > 0 {
		args := append([]string{"volume", "inspect"}, names...)
		data, err = m.Runner.Run(ctx, m.DockerBinary, args, s.Dir)
		if err != nil {
			return fmt.Errorf("inspect stack volume ownership: %s", redact(s, string(data)))
		}
		var volumes []struct {
			Name   string
			Labels map[string]string
		}
		if err = json.Unmarshal(data, &volumes); err != nil {
			return fmt.Errorf("invalid Docker volume inspection response")
		}
		for _, volume := range volumes {
			if volume.Labels["io.lazymlflow.owner"] != s.Project || volume.Labels["io.lazymlflow.stack-directory"] != s.Dir {
				return fmt.Errorf("Compose project has persistent volumes owned by another stack directory; use the original directory or initialize an independent stack")
			}
		}
	}
	return nil
}

func composeArgs(s *Stack, args ...string) []string {
	base := []string{"compose", "--project-name", s.Project, "--project-directory", s.Dir, "--file", filepath.Join(s.Dir, "compose.yaml")}
	return append(base, args...)
}
func (m *Manager) run(ctx context.Context, s *Stack, args ...string) ([]byte, error) {
	data, err := m.Runner.Run(ctx, m.DockerBinary, composeArgs(s, args...), s.Dir)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		safe := redact(s, string(data))
		if len(safe) > 12000 {
			safe = safe[len(safe)-12000:]
		}
		return nil, fmt.Errorf("docker compose %s failed: %v\n%s", args[0], err, safe)
	}
	return data, nil
}

func (m *Manager) Up(ctx context.Context, s *Stack) error {
	s, err := m.checked(s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()
	if s.Spec.Artifacts == "nas" {
		identity, e := mountIdentity(ctx, s.Spec.NASMount)
		if e != nil {
			return e
		}
		if identity != s.NASIdentity {
			return fmt.Errorf("NAS mount source or filesystem changed; verify the mount before using this stack")
		}
	}
	if err = checkArtifactPath(ctx, s.Spec, true); err != nil {
		return err
	}
	if err = m.preflight(ctx, s); err != nil {
		return err
	}
	if _, err = m.run(ctx, s, "config", "--quiet"); err != nil {
		return err
	}
	if _, err = m.run(ctx, s, "up", "--detach", "--build", "--wait", "--wait-timeout", "240"); err != nil {
		if ctx.Err() == nil {
			logsCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			args := []string{"logs", "--no-color", "--tail", "30", "mlflow"}
			if s.Spec.Auth == "native" {
				args = append(args, "auth-init")
			}
			if isS3(s.Spec.Artifacts) {
				args = append(args, "storage-init")
			}
			data, _ := m.Runner.Run(logsCtx, m.DockerBinary, composeArgs(s, args...), s.Dir)
			diagnostics := redact(s, string(data))
			if len(diagnostics) > 8000 {
				diagnostics = diagnostics[len(diagnostics)-8000:]
			}
			if strings.TrimSpace(diagnostics) != "" {
				return fmt.Errorf("%w\nStartup diagnostics:\n%s", err, diagnostics)
			}
		}
		return err
	}
	if s.Spec.TLS == "internal" {
		_, err = m.ExportCA(ctx, s, s.CAPath())
		if err != nil {
			return fmt.Errorf("stack started, but public CA export failed: %w", err)
		}
	}
	return nil
}

func (m *Manager) Status(ctx context.Context, s *Stack) (Status, error) {
	s, err := m.checked(s)
	if err != nil {
		return Status{}, err
	}
	if err = m.preflight(ctx, s); err != nil {
		return Status{}, err
	}
	data, err := m.run(ctx, s, "ps", "--all", "--format", "json")
	if err != nil {
		return Status{}, err
	}
	result := Status{Project: s.Project, TrackingURI: s.TrackingURI, Services: []ServiceStatus{}}
	type row struct {
		ID, Name, Service, State, Health string
		ExitCode                         int
	}
	rows := []row{}
	trim := bytes.TrimSpace(data)
	if len(trim) > 0 && trim[0] == '[' {
		if err = json.Unmarshal(trim, &rows); err != nil {
			return result, fmt.Errorf("parse Compose status: %w", err)
		}
	} else {
		decoder := json.NewDecoder(bytes.NewReader(trim))
		for {
			var item row
			err = decoder.Decode(&item)
			if err == io.EOF {
				break
			}
			if err != nil {
				return result, fmt.Errorf("parse Compose status: %w", err)
			}
			rows = append(rows, item)
		}
	}
	for _, r := range rows {
		result.Services = append(result.Services, ServiceStatus{ID: r.ID, Name: r.Name, Service: r.Service, State: r.State, Health: r.Health, ExitCode: r.ExitCode})
	}
	return result, nil
}

func (m *Manager) Down(ctx context.Context, s *Stack) error {
	s, err := m.checked(s)
	if err != nil {
		return err
	}
	if err = m.preflight(ctx, s); err != nil {
		return err
	}
	_, err = m.run(ctx, s, "down", "--timeout", "20")
	return err
}

func (m *Manager) Logs(ctx context.Context, s *Stack, follow bool, tail int, service string, w io.Writer) error {
	s, err := m.checked(s)
	if err != nil {
		return err
	}
	if tail < 1 || tail > 10000 {
		return fmt.Errorf("log tail must be between 1 and 10000")
	}
	if service != "" && !idPattern.MatchString(service) {
		return fmt.Errorf("invalid service name")
	}
	if err = m.preflight(ctx, s); err != nil {
		return err
	}
	args := []string{"logs", "--no-color", "--tail", strconv.Itoa(tail)}
	if follow {
		args = append(args, "--follow")
	}
	if service != "" {
		args = append(args, service)
	}
	filter := &redactingWriter{out: w, stack: s}
	defer filter.flush()
	if stream, ok := m.Runner.(StreamRunner); ok {
		err = stream.Stream(ctx, m.DockerBinary, composeArgs(s, args...), s.Dir, filter)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	data, err := m.run(ctx, s, args...)
	if err != nil {
		return err
	}
	_, err = filter.Write(data)
	return err
}

func (m *Manager) ExportCA(ctx context.Context, s *Stack, destination string) (string, error) {
	s, err := m.checked(s)
	if err != nil {
		return "", err
	}
	if s.Spec.TLS != "internal" {
		return "", fmt.Errorf("CA export is only available for internal TLS")
	}
	if err = m.preflight(ctx, s); err != nil {
		return "", err
	}
	if destination == "" {
		destination = s.CAPath()
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	var data []byte
	for attempt := 0; attempt < 12; attempt++ {
		data, err = m.run(ctx, s, "exec", "--no-TTY", "gateway", "cat", "/data/caddy/pki/authorities/local/root.crt")
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if err != nil {
		return "", err
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return "", fmt.Errorf("gateway did not return a single public CA certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return "", fmt.Errorf("gateway returned an invalid CA certificate")
	}
	if existing, err := os.ReadFile(destination); err == nil {
		if bytes.Equal(existing, data) {
			return destination, nil
		}
		return "", fmt.Errorf("CA destination already exists with different contents: %s", destination)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err = writeExclusive(destination, data, 0644); err != nil {
		return "", err
	}
	return destination, nil
}

// CAFingerprint hashes the DER certificate bytes, not the surrounding PEM text.
func CAFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return "", fmt.Errorf("expected one PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA {
		return "", fmt.Errorf("expected a CA certificate")
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}

func redact(s *Stack, text string) string {
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "secrets"))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, "secrets", entry.Name()))
		if err != nil {
			continue
		}
		value := string(data)
		if len(value) > 0 && !strings.Contains(value, "\n") {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
		if strings.HasSuffix(entry.Name(), ".env") {
			for _, line := range strings.Split(value, "\n") {
				_, v, ok := strings.Cut(line, "=")
				if ok && v != "" {
					text = strings.ReplaceAll(text, v, "[redacted]")
				}
			}
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
}

type redactingWriter struct {
	mu      sync.Mutex
	out     io.Writer
	stack   *Stack
	pending []byte
	discard bool
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for _, b := range p {
		if b == '\n' {
			if w.discard {
				_, _ = io.WriteString(w.out, "[oversized log line omitted]\n")
			} else {
				_, _ = io.WriteString(w.out, redact(w.stack, string(w.pending))+"\n")
			}
			w.pending = nil
			w.discard = false
			continue
		}
		if !w.discard {
			w.pending = append(w.pending, b)
			if len(w.pending) > 1024*1024 {
				w.pending = nil
				w.discard = true
			}
		}
	}
	return n, nil
}
func (w *redactingWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		_, _ = io.WriteString(w.out, redact(w.stack, string(w.pending)))
	}
	w.pending = nil
}
