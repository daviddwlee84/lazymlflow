package platform

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func ResourceURL(base, experiment, run string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	fragment := "/experiments"
	if experiment != "" {
		fragment += "/" + url.PathEscape(experiment)
	}
	if run != "" {
		fragment += "/runs/" + url.PathEscape(run)
	}
	u.Fragment = fragment
	return u.String()
}

func OpenURL(ctx context.Context, value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("browser URL must use http or https")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", value)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", value)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", value)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open browser: %w (%s); URL: %s", err, strings.TrimSpace(string(b)), value)
	}
	return nil
}

// Copy uses an installed desktop clipboard tool; an explicit error keeps the
// UI from claiming a successful copy when a remote terminal has no clipboard.
func Copy(ctx context.Context, value string) error {
	var commands [][]string
	switch runtime.GOOS {
	case "darwin":
		commands = [][]string{{"pbcopy"}}
	case "windows":
		commands = [][]string{{"clip"}}
	default:
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			commands = append(commands, []string{"wl-copy"})
		}
		commands = append(commands, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
	}
	for _, args := range commands {
		if _, err := exec.LookPath(args[0]); err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(value)
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("clipboard: %w (%s)", err, strings.TrimSpace(string(b)))
		}
		return nil
	}
	return errors.New("no clipboard command available; install wl-clipboard/xclip or copy the displayed ID")
}
