package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serverCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, err bytes.Buffer
	code := Execute(context.Background(), args, Options{In: strings.NewReader(""), Out: &out, Err: &err, IsTerminal: func() bool { return false }})
	return code, out.String(), err.String()
}

func TestServerRecommendAndInitNoDaemon(t *testing.T) {
	code, out, err := serverCLI(t, "server", "recommend", "--remote-training", "--json")
	if code != 0 {
		t.Fatal(code, err)
	}
	var r map[string]any
	if json.Unmarshal([]byte(out), &r) != nil {
		t.Fatal(out)
	}
	if r["spec"].(map[string]any)["auth"] != "native" {
		t.Fatal(out)
	}
	dir := filepath.Join(t.TempDir(), "new")
	code, out, err = serverCLI(t, "server", "init", "test", "--dir", dir, "--json")
	if code != 0 {
		t.Fatal(code, err)
	}
	if !strings.Contains(out, `"started": false`) {
		t.Fatal(out)
	}
	if _, e := os.Stat(filepath.Join(dir, "compose.yaml")); e != nil {
		t.Fatal(e)
	}
	code, _, err = serverCLI(t, "server", "init", "test", "--dir", dir)
	if code != 1 || !strings.Contains(err, "already exists") {
		t.Fatal(code, err)
	}
}

func TestServerPartialInputsNeverStartWizard(t *testing.T) {
	for _, args := range [][]string{{"server", "init"}, {"server", "init", "--backend", "postgres"}, {"server", "init", "test", "--dir", "/tmp/unused", "--port", "0"}, {"server", "init", "test", "--dir", "/tmp/unused", "--artifacts", "minio"}} {
		code, _, err := serverCLI(t, args...)
		if code != 2 {
			t.Fatalf("%v: %d %s", args, code, err)
		}
	}
}

func TestServerDryRunAndExplicitOptOut(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	code, out, err := serverCLI(t, "server", "init", "test", "--dir", dir, "--access", "lan", "--hostname", "mlflow.example.internal", "--tls", "off", "--auth", "off", "--dry-run", "--json")
	if code != 0 {
		t.Fatal(code, err)
	}
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("dry run wrote files")
	}
	if !strings.Contains(out, `"auth": "off"`) {
		t.Fatal(out)
	}
}
