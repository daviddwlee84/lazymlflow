// Package connection owns REST connections and the lifetime of local MLflow
// servers. Local stores are always accessed through the official server.
package connection

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/mlflow"
)

type entry struct {
	ready   chan struct{}
	session *core.Session
	err     error
}

type Manager struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	entries map[string]*entry
	closed  bool
	// These indirections permit deterministic lifecycle tests without a Python
	// installation or downloads. Production constructors always use defaults.
	prepare        func(context.Context, core.Target, []string) (runtimeCommand, error)
	startupTimeout time.Duration
}

func NewManager() *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{ctx: ctx, cancel: cancel, entries: map[string]*entry{}, prepare: prepareRuntime, startupTimeout: 45 * time.Second}
}

func (m *Manager) Open(ctx context.Context, target core.Target) (*core.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := config.NormalizeTarget(target, "")
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(target)
	key := string(b)
	if target.Transient {
		key += "|transient"
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("connection manager is closed")
	}
	e, exists := m.entries[key]
	if !exists {
		e = &entry{ready: make(chan struct{})}
		m.entries[key] = e
		go m.initialize(ctx, key, e, target)
	}
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.ctx.Done():
		return nil, errors.New("connection manager is closed")
	case <-e.ready:
		return e.session, e.err
	}
}

func (m *Manager) initialize(request context.Context, key string, e *entry, target core.Target) {
	ctx, cancel := context.WithCancel(m.ctx)
	stop := context.AfterFunc(request, cancel)
	s, err := m.connect(ctx, target)
	stop()
	cancel()
	m.mu.Lock()
	if m.closed && s != nil {
		_ = s.Close()
		s = nil
		err = errors.New("connection manager is closed")
	}
	if err != nil {
		delete(m.entries, key)
	} else {
		closeProcess := s.CloseFunc
		s.CloseFunc = func() error {
			m.mu.Lock()
			if m.entries[key] == e {
				delete(m.entries, key)
			}
			m.mu.Unlock()
			if closeProcess != nil {
				return closeProcess()
			}
			return nil
		}
	}
	e.session = s
	e.err = err
	close(e.ready)
	m.mu.Unlock()
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.Unlock()
	var errs []error
	for _, e := range entries {
		<-e.ready
		if e.session != nil {
			if err := e.session.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) connect(ctx context.Context, t core.Target) (*core.Session, error) {
	credentials, err := credentialsFor(t)
	if err != nil {
		return nil, err
	}
	client, err := httpClient(t)
	if err != nil {
		return nil, err
	}
	env, err := childEnvironment(t, credentials)
	if err != nil {
		return nil, err
	}
	secrets := append(credentials.secrets(), sensitiveValues(env)...)
	local := !strings.HasPrefix(t.TrackingURI, "http://") && !strings.HasPrefix(t.TrackingURI, "https://")
	base := t.TrackingURI
	runtimeVersion := ""
	var stop func() error
	var runtimeSpec *runtimeCommand
	if local {
		backend, artifactRoot, e := localStore(t)
		if e != nil {
			return nil, e
		}
		r, e := m.prepare(ctx, t, env)
		if e != nil {
			return nil, e
		}
		runtimeSpec = &r
		runtimeVersion = r.version
		base, stop, err = m.startServer(ctx, t, r, env, backend, artifactRoot, secrets)
		if err != nil {
			return nil, err
		}
	}
	var runtimeMu sync.Mutex
	artifactCLI := func(callCtx context.Context, args []string) ([]byte, error) {
		callCtx, callCancel := context.WithCancel(callCtx)
		defer callCancel()
		stopOnManagerClose := context.AfterFunc(m.ctx, callCancel)
		defer stopOnManagerClose()
		if err := callCtx.Err(); err != nil {
			return nil, err
		}
		runtimeMu.Lock()
		if runtimeSpec == nil {
			r, e := m.prepare(callCtx, t, env)
			if e != nil {
				runtimeMu.Unlock()
				return nil, e
			}
			runtimeSpec = &r
		}
		r := *runtimeSpec
		runtimeMu.Unlock()
		cliEnv := setEnv(env, "MLFLOW_TRACKING_URI", base)
		cliEnv = setEnv(cliEnv, "MLFLOW_REGISTRY_URI", base)
		output, err := runCapture(callCtx, r.command(args...), t.WorkingDir, cliEnv, secrets, 4<<20)
		if err != nil {
			return nil, fmt.Errorf("MLflow artifacts (%s): %w", r.version, err)
		}
		return output, nil
	}
	backend := mlflow.New(base, mlflow.Options{HTTPClient: client, Username: credentials.username, Password: credentials.password, Token: credentials.token, Local: local, ArtifactCLI: artifactCLI, ArtifactDestination: t.ArtifactsDestination})
	if !local {
		versionCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		runtimeVersion, _ = backend.ServerVersion(versionCtx)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	web := t.WebURL
	if web == "" {
		web = base
	}
	return &core.Session{Backend: backend, Target: t, BaseURL: base, WebURL: web, RuntimeVersion: runtimeVersion, Local: local, CloseFunc: func() error {
		client.CloseIdleConnections()
		if stop != nil {
			return stop()
		}
		return nil
	}}, nil
}

func httpClient(t core.Target) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 8
	transport.ResponseHeaderTimeout = 30 * time.Second
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read target CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("target CA file does not contain a PEM certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	// Metadata requests have their own deadline in the REST client. Downloads
	// must not be cut off by a wall-clock timeout while bytes are still flowing.
	// The REST client also strips credentials on cross-origin signed redirects.
	return &http.Client{Transport: transport}, nil
}

type credentials struct{ username, password, token string }

func (c credentials) secrets() []string { return []string{c.username, c.password, c.token} }
func credentialsFor(t core.Target) (credentials, error) {
	var c credentials
	for _, field := range []struct {
		ref, ambient string
		dest         *string
	}{{t.UsernameEnv, "MLFLOW_TRACKING_USERNAME", &c.username}, {t.PasswordEnv, "MLFLOW_TRACKING_PASSWORD", &c.password}, {t.TokenEnv, "MLFLOW_TRACKING_TOKEN", &c.token}} {
		ref := field.ref
		if ref == "" && t.Transient {
			ref = field.ambient
		}
		if ref == "" {
			continue
		}
		value, exists := os.LookupEnv(ref)
		if !exists && field.ref != "" {
			return c, fmt.Errorf("credential environment variable %s is not set", ref)
		}
		*field.dest = value
	}
	if c.password != "" && c.username == "" {
		return c, errors.New("a tracking password requires a username")
	}
	return c, nil
}

func childEnvironment(t core.Target, c credentials) ([]string, error) {
	var env []string
	for _, v := range os.Environ() {
		name, _, _ := strings.Cut(v, "=")
		// Server and authentication defaults must never leak from another
		// profile. Artifact provider settings remain usable as with MLflow CLI.
		if strings.HasPrefix(name, "_MLFLOW_") || strings.HasPrefix(name, "MLFLOW_SERVER_") {
			continue
		}
		switch name {
		case "MLFLOW_TRACKING_URI", "MLFLOW_REGISTRY_URI", "MLFLOW_TRACKING_USERNAME", "MLFLOW_TRACKING_PASSWORD", "MLFLOW_TRACKING_TOKEN", "MLFLOW_TRACKING_INSECURE_TLS", "MLFLOW_TRACKING_SERVER_CERT_PATH", "MLFLOW_BACKEND_STORE_URI", "MLFLOW_ARTIFACTS_DESTINATION", "MLFLOW_DEFAULT_ARTIFACT_ROOT", "MLFLOW_ENABLE_WORKSPACES", "MLFLOW_WORKSPACE_STORE_URI", "MLFLOW_WORKSPACE":
			continue
		}
		env = append(env, v)
	}
	for dest, source := range t.Env {
		value, exists := os.LookupEnv(source)
		if !exists {
			return nil, fmt.Errorf("target environment variable %s is not set (for %s)", source, dest)
		}
		env = setEnv(env, dest, value)
	}
	if c.username != "" {
		env = setEnv(env, "MLFLOW_TRACKING_USERNAME", c.username)
	}
	if c.password != "" {
		env = setEnv(env, "MLFLOW_TRACKING_PASSWORD", c.password)
	}
	if c.token != "" {
		env = setEnv(env, "MLFLOW_TRACKING_TOKEN", c.token)
	}
	if t.CAFile != "" {
		env = setEnv(env, "MLFLOW_TRACKING_SERVER_CERT_PATH", t.CAFile)
	}
	for key, value := range map[string]string{"MLFLOW_SERVER_ENABLE_JOB_EXECUTION": "false", "MLFLOW_SERVER_JOB_ENABLE_PERIODIC_TASKS": "false", "MLFLOW_ALLOW_FILE_STORE": "true", "MLFLOW_ENABLE_WORKSPACES": "false", "MLFLOW_ENABLE_TELEMETRY": "false", "DO_NOT_TRACK": "1", "PYTHONUNBUFFERED": "1"} {
		env = setEnv(env, key, value)
	}
	return env, nil
}

func setEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, v := range env {
		if !strings.HasPrefix(v, key+"=") {
			out = append(out, v)
		}
	}
	return append(out, key+"="+value)
}
