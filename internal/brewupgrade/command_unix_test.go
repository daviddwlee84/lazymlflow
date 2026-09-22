//go:build unix

package brewupgrade

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeFakeManagerArgvProgressAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-brew")
	body := `#!/bin/sh
case "$1" in
  --cellar) printf '%s\n' "$HOMEBREW_NO_AUTO_UPDATE" "$#" "$2" ;;
  upgrade) printf 'stdout:%s:%s\n' "$#" "$2"; printf 'stderr progress\n' >&2 ;;
  sleep) exec sleep 30 ;;
  fail) exit 7 ;;
  --prefix) printf '%9000s' x ;;
esac
`
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	output, err := runCommand(context.Background(), path, []string{"--cellar", "acme/tools/installed-name"}, &progress)
	if err != nil || output != "1\n2\nacme/tools/installed-name\n" || progress.Len() != 0 {
		t.Fatalf("query=%q progress=%q error=%v", output, progress.String(), err)
	}
	output, err = runCommand(context.Background(), path, []string{"upgrade", "acme/tools/installed-name"}, &progress)
	if err != nil || output != "" || !strings.Contains(progress.String(), "stdout:2:acme/tools/installed-name") || !strings.Contains(progress.String(), "stderr progress") {
		t.Fatalf("upgrade output=%q progress=%q error=%v", output, progress.String(), err)
	}
	if _, err := runCommand(context.Background(), path, []string{"fail"}, nil); err == nil {
		t.Fatal("nonzero manager status swallowed")
	}
	if _, err := runCommand(context.Background(), path, []string{"--prefix", "formula"}, nil); err == nil {
		t.Fatal("unbounded query output accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := runCommand(ctx, path, []string{"sleep"}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation did not stop manager promptly")
	}
}
