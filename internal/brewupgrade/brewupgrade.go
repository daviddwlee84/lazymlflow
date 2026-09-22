// Package brewupgrade delegates upgrades to the Homebrew that owns an executable.
// It neither queries upstream releases nor writes installation or staging files.
package brewupgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var ErrNotManaged = errors.New("executable has no Homebrew keg receipt")

// Options supplies the product-specific identity/version inspection. Inspect
// must reject another product and return the actual installed version. The
// optional command hooks support deterministic tests without invoking Homebrew.
type Options struct {
	Inspect  func(context.Context, string) (string, error)
	LookPath func(string) (string, error)
	Run      func(context.Context, string, []string, io.Writer) (string, error)
}

// Plan is a read-only observation. CurrentPath is the resolved running binary;
// StablePath is the owning formula's opt/bin path, never a fresh PATH lookup.
// Apply rejects edited public fields and revalidates its private observations.
type Plan struct {
	Formula, BrewPath, Cellar, CurrentPath, StablePath, CurrentVersion string
	state                                                              *binding
}

// Outcome describes the verified installation after Homebrew returned success.
// Changed compares installation identity and version, not an upstream release.
// An Apply error may follow partial Homebrew effects; no rollback is attempted.
type Outcome struct {
	Formula, Path, ResolvedPath, Version string
	Changed                              bool
}

type binding struct {
	view                        Plan
	opts                        Options
	executable, brewLookup      string
	owner                       ownership
	binary, receipt, manager    fingerprint
	stableBinary, stableReceipt fingerprint
}

type ownership struct {
	rack, keg, receipt, name, tap string
}

type fingerprint struct {
	path   string
	info   os.FileInfo
	digest [sha256.Size]byte
}

var component = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+_.@-]*$`)

// Prepare proves ownership using the running executable's real receipt and the
// selected brew's read-only --cellar/--prefix queries. It does not run upgrade,
// create staging, or query the latest GitHub release.
func Prepare(ctx context.Context, executable, binaryName string, opts Options) (Plan, error) {
	var plan Plan
	if err := ctx.Err(); err != nil {
		return plan, err
	}
	if opts.Inspect == nil || !component.MatchString(binaryName) {
		return plan, errors.New("Homebrew upgrade requires a binary name and product inspector")
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Run == nil {
		opts.Run = runCommand
	}
	original, err := filepath.Abs(executable)
	if err != nil {
		return plan, err
	}
	resolved, err := canonical(original)
	if err != nil {
		return plan, err
	}
	owner, receipt, err := identify(resolved)
	if err != nil {
		return plan, err
	}
	binary, err := capture(resolved)
	if err != nil {
		return plan, err
	}
	brewLookup, err := opts.LookPath("brew")
	if err != nil {
		return plan, fmt.Errorf("Homebrew owns this executable but brew is unavailable: %w", err)
	}
	brewLookup, err = filepath.Abs(brewLookup)
	if err != nil {
		return plan, err
	}
	brewPath, err := canonical(brewLookup)
	if err != nil {
		return plan, fmt.Errorf("resolve Homebrew executable: %w", err)
	}
	manager, err := capture(brewPath)
	if err != nil {
		return plan, err
	}
	plan = Plan{Formula: owner.formula(), BrewPath: brewPath, Cellar: filepath.Dir(owner.rack), CurrentPath: resolved}
	prefix, err := proveManager(ctx, opts, plan, owner.rack)
	if err != nil {
		return plan, err
	}
	plan.StablePath = filepath.Join(prefix, "bin", binaryName)
	stableBinary, stableReceipt, err := owningTarget(plan.StablePath, owner)
	if err != nil {
		return plan, fmt.Errorf("verify Homebrew opt executable: %w", err)
	}
	plan.CurrentVersion, err = opts.Inspect(ctx, resolved)
	if err != nil || strings.TrimSpace(plan.CurrentVersion) == "" {
		return plan, fmt.Errorf("inspect current Homebrew product: %w", nonnil(err, "empty installed version"))
	}
	state := &binding{view: plan, opts: opts, executable: original, brewLookup: brewLookup, owner: owner, binary: binary, receipt: receipt, manager: manager, stableBinary: stableBinary, stableReceipt: stableReceipt}
	plan.state = state
	if err := state.validate(); err != nil {
		return Plan{}, err
	}
	return plan, ctx.Err()
}

// Command returns a fresh argv copy for a preview. No shell is involved.
func (p Plan) Command() []string {
	if p.state == nil {
		return nil
	}
	return []string{p.state.view.BrewPath, "upgrade", p.state.view.Formula}
}

// Apply delegates only this formula to its verified owning manager. Output goes
// to progress (nil means discard); failures never authorize a direct replacement.
func (p Plan) Apply(ctx context.Context, progress io.Writer) (Outcome, error) {
	var result Outcome
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if p.state == nil {
		return result, errors.New("Homebrew upgrade requires a prepared plan")
	}
	state := p.state
	p.state = nil
	if p != state.view {
		return result, errors.New("Homebrew upgrade plan was edited")
	}
	result.Formula, result.Path = p.Formula, p.StablePath
	if progress == nil {
		progress = io.Discard
	}
	if err := state.validate(); err != nil {
		return result, err
	}
	prefix, err := proveManager(ctx, state.opts, p, state.owner.rack)
	if err != nil {
		return result, err
	}
	if filepath.Join(prefix, "bin", filepath.Base(p.StablePath)) != p.StablePath {
		return result, errors.New("Homebrew opt prefix changed since planning")
	}
	if err := state.validate(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if _, err := state.opts.Run(ctx, p.BrewPath, []string{"upgrade", p.Formula}, progress); err != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("brew upgrade %s failed: %w", p.Formula, err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	candidate, receipt, err := owningTarget(p.StablePath, state.owner)
	if err != nil {
		return result, fmt.Errorf("Homebrew returned success but its opt executable could not be verified: %w", err)
	}
	version, err := state.opts.Inspect(ctx, candidate.path)
	if err != nil || strings.TrimSpace(version) == "" {
		return result, fmt.Errorf("Homebrew returned success but product verification failed: %w", nonnil(err, "empty installed version"))
	}
	resolved, err := canonical(p.StablePath)
	if err != nil || resolved != candidate.path || candidate.validate() != nil || receipt.validate() != nil {
		return result, errors.New("Homebrew opt installation changed during verification")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.ResolvedPath, result.Version = candidate.path, version
	result.Changed = candidate.path != p.CurrentPath || version != p.CurrentVersion || !sameState(candidate.info, state.binary.info) || candidate.digest != state.binary.digest
	return result, nil
}

func (o ownership) formula() string {
	if o.tap != "" {
		return o.tap + "/" + o.name
	}
	return o.name
}

func identify(binary string) (ownership, fingerprint, error) {
	for dir := filepath.Dir(binary); filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
		receipt := filepath.Join(dir, "INSTALL_RECEIPT.json")
		if _, err := os.Lstat(receipt); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return ownership{}, fingerprint{}, err
		}
		stamp, err := capture(receipt)
		if err != nil {
			return ownership{}, fingerprint{}, err
		}
		if stamp.info.Size() > 1<<20 {
			return ownership{}, fingerprint{}, errors.New("Homebrew receipt exceeds size limit")
		}
		data, err := os.ReadFile(receipt)
		if err != nil || sha256.Sum256(data) != stamp.digest || stamp.validate() != nil {
			return ownership{}, fingerprint{}, errors.New("Homebrew receipt changed while reading")
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil || object == nil {
			return ownership{}, fingerprint{}, errors.New("invalid Homebrew receipt")
		}
		var source struct {
			Tap string `json:"tap"`
		}
		if raw, ok := object["source"]; ok {
			if err := json.Unmarshal(raw, &source); err != nil {
				return ownership{}, fingerprint{}, errors.New("invalid Homebrew receipt source")
			}
		}
		owner := ownership{rack: filepath.Dir(dir), keg: dir, receipt: receipt, name: filepath.Base(filepath.Dir(dir)), tap: source.Tap}
		if !component.MatchString(owner.name) {
			return ownership{}, fingerprint{}, errors.New("invalid installed Homebrew formula name")
		}
		if source.Tap != "" {
			parts := strings.Split(source.Tap, "/")
			if len(parts) != 2 || !component.MatchString(parts[0]) || !component.MatchString(parts[1]) {
				return ownership{}, fingerprint{}, errors.New("invalid installed Homebrew tap name")
			}
		}
		return owner, stamp, nil
	}
	return ownership{}, fingerprint{}, ErrNotManaged
}

func proveManager(ctx context.Context, opts Options, plan Plan, rack string) (string, error) {
	output, err := opts.Run(ctx, plan.BrewPath, []string{"--cellar", plan.Formula}, io.Discard)
	if err != nil {
		return "", fmt.Errorf("query owning Homebrew Cellar: %w", err)
	}
	actual, err := queryPath(output)
	if err != nil || actual != rack {
		return "", errors.New("selected brew does not own the executable's Cellar; select its owning brew")
	}
	output, err = opts.Run(ctx, plan.BrewPath, []string{"--prefix", plan.Formula}, io.Discard)
	if err != nil {
		return "", fmt.Errorf("query owning Homebrew opt prefix: %w", err)
	}
	prefix := strings.TrimSpace(output)
	if !filepath.IsAbs(prefix) || filepath.Clean(prefix) != prefix || strings.ContainsAny(prefix, "\r\n\x00") || filepath.Base(prefix) != filepath.Base(rack) || filepath.Base(filepath.Dir(prefix)) != "opt" {
		return "", errors.New("Homebrew returned an invalid stable opt prefix")
	}
	return prefix, ctx.Err()
}

func queryPath(output string) (string, error) {
	path := strings.TrimSpace(output)
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
		return "", errors.New("Homebrew returned an invalid path")
	}
	return canonical(path)
}

func owningTarget(path string, original ownership) (fingerprint, fingerprint, error) {
	resolved, err := canonical(path)
	if err != nil {
		return fingerprint{}, fingerprint{}, err
	}
	owner, receipt, err := identify(resolved)
	if err != nil || owner.rack != original.rack || owner.name != original.name || owner.tap != original.tap {
		return fingerprint{}, fingerprint{}, errors.New("opt executable is outside the owning formula or has a different receipt")
	}
	binary, err := capture(resolved)
	return binary, receipt, err
}

func (s *binding) validate() error {
	path, err := canonical(s.executable)
	if err != nil || path != s.view.CurrentPath || s.binary.validate() != nil || s.receipt.validate() != nil {
		return errors.New("running executable or Homebrew receipt changed since planning")
	}
	path, err = canonical(s.brewLookup)
	if err != nil || path != s.view.BrewPath || s.manager.validate() != nil {
		return errors.New("selected Homebrew executable changed since planning")
	}
	path, err = canonical(s.view.StablePath)
	if err != nil || path != s.stableBinary.path || s.stableBinary.validate() != nil || s.stableReceipt.validate() != nil {
		return errors.New("Homebrew opt installation changed since planning")
	}
	return nil
}

func canonical(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func capture(path string) (fingerprint, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fingerprint{}, err
	}
	if !info.Mode().IsRegular() {
		return fingerprint{}, errors.New("Homebrew installation object is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return fingerprint{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameState(info, opened) {
		return fingerprint{}, errors.New("Homebrew installation object changed while opening")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fingerprint{}, err
	}
	after, err := file.Stat()
	named, nameErr := os.Lstat(path)
	if err != nil || nameErr != nil || !sameState(opened, after) || !sameState(after, named) {
		return fingerprint{}, errors.New("Homebrew installation object changed while reading")
	}
	stamp := fingerprint{path: path, info: after}
	copy(stamp.digest[:], hash.Sum(nil))
	return stamp, nil
}

func (s fingerprint) validate() error {
	now, err := capture(s.path)
	if err != nil || !sameState(s.info, now.info) || s.digest != now.digest {
		return errors.New("Homebrew installation object changed")
	}
	return nil
}

func sameState(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func nonnil(err error, text string) error {
	if err != nil {
		return err
	}
	return errors.New(text)
}

func runCommand(ctx context.Context, program string, args []string, progress io.Writer) (string, error) {
	query := len(args) > 0 && (args[0] == "--cellar" || args[0] == "--prefix")
	if query {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, program, args...)
	configureCommand(cmd)
	if progress == nil {
		progress = io.Discard
	}
	var output limitedOutput
	cmd.Stdout, cmd.Stderr = progress, progress
	if query {
		cmd.Stdout = &output
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "HOMEBREW_NO_AUTO_UPDATE=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "HOMEBREW_NO_AUTO_UPDATE=1")
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return output.String(), err
}

type limitedOutput struct{ buffer bytes.Buffer }

func (w *limitedOutput) String() string { return w.buffer.String() }

func (w *limitedOutput) Write(data []byte) (int, error) {
	if w.buffer.Len()+len(data) > 8192 {
		return 0, errors.New("Homebrew query output exceeds limit")
	}
	return w.buffer.Write(data)
}
