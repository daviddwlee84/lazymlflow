package mlflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/fileuri"
)

func previewMetadataHandler(t *testing.T, root func() string, size *int64, content http.HandlerFunc) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case api + "runs/get":
			_ = json.NewEncoder(w).Encode(map[string]any{"run": core.Run{Info: core.RunInfo{RunID: "run", ExperimentID: "e", ArtifactURI: root()}}})
		case api + "artifacts/list":
			entry := map[string]any{"path": "log.json", "is_dir": false}
			if size != nil {
				entry["file_size"] = *size
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []any{entry}})
		default:
			content(w, r)
		}
	}
}

func TestPreviewUnknownAndLargeRequireApprovalBeforeContentGET(t *testing.T) {
	for _, size := range []*int64{nil, previewInt64(100)} {
		var reads atomic.Int32
		var server *httptest.Server
		server = httptest.NewServer(previewMetadataHandler(t, func() string { return server.URL + "/objects" }, size, func(w http.ResponseWriter, r *http.Request) { reads.Add(1); fmt.Fprint(w, strings.Repeat("x", 100)) }))
		client := New(server.URL, Options{})
		_, err := client.ReadArtifactPreview(context.Background(), core.PreviewRequest{RunID: "run", Path: "log.json", MaxBytes: 10})
		var approval *core.PreviewApprovalError
		if !errors.As(err, &approval) || reads.Load() != 0 {
			t.Fatalf("unapproved content fetched: reads=%d err=%v", reads.Load(), err)
		}
		result, err := client.ReadArtifactPreview(context.Background(), core.PreviewRequest{RunID: "run", Path: "log.json", MaxBytes: 10, AllowLarge: true, AllowUnknown: true})
		if err != nil || len(result.Data) != 10 || result.BytesRead != 11 || !result.Truncated {
			t.Fatalf("prefix result %+v %v", result, err)
		}
		server.Close()
	}
}

func TestPreviewOldProxyKnownSmallAndUnsupportedPresigning(t *testing.T) {
	for _, status := range []int{404, 405, 501} {
		var reads atomic.Int32
		var server *httptest.Server
		server = httptest.NewServer(previewMetadataHandler(t, func() string { return "mlflow-artifacts:/root" }, previewInt64(2), func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/presigned/") {
				w.WriteHeader(status)
				return
			}
			reads.Add(1)
			fmt.Fprint(w, "{}")
		}))
		result, err := New(server.URL, Options{}).ReadArtifactPreview(context.Background(), core.PreviewRequest{RunID: "run", Path: "log.json", MaxBytes: 10})
		if err != nil || string(result.Data) != "{}" || !result.Info.MayDownloadWhole || reads.Load() != 1 {
			t.Fatalf("old proxy small preview failed for %d: %+v %v", status, result, err)
		}
		server.Close()
	}
}

func TestPreviewOldProxyLargeNeedsWholeFetchConsentAndStaleSizeRecheck(t *testing.T) {
	var reads atomic.Int32
	size := int64(100)
	var server *httptest.Server
	server = httptest.NewServer(previewMetadataHandler(t, func() string { return "mlflow-artifacts:/root" }, &size, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/presigned/") {
			w.WriteHeader(404)
			return
		}
		reads.Add(1)
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, strings.Repeat("x", 100))
	}))
	defer server.Close()
	client := New(server.URL, Options{})
	q := core.PreviewRequest{RunID: "run", Path: "log.json", MaxBytes: 10, AllowLarge: true}
	_, err := client.ReadArtifactPreview(context.Background(), q)
	var approval *core.PreviewApprovalError
	if !errors.As(err, &approval) || !approval.WholeFetch || reads.Load() != 0 {
		t.Fatal("large proxy cost was not disclosed before transfer", err)
	}
	// The list claims the file is small; actual response headers reveal growth.
	size = 2
	q.AllowLarge = false
	_, err = client.ReadArtifactPreview(context.Background(), q)
	if !errors.As(err, &approval) || !approval.Large || !approval.WholeFetch || approval.Info.Size == nil || *approval.Info.Size != 100 {
		t.Fatal("stale list size bypassed approval", err)
	}
}

type previewTransport func(*http.Request) (*http.Response, error)

func (f previewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countingPreviewBody struct {
	count  int
	closed bool
}

func (b *countingPreviewBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.count += len(p)
	return len(p), nil
}
func (b *countingPreviewBody) Close() error { b.closed = true; return nil }

func TestPreviewIgnoredRangeReadsAtMostLimitPlusOneAndCloses(t *testing.T) {
	body := &countingPreviewBody{}
	client := New("http://tracking", Options{HTTPClient: &http.Client{Transport: previewTransport(func(r *http.Request) (*http.Response, error) {
		text := ""
		switch r.URL.Path {
		case api + "runs/get":
			text = `{"run":{"info":{"run_id":"run","artifact_uri":"http://storage/objects"}}}`
		case api + "artifacts/list":
			text = `{"files":[{"path":"log.json","is_dir":false}]}`
		default:
			if r.Header.Get("Range") != "bytes=0-10" || r.Header.Get("Accept-Encoding") != "identity" {
				t.Fatalf("missing prefix headers: %v", r.Header)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body, ContentLength: -1, Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(text)), ContentLength: int64(len(text)), Request: r}, nil
	})}})
	result, err := client.ReadArtifactPreview(context.Background(), core.PreviewRequest{RunID: "run", Path: "log.json", MaxBytes: 10, AllowLarge: true, AllowUnknown: true})
	if err != nil || body.count != 11 || !body.closed || len(result.Data) != 10 || !result.Truncated {
		t.Fatalf("unbounded read: %+v body=%+v err=%v", result, body, err)
	}
}

func TestPreviewPresignedRangeAndCredentialsAreIsolated(t *testing.T) {
	var trackingAuth, storageAuth, redirectedAuth, redirectedSecret string
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuth, redirectedSecret = r.Header.Get("Authorization"), r.Header.Get("X-Storage-Secret")
		w.Header().Set("Content-Range", "bytes 0-10/100")
		w.Header().Set("Content-Length", "11")
		w.WriteHeader(206)
		fmt.Fprint(w, strings.Repeat("x", 11))
	}))
	defer redirect.Close()
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		storageAuth = r.Header.Get("Authorization")
		if r.Header.Get("Range") != "bytes=0-10" || r.Header.Get("X-Storage-Secret") != "object-secret" {
			t.Errorf("bad storage request: %v", r.Header)
		}
		http.Redirect(w, r, redirect.URL+"/prefix?signature=private", 302)
	}))
	defer storage.Close()
	var server *httptest.Server
	metadata := previewMetadataHandler(t, func() string { return "mlflow-artifacts:/root" }, previewInt64(100), func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/presigned/") {
			t.Errorf("proxy whole-fetch fallback: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"url": storage.URL + "/object?signature=secret", "headers": map[string]string{"Authorization": "Bearer object-token", "X-Storage-Secret": "object-secret"}, "file_size": "100"})
	})
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackingAuth = r.Header.Get("Authorization")
		metadata(w, r)
	}))
	defer server.Close()
	result, err := New(server.URL, Options{Token: "tracking-secret"}).ReadArtifactPreview(context.Background(), core.PreviewRequest{RunID: "run", Path: "log.json", MaxBytes: 10, AllowLarge: true})
	if err != nil || result.Info.MayDownloadWhole || !result.Truncated || len(result.Data) != 10 || result.Info.Size == nil || *result.Info.Size != 100 {
		t.Fatalf("presigned preview %+v %v", result, err)
	}
	if trackingAuth != "Bearer tracking-secret" || storageAuth != "Bearer object-token" || redirectedAuth != "" || redirectedSecret != "" {
		t.Fatalf("credential leak tracking=%q storage=%q redirect=%q/%q", trackingAuth, storageAuth, redirectedAuth, redirectedSecret)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "signature") {
		t.Fatal("storage authorization leaked into result")
	}
}

func TestPreviewLocalConfinesPathsAndRejectsDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "log.json"), []byte(`{"large":1234567890123456789}`), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	hasSymlink := true
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		hasSymlink = false
		t.Log("symlink case unavailable without Windows symlink permission")
	}
	uri := (&url.URL{Scheme: "file", Path: fileuri.Path(root)}).String()
	server := httptest.NewServer(previewMetadataHandler(t, func() string { return uri }, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("local preview attempted network content read %s", r.URL)
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()
	client := New(server.URL, Options{Local: true})
	result, err := client.ReadArtifactPreview(context.Background(), core.PreviewRequest{RunID: "run", Path: "log.json", MaxBytes: 8, AllowLarge: true})
	if err != nil || len(result.Data) != 8 || !result.Truncated || result.Info.Size == nil || result.Info.MayDownloadWhole {
		t.Fatalf("local result %+v %v", result, err)
	}
	unsafePaths := []string{"../private"}
	if hasSymlink {
		unsafePaths = append(unsafePaths, "escape")
	}
	for _, p := range unsafePaths {
		if _, err := client.ReadArtifactPreview(context.Background(), core.PreviewRequest{RunID: "run", Path: p, MaxBytes: 8, AllowLarge: true}); err == nil {
			t.Fatalf("unsafe local path accepted: %s", p)
		}
	}
	if _, err := client.ReadArtifactPreview(context.Background(), core.PreviewRequest{RunID: "run", Path: "", MaxBytes: 8}); err == nil {
		t.Fatal("directory preview accepted")
	}
}

func TestPreviewDirectCloudNeverFallsBackToSDKDownload(t *testing.T) {
	server := httptest.NewServer(previewMetadataHandler(t, func() string { return "s3://bucket/root" }, nil, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected content request %s", r.URL)
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()
	client := New(server.URL, Options{ArtifactCLI: func(context.Context, []string) ([]byte, error) {
		t.Fatal("preview called SDK/CLI fallback")
		return nil, nil
	}})
	info, err := client.InspectArtifact(context.Background(), "run", "log.json")
	if err != nil || info.Supported || !strings.Contains(info.Reason, "bounded") {
		t.Fatalf("direct cloud capability %+v %v", info, err)
	}
	if _, err := client.ReadArtifactPreview(context.Background(), core.PreviewRequest{RunID: "run", Path: "log.json", MaxBytes: 10, AllowLarge: true, AllowUnknown: true, AllowWholeFetch: true}); err == nil {
		t.Fatal("unsupported direct cloud preview succeeded")
	}
}

func TestPreviewCancelClosesStreamingHTTPResponse(t *testing.T) {
	started := make(chan struct{})
	closed := make(chan struct{})
	var server *httptest.Server
	server = httptest.NewServer(previewMetadataHandler(t, func() string { return server.URL + "/objects" }, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := New(server.URL, Options{}).ReadArtifactPreview(ctx, core.PreviewRequest{RunID: "run", Path: "log.json", MaxBytes: 10, AllowLarge: true, AllowUnknown: true})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("preview did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("preview cancellation blocked")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("preview response remained open")
	}
}

func previewInt64(value int64) *int64 { return &value }
