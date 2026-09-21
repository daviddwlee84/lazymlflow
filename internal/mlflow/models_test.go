package mlflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/models"
)

func modelFixture(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	aliases := 0
	files := map[string]string{"MLmodel": "flavors:\n  python_function:\n    loader_module: do.not.import\n    python_model: absent.pkl\n    code: absent_code\n    env: conda.yaml\n", "conda.yaml": "dependencies:\n- python=3.11\n- pip:\n  - scikit-learn==1.7.2\n", "weights.pkl": "\x00\xffprivate bytes"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
			http.Error(w, "mutation", 500)
			return
		}
		switch r.URL.Path {
		case api + "runs/get":
			fmt.Fprint(w, `{"run":{"info":{"run_id":"r1","status":"FINISHED","artifact_uri":"mlflow-artifacts:/1/run/artifacts"},"inputs":{"model_inputs":[{"model_id":"m-one"}]},"outputs":{"model_outputs":[{"model_id":"m-one","step":"0"},{"model_id":"m-one"}]}}}`)
		case api + "registered-models/alias":
			aliases++
			version := "1"
			if aliases > 1 {
				version = "2"
			}
			fmt.Fprintf(w, `{"model_version":{"name":"iris","version":"%s","source":"s3://wrong/source","run_id":"r1","model_id":"m-one","status":"READY","creation_timestamp":"123"}}`, version)
		case api + "model-versions/get":
			fmt.Fprint(w, `{"model_version":{"name":"iris","version":"1","source":"s3://wrong/source","run_id":"r1","model_id":"m-one","status":"READY"}}`)
		case api + "model-versions/get-download-uri":
			if r.URL.Query().Get("version") != "1" {
				t.Errorf("alias was resolved twice")
			}
			fmt.Fprint(w, `{"artifact_uri":"models:/m-one"}`)
		case api + "logged-models/m-one":
			fmt.Fprint(w, `{"model":{"info":{"model_id":"m-one","artifact_uri":"mlflow-artifacts:/1/models/m-one/artifacts","source_run_id":"r1","status":"LOGGED_MODEL_READY","creation_timestamp_ms":"123","registrations":[{"name":"iris","version":"1"}]},"data":{"params":[{"key":"alpha","value":"1"}]}}}`)
		case api + "logged-models/m-one/artifacts/directories":
			entries := []map[string]any{}
			for name, body := range files {
				entries = append(entries, map[string]any{"path": name, "is_dir": false, "file_size": len(body)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": entries})
		case api + "registered-models/search":
			fmt.Fprint(w, `{"registered_models":[{"name":"iris","creation_timestamp":"123"}]}`)
		case api + "model-versions/search":
			fmt.Fprint(w, `{"model_versions":[{"name":"iris","version":"1","status":"READY"}]}`)
		default:
			prefix := "/api/2.0/mlflow-artifacts/artifacts/1/models/m-one/artifacts/"
			if strings.HasPrefix(r.URL.Path, prefix) {
				name := strings.TrimPrefix(r.URL.Path, prefix)
				if b, ok := files[name]; ok {
					fmt.Fprint(w, b)
					return
				}
			}
			http.NotFound(w, r)
		}
	}))
	return server, &aliases
}
func TestModelsResolveExportVerifyAndInspect(t *testing.T) {
	server, aliases := modelFixture(t)
	defer server.Close()
	service, e := models.New(New(server.URL, Options{}))
	if e != nil {
		t.Fatal(e)
	}
	inspection, e := service.Inspect(context.Background(), "models:/iris/1")
	if e != nil {
		t.Fatal(e)
	}
	if inspection.Resolution.Logged == nil || inspection.Metadata.RuntimeValidation != "not_run" || inspection.Metadata.Serving != "pyfunc-candidate-unverified" || len(inspection.Metadata.Environments) != 1 || len(inspection.Metadata.Environments[0].Dependencies) != 2 {
		t.Fatalf("%+v", inspection)
	}
	if len(inspection.Metadata.References) != 2 || inspection.Metadata.References[0].Status != "missing" || inspection.Metadata.References[1].Status != "missing" {
		t.Fatal("missing code/artifact not reported", inspection.Metadata)
	}
	dest := filepath.Join(t.TempDir(), "bundle")
	result, e := service.Export(context.Background(), models.ExportOptions{Source: "models:/iris@champion", Destination: dest}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if *aliases != 1 || result.Source.ResolvedURI != "models:/iris/1" || result.Files != 3 {
		t.Fatal(result, *aliases)
	}
	if _, e = os.Stat(filepath.Join(dest, "payload", "registered_model_meta")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("client-generated metadata leaked into payload")
	}
	m, e := models.ReadManifest(result.Manifest)
	if e != nil {
		t.Fatal(e)
	}
	if m.Source.Registered != nil || m.Source.Logged != nil {
		t.Fatal("receipt copied arbitrary model metadata")
	}
	v, e := models.Verify(context.Background(), dest, result.Manifest)
	if e != nil || !v.Valid {
		t.Fatal(v, e)
	}
	related, e := service.Related(context.Background(), "r1")
	if e != nil || len(related.Models) != 3 || related.Models[0].Role != "input" || related.Models[1].Step == nil || *related.Models[1].Step != 0 || related.Models[2].Step != nil {
		t.Fatal(related, e)
	}
	if _, e = service.Export(context.Background(), models.ExportOptions{Source: "models:/iris/1", Destination: dest}, nil); e == nil {
		t.Fatal("implicit overwrite")
	}
}
func TestModelsUnsupportedAndAuthorizationRemainDistinct(t *testing.T) {
	for _, status := range []int{404, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, "missing") }))
			defer s.Close()
			_, e := New(s.URL, Options{}).GetLoggedModel(context.Background(), "m-x")
			if e == nil || (strings.Contains(e.Error(), "unavailable") != (status == 404)) {
				t.Fatal(e)
			}
		})
	}
}
func TestDirectModelDownloadUsesResolvedURI(t *testing.T) {
	var passed []string
	c := New("http://unused", Options{ArtifactCLI: func(ctx context.Context, args []string) ([]byte, error) {
		passed = append([]string(nil), args...)
		var stage string
		for i, arg := range args {
			if arg == "--dst-path" {
				stage = args[i+1]
			}
		}
		p := filepath.Join(stage, "source")
		if e := os.Mkdir(p, 0755); e != nil {
			return nil, e
		}
		if e := os.WriteFile(filepath.Join(p, "weights.bin"), []byte("weights"), 0644); e != nil {
			return nil, e
		}
		return []byte(p), nil
	}})
	dest := filepath.Join(t.TempDir(), "out")
	_, e := c.DownloadModelArtifacts(context.Background(), models.ArtifactRequest{Location: models.ArtifactLocation{URI: "s3://bucket/exact-model"}, Destination: dest}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Join(passed[:4], " ") != "artifacts download --artifact-uri s3://bucket/exact-model" {
		t.Fatal(passed)
	}
}
func TestModelArtifactTraversalBeforeJoin(t *testing.T) {
	// Path validation must happen before path.Join can hide a traversal component.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"files":[{"path":"safe/../outside","is_dir":false,"file_size":1}]}`)
	}))
	defer s.Close()
	c := New(s.URL, Options{})
	_, e := c.ListModelArtifacts(context.Background(), models.ArtifactLocation{URI: "mlflow-artifacts:/root"})
	if e == nil {
		t.Fatal("escape accepted")
	}
}

func TestModelExportCancelPreservesExistingBundle(t *testing.T) {
	server, _ := modelFixture(t)
	defer server.Close()
	service, _ := models.New(New(server.URL, Options{}))
	dest := filepath.Join(t.TempDir(), "bundle")
	_ = os.Mkdir(dest, 0755)
	_ = os.WriteFile(filepath.Join(dest, "keep"), []byte("original"), 0644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, e := service.Export(ctx, models.ExportOptions{Source: "models:/iris/1", Destination: dest, Overwrite: true}, func(core.Progress) { cancel() })
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dest, "keep"))
	if e != nil || string(b) != "original" {
		t.Fatal(string(b), e)
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".lazymlflow-model-*"))
	if len(matches) != 0 {
		t.Fatal("staging leaked", matches)
	}
}
func TestModelListFollowsPagesAndRejectsRepeatedToken(t *testing.T) {
	calls := 0
	repeat := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != api+"registered-models/search" || r.URL.Query().Get("max_results") != "1" {
			t.Error(r.URL)
		}
		if r.URL.Query().Get("page_token") == "" || repeat {
			fmt.Fprint(w, `{"registered_models":[{"name":"one"}],"next_page_token":"next"}`)
		} else {
			fmt.Fprint(w, `{"registered_models":[{"name":"two"}]}`)
		}
	}))
	defer server.Close()
	service, _ := models.New(New(server.URL, Options{}))
	p, e := service.List(context.Background(), models.Query{MaxResults: 1}, true)
	if e != nil || len(p.Models) != 2 || calls != 2 {
		t.Fatal(p, calls, e)
	}
	repeat = true
	if _, e = service.List(context.Background(), models.Query{MaxResults: 1}, true); e == nil || !strings.Contains(e.Error(), "repeated") {
		t.Fatal(e)
	}
}
func TestPendingModelCannotExport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model":{"info":{"model_id":"m-pending","artifact_uri":"mlflow-artifacts:/pending","status":"LOGGED_MODEL_PENDING"}}}`)
	}))
	defer server.Close()
	service, _ := models.New(New(server.URL, Options{}))
	dest := filepath.Join(t.TempDir(), "bundle")
	_, e := service.Export(context.Background(), models.ExportOptions{Source: "models:/m-pending", Destination: dest}, nil)
	if e == nil || !strings.Contains(e.Error(), "not ready") {
		t.Fatal(e)
	}
	if _, e = os.Stat(dest); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("pending model published", e)
	}
}
