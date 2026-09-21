package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceFormsAndEscapes(t *testing.T) {
	for _, s := range []string{"runs:/run-1/checkpoint/best", "models:/iris/12", "models:/iris@champion", "models:/m-123", "models:/research%20model/1", "models:/team@model/1", "models:/team@model@champion", "models:/%E6%A8%A1%E5%9E%8B%23one/1"} {
		if _, e := ParseSource(s); e != nil {
			t.Fatalf("%s: %v", s, e)
		}
	}
	for _, s := range []string{"runs:/run", "models:/iris/Production", "models:/iris/latest", "models:/iris/01", "models:/iris/0", "models:/bad:name/1", "models:/bad%2Fname/1", "models:/iris@", "models:/iris/1/file", "models://user:password@host/iris/1", "models:/iris/1?token=x", "runs:/r/%2e%2e/secret", "runs:/r/x%2fy/../../secret", "runs:/r/%5csecret", "runs:/r/x%00"} {
		if _, e := ParseSource(s); e == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestSafeMetadataNoModelLoading(t *testing.T) {
	m, e := ParseMetadata([]byte("flavors:\n  python_function:\n    loader_module: evil.module\n    env:\n      virtualenv: python_env.yaml\n  sklearn:\n    pickled_model: model.pkl\nsignature:\n  inputs: '[{\"type\":\"double\",\"name\":\"x\"}]'\n"))
	if e != nil || m.RuntimeValidation != "not_run" || m.Serving != "pyfunc-candidate-unverified" || len(m.Flavors) != 2 || len(m.EnvironmentFiles) != 1 {
		t.Fatal(m, e)
	}
	for _, s := range []string{"!!python/object/apply:os.system [echo BAD]", "a: &a [x]\nb: *a", "flavors: {}\nflavors: {}", "---\nflavors: {}\n---\nx: y", "flavors:\n  python_function:\n    env: ../environment.yaml", "flavors:\n  python_function:\n    env: https://host/env.yaml"} {
		if _, e := ParseMetadata([]byte(s)); e == nil {
			t.Fatalf("accepted unsafe metadata %q", s)
		}
	}
	if _, e := ParseMetadata([]byte(strings.Repeat("x", int(MetadataLimit)+1))); e == nil {
		t.Fatal("accepted oversized metadata")
	}
	deps, refs, e := environmentDependencies("python_env.yaml", []byte("python: 3.11.0\ndependencies:\n- -r requirements.txt\n- some-package==1.2\n"))
	if e != nil || len(refs) != 1 || len(deps) != 2 {
		t.Fatal(deps, refs, e)
	}
	deps, _, e = environmentDependencies("requirements.txt", []byte("pkg @ https://user:secret@example.test/wheel?token=secret\n--extra-index-url https://secret\n"))
	if e != nil || strings.Contains(strings.Join(deps, ""), "secret") {
		t.Fatal(deps, e)
	}
}
func fixtureManifest(t *testing.T, root string) string {
	t.Helper()
	if e := os.MkdirAll(filepath.Join(root, "payload"), 0755); e != nil {
		t.Fatal(e)
	}
	bytes := []byte("immutable weights\x00\xff")
	if e := os.WriteFile(filepath.Join(root, "payload", "weights.bin"), bytes, 0644); e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(bytes)
	m := Manifest{Schema: ManifestSchema, Source: Resolution{ResolvedURI: "models:/iris/1", ResolvedAt: time.Now()}, ExportedAt: time.Now(), Files: []FileDigest{{Path: "weights.bin", Size: int64(len(bytes)), SHA256: hex.EncodeToString(digest[:])}}}
	b, _ := json.Marshal(m)
	lock := filepath.Join(t.TempDir(), "expected.json")
	if e := os.WriteFile(lock, b, 0644); e != nil {
		t.Fatal(e)
	}
	return lock
}
func TestVerifyIndependentManifestAndExactInventory(t *testing.T) {
	root := t.TempDir()
	lock := fixtureManifest(t, root)
	result, e := Verify(context.Background(), root, lock)
	if e != nil || !result.Valid || result.Files != 1 {
		t.Fatal(result, e)
	}
	// A rewritten local receipt cannot bless changed bytes.
	_ = os.WriteFile(filepath.Join(root, "payload", "weights.bin"), []byte("tampered"), 0644)
	_ = os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"files":[]}`), 0644)
	result, e = Verify(context.Background(), root, lock)
	if e == nil || result.Valid || len(result.Differences) != 1 || !strings.Contains(result.Differences[0], "changed") {
		t.Fatal(result, e)
	}
	_ = os.Remove(filepath.Join(root, "payload", "weights.bin"))
	_ = os.WriteFile(filepath.Join(root, "payload", "extra.bin"), nil, 0644)
	result, e = Verify(context.Background(), root, lock)
	if e == nil || len(result.Differences) != 2 {
		t.Fatal(result, e)
	}
}
func TestVerifyRejectsSymlinkAndMalformedManifest(t *testing.T) {
	root := t.TempDir()
	lock := fixtureManifest(t, root)
	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(root, "payload", "escape")
		if e := os.Symlink(lock, link); e != nil {
			t.Skipf("symlink unavailable: %v", e)
		}
		defer os.Remove(link)
		if _, e := Verify(context.Background(), root, lock); e == nil {
			t.Fatal("symlink accepted")
		}
	})
	m, e := ReadManifest(lock)
	if e != nil {
		t.Fatal(e)
	}
	m.Files = append(m.Files, m.Files[0])
	b, _ := json.Marshal(m)
	_ = os.WriteFile(lock, b, 0644)
	if _, e = ReadManifest(lock); e == nil {
		t.Fatal("duplicate file accepted")
	}
	m.Files = m.Files[:1]
	m.Files[0].Path = "../outside"
	b, _ = json.Marshal(m)
	_ = os.WriteFile(lock, b, 0644)
	if _, e = ReadManifest(lock); e == nil {
		t.Fatal("escape accepted")
	}
}
func TestVerifyCancellationAndUnknownSchema(t *testing.T) {
	root := t.TempDir()
	lock := fixtureManifest(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Verify(ctx, root, lock); e == nil {
		t.Fatal("cancel ignored")
	}
	b, _ := os.ReadFile(lock)
	_ = os.WriteFile(lock, []byte(strings.Replace(string(b), ManifestSchema, "future/v9", 1)), 0644)
	if _, e := ReadManifest(lock); e == nil {
		t.Fatal("unknown schema accepted")
	}
}

func TestReceiptURIRedaction(t *testing.T) {
	for _, raw := range []string{"file:///private/home/user/model", "/private/home/user/model", "https://user:secret@example.test/model?signature=secret#secret"} {
		safe := SafeURI(raw)
		if strings.Contains(safe, "secret") || strings.Contains(safe, "/private/home") {
			t.Fatalf("unsafe receipt URI %q", safe)
		}
	}
}

func TestManifestRequiresResolvedSourceAndExplicitInventory(t *testing.T) {
	root := t.TempDir()
	lock := fixtureManifest(t, root)
	m, e := ReadManifest(lock)
	if e != nil {
		t.Fatal(e)
	}
	m.Source.ResolvedURI = "models:/iris@champion"
	b, _ := json.Marshal(m)
	_ = os.WriteFile(lock, b, 0644)
	if _, e := ReadManifest(lock); e == nil {
		t.Fatal("mutable resolved reference accepted")
	}
	m.Source.ResolvedURI = "models:/iris/1"
	m.Files = nil
	b, _ = json.Marshal(m)
	_ = os.WriteFile(lock, b, 0644)
	if _, e := ReadManifest(lock); e == nil {
		t.Fatal("missing inventory accepted")
	}
}
