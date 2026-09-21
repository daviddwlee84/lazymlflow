package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/connection"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func targetEnvTestOptions(t *testing.T) (Options, *fakeConnector, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	// The SDK credential-file guard must never read the developer's credentials.
	t.Setenv("HOME", t.TempDir())
	return testOptions(t)
}

func TestTargetsEnvNoConnectionNoCredentialValues(t *testing.T) {
	t.Setenv("LAB_TOKEN", "must-not-be-rendered")
	for _, jsonMode := range []bool{false, true} {
		opts, connector, out, stderr := targetEnvTestOptions(t)
		opts.LoadConfig = func(string) (*config.Config, error) {
			return &config.Config{Targets: []core.Target{{ID: "lab", TrackingURI: "https://lab.example/tracking", TokenEnv: "LAB_TOKEN"}}}, nil
		}
		args := []string{"targets", "env", "lab"}
		if jsonMode {
			args = append(args, "--json")
		}
		if code := Execute(context.Background(), args, opts); code != 0 || stderr.Len() != 0 {
			t.Fatalf("code %d stderr %s", code, stderr)
		}
		if len(connector.opened) != 0 || strings.Contains(out.String(), "must-not-be-rendered") {
			t.Fatalf("side effect or secret leak: %s", out)
		}
		if jsonMode {
			var plan connection.ClientEnvPlan
			if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.References["MLFLOW_TRACKING_TOKEN"] != "LAB_TOKEN" || plan.Set["MLFLOW_TRACKING_URI"] != "https://lab.example/tracking" {
				t.Fatalf("invalid recipe: %s %v", out, err)
			}
		} else if !strings.Contains(out.String(), "${LAB_TOKEN?LAB_TOKEN is required}") {
			t.Fatal("missing credential reference", out.String())
		}
	}
}

func TestTargetsEnvironmentUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"targets", "env", "--shell", "fish"},
		{"targets", "env", "--shell", "sh", "--json"},
		{"targets", "env", "second", "--target", "first"},
		{"targets", "env", "first", "--tracking-uri", "http://temporary"},
		{"targets", "exec"}, {"targets", "exec", "first", "echo"},
		{"targets", "exec", "first", "second", "--", "echo"},
		{"targets", "exec", "--"}, {"targets", "exec", "--json", "--", "echo"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			opts, connector, out, stderr := targetEnvTestOptions(t)
			if code := Execute(context.Background(), args, opts); code != 2 || out.Len() != 0 || len(connector.opened) != 0 {
				t.Fatalf("code %d stdout %s stderr %s", code, out, stderr)
			}
		})
	}
}

func TestTargetExecHelper(t *testing.T) {
	if os.Getenv("LAZYMLFLOW_EXEC_HELPER") == "" {
		return
	}
	if os.Getenv("LAZYMLFLOW_EXEC_HELPER") == "wait" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"uri": os.Getenv("MLFLOW_TRACKING_URI"), "registry": os.Getenv("MLFLOW_REGISTRY_URI"),
		"username": os.Getenv("MLFLOW_TRACKING_USERNAME"), "token": os.Getenv("MLFLOW_TRACKING_TOKEN"),
		"args": os.Args, "no_proxy": os.Getenv("NO_PROXY"),
	})
	fmt.Fprint(os.Stderr, "child-only-error")
	os.Exit(7)
}

func TestTargetsExecPreservesChildStatusAndArgumentsWithoutConnection(t *testing.T) {
	t.Setenv("LAZYMLFLOW_EXEC_HELPER", "echo")
	t.Setenv("MLFLOW_TRACKING_TOKEN", "wrong-profile")
	opts, connector, out, stderr := targetEnvTestOptions(t)
	binary, _ := os.Executable()
	child := []string{binary, "-test.run=^TestTargetExecHelper$", "--", "space in arg", "$(not-evaluated)", "--json"}
	args := append([]string{"targets", "exec", "second", "--"}, child...)
	if code := Execute(context.Background(), args, opts); code != 7 || stderr.String() != "child-only-error" {
		t.Fatalf("code %d stdout %s stderr %s", code, out, stderr)
	}
	if len(connector.opened) != 0 {
		t.Fatal("direct child opened tracking connection")
	}
	var result struct {
		URI      string   `json:"uri"`
		Registry string   `json:"registry"`
		Token    string   `json:"token"`
		Args     []string `json:"args"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.URI != "https://second/prefix" || result.Registry != result.URI || result.Token != "" || !reflect.DeepEqual(result.Args, child) {
		t.Fatalf("wrong child state: %+v", result)
	}
}

type execTunnelConnector struct {
	opened, closed, sessionClosed int
	output                        *bytes.Buffer
	sessionClosedAfterOutput      bool
}

func (c *execTunnelConnector) Open(_ context.Context, t core.Target) (*core.Session, error) {
	c.opened++
	return &core.Session{Target: t, BaseURL: "http://127.0.0.1:49991/tracking", CloseFunc: func() error {
		c.sessionClosed++
		c.sessionClosedAfterOutput = c.output.Len() > 0
		return nil
	}}, nil
}

func (c *execTunnelConnector) Close() error { c.closed++; return nil }

func TestTargetsExecOwnsSSHLifetimeAndStripsUpstreamAuth(t *testing.T) {
	t.Setenv("LAZYMLFLOW_EXEC_HELPER", "echo")
	t.Setenv("LAB_TOKEN", "upstream-credential")
	opts, _, out, stderr := targetEnvTestOptions(t)
	opts.LoadConfig = func(string) (*config.Config, error) {
		return &config.Config{Targets: []core.Target{{ID: "ssh", TrackingURI: "https://remote.example/tracking", SSHHost: "host-alias", TokenEnv: "LAB_TOKEN"}}}, nil
	}
	connector := &execTunnelConnector{output: out}
	opts.Connector = connector
	binary, _ := os.Executable()
	code := Execute(context.Background(), []string{"targets", "exec", "ssh", "--", binary, "-test.run=^TestTargetExecHelper$"}, opts)
	if code != 7 || connector.opened != 1 || connector.closed != 1 || connector.sessionClosed != 1 || !connector.sessionClosedAfterOutput {
		t.Fatalf("code %d connector %+v stderr %s", code, connector, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["uri"] != "http://127.0.0.1:49991/tracking" || result["token"] != "" || !strings.Contains(result["no_proxy"].(string), "127.0.0.1") {
		t.Fatalf("SSH environment: %#v", result)
	}
}

func TestTargetsEnvAndExecRejectLocalAdapters(t *testing.T) {
	for _, uri := range []string{"sqlite:///missing.db", "./mlruns"} {
		for _, command := range [][]string{{"targets", "env", "local"}, {"targets", "exec", "local", "--", "must-not-run"}} {
			opts, connector, out, stderr := targetEnvTestOptions(t)
			opts.LoadConfig = func(string) (*config.Config, error) {
				return &config.Config{Targets: []core.Target{{ID: "local", TrackingURI: uri}}}, nil
			}
			if code := Execute(context.Background(), command, opts); code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "readonly") || len(connector.opened) > 0 {
				t.Fatalf("code %d stderr %s", code, stderr)
			}
		}
	}
}

func TestTargetsExecCancellationClosesTunnel(t *testing.T) {
	t.Setenv("LAZYMLFLOW_EXEC_HELPER", "wait")
	opts, _, out, _ := targetEnvTestOptions(t)
	opts.LoadConfig = func(string) (*config.Config, error) {
		return &config.Config{Targets: []core.Target{{ID: "ssh", TrackingURI: "http://remote:5000", SSHHost: "alias"}}}, nil
	}
	connector := &execTunnelConnector{output: out}
	opts.Connector = connector
	binary, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if code := Execute(ctx, []string{"targets", "exec", "ssh", "--", binary, "-test.run=^TestTargetExecHelper$"}, opts); code != 130 || connector.closed != 1 || connector.sessionClosed != 1 {
		t.Fatalf("code %d connector %+v", code, connector)
	}
}

func TestTargetsExecRejectsNamedSDKCredentialFileConflictBeforeConnection(t *testing.T) {
	opts, connector, out, stderr := targetEnvTestOptions(t)
	credentialDir := filepath.Join(os.Getenv("HOME"), ".mlflow")
	if err := os.MkdirAll(credentialDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentialDir, "credentials"), []byte("[mlflow]\nmlflow_tracking_username=stored-principal\nmlflow_tracking_password=stored-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		out.Reset()
		stderr.Reset()
		code := Execute(context.Background(), []string{"targets", "exec", id, "--", "must-not-start"}, opts)
		if code != 2 || len(connector.opened) != 0 || !strings.Contains(stderr.String(), "~/.mlflow/credentials") || strings.Contains(stderr.String(), "stored-principal") || strings.Contains(stderr.String(), "stored-secret") {
			t.Fatalf("code=%d diagnostic=%s", code, stderr)
		}
	}
	t.Setenv("LAZYMLFLOW_EXEC_HELPER", "echo")
	out.Reset()
	stderr.Reset()
	binary, _ := os.Executable()
	if code := Execute(context.Background(), []string{"--tracking-uri", "https://temporary", "targets", "exec", "--", binary, "-test.run=^TestTargetExecHelper$"}, opts); code != 7 {
		t.Fatalf("transient did not preserve SDK-default opt-in: code=%d %s", code, stderr)
	}
}
