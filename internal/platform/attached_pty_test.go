//go:build !windows

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

// This helper runs in a real controlling terminal created by pty.fork, as the
// CLI does in an ordinary interactive shell. No Python packages are needed.
func TestAttachedPTYHelper(t *testing.T) {
	if os.Getenv("LAZYMLFLOW_PTY_WRAPPER") != "1" {
		return
	}
	python := os.Getenv("LAZYMLFLOW_PTY_PYTHON")
	child := `import os, sys, termios, time
assert sys.stdin.isatty() and sys.stdout.isatty()
print("CHILD_READY", flush=True)
if os.environ["LAZYMLFLOW_PTY_CASE"] == "interrupt":
    time.sleep(20)
else:
    value = input()
    print("CHILD_INPUT=" + value, flush=True)
    state = termios.tcgetattr(0)
    state[3] &= ~(termios.ECHO | termios.ICANON)
    termios.tcsetattr(0, termios.TCSANOW, state)
    sys.exit(7)
`
	code, err := RunAttached(context.Background(), []string{python, "-c", child}, os.Environ(), os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	foreground, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		fmt.Fprintln(os.Stderr, "FOREGROUND_NOT_RESTORED")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "PARENT_RESTORED")
	os.Exit(code)
}

func TestRunAttachedRealPTY(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable for the real PTY check")
	}
	binary, _ := os.Executable()
	const driver = `import errno, os, pty, select, signal, sys, termios, time
for case in ("input", "interrupt"):
    env = dict(os.environ, LAZYMLFLOW_PTY_WRAPPER="1", LAZYMLFLOW_PTY_PYTHON=sys.argv[2], LAZYMLFLOW_PTY_CASE=case)
    pid, fd = pty.fork()
    if pid == 0:
        os.execve(sys.argv[1], [sys.argv[1], "-test.run=^TestAttachedPTYHelper$"], env)
    raw = bytearray()
    before = termios.tcgetattr(fd)
    sent = False
    status = None
    deadline = time.monotonic() + 8
    try:
        while time.monotonic() < deadline:
            readable, _, _ = select.select([fd], [], [], 0.05)
            if readable:
                try:
                    chunk = os.read(fd, 65536)
                except OSError as exc:
                    if exc.errno != errno.EIO: raise
                    chunk = b""
                raw.extend(chunk)
            if not sent and b"CHILD_READY" in raw:
                os.write(fd, b"literal jq / text\n" if case == "input" else b"\x03")
                sent = True
            done, value = os.waitpid(pid, os.WNOHANG)
            if done:
                status = value
                break
        assert status is not None, (case, "timed out", bytes(raw))
        expected = 7 if case == "input" else 130
        assert os.waitstatus_to_exitcode(status) == expected, (case, status, bytes(raw))
        assert b"PARENT_RESTORED" in raw, (case, bytes(raw))
        if case == "input": assert b"CHILD_INPUT=literal jq / text" in raw, bytes(raw)
        after = termios.tcgetattr(fd)
        assert before == after, (case, "terminal modes changed", before, after)
    finally:
        if status is None:
            os.kill(pid, signal.SIGKILL)
            os.waitpid(pid, 0)
        os.close(fd)
print("attached PTY input, foreground, terminal restoration and Ctrl+C passed")
`
	cmd := exec.Command(python, "-c", driver, binary, python)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real PTY: %v\n%s", err, out)
	}
}
