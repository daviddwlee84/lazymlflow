//go:build !windows

package scoopupgrade

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

func processStarted(int) uint64     { return 0 }
func processAlive(int, uint64) bool { return false }
func secureDirectory(string) error  { return errors.New("Scoop handoff is supported on Windows") }
func lockState(string) (func(), error) {
	return nil, errors.New("Scoop handoff is supported on Windows")
}
func configureHelper(*exec.Cmd, bool)                            {}
func configureManager(*exec.Cmd)                                 {}
func installedProcessRunning(string, string) bool                { return false }
func helperConsole(_ bool, output io.Writer) (io.Writer, func()) { return output, func() {} }
func consolePause(context.Context)                               {}
