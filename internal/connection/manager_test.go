package connection

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestRemoteConnectionsDoNotPreparePythonAndScopeCredentials(t *testing.T) {
	var auth atomic.Value
	auth.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if r.URL.Path == "/version" {
			fmt.Fprint(w, "3.16.1")
			return
		}
		fmt.Fprint(w, `{"experiments":[]}`)
	}))
	defer server.Close()
	t.Setenv("MLFLOW_TRACKING_TOKEN", "ambient-secret")
	t.Setenv("PROFILE_TOKEN", "scoped-secret")
	manager := NewManager()
	defer manager.Close()
	manager.prepare = func(context.Context, core.Target, []string) (runtimeCommand, error) {
		t.Error("prepared Python for remote metadata")
		return runtimeCommand{}, errors.New("must not run")
	}
	for _, test := range []struct {
		name   string
		target core.Target
		want   string
	}{{"configured", core.Target{ID: "configured", TrackingURI: server.URL}, ""}, {"transient", core.Target{ID: "temporary", TrackingURI: server.URL, Transient: true}, "Bearer ambient-secret"}, {"reference", core.Target{ID: "referenced", TrackingURI: server.URL, TokenEnv: "PROFILE_TOKEN"}, "Bearer scoped-secret"}} {
		t.Run(test.name, func(t *testing.T) {
			s, err := manager.Open(context.Background(), test.target)
			if err != nil {
				t.Fatal(err)
			}
			if s.RuntimeVersion != "3.16.1" || s.Local {
				t.Fatalf("session %+v", s)
			}
			if _, err = s.Backend.SearchExperiments(context.Background(), core.ExperimentQuery{}); err != nil {
				t.Fatal(err)
			}
			if got := auth.Load().(string); got != test.want {
				t.Fatalf("auth %q want %q", got, test.want)
			}
		})
	}
}

func TestRemoteClientTLSAndDownloadTimeout(t *testing.T) {
	client, err := httpClient(core.Target{})
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != 0 {
		t.Fatal("artifact downloads have a wall clock deadline")
	}
	if client.Transport.(*http.Transport).ResponseHeaderTimeout == 0 {
		t.Fatal("missing header timeout")
	}
	if _, err = httpClient(core.Target{CAFile: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing CA accepted")
	}
	path := filepath.Join(t.TempDir(), "invalid.pem")
	_ = os.WriteFile(path, []byte("not a certificate"), 0600)
	if _, err = httpClient(core.Target{CAFile: path}); err == nil {
		t.Fatal("invalid CA accepted")
	}
}

func TestChildEnvironmentIsolation(t *testing.T) {
	t.Setenv("MLFLOW_TRACKING_TOKEN", "ambient")
	t.Setenv("_MLFLOW_SERVER_FILE_STORE", "wrong")
	t.Setenv("MLFLOW_SERVER_ENABLE_JOB_EXECUTION", "true")
	t.Setenv("PROFILE_SECRET", "chosen")
	t.Setenv("MLFLOW_TRACKING_INSECURE_TLS", "true")
	target := core.Target{Env: map[string]string{"AWS_SECRET_ACCESS_KEY": "PROFILE_SECRET"}}
	env, err := childEnvironment(target, credentials{})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"MLFLOW_TRACKING_TOKEN=ambient", "_MLFLOW_SERVER_FILE_STORE=wrong", "MLFLOW_TRACKING_INSECURE_TLS=true", "MLFLOW_SERVER_ENABLE_JOB_EXECUTION=true"} {
		if strings.Contains(joined, bad) {
			t.Fatal("ambient profile leaked", bad)
		}
	}
	for _, want := range []string{"AWS_SECRET_ACCESS_KEY=chosen", "MLFLOW_SERVER_ENABLE_JOB_EXECUTION=false", "MLFLOW_ALLOW_FILE_STORE=true"} {
		if !strings.Contains(joined, want) {
			t.Fatal("missing", want)
		}
	}
	if _, err = credentialsFor(core.Target{TokenEnv: "LAZYMLFLOW_TEST_UNSET_CREDENTIAL"}); err == nil {
		t.Fatal("missing reference accepted")
	}
}

func makeFileStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "0"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0", "meta.yaml"), []byte("experiment_id: '0'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestRuntimeHelper is a subprocess fake. It implements readiness and records
// startup arguments while running in the same process-group policy as MLflow.
func TestRuntimeHelper(t *testing.T) {
	if os.Getenv("LAZYMLFLOW_RUNTIME_HELPER") != "1" {
		return
	}
	port := ""
	for i, arg := range os.Args {
		if arg == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if port == "" {
		fmt.Fprintln(os.Stderr, "no port")
		os.Exit(2)
	}
	if path := os.Getenv("LAZYMLFLOW_RUNTIME_PID"); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600)
	}
	if os.Getenv("LAZYMLFLOW_RUNTIME_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "fake secret: test-credential")
		os.Exit(7)
	}
	if os.Getenv("LAZYMLFLOW_RUNTIME_HANG") == "1" {
		_ = http.ListenAndServe("127.0.0.1:"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
		os.Exit(0)
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "OK") })
	http.HandleFunc("/api/2.0/mlflow/experiments/search", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"experiments":[{"experiment_id":"0","name":"Default"}]}`)
	})
	_ = http.ListenAndServe("127.0.0.1:"+port, nil)
	os.Exit(0)
}

func helperManager(t *testing.T) *Manager {
	t.Helper()
	t.Setenv("LAZYMLFLOW_RUNTIME_HELPER", "1")
	m := NewManager()
	m.prepare = func(context.Context, core.Target, []string) (runtimeCommand, error) {
		return runtimeCommand{executable: os.Args[0], prefix: []string{"-test.run=^TestRuntimeHelper$", "--"}, version: "3.16.1"}, nil
	}
	m.startupTimeout = 3 * time.Second
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestManagedServerReuseSurvivesCallerCancellation(t *testing.T) {
	m := helperManager(t)
	target := core.Target{ID: "local", TrackingURI: makeFileStore(t)}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := m.Open(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	second, err := m.Open(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if s != second {
		t.Fatal("target spawned a second server")
	}
	page, err := s.Backend.SearchExperiments(context.Background(), core.ExperimentQuery{})
	if err != nil || len(page.Experiments) != 1 {
		t.Fatal("established server died with request context", err)
	}
	url := s.BaseURL
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	client := http.Client{Timeout: 200 * time.Millisecond}
	if resp, err := client.Get(url + "/health"); err == nil {
		resp.Body.Close()
		t.Fatal("server survived manager close")
	}
	if _, err = m.Open(context.Background(), target); err == nil {
		t.Fatal("closed manager accepted Open")
	}
}

func TestManagedServerConcurrentOpenAndExplicitClose(t *testing.T) {
	m := helperManager(t)
	target := core.Target{ID: "local", TrackingURI: makeFileStore(t)}
	var wg sync.WaitGroup
	results := make(chan *core.Session, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := m.Open(context.Background(), target)
			if err != nil {
				t.Error(err)
				return
			}
			results <- s
		}()
	}
	wg.Wait()
	close(results)
	var first *core.Session
	for s := range results {
		if first == nil {
			first = s
		} else if s != first {
			t.Fatal("duplicate sessions")
		}
	}
	if first == nil {
		t.Fatal("no session")
	}
	_ = first.Close()
	next, err := m.Open(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if first == next {
		t.Fatal("closed session reused")
	}
}

func TestManagedServerFailureRedactsCredentials(t *testing.T) {
	m := helperManager(t)
	t.Setenv("LAZYMLFLOW_RUNTIME_FAIL", "1")
	t.Setenv("TEST_TOKEN", "test-credential")
	_, err := m.Open(context.Background(), core.Target{ID: "broken", TrackingURI: makeFileStore(t), TokenEnv: "TEST_TOKEN"})
	if err == nil || strings.Contains(err.Error(), "test-credential") || !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("error %v", err)
	}
}

func TestManagedStartupCancellation(t *testing.T) {
	m := helperManager(t)
	t.Setenv("LAZYMLFLOW_RUNTIME_HANG", "1")
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("LAZYMLFLOW_RUNTIME_PID", pidFile)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err := m.Open(ctx, core.Target{ID: "slow", TrackingURI: makeFileStore(t)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// Close waits for an in-flight startup to clean up its process group.
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.ReadFile(pidFile); err != nil {
		t.Fatal("helper did not start", err)
	}
}

func TestLocalStorePreflightAndReadOnlyURI(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := localStore(core.Target{TrackingURI: dir}); err == nil {
		t.Fatal("empty directory accepted")
	}
	if _, _, err := localStore(core.Target{TrackingURI: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("missing directory accepted")
	}
	path := filepath.Join(dir, "a space.db")
	if err := os.WriteFile(path, []byte("SQLite format 3\x00rest"), 0600); err != nil {
		t.Fatal(err)
	}
	uri, root, err := localStore(core.Target{TrackingURI: "sqlite:///" + path})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uri, "a%20space.db?mode=ro&uri=true") || !strings.HasPrefix(root, "file:") {
		t.Fatal(uri, root)
	}
	args := strings.Join(serverArgs(core.Target{}, runtimeCommand{version: "3.16.1"}, uri, root, 34567), " ")
	if strings.Count(args, uri) != 2 || !strings.Contains(args, "--workers 1") || !strings.Contains(args, "--no-serve-artifacts") || !strings.Contains(args, "--lifespan off") {
		t.Fatal(args)
	}
	old := strings.Join(serverArgs(core.Target{}, runtimeCommand{version: "2.22.0"}, uri, root, 34567), " ")
	if strings.Contains(old, "uvicorn") {
		t.Fatal(old)
	}
}

func TestBoundedRedactedLogs(t *testing.T) {
	b := boundedBuffer{limit: 18, secrets: []string{"secret"}}
	b.Write([]byte("older discarded data"))
	b.Write([]byte("split se"))
	b.Write([]byte("cret error"))
	s := b.String()
	if strings.Contains(s, "secret") || !strings.Contains(s, "<redacted>") || strings.Contains(s, "older") {
		t.Fatal(s)
	}
}
