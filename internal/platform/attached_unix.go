//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func prepareAttached(cmd *exec.Cmd, in io.Reader) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, ok := in.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return func() {}, nil
	}
	fd := int(input.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return nil, fmt.Errorf("read terminal state: %w", err)
	}
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		if errors.Is(err, unix.ENOTTY) {
			// A terminal supplied by a supervisor may not be this process's
			// controlling terminal; it has no foreground group to hand off.
			return func() { _ = term.Restore(fd, state) }, nil
		}
		return nil, fmt.Errorf("read terminal foreground group: %w", err)
	}
	cmd.SysProcAttr.Foreground = true
	cmd.SysProcAttr.Ctty = fd
	return func() {
		// A background parent cannot reclaim its terminal while SIGTTOU is
		// enabled. Retain an inherited ignored disposition after restoration.
		ignored := signal.Ignored(syscall.SIGTTOU)
		signal.Ignore(syscall.SIGTTOU)
		_ = unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, foreground)
		_ = term.Restore(fd, state)
		if !ignored {
			signal.Reset(syscall.SIGTTOU)
		}
	}, nil
}

func terminateAttached(cmd *exec.Cmd, force bool) {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}
}

func attachedExitCode(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}
