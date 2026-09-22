package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/brewupgrade"
	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/spf13/cobra"
)

func TestUpgradeReviewAndMachineOutput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		input      string
		terminal   bool
		wantApply  bool
		wantJSON   bool
		wantError  string
		wantPrompt bool
		failApply  bool
	}{
		{name: "check", args: []string{"--check"}, input: "yes\n"},
		{name: "check ignores approval", args: []string{"--check", "--yes", "--json"}, wantJSON: true},
		{name: "nonterminal approval", wantError: "pass --yes"},
		{name: "JSON approval", args: []string{"--json"}, terminal: true, wantError: "pass --yes"},
		{name: "default no", terminal: true, input: "\n", wantError: "cancelled", wantPrompt: true},
		{name: "explicit no", terminal: true, input: "no\n", wantError: "cancelled", wantPrompt: true},
		{name: "interactive yes", terminal: true, input: "yes\n", wantApply: true, wantPrompt: true},
		{name: "nonterminal yes", args: []string{"--yes"}, wantApply: true},
		{name: "JSON yes", args: []string{"--json", "--yes"}, wantApply: true, wantJSON: true},
		{name: "manager failure", args: []string{"--json", "--yes"}, wantApply: true, wantJSON: true, wantError: "manager failed", failApply: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			applies := 0
			formula := "daviddwlee84/tap/lazymlflow"
			stable := filepath.Join(t.TempDir(), "opt", "lazymlflow", "bin", "lazymlflow")
			manager := filepath.Join(t.TempDir(), "bin", "brew")
			ctx := context.WithValue(context.Background(), upgradeTestKey{}, "kept")
			command := newUpgradeCommand(upgradeOptions{
				isTerminal: func(*cobra.Command) bool { return tc.terminal },
				prepare: func(ctx context.Context) (managedUpgradePlan, error) {
					return managedUpgradePlan{
						report: upgradeReport{Status: "checked", Manager: "homebrew", Formula: formula, CurrentVersion: "v0.1.0", ResolvedPath: stable, Command: []string{manager, "upgrade", formula}, CanUpgrade: true},
						apply: func(applyContext context.Context, progress io.Writer) (brewupgrade.Outcome, error) {
							applies++
							if applyContext.Value(upgradeTestKey{}) != "kept" {
								t.Fatal("apply lost command context")
							}
							fmtProgress := "manager progress must not enter JSON stdout\n"
							_, _ = io.WriteString(progress, fmtProgress)
							if tc.failApply {
								return brewupgrade.Outcome{}, errors.New("manager failed")
							}
							return brewupgrade.Outcome{Formula: formula, Path: stable, ResolvedPath: stable, Version: "v0.2.0", Changed: true}, nil
						},
					}, nil
				},
			})
			command.SetArgs(tc.args)
			command.SetIn(strings.NewReader(tc.input))
			command.SetOut(&out)
			command.SetErr(&stderr)
			command.SilenceUsage, command.SilenceErrors = true, true
			err := command.ExecuteContext(ctx)
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error=%v, want %q; stdout=%s stderr=%s", err, tc.wantError, &out, &stderr)
			}
			if (applies == 1) != tc.wantApply || applies > 1 {
				t.Fatalf("applied %d times, wantApply=%t", applies, tc.wantApply)
			}
			if strings.Contains(stderr.String(), "[y/N]") != tc.wantPrompt {
				t.Fatalf("prompt=%q, wantPrompt=%t", &stderr, tc.wantPrompt)
			}
			if tc.wantJSON {
				var report upgradeReport
				if err := json.Unmarshal(out.Bytes(), &report); err != nil {
					t.Fatalf("invalid machine output: %v: %s", err, &out)
				}
				if report.Formula != formula || len(report.Command) != 3 || report.Command[0] != manager || report.Command[2] != formula {
					t.Fatalf("reviewed target changed: %+v", report)
				}
				if tc.failApply && report.Status != "failed" {
					t.Fatalf("failed manager reported success: %+v", report)
				}
				if !tc.failApply && tc.wantApply && (report.Status != "updated" || report.Version != "v0.2.0") {
					t.Fatalf("successful result missing: %+v", report)
				}
				if stderr.Len() != 0 {
					t.Fatalf("JSON mode leaked progress: %s", &stderr)
				}
			}
		})
	}
}

type upgradeTestKey struct{}

func TestUpgradeUnsupportedAndValidationArePassive(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		cancel     bool
		wantChecks int
		wantError  bool
	}{
		{name: "unsupported check", args: []string{"--check", "--json"}, wantChecks: 1},
		{name: "unsupported apply", args: []string{"--yes", "--json"}, wantChecks: 1, wantError: true},
		{name: "extra argument", args: []string{"unexpected"}, wantError: true},
		{name: "cancelled", args: []string{"--yes"}, cancel: true, wantError: true},
		{name: "help", args: []string{"--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			var out bytes.Buffer
			command := newUpgradeCommand(upgradeOptions{prepare: func(context.Context) (managedUpgradePlan, error) {
				checks++
				return managedUpgradePlan{report: upgradeReport{Status: "unsupported", Reason: "use its original manager"}}, nil
			}})
			command.SetArgs(tc.args)
			command.SetOut(&out)
			command.SetErr(io.Discard)
			command.SilenceUsage, command.SilenceErrors = true, true
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err := command.ExecuteContext(ctx)
			if (err != nil) != tc.wantError || checks != tc.wantChecks {
				t.Fatalf("err=%v checks=%d, want error=%t checks=%d", err, checks, tc.wantError, tc.wantChecks)
			}
		})
	}
}

func TestUpgradePromptCancellation(t *testing.T) {
	input, output := io.Pipe()
	defer input.Close()
	defer output.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := confirmManagedUpgrade(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled confirmation: %v", err)
	}
}

func TestUpgradeBinaryIdentityAndVersion(t *testing.T) {
	valid := &debug.BuildInfo{Path: "github.com/daviddwlee84/lazymlflow/cmd/lazymlflow", Main: debug.Module{Path: "github.com/daviddwlee84/lazymlflow"}}
	if !upgradeBinaryIdentity(valid) || upgradeBinaryIdentity(nil) {
		t.Fatal("exact main/module identity was not required")
	}
	for _, mutate := range []func(*debug.BuildInfo){
		func(info *debug.BuildInfo) { info.Path = "github.com/example/other" },
		func(info *debug.BuildInfo) { info.Main.Path = "github.com/example/other" },
		func(info *debug.BuildInfo) { info.Main.Replace = &debug.Module{Path: "./replacement"} },
	} {
		copy := *valid
		mutate(&copy)
		if upgradeBinaryIdentity(&copy) {
			t.Fatal("foreign/replaced product identity accepted")
		}
	}
	for _, tc := range []struct{ value, version string }{
		{"lazymlflow version v0.1.0\n", "v0.1.0"},
		{"lazymlflow version dev\n", "dev"},
		{"other version v0.1.0\n", ""},
		{"lazymlflow version \n", ""},
		{"lazymlflow version v0.1.0\nextra", ""},
		{"lazymlflow version v0.1.0\x1b", ""},
		{"lazymlflow version " + strings.Repeat("x", 129), ""},
	} {
		version, err := parseUpgradeVersion(tc.value)
		if version != tc.version || (err == nil) != (tc.version != "") {
			t.Fatalf("parse %q = %q, %v", tc.value, version, err)
		}
	}
	out := &upgradeVersionOutput{}
	if _, err := out.Write([]byte(strings.Repeat("x", 4097))); err == nil || out.Len() != 0 {
		t.Fatal("version output cap was bypassed")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspectUpgradeBinary(context.Background(), executable); err == nil {
		t.Fatal("test executable was accepted as the product and executed")
	}
}

func TestUpgradeUnsupportedManagerInstructions(t *testing.T) {
	for path, want := range map[string]string{
		"/nix/store/fixture/bin/lazymlflow":              "Nix profile",
		"/tmp/mise/installs/lazymlflow/1/bin/lazymlflow": "mise upgrade",
		filepath.Join(t.TempDir(), "lazymlflow"):         "go install github.com/daviddwlee84/lazymlflow/cmd/lazymlflow@latest",
	} {
		if got := unsupportedUpgradeInstructions(path); !strings.Contains(got, want) {
			t.Fatalf("%q: %s", path, got)
		}
	}
}

func TestUpgradeInspectorNativeVersionProcess(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	mainDir := filepath.Join(root, "cmd/lazymlflow")
	if err := os.MkdirAll(mainDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/daviddwlee84/lazymlflow\n\ngo 1.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := `package main
import ("fmt"; "os"; "strings")
func main() {
  switch os.Getenv("LAZY_UPGRADE_INSPECT_FIXTURE_MODE") {
  case "flood": fmt.Print(strings.Repeat("x", 8192))
  case "wrong": fmt.Println("another-tool version v0.2.3")
  case "failure": fmt.Fprintln(os.Stderr, "private child diagnostic"); os.Exit(7)
  default: fmt.Println("lazymlflow version v0.2.3")
  }
}
`
	if err := os.WriteFile(filepath.Join(mainDir, "main.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "lazymlflow")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(goBinary, "build", "-o", binary, "./cmd/lazymlflow")
	build.Dir = root
	build.Env = append(os.Environ(), "GOWORK=off", "GOENV=off", "GOFLAGS=", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native version fixture: %v\n%s", err, output)
	}
	for _, mode := range []string{"", "flood", "wrong", "failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("LAZY_UPGRADE_INSPECT_FIXTURE_MODE", mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			version, err := inspectUpgradeBinary(ctx, binary)
			if mode == "" {
				if err != nil || version != "v0.2.3" {
					t.Fatalf("native version: %q, %v", version, err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private child diagnostic") {
				t.Fatalf("unsafe version process %q: %q, %v", mode, version, err)
			}
		})
	}
}

func TestUpgradeRootBypassesConfigAndBackend(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, arguments := range [][]string{{"upgrade", "--check", "--json"}, {"upgrade", "--help"}, {"--version"}, {"version"}, {"completion", "bash"}} {
		opts, connector, out, stderr := testOptions(t)
		opts.Version = "v0.2.0"
		opts.LoadConfig = func(string) (*config.Config, error) {
			t.Fatal("upgrade/static command loaded configuration")
			return nil, nil
		}
		arguments = append([]string{"--config", "/does/not/exist", "--tracking-uri", "https://must-not-connect.invalid"}, arguments...)
		if code := Execute(context.Background(), arguments, opts); code != 0 || out.Len() == 0 || len(connector.opened) != 0 || connector.closed != 0 {
			t.Fatalf("%v touched backend/config: code=%d stdout=%s stderr=%s", arguments, code, out, stderr)
		}
		if strings.Contains(strings.Join(arguments, " "), "--check") {
			var report upgradeReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Status != "unsupported" || report.CanUpgrade {
				t.Fatalf("unmanaged test binary: %+v %v", report, err)
			}
		}
	}
	opts, connector, _, stderr := testOptions(t)
	if code := Execute(context.Background(), []string{"upgrade", "--interactive", "--check"}, opts); code != 2 || len(connector.opened) != 0 {
		t.Fatalf("interactive policy changed: code=%d %s", code, stderr)
	}
}
