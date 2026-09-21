package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func (s *Service) Export(ctx context.Context, opts ExportOptions, progress func(core.Progress)) (ExportResult, error) {
	var out ExportResult
	if opts.Destination == "" {
		return out, errors.New("bundle destination is required")
	}
	dest, e := filepath.Abs(opts.Destination)
	if e != nil {
		return out, e
	}
	cwd, _ := os.Getwd()
	if dest == cwd || dest == filepath.Dir(dest) {
		return out, errors.New("destination must name a bundle directory, not the current directory or filesystem root")
	}
	if _, e := os.Lstat(dest); e == nil && !opts.Overwrite {
		return out, errors.New("bundle destination exists; use overwrite explicitly")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return out, e
	}
	r, e := s.Resolve(ctx, opts.Source)
	if e != nil {
		return out, e
	}
	if !ready(r) {
		return out, fmt.Errorf("model is not ready for export (status %s)", r.Status)
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
		return out, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(dest), ".lazymlflow-model-")
	if e != nil {
		return out, e
	}
	defer os.RemoveAll(stage)
	payload := filepath.Join(stage, "payload")
	_, e = s.models.DownloadModelArtifacts(ctx, ArtifactRequest{Location: r.Location, Destination: payload}, progress)
	if e != nil {
		return out, e
	}
	info, e := os.Lstat(payload)
	if e != nil {
		return out, e
	}
	if info.Mode().IsRegular() {
		one := filepath.Join(stage, "single-artifact")
		if e = os.Rename(payload, one); e != nil {
			return out, e
		}
		if e = os.Mkdir(payload, 0755); e != nil {
			return out, e
		}
		name := path.Base(r.Location.Path)
		if name == "." || name == "/" || name == "" {
			name = "artifact"
		}
		if e = SafePath(name); e != nil {
			return out, e
		}
		if e = os.Rename(one, filepath.Join(payload, name)); e != nil {
			return out, e
		}
	}
	files, total, e := inventory(ctx, payload)
	if e != nil {
		return out, e
	}
	// Receipts retain identity, not arbitrary remote tags/config or credential-bearing URIs.
	receipt := r
	receipt.Registered = nil
	receipt.Logged = nil
	manifest := Manifest{Schema: ManifestSchema, Source: receipt, ExportedAt: time.Now().UTC(), Files: files}
	b, e := json.MarshalIndent(manifest, "", "  ")
	if e != nil {
		return out, e
	}
	b = append(b, '\n')
	if e = os.WriteFile(filepath.Join(stage, "manifest.json"), b, 0644); e != nil {
		return out, e
	}
	if e = ctx.Err(); e != nil {
		return out, e
	}
	if e = publishBundle(stage, dest, opts.Overwrite); e != nil {
		return out, e
	}
	return ExportResult{Path: dest, Manifest: filepath.Join(dest, "manifest.json"), Files: len(files), Bytes: total, Source: receipt}, nil
}
func publishBundle(stage, dest string, overwrite bool) error {
	_, e := os.Lstat(dest)
	if errors.Is(e, os.ErrNotExist) {
		return os.Rename(stage, dest)
	}
	if e != nil {
		return e
	}
	if !overwrite {
		return errors.New("bundle destination appeared during export")
	}
	backup, e := os.MkdirTemp(filepath.Dir(dest), ".lazymlflow-model-backup-")
	if e != nil {
		return e
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(backup)
		}
	}()
	old := filepath.Join(backup, "original")
	if e = os.Rename(dest, old); e != nil {
		return e
	}
	if e = os.Rename(stage, dest); e != nil {
		if restore := os.Rename(old, dest); restore != nil {
			keep = true
			return fmt.Errorf("bundle publish failed: %v; restore failed: %v; original at %s", e, restore, old)
		}
		return e
	}
	return nil
}
func inventory(ctx context.Context, root string) ([]FileDigest, int64, error) {
	files := []FileDigest{}
	var total int64
	info, e := os.Lstat(root)
	if e != nil {
		return files, total, e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return files, total, errors.New("payload must be a real directory")
	}
	e = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("payload contains a symlink")
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("payload contains a non-regular file")
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if e = SafePath(rel); e != nil {
			return e
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		h := sha256.New()
		buf := make([]byte, 128<<10)
		var n int64
		for {
			if e = ctx.Err(); e != nil {
				_ = f.Close()
				return e
			}
			count, readErr := f.Read(buf)
			if count > 0 {
				_, _ = h.Write(buf[:count])
				n += int64(count)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = f.Close()
				return readErr
			}
		}
		if e = f.Close(); e != nil {
			return e
		}
		if n != info.Size() {
			return errors.New("payload changed while hashing")
		}
		total += n
		files = append(files, FileDigest{Path: rel, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
		return nil
	})
	sortedFiles(files)
	return files, total, e
}
func ReadManifest(file string) (Manifest, error) {
	var m Manifest
	f, e := os.Open(file)
	if e != nil {
		return m, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if e != nil {
		return m, e
	}
	if len(b) > 8<<20 {
		return m, errors.New("manifest exceeds 8 MiB")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if e = dec.Decode(&m); e != nil {
		return m, e
	}
	var tail any
	if dec.Decode(&tail) != io.EOF {
		return m, errors.New("manifest has trailing JSON")
	}
	if m.Schema != ManifestSchema {
		return m, fmt.Errorf("unsupported manifest schema %q", m.Schema)
	}
	ref, err := ParseSource(m.Source.ResolvedURI)
	if err != nil {
		return m, fmt.Errorf("invalid manifest source: %w", err)
	}
	if ref.Alias != "" {
		return m, errors.New("manifest resolved_uri must pin a numeric version, logged model, or run artifact")
	}
	if m.Files == nil {
		return m, errors.New("manifest must include an explicit files inventory")
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if f.Path == "" || SafePath(f.Path) != nil || seen[f.Path] || f.Size < 0 {
			return m, errors.New("manifest contains an invalid or duplicate file path/size")
		}
		seen[f.Path] = true
		b, e := hex.DecodeString(f.SHA256)
		if e != nil || len(b) != sha256.Size || strings.ToLower(f.SHA256) != f.SHA256 {
			return m, errors.New("manifest contains an invalid SHA-256")
		}
	}
	return m, nil
}

// Verify compares bytes only with the caller's independent expected manifest.
// It does not consult a bundle's own receipt and does not contact any server.
func Verify(ctx context.Context, bundle, expectedManifest string) (VerifyResult, error) {
	out := VerifyResult{Differences: []string{}}
	if expectedManifest == "" {
		return out, errors.New("an independent expected manifest is required")
	}
	m, e := ReadManifest(expectedManifest)
	if e != nil {
		return out, e
	}
	info, e := os.Lstat(bundle)
	if e != nil {
		return out, e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return out, errors.New("bundle must be a real directory")
	}
	files, total, e := inventory(ctx, filepath.Join(bundle, "payload"))
	if e != nil {
		return out, e
	}
	out.Files = len(files)
	out.Bytes = total
	expected := map[string]FileDigest{}
	for _, f := range m.Files {
		expected[f.Path] = f
	}
	for _, f := range files {
		x, ok := expected[f.Path]
		if !ok {
			out.Differences = append(out.Differences, "unexpected: "+f.Path)
		} else if x.Size != f.Size || x.SHA256 != f.SHA256 {
			out.Differences = append(out.Differences, "changed: "+f.Path)
		}
		delete(expected, f.Path)
	}
	for p := range expected {
		out.Differences = append(out.Differences, "missing: "+p)
	}
	sort.Strings(out.Differences)
	out.Valid = len(out.Differences) == 0
	if !out.Valid {
		return out, errors.New("bundle payload does not match the expected manifest")
	}
	return out, nil
}
