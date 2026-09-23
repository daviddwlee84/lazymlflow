package scoopupgrade

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const helperArgument = "--internal-scoop-upgrade"

type activeOperation struct {
	ID      string
	PID     int
	Started uint64
}

// Handoff starts the verified copied executable outside Scoop's app directory.
// It returns only after the helper is ready to observe this exact process exit.
func (p Plan) Handoff(ctx context.Context, interactive bool) (Report, error) {
	result := p.Check()
	if p.request == nil {
		return result, errors.New("check the installation before upgrading")
	}
	r := *p.request
	if p.Package != r.Package || p.Bucket != r.Bucket || p.Root != r.Root || p.CurrentPath != r.CurrentPath || p.StablePath != r.StablePath || p.CurrentVersion != r.Version || !reflect.DeepEqual(p.Command, r.command()) {
		return result, errors.New("reviewed upgrade plan was modified")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := r.validate(); err != nil {
		return result, err
	}
	if err := os.MkdirAll(p.options.StateRoot, 0700); err != nil {
		return result, err
	}
	root, err := canonical(p.options.StateRoot)
	if err != nil {
		return result, err
	}
	unlock, err := lockState(root)
	if err != nil {
		return result, err
	}
	defer unlock()
	activePath := filepath.Join(root, "active.json")
	if data, err := readLimited(activePath, 4096); err == nil {
		var active activeOperation
		if json.Unmarshal(data, &active) != nil {
			return result, errors.New("invalid existing upgrade lock; inspect local upgrade state")
		}
		if processAlive(active.PID, active.Started) {
			previous, err := readStatus(root, active.ID)
			if err != nil || !terminal(previous.Status) {
				return result, fmt.Errorf("upgrade %s is already active; use its status_command to inspect it", active.ID)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return result, err
	}
	id := hex.EncodeToString(token[:])
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		return result, err
	}
	if err := secureDirectory(dir); err != nil {
		return result, err
	}
	r.ParentPID, r.ParentStarted, r.Interactive, r.OperationID = os.Getpid(), processStarted(os.Getpid()), interactive, id
	if r.ParentStarted == 0 {
		return result, errors.New("cannot bind the initiating process identity")
	}
	helper := filepath.Join(dir, "helper.exe")
	data, err := readLimited(r.CurrentPath, 256<<20)
	if err != nil || hexHash(data) != r.Files[0].Hash {
		return result, errors.New("running executable changed before handoff")
	}
	if err := os.WriteFile(helper, data, 0700); err != nil {
		return result, err
	}
	requestPath := filepath.Join(dir, "request.json")
	data, err = json.Marshal(r)
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(requestPath, data, 0600); err != nil {
		return result, err
	}
	result.OperationID, result.ResultPath, result.LogPath = id, filepath.Join(dir, "result.json"), filepath.Join(dir, "progress.log")
	result.Status = "starting"
	if err := writeJSON(result.ResultPath, result); err != nil {
		return result, err
	}
	cmd, jobBound, err := startHelper(helper, []string{helperArgument, requestPath, hexHash(data), id}, interactive)
	if err != nil {
		return result, fmt.Errorf("start update helper: %w", err)
	}
	result.HelperPID, result.HelperStarted = cmd.Process.Pid, processStarted(cmd.Process.Pid)
	failed := true
	defer func() {
		if failed {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			result.Status, result.Reason = "failed", "Upgrade handoff did not complete; the manager was not authorized to run."
			_ = writeJSON(result.ResultPath, result)
		} else {
			_ = cmd.Process.Release()
		}
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(30 * time.Millisecond)
	defer tick.Stop()
	for {
		ready, err := readStatus(root, id)
		if err == nil && ready.Status == "waiting" && ready.HelperPID == result.HelperPID && ready.HelperStarted == result.HelperStarted {
			if err := r.validate(); err != nil {
				return result, err
			}
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if err := writeJSON(activePath, activeOperation{id, ready.HelperPID, ready.HelperStarted}); err != nil {
				return result, err
			}
			// An explicit ready/authorized handshake prevents a timed-out or
			// canceled launcher from accidentally authorizing a delayed helper.
			if err := os.WriteFile(filepath.Join(dir, "authorized"), []byte(id), 0600); err != nil {
				return result, err
			}
			failed = false
			ready.Status = "handed-off"
			if jobBound {
				ready.Reason = "The host retains process lifetime control. Keep the launching terminal open until upgrade --status reports a final result."
			}
			return ready, nil
		}
		if err == nil && terminal(ready.Status) {
			return result, errors.New("upgrade helper failed before handoff; inspect its result")
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-deadline.C:
			return result, errors.New("upgrade helper did not become ready; no update was authorized")
		case <-tick.C:
		}
	}
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".result-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// HandleHelper must run before normal configuration/backend initialization.
// The hidden mode accepts only a hash-bound request in this product's state
// directory; no arbitrary command or executable is accepted from JSON.
func HandleHelper(product Product) (int, bool) {
	if len(os.Args) < 2 || os.Args[1] != helperArgument {
		return 0, false
	}
	if len(os.Args) != 5 || !operationID.MatchString(os.Args[4]) {
		return 2, true
	}
	root, err := stateRoot(product.Binary)
	if err != nil {
		return 1, true
	}
	expected := filepath.Join(root, os.Args[4], "request.json")
	actual, err := canonical(os.Args[2])
	if err != nil {
		return 1, true
	}
	allowed, err := canonical(expected)
	if err != nil || !samePath(actual, allowed) {
		return 1, true
	}
	data, err := readLimited(actual, 1<<20)
	if err != nil || hexHash(data) != os.Args[3] {
		return 1, true
	}
	var r request
	if json.Unmarshal(data, &r) != nil || r.Product != product || r.OperationID != os.Args[4] {
		return 1, true
	}
	dir := filepath.Dir(actual)
	executable, err := os.Executable()
	if err != nil {
		return 1, true
	}
	executable, err = canonical(executable)
	if err != nil || !samePath(executable, filepath.Join(dir, "helper.exe")) {
		return 1, true
	}
	f, err := capture(executable)
	if err != nil || len(r.Files) != 5 || f.Hash != r.Files[0].Hash {
		return 1, true
	}
	// Claim the hash-bound request once, before writing any status. Replaying
	// a completed helper cannot run Scoop twice or overwrite its result.
	claim, err := os.OpenFile(filepath.Join(dir, "helper.claim"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 1, true
	}
	if _, err := fmt.Fprintln(claim, os.Getpid()); err != nil {
		claim.Close()
		return 1, true
	}
	if err := claim.Close(); err != nil {
		return 1, true
	}
	return runHelper(r, dir), true
}

func runHelper(r request, dir string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runHelperContext(ctx, r, dir)
}

func runHelperContext(ctx context.Context, r request, dir string) int {
	result := Report{Status: "waiting", Manager: "scoop", Package: r.Package, Bucket: r.Bucket, CurrentVersion: r.Version,
		Path: r.StablePath, Command: r.command(), CanUpgrade: true, OperationID: r.OperationID,
		ResultPath: filepath.Join(dir, "result.json"), LogPath: filepath.Join(dir, "progress.log"),
		HelperPID: os.Getpid(), HelperStarted: processStarted(os.Getpid()),
		StatusCommand: []string{filepath.Join(dir, "helper.exe"), "upgrade", "--status", r.OperationID, "--json"}}
	if result.HelperStarted == 0 || r.ParentPID <= 0 || r.ParentStarted == 0 {
		return 1
	}
	if err := r.validate(); err != nil {
		result.Status, result.Reason = "failed", err.Error()
		_ = writeJSON(result.ResultPath, result)
		return 1
	}
	log, err := os.OpenFile(result.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 1
	}
	defer log.Close()
	output, closeConsole := helperConsole(r.Interactive, log)
	defer closeConsole()
	finish := func(status, reason string) int {
		result.Status, result.Reason = status, reason
		if err := writeJSON(result.ResultPath, result); err != nil {
			fmt.Fprintln(output, "Could not record final upgrade result:", err)
			return 1
		}
		fmt.Fprintf(output, "%s: %s\n%s\nResult: %s\n", r.Product.Binary, status, reason, result.ResultPath)
		if r.Interactive {
			consolePause(ctx)
		}
		if status == "updated" || status == "up-to-date" {
			return 0
		}
		return 1
	}
	if err := writeJSON(result.ResultPath, result); err != nil {
		return 1
	}
	fmt.Fprintln(output, "Waiting for the initiating tool to exit before updating", r.Package)
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for processAlive(r.ParentPID, r.ParentStarted) {
		select {
		case <-ctx.Done():
			return finish("canceled", "Canceled before starting Scoop.")
		case <-deadline.C:
			return finish("blocked", "The initiating process did not exit; no update was started.")
		case <-tick.C:
		}
	}
	authorized, err := readLimited(filepath.Join(dir, "authorized"), 128)
	if err != nil || string(authorized) != r.OperationID {
		return finish("failed", "The initiating process did not authorize this handoff.")
	}
	if err := ctx.Err(); err != nil {
		return finish("canceled", "Canceled before starting Scoop.")
	}
	if err := r.validate(); err != nil {
		return finish("blocked", err.Error())
	}
	if installedProcessRunning(r.Root, r.Package) {
		return finish("blocked", "Another instance is using this Scoop package; close it and retry.")
	}
	result.Status = "running"
	if err := writeJSON(result.ResultPath, result); err != nil {
		return 1
	}
	args := r.command()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = replaceEnv(os.Environ(), "SCOOP", r.Root)
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = 2 * time.Second
	configureManager(cmd)
	err = cmd.Run()
	if ctx.Err() != nil {
		return finish("canceled", "Scoop was canceled; inspect the package before retrying.")
	}
	if err != nil {
		return finish("failed", "Scoop failed; no other installer was attempted. See the progress log.")
	}
	// Scoop can exit zero after skipping an in-use app. Treat that as blocked,
	// not a successful update; never enable IGNORE_RUNNING_PROCESSES.
	logData, logErr := readLimited(result.LogPath, 8<<20)
	if logErr != nil {
		return finish("failed", "Scoop output could not be fully verified; inspect the progress log.")
	}
	if bytesContainRunningSkip(logData) {
		return finish("blocked", "Scoop skipped a running package; close its other instances and retry.")
	}
	if managerReportedError(logData) {
		return finish("failed", "Scoop reported an error despite its exit status; inspect the progress log. No other installer was attempted.")
	}
	current, err := canonical(r.StablePath)
	if err != nil {
		return finish("failed", "Scoop returned success but its current executable is missing.")
	}
	// One post-manager observation binds the actual product/version, receipt,
	// manager root and current junction; do not combine separate version reads.
	after, err := Prepare(ctx, current, r.Product, Options{})
	if err != nil || !samePath(after.Root, r.Root) || after.Package != r.Package || after.Bucket != r.Bucket {
		return finish("failed", "Scoop returned success but the effective package ownership or executable could not be verified.")
	}
	version := after.CurrentVersion
	result.Version, result.Path = version, after.StablePath
	result.ChangeKnown = true
	result.Changed = version != r.Version || !samePath(after.CurrentPath, r.CurrentPath) || after.request.Files[0].Hash != r.Files[0].Hash
	if result.Changed {
		return finish("updated", "Verified installed version: "+version)
	}
	return finish("up-to-date", "Scoop left the installed version unchanged: "+version)
}

func bytesContainRunningSkip(data []byte) bool {
	text := strings.ToLower(string(data))
	return strings.Contains(text, "running process detected") || strings.Contains(text, "close them and try again")
}
func replaceEnv(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, key) {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+value)
}

var managerANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
var managerError = regexp.MustCompile(`(?m)(?:^|[\r\n\t ])ERROR[ :!]`)

func managerReportedError(data []byte) bool {
	return managerError.Match(managerANSI.ReplaceAll(data, nil))
}
