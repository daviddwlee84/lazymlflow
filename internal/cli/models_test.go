package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/models"
)

func TestModelInvalidSourceDoesNotOpenTarget(t *testing.T) {
	for _, args := range [][]string{{"models", "inspect", "models:/name/Production", "--json"}, {"models", "export", "runs:/r/checkpoint", "--json"}, {"models", "verify", "bundle", "--json"}} {
		opts, c, out, stderr := testOptions(t)
		status := Execute(context.Background(), args, opts)
		if status != 2 || len(c.opened) != 0 || out.Len() != 0 || !json.Valid(stderr.Bytes()) {
			t.Fatalf("%v status=%d opened=%d out=%s err=%s", args, status, len(c.opened), out, stderr)
		}
	}
}
func TestModelVerifyIsOfflineAndUsesExternalManifest(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle")
	_ = os.MkdirAll(filepath.Join(bundle, "payload"), 0755)
	data := []byte("weights")
	_ = os.WriteFile(filepath.Join(bundle, "payload", "weights.bin"), data, 0644)
	sum := sha256.Sum256(data)
	manifest := models.Manifest{Schema: models.ManifestSchema, Source: models.Resolution{ResolvedURI: "models:/example/1"}, Files: []models.FileDigest{{Path: "weights.bin", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
	b, _ := json.Marshal(manifest)
	expected := filepath.Join(dir, "expected.json")
	_ = os.WriteFile(expected, b, 0644)
	opts, c, out, stderr := testOptions(t)
	opts.LoadConfig = func(string) (*config.Config, error) { t.Fatal("offline verify loaded targets"); return nil, nil }
	status := Execute(context.Background(), []string{"models", "verify", bundle, "--manifest", expected, "--json"}, opts)
	if status != 0 || len(c.opened) != 0 {
		t.Fatal(status, stderr)
	}
	var result models.VerifyResult
	if e := json.Unmarshal(out.Bytes(), &result); e != nil || !result.Valid {
		t.Fatal(result, e)
	}
	_ = os.WriteFile(filepath.Join(bundle, "payload", "weights.bin"), []byte("modified"), 0644)
	out.Reset()
	stderr.Reset()
	status = Execute(context.Background(), []string{"models", "verify", bundle, "--manifest", expected, "--json"}, opts)
	if status != 1 || !json.Valid(out.Bytes()) || !json.Valid(stderr.Bytes()) {
		t.Fatal(status, out, stderr)
	}
}
