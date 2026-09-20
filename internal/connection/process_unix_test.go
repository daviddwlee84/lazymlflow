//go:build !windows

package connection

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCancellationCleansDescendantProcesses(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	env := append(os.Environ(), "LAZYMLFLOW_TEST_PID_FILE="+pidFile)
	// The shell waits for a child that owns inherited output pipes, just as
	// MLflow's launcher waits for its gunicorn/uvicorn child process.
	argv := []string{"/bin/sh", "-c", "sleep 300 & child=$!; printf '%s' \"$child\" > \"$LAZYMLFLOW_TEST_PID_FILE\"; wait \"$child\""}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := runCapture(ctx, argv, "", env, nil, 1024); done <- err }()
	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(string(b))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		cancel()
		t.Fatal("descendant failed to start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not finish")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		// An already killed orphan can briefly await init's reaper on Linux.
		b, _ := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
		if strings.HasPrefix(strings.TrimSpace(string(b)), "Z") || len(strings.TrimSpace(string(b))) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("descendant survived cancellation")
}
