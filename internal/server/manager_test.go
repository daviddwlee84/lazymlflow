package server

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls [][]string
	reply func([]string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, _ string, args []string, _ string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{}, args...))
	if f.reply != nil {
		if data, err := f.reply(args); data != nil || err != nil {
			return data, err
		}
	}
	if args[0] == "context" {
		return []byte(`"unix:///var/run/docker.sock"`), nil
	}
	if len(args) > 1 && args[0] == "compose" && args[1] == "version" {
		return []byte("5.1.0\n"), nil
	}
	return []byte{}, nil
}

func TestLifecycleScopesAndPreservesData(t *testing.T) {
	s := DefaultSpec()
	s.ID = "lifecycle"
	stack, err := Init(context.Background(), filepath.Join(t.TempDir(), "stack"), s)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeRunner{}
	manager := &Manager{Runner: fake}
	if err = manager.Up(context.Background(), stack); err != nil {
		t.Fatal(err)
	}
	if err = manager.Down(context.Background(), stack); err != nil {
		t.Fatal(err)
	}
	var down []string
	for _, args := range fake.calls {
		if args[0] != "compose" || args[1] == "version" {
			continue
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--project-name "+stack.Project) || !strings.Contains(joined, filepath.Join(stack.Dir, "compose.yaml")) {
			t.Fatalf("unscoped command: %v", args)
		}
		for _, a := range args {
			if a == "--volumes" || a == "-v" || a == "--rmi" {
				t.Fatal("destructive lifecycle", args)
			}
		}
		if strings.Contains(joined, " down ") {
			down = args
		}
	}
	if down == nil {
		t.Fatal("down was not executed")
	}
}

func TestExistingOtherStackIsRejected(t *testing.T) {
	s := DefaultSpec()
	s.ID = "ownership"
	stack, err := Init(context.Background(), filepath.Join(t.TempDir(), "stack"), s)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{reply: func(args []string) ([]byte, error) {
		if args[0] == "ps" {
			return []byte("id\t" + stack.Project + "\t/another/directory\n"), nil
		}
		return nil, nil
	}}
	err = (&Manager{Runner: f}).Down(context.Background(), stack)
	if err == nil || !strings.Contains(err.Error(), "owned by another") {
		t.Fatalf("expected ownership rejection: %v", err)
	}
}

func TestDockerContextOverridesLocalHostGuard(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	t.Setenv("DOCKER_CONTEXT", "remote-test")
	s := DefaultSpec()
	s.ID = "context"
	stack, err := Init(context.Background(), filepath.Join(t.TempDir(), "stack"), s)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{reply: func(args []string) ([]byte, error) {
		if args[0] == "context" {
			return []byte(`"ssh://remote.example"`), nil
		}
		return nil, nil
	}}
	err = (&Manager{Runner: f}).Down(context.Background(), stack)
	if err == nil || !strings.Contains(err.Error(), "remote engine") {
		t.Fatalf("expected effective-context guard: %v", err)
	}
	for _, args := range f.calls {
		if strings.Contains(strings.Join(args, " "), " down ") {
			t.Fatal("touched a remote deployment")
		}
	}
}

func TestRetainedVolumeOwnershipRejectsCopiedDirectory(t *testing.T) {
	s := DefaultSpec()
	s.ID = "volume"
	stack, err := Init(context.Background(), filepath.Join(t.TempDir(), "stack"), s)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{reply: func(args []string) ([]byte, error) {
		if len(args) > 1 && args[0] == "volume" && args[1] == "ls" {
			return []byte(stack.Project + "_mlflow-data\n"), nil
		}
		if len(args) > 1 && args[0] == "volume" && args[1] == "inspect" {
			return []byte(`[{"Name":"old","Labels":{"io.lazymlflow.owner":"` + stack.Project + `","io.lazymlflow.stack-directory":"/old/stack"}}]`), nil
		}
		return nil, nil
	}}
	if err = (&Manager{Runner: f}).Up(context.Background(), stack); err == nil || !strings.Contains(err.Error(), "persistent volumes") {
		t.Fatalf("expected retained volume ownership rejection: %v", err)
	}
}

func TestStatusAndRedactedLogs(t *testing.T) {
	s := DefaultSpec()
	s.ID = "logs"
	s.Auth = "native"
	stack, err := Init(context.Background(), filepath.Join(t.TempDir(), "stack"), s)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := os.ReadFile(stack.AdminPasswordPath())
	f := &fakeRunner{reply: func(args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " logs ") {
			return []byte("password=" + string(secret) + "\n"), nil
		}
		if strings.Contains(joined, " ps --all") {
			return []byte(`{"Name":"x","Service":"mlflow","State":"running","Health":"healthy","ExitCode":0}` + "\n"), nil
		}
		return nil, nil
	}}
	manager := &Manager{Runner: f}
	status, err := manager.Status(context.Background(), stack)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Services) != 1 || status.Services[0].Health != "healthy" {
		t.Fatal(status)
	}
	var output bytes.Buffer
	if err = manager.Logs(context.Background(), stack, false, 100, "", &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), string(secret)) || !strings.Contains(output.String(), "[redacted]") {
		t.Fatal("unredacted log")
	}
	w := &redactingWriter{out: &output, stack: stack}
	_, _ = w.Write(secret[:8])
	_, _ = w.Write(append(secret[8:], '\n'))
	w.flush()
	if strings.Contains(output.String(), string(secret)) {
		t.Fatal("chunked secret leaked")
	}
}

func TestComposeErrorsDoNotExposeCredentials(t *testing.T) {
	s := DefaultSpec()
	s.ID = "error"
	s.Auth = "native"
	stack, err := Init(context.Background(), filepath.Join(t.TempDir(), "stack"), s)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := os.ReadFile(stack.AdminPasswordPath())
	fake := &fakeRunner{reply: func(args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), " up ") {
			return []byte("fatal " + string(secret)), fmt.Errorf("exit status 1")
		}
		return nil, nil
	}}
	err = (&Manager{Runner: fake}).Up(context.Background(), stack)
	if err == nil || strings.Contains(err.Error(), string(secret)) {
		t.Fatal("unredacted failure")
	}
}
