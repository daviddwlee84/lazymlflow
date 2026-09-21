//go:build windows

package platform

import (
	"io"
	"os"
	"os/exec"
)

func prepareAttached(cmd *exec.Cmd, in io.Reader) (func(), error) {
	return func() {}, nil
}

func terminateAttached(cmd *exec.Cmd, force bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func attachedExitCode(state *os.ProcessState) int { return state.ExitCode() }
