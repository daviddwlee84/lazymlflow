//go:build windows

package platform

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

type attachedWindowsState struct {
	process  windows.Handle
	once     sync.Once
	killTree func(int)
}

var attachedWindowsProcesses sync.Map // *exec.Cmd -> *attachedWindowsState

func prepareAttached(cmd *exec.Cmd, in io.Reader) (func(), error) {
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, fmt.Errorf("locate Windows process-tree termination tool: %w", err)
	}
	// Resolve through the native API, not PATH, SystemRoot or a command shell.
	taskkill := filepath.Join(systemDirectory, "taskkill.exe")
	state := &attachedWindowsState{killTree: func(pid int) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		kill := exec.CommandContext(ctx, taskkill, "/PID", strconv.Itoa(pid), "/T", "/F")
		kill.Dir = systemDirectory
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		kill.Stdout, kill.Stderr = io.Discard, io.Discard
		kill.WaitDelay = time.Second
		_ = kill.Run()
	}}
	attachedWindowsProcesses.Store(cmd, state)
	return func() {
		attachedWindowsProcesses.Delete(cmd)
		if state.process != 0 {
			_ = windows.CloseHandle(state.process)
		}
	}, nil
}

func startedAttached(cmd *exec.Cmd) error {
	value, ok := attachedWindowsProcesses.Load(cmd)
	if !ok || cmd.Process == nil {
		return fmt.Errorf("attached Windows process was not prepared")
	}
	state := value.(*attachedWindowsState)
	// Capture while Go still owns its initial process handle and before Wait
	// can release it. An open handle keeps the process object and PID alive,
	// even after exit, so a numeric tree kill cannot target a reused PID.
	// https://devblogs.microsoft.com/oldnewthing/20110107-00/?p=11803
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("retain attached Windows process identity: %w", err)
	}
	state.process = process
	return nil
}

func terminateAttached(cmd *exec.Cmd, force bool) {
	if cmd.Process == nil {
		return
	}
	if value, ok := attachedWindowsProcesses.Load(cmd); ok {
		state := value.(*attachedWindowsState)
		if state.process != 0 {
			// Terminate descendants before the leader disappears. The common
			// cancellation path invokes us twice; only the first call may use a
			// numeric PID. The retained handle is closed after that path returns.
			state.once.Do(func() { state.killTree(cmd.Process.Pid) })
		}
	}
	// Go's Process uses the original handle, not a fresh PID lookup. This is
	// safe after Wait, and also provides a bounded fallback if taskkill fails.
	_ = cmd.Process.Kill()
}

func attachedExitCode(state *os.ProcessState) int { return state.ExitCode() }
