package mlflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/models"
)

func modelQuery(q models.Query) url.Values {
	v := url.Values{}
	if q.MaxResults <= 0 {
		q.MaxResults = 100
	}
	v.Set("max_results", strconv.Itoa(q.MaxResults))
	if q.Filter != "" {
		v.Set("filter", q.Filter)
	}
	if q.PageToken != "" {
		v.Set("page_token", q.PageToken)
	}
	for _, o := range q.OrderBy {
		v.Add("order_by", o)
	}
	return v
}
func modelAPIError(err error) error {
	var api *APIError
	if errors.As(err, &api) && (api.Status == 405 || api.Status == 501 || (api.Status == 404 && api.ErrorCode == "")) {
		return fmt.Errorf("model API is unavailable on this server; registry and MLflow 3 Logged Models require compatible server support: %w", err)
	}
	return err
}
func (c *Client) SearchRegisteredModels(ctx context.Context, q models.Query) (models.RegisteredPage, error) {
	out := models.RegisteredPage{Models: []models.RegisteredModel{}}
	e := c.json(ctx, "GET", c.endpoint("registered-models/search", modelQuery(q)), nil, &out)
	return out, modelAPIError(e)
}
func (c *Client) SearchModelVersions(ctx context.Context, q models.Query) (models.VersionPage, error) {
	out := models.VersionPage{Versions: []models.ModelVersion{}}
	e := c.json(ctx, "GET", c.endpoint("model-versions/search", modelQuery(q)), nil, &out)
	return out, modelAPIError(e)
}
func (c *Client) GetModelVersion(ctx context.Context, name, version string) (models.ModelVersion, error) {
	var out struct {
		Model models.ModelVersion `json:"model_version"`
	}
	e := c.json(ctx, "GET", c.endpoint("model-versions/get", url.Values{"name": {name}, "version": {version}}), nil, &out)
	return out.Model, modelAPIError(e)
}
func (c *Client) GetModelVersionByAlias(ctx context.Context, name, alias string) (models.ModelVersion, error) {
	var out struct {
		Model models.ModelVersion `json:"model_version"`
	}
	e := c.json(ctx, "GET", c.endpoint("registered-models/alias", url.Values{"name": {name}, "alias": {alias}}), nil, &out)
	return out.Model, modelAPIError(e)
}
func (c *Client) GetModelVersionDownloadURI(ctx context.Context, name, version string) (string, error) {
	var out struct {
		URI string `json:"artifact_uri"`
	}
	e := c.json(ctx, "GET", c.endpoint("model-versions/get-download-uri", url.Values{"name": {name}, "version": {version}}), nil, &out)
	return out.URI, modelAPIError(e)
}
func (c *Client) GetLoggedModel(ctx context.Context, id string) (models.LoggedModel, error) {
	var out struct {
		Model models.LoggedModel `json:"model"`
	}
	e := c.json(ctx, "GET", c.endpoint("logged-models/"+url.PathEscape(id), nil), nil, &out)
	return out.Model, modelAPIError(e)
}
func storagePath(raw, p string) (string, error) {
	if e := models.SafePath(p); e != nil {
		return "", e
	}
	u, e := url.Parse(raw)
	if e != nil {
		return "", e
	}
	if u.Scheme == "runs" || u.Scheme == "models" {
		return "", errors.New("artifact storage must be resolved before downloading")
	}
	if p != "" {
		u.Path = strings.TrimRight(u.Path, "/") + "/" + p
		u.RawPath = ""
	}
	return u.String(), nil
}
func (c *Client) modelSource(l models.ArtifactLocation) artifactSource {
	return artifactSource{uri: l.URI, list: func(ctx context.Context, p string) (core.ArtifactPage, error) {
		l.Path = p
		return c.ListModelArtifacts(ctx, l)
	}, cliArgs: func(p, stage string) []string {
		uri, _ := storagePath(l.URI, p)
		return []string{"artifacts", "download", "--artifact-uri", uri, "--dst-path", stage}
	}}
}
func (c *Client) ListModelArtifacts(ctx context.Context, l models.ArtifactLocation) (core.ArtifactPage, error) {
	if e := models.SafePath(l.Path); e != nil {
		return core.ArtifactPage{}, e
	}
	if l.RunID != "" {
		return c.listArtifacts(ctx, core.Run{Info: core.RunInfo{RunID: l.RunID, ArtifactURI: l.URI}}, l.Path)
	}
	out := core.ArtifactPage{Files: []core.Artifact{}, RootURI: l.URI}
	uri, proxy, e := c.proxyURI(l.URI)
	if e != nil {
		return out, e
	}
	if !proxy {
		if c.opts.ArtifactCLI == nil {
			return out, errors.New("direct model artifacts need the official MLflow CLI; configure Python or install uv")
		}
		full, e := storagePath(l.URI, l.Path)
		if e != nil {
			return out, e
		}
		b, e := c.opts.ArtifactCLI(ctx, []string{"artifacts", "list", "--artifact-uri", full})
		if e != nil {
			return out, e
		}
		if e = decodeJSON(b, &out.Files); e != nil {
			return out, e
		}
		// CLI listing paths are relative to the supplied URI, unlike run/list.
		for i := range out.Files {
			if err := models.SafePath(out.Files[i].Path); err != nil {
				return out, err
			}
			out.Files[i].Path = path.Join(l.Path, out.Files[i].Path)
		}
	} else if l.LoggedModelID != "" {
		token := ""
		seen := map[string]bool{}
		for {
			q := url.Values{}
			if l.Path != "" {
				q.Set("artifact_directory_path", l.Path)
			}
			if token != "" {
				q.Set("page_token", token)
			}
			var page core.ArtifactPage
			e = c.json(ctx, "GET", c.endpoint("logged-models/"+url.PathEscape(l.LoggedModelID)+"/artifacts/directories", q), nil, &page)
			if e != nil {
				return out, modelAPIError(e)
			}
			out.Files = append(out.Files, page.Files...)
			if page.NextPageToken == "" {
				break
			}
			if seen[page.NextPageToken] {
				return out, errors.New("MLflow repeated a logged-model artifact page token")
			}
			seen[page.NextPageToken] = true
			token = page.NextPageToken
		}
	} else {
		// A registry's download URI may point at the artifact server without a run.
		u, e := url.Parse(uri)
		if e != nil {
			return out, e
		}
		const marker = "/api/2.0/mlflow-artifacts/artifacts"
		index := strings.Index(u.Path, marker)
		if index < 0 {
			return out, errors.New("HTTP model storage does not expose the MLflow artifact listing API")
		}
		root := strings.TrimPrefix(u.Path[index+len(marker):], "/")
		u.Path = u.Path[:index] + marker
		u.RawPath = ""
		q := u.Query()
		q.Set("path", path.Join(root, l.Path))
		u.RawQuery = q.Encode()
		var page core.ArtifactPage
		if e = c.json(ctx, "GET", u.String(), nil, &page); e != nil {
			return out, e
		}
		for _, f := range page.Files {
			if err := models.SafePath(f.Path); err != nil {
				return out, err
			}
			f.Path = path.Join(l.Path, f.Path)
			out.Files = append(out.Files, f)
		}
	}
	for _, f := range out.Files {
		if e := models.SafePath(f.Path); e != nil {
			return out, e
		}
		if f.Path == "" || path.Dir(f.Path) != parentDir(l.Path) {
			return out, errors.New("server returned an artifact outside the requested model directory")
		}
	}
	sort.SliceStable(out.Files, func(i, j int) bool {
		if out.Files[i].IsDir != out.Files[j].IsDir {
			return out.Files[i].IsDir
		}
		return out.Files[i].Path < out.Files[j].Path
	})
	return out, nil
}
func (c *Client) DownloadModelArtifacts(ctx context.Context, q models.ArtifactRequest, progress func(core.Progress)) (core.DownloadResult, error) {
	if _, e := storagePath(q.Location.URI, q.Location.Path); e != nil {
		return core.DownloadResult{}, e
	}
	return c.downloadSource(ctx, core.DownloadRequest{Path: q.Location.Path, Destination: q.Destination, Overwrite: q.Overwrite}, c.modelSource(q.Location), progress)
}
func (c *Client) ReadModelArtifact(ctx context.Context, l models.ArtifactLocation, limit int64) ([]byte, error) {
	if limit < 1 || limit > models.MetadataLimit {
		return nil, errors.New("invalid model metadata byte limit")
	}
	uri, proxy, e := c.proxyURI(l.URI)
	if e != nil {
		return nil, e
	}
	if proxy {
		full, e := storagePath(uri, l.Path)
		if e != nil {
			return nil, e
		}
		resp, e := c.request(ctx, "GET", full, nil)
		if e != nil {
			return nil, e
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if int64(len(b)) > limit {
			return nil, errors.New("model metadata exceeds its byte limit")
		}
		return b, e
	}
	parent := l
	parent.Path = path.Dir(l.Path)
	if parent.Path == "." {
		parent.Path = ""
	}
	page, e := c.ListModelArtifacts(ctx, parent)
	if e != nil {
		return nil, e
	}
	found := false
	for _, f := range page.Files {
		if f.Path == l.Path && !f.IsDir && f.FileSize >= 0 && f.FileSize <= limit {
			found = true
		}
	}
	if !found {
		return nil, errors.New("model metadata is missing or exceeds its byte limit")
	}
	dir, e := os.MkdirTemp("", "lazymlflow-model-metadata-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "metadata")
	if _, e = c.DownloadModelArtifacts(ctx, models.ArtifactRequest{Location: l, Destination: dest}, nil); e != nil {
		return nil, e
	}
	f, e := os.Open(dest)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("model metadata exceeds its byte limit")
	}
	return b, e
}

var _ models.Backend = (*Client)(nil)
