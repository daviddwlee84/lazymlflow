package mlflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestSearchPreservesFullQueryAndPrefix(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix"+api+"runs/search" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing auth")
		}
		var q core.RunQuery
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
		}
		if q.Filter != "metrics.loss < 1" || q.PageToken != "opaque" || q.OrderBy[0] != "metrics.loss ASC" {
			t.Errorf("query %#v", q)
		}
		fmt.Fprint(w, `{"runs":[{"info":{"run_id":"000123","start_time":"1000000000001"},"data":{"metrics":[{"key":"loss","value":"NaN","step":"12","timestamp":"1000000000002"}]}}],"next_page_token":"next"}`)
	}))
	defer s.Close()
	p, err := New(s.URL+"/prefix", Options{Token: "secret"}).SearchRuns(context.Background(), core.RunQuery{ExperimentIDs: []string{"0"}, Filter: "metrics.loss < 1", PageToken: "opaque", OrderBy: []string{"metrics.loss ASC"}})
	if err != nil {
		t.Fatal(err)
	}
	if p.NextPageToken != "next" || p.Runs[0].ID() != "000123" || p.Runs[0].Info.StartTime != 1000000000001 || !math.IsNaN(float64(p.Runs[0].Data.Metrics[0].Value)) {
		t.Fatalf("response %#v", p)
	}
}
func TestHistoryFollowsTokensAndPreservesDuplicates(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page_token") == "" {
			fmt.Fprint(w, `{"metrics":[{"key":"loss","value":2,"step":8,"timestamp":10},{"key":"loss","value":1,"step":8,"timestamp":20}],"next_page_token":"x"}`)
		} else {
			fmt.Fprint(w, `{"metrics":[{"key":"loss","value":"Infinity","step":9,"timestamp":30}]}`)
		}
	}))
	defer s.Close()
	points, err := New(s.URL, Options{}).MetricHistory(context.Background(), "run", "loss")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(points) != 3 || points[0].Step != points[1].Step || !math.IsInf(float64(points[2].Value), 1) {
		t.Fatal(points, calls)
	}
}
func TestHistoryRejectsRepeatedToken(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"next_page_token":"x"}`) }))
	defer s.Close()
	_, err := New(s.URL, Options{}).MetricHistory(context.Background(), "r", "x")
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatal(err)
	}
}
func TestErrorRedactionAndRedirectCredentials(t *testing.T) {
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error_code":"DENIED","message":"secret-token"}`)
	}))
	defer other.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer s.Close()
	_, err := New(s.URL, Options{Token: "secret-token"}).GetRun(context.Background(), "r")
	if err == nil || strings.Contains(err.Error(), "secret-token") || leaked != "" {
		t.Fatal(err, leaked)
	}
	var e *APIError
	if !errors.As(err, &e) || e.Status != 403 {
		t.Fatal(err)
	}
}
func artifactServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prefix" + api + "runs/get":
			fmt.Fprint(w, `{"run":{"info":{"run_id":"run","artifact_uri":"mlflow-artifacts:/0/run/artifacts"}}}`)
		case "/prefix" + api + "artifacts/list":
			if r.URL.Query().Get("path") == "folder" {
				fmt.Fprint(w, `{"files":[{"path":"folder/a b.txt","is_dir":false,"file_size":"5"}]}`)
			} else {
				fmt.Fprint(w, `{"files":[{"path":"folder","is_dir":true},{"path":"empty","is_dir":true}]}`)
			}
		case "/prefix/api/2.0/mlflow-artifacts/artifacts/0/run/artifacts/folder/a b.txt":
			fmt.Fprint(w, "hello")
		default:
			t.Errorf("unexpected %s", r.URL)
			http.NotFound(w, r)
		}
	}))
}
func TestArtifactDirectoryAtomicDownloadAndOverwrite(t *testing.T) {
	s := artifactServer(t)
	defer s.Close()
	c := New(s.URL+"/prefix", Options{})
	dest := filepath.Join(t.TempDir(), "saved")
	r, err := c.DownloadArtifact(context.Background(), core.DownloadRequest{RunID: "run", Path: "folder", Destination: dest}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dest, "a b.txt"))
	if string(b) != "hello" || r.Files != 1 || r.Bytes != 5 {
		t.Fatal(string(b), r)
	}
	if _, err = c.DownloadArtifact(context.Background(), core.DownloadRequest{RunID: "run", Path: "folder", Destination: dest}, nil); err == nil {
		t.Fatal("overwrote without approval")
	}
	if _, err = c.DownloadArtifact(context.Background(), core.DownloadRequest{RunID: "run", Path: "folder", Destination: dest, Overwrite: true}, nil); err != nil {
		t.Fatal(err)
	}
}
func TestCanceledDownloadKeepsOldDestination(t *testing.T) {
	s := artifactServer(t)
	defer s.Close()
	dest := filepath.Join(t.TempDir(), "old")
	if err := os.WriteFile(dest, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := New(s.URL+"/prefix", Options{}).DownloadArtifact(ctx, core.DownloadRequest{RunID: "run", Path: "folder/a b.txt", Destination: dest, Overwrite: true}, func(core.Progress) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "keep" {
		t.Fatal(string(b))
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Fatal("left staging files", entries)
	}
}
func TestArtifactEscapeAndRemoteFilesystemRejected(t *testing.T) {
	for _, p := range []string{"../file", "dir/../../file", "/absolute", "x\\y"} {
		if _, err := artifactPath(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"run":{"info":{"run_id":"r","artifact_uri":"file:///sensitive"}}}`)
	}))
	defer s.Close()
	var called atomic.Bool
	c := New(s.URL, Options{ArtifactCLI: func(context.Context, []string) ([]byte, error) { called.Store(true); return nil, nil }})
	if _, err := c.ListArtifacts(context.Background(), "r", ""); err == nil || called.Load() {
		t.Fatal("tried local path from remote server", err)
	}
}
func TestDirectArtifactCLIStaging(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"run":{"info":{"run_id":"r","artifact_uri":"s3://bucket/r"}}}`)
	}))
	defer s.Close()
	c := New(s.URL, Options{ArtifactCLI: func(ctx context.Context, args []string) ([]byte, error) {
		if args[1] == "list" {
			return []byte(`[{"path":"a.txt","is_dir":false,"file_size":"4"}]`), nil
		}
		stage := ""
		for i, a := range args {
			if a == "--dst-path" {
				stage = args[i+1]
			}
		}
		p := filepath.Join(stage, "a.txt")
		err := os.WriteFile(p, []byte("data"), 0600)
		return []byte("\n" + p + "\n"), err
	}})
	p, err := c.ListArtifacts(context.Background(), "r", "")
	if err != nil || len(p.Files) != 1 {
		t.Fatal(p, err)
	}
	dest := filepath.Join(t.TempDir(), "saved.txt")
	r, err := c.DownloadArtifact(context.Background(), core.DownloadRequest{RunID: "r", Path: "a.txt", Destination: dest}, nil)
	if err != nil || r.Bytes != 4 {
		t.Fatal(r, err)
	}
}

func TestSSHArtifactOriginTranslation(t *testing.T) {
	c := New("http://127.0.0.1:18000/prefix", Options{OriginalTrackingURI: "https://tracking.internal:8443/prefix"})
	for _, test := range []struct{ uri, want string }{
		{"mlflow-artifacts:/1/run/artifacts", "http://127.0.0.1:18000/prefix/api/2.0/mlflow-artifacts/artifacts/1/run/artifacts"},
		{"mlflow-artifacts://tracking.internal:8443/1/run/artifacts", "http://127.0.0.1:18000/prefix/api/2.0/mlflow-artifacts/artifacts/1/run/artifacts"},
		{"https://tracking.internal:8443/prefix/api/2.0/mlflow-artifacts/artifacts/1/run/artifacts", "http://127.0.0.1:18000/prefix/api/2.0/mlflow-artifacts/artifacts/1/run/artifacts"},
		{"https://tracking.internal:8443/unrelated/artifacts", "https://tracking.internal:8443/unrelated/artifacts"},
		{"https://other.internal/prefix/artifacts", "https://other.internal/prefix/artifacts"},
		{"mlflow-artifacts://other.internal:8443/1/run/artifacts", "https://other.internal:8443/prefix/api/2.0/mlflow-artifacts/artifacts/1/run/artifacts"},
	} {
		got, proxy, err := c.proxyURI(test.uri)
		if err != nil || !proxy || got != test.want {
			t.Fatalf("%s: got %q want %q (%v)", test.uri, got, test.want, err)
		}
	}
}
