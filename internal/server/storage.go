package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func checkArtifactPath(ctx context.Context, s ServerSpec, write bool) error {
	if s.ArtifactPath == "" {
		return nil
	}
	info, err := os.Stat(s.ArtifactPath)
	if err != nil {
		return fmt.Errorf("artifact directory must already exist: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("artifact-path is not a directory")
	}
	if s.Artifacts == "nas" {
		resolvedRoot, e := filepath.EvalSymlinks(s.NASMount)
		if e != nil {
			return e
		}
		resolvedArtifact, e := filepath.EvalSymlinks(s.ArtifactPath)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(resolvedRoot, resolvedArtifact)
		if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("resolved artifact directory escapes the NAS mount through a symlink")
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := checkMount(probeCtx, s.NASMount); err != nil {
			return fmt.Errorf("NAS mount unavailable; refusing to write into an unmounted local directory: %w", err)
		}
	}
	if write {
		f, err := os.CreateTemp(s.ArtifactPath, ".lazymlflow-write-check-")
		if err != nil {
			return fmt.Errorf("artifact directory is not writable by this user: %w", err)
		}
		name := f.Name()
		defer os.Remove(name)
		if _, err = f.Write([]byte("lazymlflow storage check\n")); err != nil {
			f.Close()
			return err
		}
		if err = f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil || string(data) != "lazymlflow storage check\n" {
			return fmt.Errorf("artifact directory read/write check failed")
		}
	}
	return nil
}

func checkMount(ctx context.Context, path string) error {
	_, err := mountIdentity(ctx, path)
	return err
}

func mountIdentity(ctx context.Context, path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	var source, kind string
	switch runtime.GOOS {
	case "linux":
		data, err := exec.CommandContext(ctx, "findmnt", "--json", "--output", "TARGET,FSTYPE,SOURCE", "--mountpoint", resolved).Output()
		if err != nil {
			return "", fmt.Errorf("%s is not an active mount point (findmnt): %w", path, err)
		}
		var result struct {
			Filesystems []struct{ Target, Fstype, Source string }
		}
		if err = json.Unmarshal(data, &result); err != nil || len(result.Filesystems) != 1 || result.Filesystems[0].Target != resolved {
			return "", fmt.Errorf("%s is not an exact mount point", path)
		}
		source, kind = result.Filesystems[0].Source, result.Filesystems[0].Fstype
	case "darwin":
		data, err := exec.CommandContext(ctx, "/sbin/mount").Output()
		if err != nil {
			return "", err
		}
		found := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, " on "+resolved+" (") {
				found = true
				parts := strings.SplitN(line, " on "+resolved+" (", 2)
				source = parts[0]
				kind = strings.TrimSuffix(strings.SplitN(parts[1], ",", 2)[0], ")")
				break
			}
		}
		if !found {
			return "", fmt.Errorf("%s is not an active mount point", path)
		}
	default:
		return "", fmt.Errorf("NAS mount verification is supported on Linux and macOS; use an external S3 endpoint on %s", runtime.GOOS)
	}
	if resolved == string(filepath.Separator) {
		return "", fmt.Errorf("the root filesystem is not a NAS mount")
	}
	if kind != "nfs" && kind != "nfs4" && kind != "cifs" && kind != "smbfs" && kind != "smb3" {
		return "", fmt.Errorf("%s is mounted as %s; NAS artifacts require an NFS or SMB/CIFS filesystem", path, kind)
	}
	sum := sha256.Sum256([]byte(kind + "\n" + source))
	return hex.EncodeToString(sum[:]), nil
}
