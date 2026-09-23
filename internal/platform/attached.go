package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RunAttached runs argv directly, preserving the child's streams and status.
// The caller owns any tracking connection for the duration of this call.
func RunAttached(ctx context.Context, argv, env []string, in io.Reader, out, stderr io.Writer) (int, error) {
	return RunAttachedInDir(ctx, argv, env, "", in, out, stderr)
}

// RunAttachedInDir applies the same owned process lifecycle in a caller-selected
// working directory. Empty directory inherits the parent working directory.
func RunAttachedInDir(ctx context.Context, argv, env []string, directory string, in io.Reader, out, stderr io.Writer) (int, error) {
	if len(argv) == 0 || argv[0] == "" {
		return 0, errors.New("a command is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	path, err := attachedExecutable(argv[0], env)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(path, argv[1:]...)
	cmd.Dir = directory
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, in, out, stderr
	// Bound pipes held open by descendants after cancellation or child exit.
	cmd.WaitDelay = 2 * time.Second
	restore, err := prepareAttached(cmd, in)
	if err != nil {
		return 0, err
	}
	defer restore()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start command %q: %w", argv[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return attachedExitCode(exit.ProcessState), nil
		}
		return 0, err
	case <-ctx.Done():
		terminateAttached(cmd, false)
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-done:
			// The leader may exit before descendants; kill any remaining members
			// of this owned process group before releasing its tracking tunnel.
			terminateAttached(cmd, true)
		case <-timer.C:
			terminateAttached(cmd, true)
			<-done
		}
		return 0, ctx.Err()
	}
}

func attachedExecutable(name string, env []string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		return name, nil
	}
	search, pathExt := "", ".COM;.EXE;.BAT;.CMD"
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if key == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(key, "PATH")) {
			search = value
		}
		if runtime.GOOS == "windows" && strings.EqualFold(key, "PATHEXT") {
			pathExt = value
		}
	}
	names := []string{name}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		names = nil
		for _, ext := range strings.Split(pathExt, ";") {
			if strings.HasPrefix(ext, ".") && !strings.ContainsAny(ext, `/\\`) {
				names = append(names, name+ext)
			}
		}
	}
	for _, directory := range filepath.SplitList(search) {
		if directory == "" {
			directory = "."
		}
		for _, candidate := range names {
			path := filepath.Join(directory, candidate)
			if info, err := os.Stat(path); err == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0111 != 0) {
				if !filepath.IsAbs(path) {
					return "", fmt.Errorf("command %q resolves relative to the current directory; use an explicit ./ path", name)
				}
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("command %q was not found in the target PATH", name)
}
