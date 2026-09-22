package brewupgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fixture struct {
	t                                               *testing.T
	root, rack, opt, brew, executable, current, tap string
	calls                                           [][]string
	upgrades, inspections                           int
	onUpgrade                                       func(context.Context) error
	onInspect                                       func(string)
	opts                                            Options
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, tap: "acme/tools"}
	// A custom Cellar need not be below the Homebrew prefix. Formula names
	// also need not equal the installed executable's basename.
	f.rack = filepath.Join(root, "separate storage", "Cellar", "installed-name")
	f.opt = filepath.Join(root, "custom prefix", "opt", "installed-name")
	f.brew = filepath.Join(root, "custom prefix", "bin", "brew")
	f.executable = filepath.Join(root, "custom prefix", "bin", "tool")
	f.write(f.brew, "fake manager")
	f.current = f.install("1.0", "v1.0.0")
	f.link(f.current, f.executable)
	f.opts = Options{
		LookPath: func(name string) (string, error) {
			if name != "brew" {
				t.Fatalf("unexpected PATH lookup: %s", name)
			}
			return f.brew, nil
		},
		Inspect: func(_ context.Context, path string) (string, error) {
			f.inspections++
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			if !strings.HasPrefix(string(data), "verified-product ") {
				return "", errors.New("wrong product")
			}
			if f.onInspect != nil {
				f.onInspect(path)
			}
			return strings.TrimPrefix(string(data), "verified-product "), nil
		},
		Run: func(ctx context.Context, program string, args []string, out io.Writer) (string, error) {
			if program != f.brew {
				t.Fatalf("unexpected manager: %q", program)
			}
			f.calls = append(f.calls, append([]string(nil), args...))
			if len(args) != 2 || args[1] != f.formula() {
				t.Fatalf("non-exact formula argv: %#v", args)
			}
			switch args[0] {
			case "--cellar":
				return f.rack + "\n", nil
			case "--prefix":
				return f.opt + "\n", nil
			case "upgrade":
				f.upgrades++
				fmt.Fprintln(out, "manager progress")
				if f.onUpgrade != nil {
					return "", f.onUpgrade(ctx)
				}
				return "", nil
			default:
				t.Fatalf("unexpected manager operation: %#v", args)
				return "", nil
			}
		},
	}
	return f
}

func (f *fixture) formula() string {
	if f.tap == "" {
		return "installed-name"
	}
	return f.tap + "/installed-name"
}
func (f *fixture) write(path, data string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0755); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) link(target, path string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		f.t.Skipf("symlink fixture unavailable: %v", err)
	}
}
func (f *fixture) install(keg, version string) string {
	f.t.Helper()
	path := filepath.Join(f.rack, keg, "bin", "tool")
	f.write(path, "verified-product "+version)
	body, _ := json.Marshal(map[string]any{"homebrew_version": "test", "source": map[string]string{"tap": f.tap}})
	f.write(filepath.Join(f.rack, keg, "INSTALL_RECEIPT.json"), string(body))
	f.link(filepath.Join(f.rack, keg), f.opt)
	return path
}
func (f *fixture) plan() Plan {
	f.t.Helper()
	plan, err := Prepare(context.Background(), f.executable, "tool", f.opts)
	if err != nil {
		f.t.Fatal(err)
	}
	return plan
}

func TestPrepareIsReadOnlyAndUsesActualReceiptFormula(t *testing.T) {
	f := newFixture(t)
	before := tree(t, f.root)
	plan := f.plan()
	if after := tree(t, f.root); !reflect.DeepEqual(before, after) {
		t.Fatal("planning wrote installation or staging files")
	}
	if f.upgrades != 0 || !reflect.DeepEqual(f.calls, [][]string{{"--cellar", "acme/tools/installed-name"}, {"--prefix", "acme/tools/installed-name"}}) {
		t.Fatalf("planning calls: %#v", f.calls)
	}
	if plan.CurrentPath != f.current || plan.StablePath != filepath.Join(f.opt, "bin", "tool") || plan.CurrentVersion != "v1.0.0" || plan.Cellar != filepath.Dir(f.rack) {
		t.Fatalf("plan: %+v", plan)
	}
	argv := plan.Command()
	argv[2] = "--all"
	if !reflect.DeepEqual(plan.Command(), []string{f.brew, "upgrade", f.formula()}) {
		t.Fatal("command preview mutated plan")
	}
}

func TestUpgradeFollowsOwningOptAfterOldKegIsRemoved(t *testing.T) {
	f := newFixture(t)
	f.write(filepath.Join(f.root, "PATH shadow", "tool"), "wrong product")
	plan := f.plan()
	var installed string
	f.onUpgrade = func(context.Context) error {
		installed = f.install("2.0", "v2.0.0")
		return os.RemoveAll(filepath.Join(f.rack, "1.0"))
	}
	var progress bytes.Buffer
	result, err := plan.Apply(context.Background(), &progress)
	if err != nil || !result.Changed || result.Version != "v2.0.0" || result.Path != plan.StablePath || result.ResolvedPath != installed || result.Formula != f.formula() {
		t.Fatalf("outcome=%+v error=%v", result, err)
	}
	if f.upgrades != 1 || !strings.Contains(progress.String(), "manager progress") {
		t.Fatalf("upgrades=%d progress=%q", f.upgrades, progress.String())
	}
	if !reflect.DeepEqual(f.calls[len(f.calls)-1], []string{"upgrade", f.formula()}) {
		t.Fatalf("wrong upgrade argv: %#v", f.calls)
	}
}

func TestNoOpReturnsActualVersionWithoutLatestClaim(t *testing.T) {
	for _, tap := range []string{"acme/tools", ""} {
		t.Run("tap="+tap, func(t *testing.T) {
			f := newFixture(t)
			f.tap = tap
			f.install("1.0", "v1.0.0")
			result, err := f.plan().Apply(context.Background(), io.Discard)
			if err != nil || result.Changed || result.Version != "v1.0.0" || f.upgrades != 1 {
				t.Fatalf("pinned/tap-lag result=%+v error=%v", result, err)
			}
		})
	}
}

func TestUnmanagedMissingAndWrongBrewDoNotUpgrade(t *testing.T) {
	for _, kind := range []string{"unmanaged", "missing", "wrong cellar", "bad receipt", "bad tap"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "unmanaged":
				f.executable = filepath.Join(f.root, "outside", "tool")
				f.write(f.executable, "unknown")
			case "missing":
				f.opts.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
			case "wrong cellar":
				run := f.opts.Run
				f.opts.Run = func(ctx context.Context, p string, args []string, w io.Writer) (string, error) {
					if args[0] == "--cellar" {
						return f.root, nil
					}
					return run(ctx, p, args, w)
				}
			case "bad receipt":
				f.write(filepath.Join(f.rack, "1.0", "INSTALL_RECEIPT.json"), "not json")
			case "bad tap":
				f.write(filepath.Join(f.rack, "1.0", "INSTALL_RECEIPT.json"), `{"source":{"tap":"--all"}}`)
			}
			_, err := Prepare(context.Background(), f.executable, "tool", f.opts)
			if err == nil || f.upgrades != 0 || f.inspections != 0 {
				t.Fatalf("error=%v upgrades=%d inspections=%d", err, f.upgrades, f.inspections)
			}
			if kind == "unmanaged" && !errors.Is(err, ErrNotManaged) {
				t.Fatalf("wrong unmanaged classification: %v", err)
			}
		})
	}
}

func TestApplyRejectsStaleBindingsBeforeAnyUpgrade(t *testing.T) {
	for _, kind := range []string{"binary", "receipt", "brew", "executable link", "opt target", "public plan", "changed cellar"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			plan := f.plan()
			switch kind {
			case "binary":
				f.write(f.current, "verified-product v9.0.0")
			case "receipt":
				f.write(filepath.Join(f.rack, "1.0", "INSTALL_RECEIPT.json"), `{"source":{"tap":"another/tap"}}`)
			case "brew":
				f.write(f.brew, "another manager")
			case "executable link":
				other := filepath.Join(f.root, "other")
				f.write(other, "other")
				f.link(other, f.executable)
			case "opt target":
				f.install("2.0", "v2.0.0")
			case "public plan":
				plan.Formula = "another/tap/formula"
			case "changed cellar":
				plan.state.opts.Run = func(context.Context, string, []string, io.Writer) (string, error) { return f.root, nil }
			}
			if _, err := plan.Apply(context.Background(), nil); err == nil || f.upgrades != 0 {
				t.Fatalf("stale apply error=%v upgrades=%d", err, f.upgrades)
			}
		})
	}
}

func TestManagerFailureAndCancellationHaveNoFallback(t *testing.T) {
	f := newFixture(t)
	plan := f.plan()
	failure := errors.New("manager failed")
	f.onUpgrade = func(context.Context) error { return failure }
	if result, err := plan.Apply(context.Background(), nil); !errors.Is(err, failure) || result.Version != "" || f.upgrades != 1 {
		t.Fatalf("failure result=%+v error=%v", result, err)
	}
	f.onUpgrade = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := plan.Apply(ctx, nil); !errors.Is(err, context.Canceled) || f.upgrades != 1 {
		t.Fatalf("canceled before apply: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	f.onUpgrade = func(context.Context) error { cancel(); return context.Canceled }
	if _, err := plan.Apply(ctx, nil); !errors.Is(err, context.Canceled) || f.upgrades != 2 {
		t.Fatalf("canceled during manager: %v", err)
	}
}

func TestSuccessStillRequiresVerifiedOwningProduct(t *testing.T) {
	for _, kind := range []string{"missing opt", "foreign opt", "changed tap", "wrong product", "inspection race"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			plan := f.plan()
			f.onUpgrade = func(context.Context) error {
				switch kind {
				case "missing opt":
					return os.Remove(f.opt)
				case "foreign opt":
					other := filepath.Join(f.root, "foreign")
					f.write(filepath.Join(other, "bin", "tool"), "verified-product v2.0.0")
					f.link(other, f.opt)
				case "changed tap":
					f.tap = "other/tap"
					f.install("2.0", "v2.0.0")
				case "wrong product":
					path := f.install("2.0", "v2.0.0")
					f.write(path, "wrong product")
				case "inspection race":
					f.install("2.0", "v2.0.0")
					f.onInspect = func(path string) { f.write(path, "verified-product v9.0.0") }
				}
				return nil
			}
			if result, err := plan.Apply(context.Background(), nil); err == nil || result.Version != "" || f.upgrades != 1 {
				t.Fatalf("unverified result=%+v error=%v upgrades=%d", result, err, f.upgrades)
			}
		})
	}
}

func TestLegacyReceiptDoesNotAuthorizeDifferentExplicitTap(t *testing.T) {
	f := newFixture(t)
	f.tap = ""
	f.install("1.0", "v1.0.0")
	plan := f.plan()
	f.onUpgrade = func(context.Context) error {
		f.tap = "different/owner"
		f.install("2.0", "v2.0.0")
		return nil
	}
	if result, err := plan.Apply(context.Background(), nil); err == nil || result.Version != "" {
		t.Fatalf("unbound tap accepted: result=%+v error=%v", result, err)
	}
}

func tree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err == nil {
			paths = append(paths, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return paths
}
