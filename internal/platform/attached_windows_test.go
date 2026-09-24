//go:build windows

package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestAttachedWindowsTreeHelper(t *testing.T) {
	mode := os.Getenv("LAZYMLFLOW_ATTACHED_WINDOWS_TREE")
	if mode == "" {
		return
	}
	if mode == "exit" {
		os.Exit(0)
	}
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestAttachedWindowsTreeHelper$")
		child.Env = append(os.Environ(), "LAZYMLFLOW_ATTACHED_WINDOWS_TREE=child")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		data, _ := json.Marshal([]int{os.Getpid(), child.Process.Pid})
		if err := os.WriteFile(os.Getenv("LAZYMLFLOW_WINDOWS_PID_FILE"), data, 0600); err != nil {
			os.Exit(4)
		}
	} else {
		name, err := windows.UTF16PtrFromString(os.Getenv("LAZYMLFLOW_WINDOWS_PREVIEW_FILE"))
		if err != nil {
			os.Exit(5)
		}
		// Deliberately deny FILE_SHARE_DELETE: this mimics a pager retaining a
		// preview handle, and proves cancellation releases the temporary file.
		handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			os.Exit(6)
		}
		defer windows.CloseHandle(handle)
		if err := os.WriteFile(os.Getenv("LAZYMLFLOW_WINDOWS_READY_FILE"), []byte("ready"), 0600); err != nil {
			os.Exit(7)
		}
	}
	time.Sleep(time.Hour)
	os.Exit(0)
}

func TestRunAttachedWindowsCancellationTerminatesTreeAndReleasesPreview(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	pidFile, readyFile, previewFile := filepath.Join(directory, "pids.json"), filepath.Join(directory, "ready"), filepath.Join(directory, "preview.txt")
	if err := os.WriteFile(previewFile, []byte("bounded preview"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RunAttached(ctx, []string{binary, "-test.run=^TestAttachedWindowsTreeHelper$"}, append(os.Environ(), "LAZYMLFLOW_ATTACHED_WINDOWS_TREE=parent", "LAZYMLFLOW_WINDOWS_PID_FILE="+pidFile, "LAZYMLFLOW_WINDOWS_READY_FILE="+readyFile, "LAZYMLFLOW_WINDOWS_PREVIEW_FILE="+previewFile), strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		done <- err
	}()
	var pids []int
	ready := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			_ = json.Unmarshal(data, &pids)
		}
		if _, err := os.Stat(readyFile); err == nil && len(pids) == 2 {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		cancel()
		select {
		case err := <-done:
			t.Fatalf("Windows pager helpers did not start: %v", err)
		case <-time.After(8 * time.Second):
			t.Fatal("Windows pager helper startup/cancellation timed out")
		}
	}
	var handles []windows.Handle
	for _, pid := range pids {
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
		if err != nil {
			t.Fatal(err)
		}
		handles = append(handles, handle)
		defer func() { _ = windows.TerminateProcess(handle, 9); _ = windows.CloseHandle(handle) }()
	}
	if err := os.Remove(previewFile); err == nil {
		t.Fatal("helper did not hold the preview file against deletion")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("attached Windows process-tree cancellation was not bounded")
	}
	for i, handle := range handles {
		status, err := windows.WaitForSingleObject(handle, 3000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Fatalf("owned process %d survived cancellation: status=%d error=%v", pids[i], status, err)
		}
	}
	if err := os.Remove(previewFile); err != nil {
		t.Fatalf("cancelled pager still holds preview file: %v", err)
	}
}

func TestWindowsTreeTerminationUsesOriginalIdentityOnlyOnce(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestAttachedWindowsTreeHelper$")
	cmd.Env = append(os.Environ(), "LAZYMLFLOW_ATTACHED_WINDOWS_TREE=exit")
	restore, err := prepareAttached(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := startedAttached(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	value, ok := attachedWindowsProcesses.Load(cmd)
	if !ok {
		t.Fatal("owned process identity was released before cancellation cleanup")
	}
	state := value.(*attachedWindowsState)
	if _, err := windows.GetProcessId(state.process); err != nil {
		t.Fatal("original process handle did not survive Wait", err)
	}
	calls := 0
	state.killTree = func(pid int) {
		if pid != cmd.Process.Pid {
			t.Fatal("wrong process identity")
		}
		calls++
	}
	terminateAttached(cmd, false)
	terminateAttached(cmd, true)
	if calls != 1 {
		t.Fatalf("numeric process-tree termination repeated %d times", calls)
	}
}
