//go:build !windows

package platform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAttachedTreeHelper(t *testing.T) {
	mode := os.Getenv("LAZYMLFLOW_ATTACHED_TREE")
	if mode == "" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestAttachedTreeHelper$")
		child.Env = append(os.Environ(), "LAZYMLFLOW_ATTACHED_TREE=descendant")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Getenv("LAZYMLFLOW_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(4)
		}
	}
	time.Sleep(time.Hour)
	os.Exit(0)
}

func TestRunAttachedCancellationTerminatesDescendants(t *testing.T) {
	binary, _ := os.Executable()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RunAttached(ctx, []string{binary, "-test.run=^TestAttachedTreeHelper$"}, append(os.Environ(), "LAZYMLFLOW_ATTACHED_TREE=parent", "LAZYMLFLOW_PID_FILE="+pidFile), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		done <- err
	}()
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(string(b))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		cancel()
		<-done
		t.Fatal("descendant did not start")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("cancel result: %v", err)
	}
	// A killed orphan can briefly remain a zombie before its new parent reaps it.
	for attempt := 0; attempt < 100; attempt++ {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		state, _ := exec.Command("ps", "-o", "stat=", "-p", fmt.Sprint(pid)).Output()
		if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned descendant survived cancellation")
}
