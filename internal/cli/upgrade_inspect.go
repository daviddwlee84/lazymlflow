package cli

import (
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Only a binary with this product's Go package identity may be executed while
// inspecting the package-owned installation or the result of an upgrade.
func inspectUpgradeBinary(ctx context.Context, path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil || !upgradeBinaryIdentity(info) {
		return "", errors.New("Homebrew executable does not identify the lazymlflow main package and module")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output := &upgradeVersionOutput{}
	child := exec.CommandContext(ctx, path, "--version")
	child.Stdout, child.Stderr = output, io.Discard
	child.WaitDelay = time.Second
	if err := child.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("installed lazymlflow failed its version check")
	}
	return parseUpgradeVersion(output.String())
}

func upgradeBinaryIdentity(info *debug.BuildInfo) bool {
	return info != nil && info.Path == "github.com/daviddwlee84/lazymlflow/cmd/lazymlflow" &&
		info.Main.Path == "github.com/daviddwlee84/lazymlflow" && info.Main.Replace == nil
}

func parseUpgradeVersion(output string) (string, error) {
	value := strings.TrimSpace(output)
	prefix := "lazymlflow version "
	if !utf8.ValidString(value) || !strings.HasPrefix(value, prefix) {
		return "", errors.New("installed lazymlflow reported an unexpected version")
	}
	version := strings.TrimPrefix(value, prefix)
	if version == "" || len(version) > 128 || strings.IndexFunc(version, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", errors.New("installed lazymlflow reported an invalid version")
	}
	return version, nil
}

type upgradeVersionOutput struct{ text strings.Builder }

func (out *upgradeVersionOutput) String() string { return out.text.String() }
func (out *upgradeVersionOutput) Len() int       { return out.text.Len() }

func (out *upgradeVersionOutput) Write(data []byte) (int, error) {
	if out.Len()+len(data) > 4096 {
		return 0, fmt.Errorf("version output exceeded 4096 bytes")
	}
	return out.text.Write(data)
}
