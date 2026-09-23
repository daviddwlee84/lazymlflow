// Package scoopupgrade verifies Scoop ownership and hands an explicit update to
// a helper outside the installed package. Scoop refuses to update running apps.
package scoopupgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrNotManaged = errors.New("executable has no Scoop installation receipt")
var component = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
var operationID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Product is compiled into the application and its copied helper. The request
// cannot select another product or an arbitrary command to run.
type Product struct {
	Binary string `json:"binary"`
	Module string `json:"module"`
	Main   string `json:"main"`
}

type Options struct {
	Inspect   func(context.Context, string) (string, error)
	LookPath  func(string) (string, error)
	StateRoot string
}

type Plan struct {
	Package, Bucket, Root, CurrentPath, StablePath, CurrentVersion string
	Command                                                        []string
	request                                                        *request
	options                                                        Options
}

// Report is additive to each application's existing JSON envelope. Handed-off
// means accepted, never completed. Status reads never launch or repair a job.
type Report struct {
	Status         string   `json:"status"`
	Manager        string   `json:"manager"`
	Package        string   `json:"package"`
	Bucket         string   `json:"bucket,omitempty"`
	CurrentVersion string   `json:"current_version,omitempty"`
	Version        string   `json:"version,omitempty"`
	Path           string   `json:"path,omitempty"`
	Command        []string `json:"command,omitempty"`
	CanUpgrade     bool     `json:"can_upgrade"`
	Changed        bool     `json:"changed"`
	OperationID    string   `json:"operation_id,omitempty"`
	ResultPath     string   `json:"result_path,omitempty"`
	LogPath        string   `json:"log_path,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	HelperPID      int      `json:"helper_pid,omitempty"`
	HelperStarted  uint64   `json:"helper_started,omitempty"`
}

type fingerprint struct {
	Path string `json:"path"`
	Hash string `json:"sha256"`
}

type request struct {
	Product                                                                      Product `json:"product"`
	Root, Package, Bucket, CurrentPath, StablePath, Manager, PowerShell, Version string
	Files                                                                        []fingerprint
	ParentPID                                                                    int
	ParentStarted                                                                uint64
	Interactive                                                                  bool
	OperationID                                                                  string
}

func stateRoot(binary string) (string, error) {
	if !component.MatchString(binary) {
		return "", errors.New("invalid product name")
	}
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		if !filepath.IsAbs(state) {
			return "", errors.New("XDG_STATE_HOME must be absolute")
		}
		return filepath.Join(state, binary, "upgrades"), nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, binary, "state", "upgrades"), nil
}

func canonical(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func capture(path string) (fingerprint, error) {
	p, err := canonical(path)
	if err != nil {
		return fingerprint{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return fingerprint{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fingerprint{}, errors.New("expected a regular installation file")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return fingerprint{}, err
	}
	after, err := os.Stat(p)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || info.ModTime() != after.ModTime() {
		return fingerprint{}, errors.New("installation changed during inspection")
	}
	return fingerprint{p, hex.EncodeToString(h.Sum(nil))}, nil
}

func (f fingerprint) validate() error {
	now, err := capture(f.Path)
	if err != nil || !samePath(now.Path, f.Path) || now.Hash != f.Hash {
		return errors.New("inspected installation or manager changed; check again")
	}
	return nil
}

// Inspect verifies Go product identity before executing a bounded version probe.
func Inspect(ctx context.Context, path string, product Product) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil || info.Main.Path != product.Module || info.Path != product.Main || info.Main.Replace != nil {
		return "", errors.New("executable does not identify the expected product and main package")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var output boundedOutput
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = &output, io.Discard, time.Second
	if err := cmd.Run(); err != nil {
		return "", errors.New("installed executable failed its version check")
	}
	value := strings.TrimSpace(output.String())
	prefix := product.Binary + " version "
	if !utf8.ValidString(value) || !strings.HasPrefix(value, prefix) {
		return "", errors.New("unexpected product version output")
	}
	version := strings.TrimPrefix(value, prefix)
	if version == "" || len(version) > 128 || strings.IndexFunc(version, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", errors.New("invalid installed version")
	}
	return version, nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, errors.New("version output exceeds limit")
	}
	return b.Buffer.Write(p)
}

// Prepare reads the exact resolved package receipt, manifest and manager. It
// makes no network request, starts no manager, and writes no files.
func Prepare(ctx context.Context, executable string, product Product, opts Options) (Plan, error) {
	var plan Plan
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	if !component.MatchString(product.Binary) || product.Module == "" || product.Main == "" {
		return plan, errors.New("invalid product identity")
	}
	resolved, err := canonical(executable)
	if err != nil {
		return plan, err
	}
	versionDir := filepath.Dir(resolved)
	if !strings.EqualFold(filepath.Base(resolved), product.Binary+".exe") {
		return plan, ErrNotManaged
	}
	packageDir := filepath.Dir(versionDir)
	apps := filepath.Dir(packageDir)
	if !strings.EqualFold(filepath.Base(apps), "apps") {
		return plan, ErrNotManaged
	}
	receiptPath := filepath.Join(versionDir, "install.json")
	if _, err := os.Stat(receiptPath); errors.Is(err, os.ErrNotExist) {
		return plan, ErrNotManaged
	}
	root := filepath.Dir(apps)
	for _, global := range []string{os.Getenv("SCOOP_GLOBAL"), filepath.Join(os.Getenv("ProgramData"), "scoop")} {
		if filepath.IsAbs(global) {
			if p, e := canonical(global); e == nil && samePath(root, p) {
				return plan, errors.New("global Scoop installation requires an explicit external administrator update")
			}
		}
	}
	name := filepath.Base(packageDir)
	if !component.MatchString(name) {
		return plan, errors.New("invalid installed Scoop package name")
	}
	var receipt struct {
		Bucket       string `json:"bucket"`
		Architecture string `json:"architecture"`
	}
	data, err := readLimited(receiptPath, 1<<20)
	if err != nil || json.Unmarshal(data, &receipt) != nil || !component.MatchString(receipt.Bucket) {
		return plan, errors.New("Scoop receipt has no unambiguous installed bucket")
	}
	if receipt.Architecture != "64bit" && receipt.Architecture != "arm64" {
		return plan, errors.New("unsupported or missing Scoop architecture")
	}
	stable := filepath.Join(packageDir, "current", product.Binary+".exe")
	current, err := canonical(stable)
	if err != nil || !samePath(current, resolved) {
		return plan, errors.New("running executable is not the current Scoop installation")
	}
	manager := filepath.Join(root, "apps", "scoop", "current", "bin", "scoop.ps1")
	manifest := filepath.Join(versionDir, "manifest.json")
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	pwsh, err := opts.LookPath("pwsh.exe")
	if err != nil {
		return plan, errors.New("Scoop upgrade requires PowerShell 7 (pwsh.exe)")
	}
	pwsh, err = canonical(pwsh)
	if err != nil {
		return plan, err
	}
	if opts.Inspect == nil {
		opts.Inspect = func(c context.Context, p string) (string, error) { return Inspect(c, p, product) }
	}
	files := []fingerprint{}
	for _, path := range []string{resolved, receiptPath, manifest, manager, pwsh} {
		f, err := capture(path)
		if err != nil {
			return plan, fmt.Errorf("verify Scoop installation: %w", err)
		}
		files = append(files, f)
	}
	if hexHash(data) != files[1].Hash {
		return plan, errors.New("Scoop receipt changed during inspection")
	}
	var manifestData map[string]any
	data, err = readLimited(manifest, 4<<20)
	if err != nil || json.Unmarshal(data, &manifestData) != nil || hexHash(data) != files[2].Hash {
		return plan, errors.New("invalid or changing installed Scoop manifest")
	}
	version, err := opts.Inspect(ctx, resolved)
	if err != nil {
		return plan, err
	}
	if opts.StateRoot == "" {
		opts.StateRoot, err = stateRoot(product.Binary)
		if err != nil {
			return plan, err
		}
	}
	r := &request{Product: product, Root: root, Package: name, Bucket: receipt.Bucket, CurrentPath: resolved, StablePath: stable, Manager: manager, PowerShell: pwsh, Version: version, Files: files}
	if err := r.validate(); err != nil {
		return plan, err
	}
	return Plan{Package: name, Bucket: receipt.Bucket, Root: root, CurrentPath: resolved, StablePath: stable, CurrentVersion: version,
		Command: r.command(), request: r, options: opts}, nil
}

func hexHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("state file exceeds limit")
	}
	return b, err
}
func (r request) command() []string {
	return []string{r.PowerShell, "-NoLogo", "-NoProfile", "-File", r.Manager, "update", r.Package}
}
func (r request) validate() error {
	if !component.MatchString(r.Package) || !component.MatchString(r.Bucket) || len(r.Files) != 5 {
		return errors.New("invalid Scoop plan")
	}
	expected := []string{r.CurrentPath, filepath.Join(filepath.Dir(r.CurrentPath), "install.json"), filepath.Join(filepath.Dir(r.CurrentPath), "manifest.json"), r.Manager, r.PowerShell}
	if !samePath(r.Manager, filepath.Join(r.Root, "apps", "scoop", "current", "bin", "scoop.ps1")) ||
		!samePath(r.StablePath, filepath.Join(r.Root, "apps", r.Package, "current", r.Product.Binary+".exe")) {
		return errors.New("Scoop plan changed its owner")
	}
	for i, path := range expected {
		resolved, err := canonical(path)
		if err != nil || !samePath(resolved, r.Files[i].Path) {
			return errors.New("Scoop plan target changed")
		}
		if err := r.Files[i].validate(); err != nil {
			return err
		}
	}
	p, err := canonical(r.StablePath)
	if err != nil || !samePath(p, r.CurrentPath) {
		return errors.New("Scoop current installation changed; check again")
	}
	return nil
}

func (p Plan) Check() Report {
	return Report{Status: "checked", Manager: "scoop", Package: p.Package, Bucket: p.Bucket, CurrentVersion: p.CurrentVersion,
		Path: p.StablePath, Command: append([]string(nil), p.Command...), CanUpgrade: true}
}

func Status(product Product, id string) (Report, error) {
	root, err := stateRoot(product.Binary)
	if err != nil {
		return Report{}, err
	}
	return readStatus(root, id)
}
func readStatus(root, id string) (Report, error) {
	if !operationID.MatchString(id) {
		return Report{}, errors.New("invalid upgrade operation ID")
	}
	data, err := readLimited(filepath.Join(root, id, "result.json"), 1<<20)
	if err != nil {
		return Report{}, fmt.Errorf("read upgrade status: %w", err)
	}
	var result Report
	if err := json.Unmarshal(data, &result); err != nil || result.OperationID != id {
		return Report{}, errors.New("invalid upgrade status record")
	}
	if !terminal(result.Status) && result.HelperPID > 0 && !processAlive(result.HelperPID, result.HelperStarted) {
		result.Status, result.Reason = "interrupted", "Upgrade helper exited before recording a final result; inspect the log and the installed package before retrying."
	}
	return result, nil
}
func terminal(s string) bool {
	switch s {
	case "updated", "up-to-date", "blocked", "failed", "canceled", "interrupted":
		return true
	}
	return false
}
