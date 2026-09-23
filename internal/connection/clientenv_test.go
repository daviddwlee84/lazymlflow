package connection

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func environmentMap(env []string) map[string]string {
	result := map[string]string{}
	for _, item := range env {
		name, value, _ := strings.Cut(item, "=")
		result[name] = value
	}
	return result
}

func TestClientEnvPlanKeepsReferencesAndIsolatesProfile(t *testing.T) {
	env := []string{"PATH=/bin", "LAB_TOKEN=credential-value", "MLFLOW_TRACKING_TOKEN=other", "MLFLOW_TRACKING_USERNAME=other-user", "MLFLOW_SERVER_WORKERS=99", "_MLFLOW_SERVER_FILE_STORE=other", "MLFLOW_TRACKING_INSECURE_TLS=true", "MLFLOW_TRACKING_AUTH=other-plugin", "MLFLOW_TRACKING_AWS_SIGV4=true", "MLFLOW_TRACKING_CLIENT_CERT_PATH=/other.pem", "AWS_PROFILE=ambient", "PROFILE_SOURCE=chosen", "MLFLOW_S3_ENDPOINT_URL=https://objects.example.com"}
	target := core.Target{ID: "lab", TrackingURI: "https://mlflow.example.com/tracking", TokenEnv: "LAB_TOKEN", CAFile: filepath.Join(t.TempDir(), "lab.pem"), Env: map[string]string{"AWS_PROFILE": "PROFILE_SOURCE"}}
	plan, err := clientEnvironmentPlan(target, target.TrackingURI, env)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(plan)
	for _, secret := range []string{"credential-value", "chosen", "other-user"} {
		if strings.Contains(string(b), secret) || strings.Contains(plan.RenderSH(), secret) {
			t.Fatalf("resolved value leaked into export: %s", b)
		}
	}
	resolved, err := ResolveClientEnvironment(plan, env)
	if err != nil {
		t.Fatal(err)
	}
	values := environmentMap(resolved)
	for _, name := range []string{"MLFLOW_TRACKING_USERNAME", "MLFLOW_SERVER_WORKERS", "_MLFLOW_SERVER_FILE_STORE", "MLFLOW_TRACKING_INSECURE_TLS", "MLFLOW_TRACKING_AUTH", "MLFLOW_TRACKING_AWS_SIGV4", "MLFLOW_TRACKING_CLIENT_CERT_PATH", "MLFLOW_SERVER_ENABLE_JOB_EXECUTION", "MLFLOW_ALLOW_FILE_STORE", "PYTHONUNBUFFERED"} {
		if _, exists := values[name]; exists {
			t.Fatalf("unexpected managed runtime variable %s", name)
		}
	}
	for name, want := range map[string]string{"MLFLOW_TRACKING_TOKEN": "credential-value", "MLFLOW_TRACKING_URI": target.TrackingURI, "MLFLOW_REGISTRY_URI": target.TrackingURI, "MLFLOW_TRACKING_SERVER_CERT_PATH": target.CAFile, "AWS_PROFILE": "chosen", "MLFLOW_S3_ENDPOINT_URL": "https://objects.example.com", "PATH": "/bin"} {
		if values[name] != want {
			t.Errorf("%s=%q want %q", name, values[name], want)
		}
	}
}

func TestClientEnvShellQuotesAndSimultaneousReferences(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	uri := "https://mlflow.example.com/path'\";$(touch " + marker + ")`touch " + marker + "`"
	// URL parsing permits quotes/dollar characters in paths. They remain data.
	target := core.Target{ID: "lab", TrackingURI: uri, TokenEnv: "MLFLOW_TRACKING_USERNAME", Env: map[string]string{"FIRST": "SECOND", "SECOND": "FIRST", "SELF": "SELF", "COPY": "MLFLOW_SERVER_WORKERS"}}
	env := []string{"PATH=/usr/bin:/bin", "FIRST=first", "SECOND=second", "SELF=same", "MLFLOW_TRACKING_USERNAME=test-token", "MLFLOW_SERVER_WORKERS=17"}
	plan, err := clientEnvironmentPlan(target, uri, env)
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := ResolveClientEnvironment(plan, env)
	if err != nil {
		t.Fatal(err)
	}
	wantMap := environmentMap(wanted)
	for _, shell := range []string{"sh", "bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX export execution is verified on native Unix; Git Bash rewrites PATH during startup")
			}
			path, err := exec.LookPath(shell)
			if err != nil {
				t.Skip("shell unavailable")
			}
			command := exec.Command(path, "-c", plan.RenderSH()+"env -0")
			command.Env = env
			output, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			got := environmentMap(strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00"))
			for name, want := range wantMap {
				if got[name] != want {
					t.Errorf("%s=%q want %q", name, got[name], want)
				}
			}
			for _, name := range plan.Unset {
				if _, exists := got[name]; exists {
					t.Errorf("did not unset %s", name)
				}
			}
		})
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("export executed text from a literal")
	}
}

func TestClientEnvMissingReferenceAndAuthValidation(t *testing.T) {
	target := core.Target{ID: "lab", TrackingURI: "http://localhost:5000", UsernameEnv: "USERNAME", PasswordEnv: "PASSWORD"}
	plan, err := clientEnvironmentPlan(target, target.TrackingURI, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ResolveClientEnvironment(plan, []string{"USERNAME=user"}); err == nil || !strings.Contains(err.Error(), "PASSWORD") {
		t.Fatalf("missing reference: %v", err)
	}
	if _, err = ResolveClientEnvironment(plan, []string{"USERNAME=user", "PASSWORD="}); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty password: %v", err)
	}
	target.UsernameEnv, target.TokenEnv = "USERNAME", "TOKEN"
	plan, _ = clientEnvironmentPlan(target, target.TrackingURI, nil)
	resolved, err := ResolveClientEnvironment(plan, []string{"PASSWORD=p", "USERNAME=u", "TOKEN=t"})
	if err != nil || environmentMap(resolved)["MLFLOW_TRACKING_USERNAME"] != "u" || environmentMap(resolved)["MLFLOW_TRACKING_TOKEN"] != "t" {
		t.Fatalf("preserve MLflow basic-over-token precedence: %v", err)
	}
}

func TestClientEnvNamedBasicRejectsIncompleteAndEmptyWithoutTokenFallback(t *testing.T) {
	for _, target := range []core.Target{
		{ID: "lab", TrackingURI: "https://example.com", UsernameEnv: "USERNAME"},
		{ID: "lab", TrackingURI: "https://example.com", PasswordEnv: "PASSWORD"},
	} {
		if _, err := ClientEnvironmentPlan(target); err == nil || !strings.Contains(err.Error(), "together") {
			t.Fatalf("incomplete Basic pair accepted: %v", err)
		}
	}
	target := core.Target{ID: "lab", TrackingURI: "https://example.com", UsernameEnv: "USERNAME", PasswordEnv: "PASSWORD", TokenEnv: "TOKEN"}
	plan, err := ClientEnvironmentPlan(target)
	if err != nil || len(plan.NonEmptyReferences) != 2 {
		t.Fatalf("Basic requirements: %+v %v", plan, err)
	}
	for _, env := range [][]string{{"USERNAME=user", "PASSWORD=", "TOKEN=wrong-principal"}, {"USERNAME=", "PASSWORD=password", "TOKEN=wrong-principal"}} {
		if _, err := ResolveClientEnvironment(plan, env); err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Fatalf("empty Basic pair fell back to token: %v", err)
		}
		cmd := exec.Command("sh", "-c", plan.RenderSH()+"printf unexpected")
		cmd.Env = env
		out, err := cmd.Output()
		if err == nil || len(out) != 0 {
			t.Fatalf("shell accepted empty Basic: %s %v", out, err)
		}
	}
	transient := core.Target{ID: "temporary", TrackingURI: "https://example.com", Transient: true}
	env := []string{"MLFLOW_TRACKING_USERNAME=user", "MLFLOW_TRACKING_PASSWORD=", "MLFLOW_TRACKING_TOKEN=token", "MLFLOW_TRACKING_AUTH=plugin", "MLFLOW_TRACKING_AWS_SIGV4=true", "MLFLOW_TRACKING_CLIENT_CERT_PATH=/client.pem"}
	plan, err = clientEnvironmentPlan(transient, transient.TrackingURI, env)
	if err != nil || len(plan.NonEmptyReferences) != 0 {
		t.Fatalf("transient should keep SDK defaults: %+v %v", plan, err)
	}
	resolved, err := ResolveClientEnvironment(plan, env)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"MLFLOW_TRACKING_AUTH", "MLFLOW_TRACKING_AWS_SIGV4", "MLFLOW_TRACKING_CLIENT_CERT_PATH"} {
		if environmentMap(resolved)[name] != environmentMap(env)[name] {
			t.Fatalf("transient lost %s", name)
		}
	}
}

func TestClientEnvTransientSSHAndUnsupportedTargets(t *testing.T) {
	env := []string{"MLFLOW_TRACKING_TOKEN=token", "MLFLOW_TRACKING_USERNAME=user", "MLFLOW_TRACKING_PASSWORD=password", "NO_PROXY=already.example", "no_proxy=second.example", "AWS_PROFILE=training"}
	target := core.Target{ID: "temporary", TrackingURI: "https://mlflow.example.com", Transient: true}
	plan, err := clientEnvironmentPlan(target, target.TrackingURI, env)
	if err != nil || plan.References["MLFLOW_TRACKING_TOKEN"] != "MLFLOW_TRACKING_TOKEN" {
		t.Fatalf("transient references: %+v %v", plan, err)
	}
	target.SSHHost = "training-host"
	if _, err := ClientEnvironmentPlan(target); err == nil || !strings.Contains(err.Error(), "targets exec") {
		t.Fatalf("exported SSH lifetime: %v", err)
	}
	resolved, err := ExecClientEnvironment(target, "http://127.0.0.1:43210", env)
	if err != nil {
		t.Fatal(err)
	}
	got := environmentMap(resolved)
	if got["MLFLOW_TRACKING_URI"] != "http://127.0.0.1:43210" || !strings.Contains(got["NO_PROXY"], "127.0.0.1") || got["NO_PROXY"] != got["no_proxy"] || !strings.Contains(got["NO_PROXY"], "already.example") || got["AWS_PROFILE"] != "training" {
		t.Fatalf("SSH child env: %#v", got)
	}
	for _, name := range []string{"MLFLOW_TRACKING_TOKEN", "MLFLOW_TRACKING_USERNAME", "MLFLOW_TRACKING_PASSWORD"} {
		if _, exists := got[name]; exists {
			t.Fatalf("proxy auth was passed to child: %s", name)
		}
	}
	for _, uri := range []string{"./mlruns", "sqlite:///db.sqlite"} {
		if _, err := ClientEnvironmentPlan(core.Target{ID: "local", TrackingURI: uri}); err == nil || !strings.Contains(err.Error(), "readonly") {
			t.Fatalf("local store %s: %v", uri, err)
		}
	}
	for _, dest := range []string{"MLFLOW_TRACKING_URI", "MLFLOW_TRACKING_TOKEN", "MLFLOW_TRACKING_INSECURE_TLS", "MLFLOW_SERVER_WORKERS", "_MLFLOW_SERVER_FILE_STORE"} {
		target := core.Target{ID: "lab", TrackingURI: "https://mlflow.example.com", Env: map[string]string{dest: "SOURCE"}}
		if _, err := ClientEnvironmentPlan(target); err == nil {
			t.Fatalf("accepted reserved destination %s", dest)
		}
	}
	if !reflect.DeepEqual(env, []string{"MLFLOW_TRACKING_TOKEN=token", "MLFLOW_TRACKING_USERNAME=user", "MLFLOW_TRACKING_PASSWORD=password", "NO_PROXY=already.example", "no_proxy=second.example", "AWS_PROFILE=training"}) {
		t.Fatal("changed inherited environment")
	}
}
