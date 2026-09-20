package connection

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestSSHProxyScopesHostPathCredentialsAndRedirects(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != "tracking.internal:8000" {
			t.Errorf("Host=%q", r.Host)
		}
		if r.Header.Get("Authorization") != "Bearer selected-secret" {
			t.Errorf("auth=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Forwarded-Host") != "" || r.Header.Get("Forwarded") != "" {
			t.Error("forwarded headers leaked")
		}
		switch r.URL.Path {
		case "/tracking/redirect":
			w.Header().Set("Location", "http://tracking.internal:8000/tracking/landing")
			w.WriteHeader(302)
		case "/tracking/external":
			w.Header().Set("Location", "https://artifacts.example/object?signature=value")
			w.WriteHeader(302)
		default:
			fmt.Fprint(w, r.URL.Path+"?"+r.URL.RawQuery)
		}
	}))
	defer upstream.Close()
	client, _ := httpClient(core.Target{})
	var stops atomic.Int32
	base, stop, err := startSSHProxy(core.Target{TrackingURI: "http://tracking.internal:8000/tracking"}, strings.TrimPrefix(upstream.URL, "http://"), client, credentials{token: "selected-secret"}, func() error { stops.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", base+"/metric?key=x%20y", nil)
	req.Header.Set("Authorization", "Bearer unrelated-secret")
	req.Header.Set("X-Forwarded-Host", "unrelated.example")
	req.Header.Set("Forwarded", "host=unrelated.example")
	res, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "/tracking/metric?key=x%20y" {
		t.Fatalf("%s %q", res.Status, body)
	}
	for _, tc := range []struct{ suffix, want string }{{"/redirect", base + "/landing"}, {"/external", "https://artifacts.example/object?signature=value"}} {
		res, err = browser.Get(base + tc.suffix)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.Header.Get("Location") != tc.want {
			t.Fatalf("redirect=%q", res.Header.Get("Location"))
		}
	}
	origin := strings.TrimSuffix(base, "/tracking")
	for _, p := range []string{"/elsewhere", "/tracking/../private", "/tracking/%2e%2e/private", "/tracking-other"} {
		res, err = browser.Get(origin + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatalf("out-of-base path %s accepted: %s", p, res.Status)
		}
	}
	req, _ = http.NewRequest("GET", base+"/metric", nil)
	req.Host = "foreign.example"
	res, err = browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("proxy Host was not checked")
	}
	if calls.Load() != 3 {
		t.Fatalf("unexpected upstream calls=%d", calls.Load())
	}
	stop()
	stop()
	if stops.Load() != 1 {
		t.Fatal("forwarder stopped more than once")
	}
	if _, err = browser.Get(base + "/metric"); err == nil {
		t.Fatal("proxy survived close")
	}
}

func TestSSHProxyValidatesOriginalTLSNameAndCA(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "secure") }))
	defer upstream.Close()
	cert := upstream.Certificate()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	forward := strings.TrimPrefix(upstream.URL, "https://")
	for _, tc := range []struct {
		name, origin, ca string
		status           int
	}{
		{"trusted original name", upstream.URL, ca, 200},
		{"untrusted", upstream.URL, "", 502},
		{"wrong original name", "https://wrong-name.example:443", ca, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := core.Target{TrackingURI: tc.origin, CAFile: tc.ca}
			client, err := httpClient(target)
			if err != nil {
				t.Fatal(err)
			}
			base, stop, err := startSSHProxy(target, forward, client, credentials{}, func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			res, err := http.Get(base + "/")
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.status {
				t.Fatalf("TLS status %d want %d", res.StatusCode, tc.status)
			}
		})
	}
}

func TestSSHSessionReuseAndCleanup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			fmt.Fprint(w, "3.6.0")
			return
		}
		fmt.Fprint(w, `{"experiments":[]}`)
	}))
	defer upstream.Close()
	manager := NewManager()
	defer manager.Close()
	var starts, stops atomic.Int32
	manager.sshStart = func(context.Context, core.Target, []string) (string, func() error, error) {
		starts.Add(1)
		return strings.TrimPrefix(upstream.URL, "http://"), func() error { stops.Add(1); return nil }, nil
	}
	manager.prepare = func(context.Context, core.Target, []string) (runtimeCommand, error) {
		t.Error("Python prepared for SSH metadata")
		return runtimeCommand{}, errors.New("unexpected runtime")
	}
	target := core.Target{ID: "ssh", SSHHost: "remote-lab", TrackingURI: "http://127.0.0.1:8000"}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := manager.Open(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if s.Local || s.BaseURL == target.TrackingURI || s.WebURL != s.BaseURL || s.RuntimeVersion != "3.6.0" {
		t.Fatalf("session %+v", s)
	}
	next, err := manager.Open(context.Background(), target)
	if err != nil || s != next || starts.Load() != 1 {
		t.Fatalf("session not reused: %v", err)
	}
	if _, err = s.Backend.SearchExperiments(context.Background(), core.ExperimentQuery{}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 1 {
		t.Fatal("forwarder not stopped")
	}
	if _, err = manager.Open(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	manager.Close()
	if stops.Load() != 2 {
		t.Fatal("manager did not stop replacement")
	}
}

func TestSSHSessionCancellationCleansProxy(t *testing.T) {
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer upstream.Close()
	manager := NewManager()
	defer manager.Close()
	var stopped atomic.Bool
	manager.sshStart = func(context.Context, core.Target, []string) (string, func() error, error) {
		return strings.TrimPrefix(upstream.URL, "http://"), func() error { stopped.Store(true); return nil }, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := manager.Open(ctx, core.Target{ID: "ssh", SSHHost: "host", TrackingURI: "http://127.0.0.1:8000"})
		done <- err
	}()
	<-entered
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("cancel not returned")
	}
	manager.Close()
	if !stopped.Load() {
		t.Fatal("canceled SSH connection leaked")
	}
}

// Explicit opt-in: no ordinary test run contacts an SSH host.
func TestSSHRealTarget(t *testing.T) {
	host := os.Getenv("LAZYMLFLOW_TEST_SSH_HOST")
	if host == "" {
		t.Skip("set LAZYMLFLOW_TEST_SSH_HOST and optional LAZYMLFLOW_TEST_SSH_URI")
	}
	uri := os.Getenv("LAZYMLFLOW_TEST_SSH_URI")
	if uri == "" {
		uri = "http://127.0.0.1:8000"
	}
	manager := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := manager.Open(ctx, core.Target{ID: "integration", SSHHost: host, TrackingURI: uri})
	if err != nil {
		manager.Close()
		t.Fatal(err)
	}
	page, err := s.Backend.SearchExperiments(ctx, core.ExperimentQuery{MaxResults: 100})
	if err != nil {
		manager.Close()
		t.Fatal(err)
	}
	t.Logf("server %s, experiments %d, Local=%v", s.RuntimeVersion, len(page.Experiments), s.Local)
	base, _ := url.Parse(s.BaseURL)
	res, err := http.Get(base.Scheme + "://" + base.Host + base.Path + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("browser returned %s", res.Status)
	}
	if err = manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = http.Get(s.BaseURL + "/version"); err == nil {
		t.Fatal("proxy survived close")
	}
}

func TestSSHChangedTargetReplacesOwnedResources(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "3.6.0") }))
	defer upstream.Close()
	manager := NewManager()
	defer manager.Close()
	var stops atomic.Int32
	manager.sshStart = func(context.Context, core.Target, []string) (string, func() error, error) {
		return strings.TrimPrefix(upstream.URL, "http://"), func() error { stops.Add(1); return nil }, nil
	}
	target := core.Target{ID: "same", SSHHost: "lab", TrackingURI: "http://localhost:8000"}
	first, err := manager.Open(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	target.TrackingURI = "http://localhost:8001"
	second, err := manager.Open(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || stops.Load() != 1 {
		t.Fatal("changed target retained the old tunnel")
	}
	if _, err = http.Get(first.BaseURL + "/version"); err == nil {
		t.Fatal("old proxy is still alive")
	}
	manager.Close()
	if stops.Load() != 2 {
		t.Fatal("replacement proxy wasn't closed")
	}
}

func TestSSHStartingTargetReplacementIsCancelled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "3.6.0") }))
	defer upstream.Close()
	manager := NewManager()
	defer manager.Close()
	entered := make(chan struct{})
	manager.sshStart = func(ctx context.Context, target core.Target, _ []string) (string, func() error, error) {
		if target.SSHHost == "slow" {
			close(entered)
			<-ctx.Done()
			return "", nil, ctx.Err()
		}
		return strings.TrimPrefix(upstream.URL, "http://"), func() error { return nil }, nil
	}
	target := core.Target{ID: "same", SSHHost: "slow", TrackingURI: "http://localhost:8000"}
	result := make(chan error, 1)
	go func() { _, err := manager.Open(context.Background(), target); result <- err }()
	<-entered
	replacement := target
	replacement.SSHHost = "ready"
	if _, err := manager.Open(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("old initializer err=%v", err)
	}
}

func TestSSHArtifactEnvironmentBypassesOnlyLoopbackProxy(t *testing.T) {
	env := bypassLoopbackProxy([]string{"HTTP_PROXY=http://corporate-proxy:8080", "NO_PROXY=company.internal", "no_proxy=another.internal", "OTHER=value"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"HTTP_PROXY=http://corporate-proxy:8080", "NO_PROXY=company.internal,another.internal,127.0.0.1", "no_proxy=company.internal,another.internal,127.0.0.1", "OTHER=value"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
}

func TestSSHProxyBasicAuthPrecedenceAndBrowserOrigins(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "selected-user" || password != "selected-password" {
			t.Error("Basic auth did not take precedence over token")
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://tracking.internal:8000" {
			t.Errorf("upstream origin=%q", origin)
		}
		fmt.Fprint(w, "OK")
	}))
	defer upstream.Close()
	client, _ := httpClient(core.Target{})
	base, stop, err := startSSHProxy(core.Target{TrackingURI: "http://tracking.internal:8000"}, strings.TrimPrefix(upstream.URL, "http://"), client, credentials{username: "selected-user", password: "selected-password", token: "unused-token"}, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, test := range []struct {
		origin string
		status int
	}{{"", 200}, {base, 200}, {"http://tracking.internal:8000", 200}, {"https://unrelated.example", 403}, {"null", 403}} {
		req, _ := http.NewRequest("POST", base+"/api/2.0/mlflow/experiments/search", strings.NewReader(`{}`))
		if test.origin != "" {
			req.Header.Set("Origin", test.origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != test.status {
			t.Errorf("origin %q returned %d want %d", test.origin, res.StatusCode, test.status)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("unrelated browser origin reached upstream")
	}
}
