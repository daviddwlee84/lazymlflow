package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/artifactpreview"
	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type previewCLIBackend struct {
	*fakeBackend
	info     core.PreviewInfo
	data     []byte
	reads    int
	requests []core.PreviewRequest
}

func (b *previewCLIBackend) InspectArtifact(_ context.Context, run, path string) (core.PreviewInfo, error) {
	info := b.info
	info.RunID, info.Path = run, path
	return info, nil
}
func (b *previewCLIBackend) ReadArtifactPreview(ctx context.Context, request core.PreviewRequest) (core.PreviewResult, error) {
	b.requests = append(b.requests, request)
	info, _ := b.InspectArtifact(ctx, request.RunID, request.Path)
	if err := core.CheckPreviewApproval(info, request); err != nil {
		return core.PreviewResult{Info: info}, err
	}
	b.reads++
	n := min(int64(len(b.data)), request.MaxBytes)
	return core.PreviewResult{Info: info, Data: append([]byte(nil), b.data[:n]...), BytesRead: n, Truncated: int64(len(b.data)) > n}, nil
}
func previewCLIFixture(body string) *previewCLIBackend {
	size := int64(len(body))
	return &previewCLIBackend{fakeBackend: &fakeBackend{}, info: core.PreviewInfo{Supported: true, Size: &size}, data: []byte(body)}
}

func TestArtifactPreviewCLIJSONAndConfiguredLimit(t *testing.T) {
	opts, connector, out, stderr := testOptions(t)
	b := previewCLIFixture(`{"loss":0.5,"status":"ready"}`)
	connector.backend = b
	if status := Execute(context.Background(), []string{"artifacts", "preview", "r", "report.json", "--json"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	var document artifactpreview.Document
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if !document.Formatted || document.Info.Path != "report.json" || document.BytesRead != int64(len(b.data)) || !strings.Contains(document.Text, "\n") {
		t.Fatalf("wrong document: %+v", document)
	}
	if connector.closed != 1 || connector.sessionClosed != 1 {
		t.Fatal("preview leaked tracking session")
	}

	opts, connector, out, stderr = testOptions(t)
	b = previewCLIFixture("123456789")
	connector.backend = b
	opts.LoadConfig = func(string) (*config.Config, error) {
		return &config.Config{Targets: []core.Target{{ID: "first", TrackingURI: "http://first:5000"}}, TUI: config.Preferences{PreviewMaxBytes: 4}}, nil
	}
	if status := Execute(context.Background(), []string{"artifacts", "preview", "r", "report.txt", "--allow-large", "--json"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Text != "1234" || !document.Truncated || b.requests[0].MaxBytes != 4 {
		t.Fatalf("configured prefix cap ignored: %+v", document)
	}
}

func TestArtifactPreviewCLIApprovalBeforeReadingBytes(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		for _, allow := range []bool{false, true} {
			opts, connector, out, stderr := testOptions(t)
			b := previewCLIFixture("0123456789")
			if unknown {
				b.info.Size = nil
			}
			b.info.MayDownloadWhole = true
			connector.backend = b
			args := []string{"artifacts", "preview", "r", "file.txt", "--max-bytes", "4", "--json"}
			if allow {
				args = append(args, "--allow-large")
			}
			status := Execute(context.Background(), args, opts)
			if !allow {
				if status != 2 || b.reads != 0 || out.Len() != 0 || !strings.Contains(stderr.String(), "--allow-large") {
					t.Fatalf("unapproved transfer status=%d reads=%d output=%s err=%s", status, b.reads, out, stderr)
				}
			} else if status != 0 || b.reads != 1 || !strings.Contains(out.String(), `"truncated": true`) {
				t.Fatalf("approved bounded transfer status=%d reads=%d output=%s err=%s", status, b.reads, out, stderr)
			}
		}
	}
}

func TestArtifactPreviewCLIInteractiveCancelIsDefault(t *testing.T) {
	opts, connector, out, stderr := testOptions(t)
	opts.IsTerminal = func() bool { return true }
	opts.In = strings.NewReader("\n")
	b := previewCLIFixture("123456")
	connector.backend = b
	if status := Execute(context.Background(), []string{"artifacts", "preview", "r", "file.txt", "--max-bytes", "2"}, opts); status != 130 || b.reads != 0 || out.Len() != 0 || !strings.Contains(stderr.String(), "[y/N]") {
		t.Fatalf("cancel status=%d reads=%d out=%s err=%s", status, b.reads, out, stderr)
	}
}

func TestArtifactPreviewCLIInvalidModesStayOffline(t *testing.T) {
	for _, args := range [][]string{
		{"artifacts", "preview", "r", "file", "--pager", "--json"},
		{"artifacts", "preview", "r", "file", "--pager"},
		{"artifacts", "preview", "r", "file", "--max-bytes", "0"},
		{"artifacts", "preview", "r", "file", "--max-bytes", "67108865"},
	} {
		opts, connector, out, stderr := testOptions(t)
		if status := Execute(context.Background(), args, opts); status != 2 || len(connector.opened) != 0 || out.Len() != 0 {
			t.Fatalf("%v status=%d out=%s err=%s", args, status, out, stderr)
		}
	}
}

func TestArtifactPreviewCLIEmptyBinaryAndUnavailableBackend(t *testing.T) {
	for _, body := range []string{"", "\x00\x01binary"} {
		opts, connector, out, stderr := testOptions(t)
		connector.backend = previewCLIFixture(body)
		if status := Execute(context.Background(), []string{"artifacts", "preview", "r", "artifact", "--json"}, opts); status != 0 {
			t.Fatalf("%d %s", status, stderr)
		}
		var doc artifactpreview.Document
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Binary != (body != "") {
			t.Fatalf("binary detection: %+v", doc)
		}
	}
	opts, _, out, stderr := testOptions(t)
	if status := Execute(context.Background(), []string{"artifacts", "preview", "r", "artifact", "--json"}, opts); status != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "explicit download") {
		t.Fatalf("unsupported backend %d %s", status, stderr)
	}
}

func TestPreviewConfigRoundTripPreservesTUIComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	text := "[tui]\npreview_max_bytes = 4096 # bounded\nfuture = true\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TUI.PreviewMaxBytes = 8192
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	result, err := config.Load(path)
	if err != nil || result.TUI.PreviewMaxBytes != 8192 {
		t.Fatalf("round trip: %+v %v", result, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# bounded") || !strings.Contains(string(data), "future = true") {
		t.Fatalf("lost settings: %s", data)
	}
}

func TestArtifactPreviewCLITruncationDescribesPrefix(t *testing.T) {
	opts, connector, out, stderr := testOptions(t)
	connector.backend = previewCLIFixture("123456789")
	if status := Execute(context.Background(), []string{"artifacts", "preview", "r", "file.txt", "--max-bytes", "4", "--allow-large"}, opts); status != 0 {
		t.Fatalf("%d %s", status, stderr)
	}
	if out.String() != "1234\n" || !strings.Contains(stderr.String(), "Preview shows a bounded prefix") {
		t.Fatalf("inaccurate prefix description: out=%s err=%s", out, stderr)
	}
}
