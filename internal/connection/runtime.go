package connection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type runtimeCommand struct {
	executable string
	prefix     []string
	version    string
}

func (r runtimeCommand) command(args ...string) []string {
	return append(append([]string{r.executable}, r.prefix...), args...)
}

func prepareRuntime(ctx context.Context, t core.Target, env []string) (runtimeCommand, error) {
	var r runtimeCommand
	var probe []string
	if t.Python != "" {
		python := t.Python
		if info, err := os.Stat(python); err == nil && info.IsDir() {
			python = filepath.Join(python, "bin", "python")
		}
		path, err := exec.LookPath(python)
		if err != nil {
			return r, fmt.Errorf("target Python is unavailable: %w", err)
		}
		r = runtimeCommand{executable: path, prefix: []string{"-m", "mlflow"}}
		probe = []string{path, "-c", "import importlib.metadata; print(importlib.metadata.version('mlflow'))"}
	} else {
		uvx, err := exec.LookPath("uvx")
		if err != nil {
			return r, errors.New("local stores and direct artifact storage require uv (uvx), or set the target's python to an existing MLflow environment")
		}
		version := t.MLflowVersion
		if version == "" {
			version = config.DefaultMLflowVersion
		}
		args := []string{"--from", "mlflow==" + version}
		for _, pkg := range t.ExtraPackages {
			args = append(args, "--with", pkg)
		}
		r = runtimeCommand{executable: uvx, prefix: append(append([]string(nil), args...), "mlflow")}
		probe = append(append([]string{uvx}, args...), "python", "-c", "import importlib.metadata; print(importlib.metadata.version('mlflow'))")
	}
	output, err := runCapture(ctx, probe, t.WorkingDir, env, sensitiveValues(env), 64<<10)
	if err != nil {
		return r, fmt.Errorf("prepare MLflow runtime; set python to an environment containing MLflow, or allow uvx to prepare it: %w", err)
	}
	r.version = strings.TrimSpace(string(output))
	parts := strings.Split(r.version, ".")
	if len(parts) < 2 {
		return r, errors.New("MLflow runtime did not report a valid version")
	}
	if _, err = strconv.Atoi(parts[0]); err != nil {
		return r, errors.New("MLflow runtime did not report a valid version")
	}
	if t.MLflowVersion != "" && r.version != t.MLflowVersion {
		return r, fmt.Errorf("target requested MLflow %s but its Python environment contains %s; select the matching environment or update mlflow_version", t.MLflowVersion, r.version)
	}
	return r, nil
}

func localStore(t core.Target) (backend, artifactRoot string, err error) {
	if t.WorkingDir != "" {
		info, e := os.Stat(t.WorkingDir)
		if e != nil || !info.IsDir() {
			return "", "", fmt.Errorf("working_dir must be an existing directory: %s", t.WorkingDir)
		}
	}
	if strings.HasPrefix(t.TrackingURI, "sqlite:") {
		path, e := config.SQLitePath(t.TrackingURI, t.WorkingDir)
		if e != nil {
			return "", "", e
		}
		f, e := os.Open(path)
		if e != nil {
			return "", "", fmt.Errorf("SQLite store must already exist (lazymlflow never creates or migrates it): %w", e)
		}
		info, e := f.Stat()
		if e != nil {
			f.Close()
			return "", "", e
		}
		if !info.Mode().IsRegular() {
			f.Close()
			return "", "", errors.New("SQLite store must be a regular file")
		}
		var header [16]byte
		_, e = io.ReadFull(f, header[:])
		f.Close()
		if e != nil || string(header[:]) != "SQLite format 3\x00" {
			return "", "", errors.New("SQLite store is not an initialized SQLite database")
		}
		// SQLAlchemy must hand SQLite a URI filename: mode=ro prevents both
		// implicit creation and schema upgrades, including registry setup.
		backend = "sqlite:///file:" + (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath() + "?mode=ro&uri=true"
		artifactRoot = (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Dir(path))}).String()
		return backend, artifactRoot, nil
	}
	path, e := config.FilePath(t.TrackingURI, t.WorkingDir)
	if e != nil {
		return "", "", e
	}
	info, e := os.Stat(path)
	if e != nil {
		return "", "", fmt.Errorf("FileStore must already exist (lazymlflow never creates a new store): %w", e)
	}
	if !info.IsDir() {
		return "", "", errors.New("FileStore must be an existing directory")
	}
	if !hasExperiment(path) && !hasExperiment(filepath.Join(path, ".trash")) {
		return "", "", errors.New("directory does not contain an MLflow experiment (expected an experiment's meta.yaml); select an existing mlruns store")
	}
	backend = (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	return backend, backend, nil
}

func hasExperiment(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if info, err := os.Stat(filepath.Join(path, entry.Name(), "meta.yaml")); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func serverArgs(t core.Target, r runtimeCommand, backend, artifactRoot string, port int) []string {
	args := []string{"server", "--backend-store-uri", backend, "--registry-store-uri", backend, "--default-artifact-root", artifactRoot, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--workers", "1"}
	if t.ArtifactsDestination == "" {
		args = append(args, "--no-serve-artifacts")
	} else {
		args = append(args, "--serve-artifacts", "--artifacts-destination", t.ArtifactsDestination)
	}
	parts := strings.Split(r.version, ".")
	major, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	// Uvicorn became MLflow's default server in 3.5. Disable lifespan
	// background jobs; older MLflow uses gunicorn and has no such switch.
	if major > 3 || (major == 3 && minor >= 5) {
		args = append(args, "--uvicorn-opts", "--lifespan off")
	}
	return args
}

func (m *Manager) startServer(ctx context.Context, t core.Target, r runtimeCommand, env []string, backend, artifactRoot string, secrets []string) (string, func() error, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	args := r.command(serverArgs(t, r, backend, artifactRoot, port)...)
	p, err := startProcess(args, t.WorkingDir, env, secrets, 64<<10)
	if err != nil {
		return "", nil, fmt.Errorf("start MLflow %s: %w", r.version, err)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline, cancel := context.WithTimeout(ctx, m.startupTimeout)
	defer cancel()
	probe := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.Done():
			_ = p.stop()
			return "", nil, fmt.Errorf("MLflow %s startup: %w\n%s", r.version, deadline.Err(), p.logs.String())
		case <-p.done:
			_ = p.stop()
			return "", nil, fmt.Errorf("MLflow %s could not open this store; select the Python/MLflow version that created it (no migration was performed): %v\n%s", r.version, p.err, p.logs.String())
		case <-tick.C:
			req, _ := http.NewRequestWithContext(deadline, http.MethodGet, base+"/health", nil)
			resp, e := probe.Do(req)
			if e == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return base, p.stop, nil
				}
			}
		}
	}
}

type boundedBuffer struct {
	mu      sync.Mutex
	data    []byte
	limit   int
	secrets []string
}

func sensitiveValues(env []string) []string {
	var values []string
	for _, item := range env {
		name, value, _ := strings.Cut(item, "=")
		if value == "" {
			continue
		}
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "TOKEN") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "SECRET") || strings.HasSuffix(upper, "_KEY") || strings.HasSuffix(upper, "_KEY_ID") {
			values = append(values, value)
		}
		if strings.HasSuffix(upper, "_URL") {
			if u, err := url.Parse(value); err == nil && u.User != nil {
				if password, exists := u.User.Password(); exists && password != "" {
					values = append(values, password)
				}
			}
		}
	}
	return values
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if n >= b.limit {
		b.data = append(b.data[:0], p[n-b.limit:]...)
	} else {
		if excess := len(b.data) + n - b.limit; excess > 0 {
			b.data = append(b.data[:0], b.data[excess:]...)
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := string(b.data)
	for _, secret := range b.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "<redacted>")
		}
	}
	return s
}

type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	logs *boundedBuffer
	once sync.Once
}

func startProcess(argv []string, dir string, env, secrets []string, limit int) (*process, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty process command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	configureProcess(cmd)
	logs := &boundedBuffer{limit: limit, secrets: secrets}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{}), logs: logs}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}
func (p *process) stop() error {
	p.once.Do(func() {
		_ = terminateProcess(p.cmd)
		select {
		case <-p.done:
		case <-time.After(1200 * time.Millisecond):
		}
		_ = killProcess(p.cmd)
		select {
		case <-p.done:
		case <-time.After(3 * time.Second):
		}
	})
	return nil
}

func runCapture(ctx context.Context, argv []string, dir string, env, secrets []string, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, errors.New("empty runtime command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	configureProcess(cmd)
	stdout := &limitedOutput{limit: limit}
	stderr := &boundedBuffer{limit: 64 << 10, secrets: secrets}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = terminateProcess(cmd)
		select {
		case <-done:
		case <-time.After(600 * time.Millisecond):
			_ = killProcess(cmd)
			<-done
		}
		_ = killProcess(cmd)
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, stderr.String())
		}
		if stdout.overflow {
			return nil, errors.New("MLflow CLI output exceeded the size limit")
		}
		return bytes.Clone(stdout.Bytes()), nil
	}
}

type limitedOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if n > remaining {
		b.overflow = true
		p = p[:max(0, remaining)]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
