package scoopupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixture(t *testing.T) (string, Product, Options) {
	t.Helper()
	root := t.TempDir()
	product := Product{Binary: "tool", Module: "example.invalid/tool", Main: "example.invalid/tool/cmd/tool"}
	keg := filepath.Join(root, "apps", "actual-package", "1.0.0")
	for path, body := range map[string]string{
		filepath.Join(keg, "tool.exe"):                                      "fixture-product",
		filepath.Join(keg, "install.json"):                                  `{"bucket":"private-tools","architecture":"64bit"}`,
		filepath.Join(keg, "manifest.json"):                                 `{"version":"1.0.0","bin":"tool.exe"}`,
		filepath.Join(root, "apps", "scoop", "current", "bin", "scoop.ps1"): "fixture-manager",
		filepath.Join(root, "pwsh.exe"):                                     "fixture-powershell",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(keg, filepath.Join(root, "apps", "actual-package", "current")); err != nil {
		t.Skipf("fixture symlink unavailable: %v", err)
	}
	return filepath.Join(keg, "tool.exe"), product, Options{
		Inspect: func(context.Context, string) (string, error) { return "v1.0.0", nil },
		LookPath: func(name string) (string, error) {
			if name != "pwsh.exe" {
				t.Fatalf("unexpected discovery %q", name)
			}
			return filepath.Join(root, "pwsh.exe"), nil
		},
		StateRoot: filepath.Join(root, "state-must-not-be-created"),
	}
}

func TestCheckBindsExactPackageAndDoesNotCreateState(t *testing.T) {
	exe, product, opts := fixture(t)
	p, err := Prepare(context.Background(), exe, product, opts)
	if err != nil {
		t.Fatal(err)
	}
	if p.Package != "actual-package" || p.Bucket != "private-tools" || p.CurrentVersion != "v1.0.0" {
		t.Fatalf("wrong owner: %+v", p)
	}
	want := []string{filepath.Join(p.Root, "pwsh.exe"), "-NoLogo", "-NoProfile", "-File", filepath.Join(p.Root, "apps", "scoop", "current", "bin", "scoop.ps1"), "update", "actual-package"}
	if !reflect.DeepEqual(p.Command, want) {
		t.Fatalf("command=%q", p.Command)
	}
	if _, err := os.Stat(opts.StateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("check wrote state")
	}
	if p.Check().Status != "checked" || !p.Check().CanUpgrade {
		t.Fatal("invalid check report")
	}
}

func TestOwnershipFailuresNeverBecomeAnUpdatePlan(t *testing.T) {
	for _, scenario := range []string{"receipt", "bucket", "architecture", "manifest", "manager", "pwsh", "product", "old-version"} {
		t.Run(scenario, func(t *testing.T) {
			exe, product, opts := fixture(t)
			root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(exe))))
			switch scenario {
			case "receipt":
				os.WriteFile(filepath.Join(filepath.Dir(exe), "install.json"), []byte("broken"), 0600)
			case "bucket":
				os.WriteFile(filepath.Join(filepath.Dir(exe), "install.json"), []byte(`{"bucket":"../wrong","architecture":"64bit"}`), 0600)
			case "architecture":
				os.WriteFile(filepath.Join(filepath.Dir(exe), "install.json"), []byte(`{"bucket":"tools","architecture":"32bit"}`), 0600)
			case "manifest":
				os.WriteFile(filepath.Join(filepath.Dir(exe), "manifest.json"), []byte("broken"), 0600)
			case "manager":
				os.Remove(filepath.Join(root, "apps", "scoop", "current", "bin", "scoop.ps1"))
			case "pwsh":
				opts.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
			case "product":
				opts.Inspect = func(context.Context, string) (string, error) { return "", errors.New("wrong product") }
			case "old-version":
				os.Remove(filepath.Join(root, "apps", "actual-package", "current"))
			}
			if p, err := Prepare(context.Background(), exe, product, opts); err == nil || p.request != nil {
				t.Fatalf("accepted invalid owner: %+v %v", p, err)
			}
		})
	}
}

func TestStaleOrEditedPlanCannotHandoff(t *testing.T) {
	for _, scenario := range []string{"binary", "receipt", "manager", "public-command", "public-package"} {
		t.Run(scenario, func(t *testing.T) {
			exe, product, opts := fixture(t)
			p, err := Prepare(context.Background(), exe, product, opts)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "binary":
				os.WriteFile(exe, []byte("swapped"), 0700)
			case "receipt":
				os.WriteFile(filepath.Join(filepath.Dir(exe), "install.json"), []byte(`{"bucket":"other","architecture":"64bit"}`), 0600)
			case "manager":
				os.WriteFile(p.request.Files[3].Path, []byte("swapped"), 0600)
			case "public-command":
				p.Command[len(p.Command)-1] = "*"
			case "public-package":
				p.Package = "another"
			}
			if _, err := p.Handoff(context.Background(), false); err == nil {
				t.Fatal("stale or modified plan accepted")
			}
			if _, err := os.Stat(opts.StateRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid plan wrote state")
			}
		})
	}
}

func TestStatusIsReadOnlyAndRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef0123456789abcdef"
	for _, bad := range []string{"../outside", "", "UPPER", id + "/other"} {
		if _, err := readStatus(root, bad); err == nil {
			t.Fatal("bad operation ID accepted")
		}
	}
	dir := filepath.Join(root, id)
	os.Mkdir(dir, 0700)
	report := Report{Status: "updated", Manager: "scoop", Package: "tool", OperationID: id, Version: "v2.0.0", Changed: true}
	data, _ := json.Marshal(report)
	os.WriteFile(filepath.Join(dir, "result.json"), data, 0600)
	got, err := readStatus(root, id)
	if err != nil || got.Version != "v2.0.0" || !got.Changed {
		t.Fatalf("wrong result: %+v %v", got, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "result.json"))
	if string(after) != string(data) {
		t.Fatal("status rewrote its record")
	}
}

func TestScoopSkippedRunningProcessIsNotSuccess(t *testing.T) {
	if !bytesContainRunningSkip([]byte("Running process detected, skip updating.")) {
		t.Fatal("missed Scoop's successful-exit skip")
	}
	if bytesContainRunningSkip([]byte("tool is already installed and up to date")) {
		t.Fatal("no-op mislabeled blocked")
	}
}
