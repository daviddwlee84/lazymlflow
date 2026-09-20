package mlflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func artifactPath(value string) (string, error) {
	value = strings.TrimSuffix(value, "/")
	if value == "" || value == "." {
		return "", nil
	}
	if strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("unsafe artifact path %q: use a path relative to the run", value)
	}
	return value, nil
}
func (c *Client) proxyURI(uri string) (string, bool, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", false, err
	}
	switch u.Scheme {
	case "mlflow-artifacts":
		if c.opts.Local && c.opts.ArtifactDestination == "" {
			return "", false, errors.New("this store records mlflow-artifacts URIs; set the target's artifacts_destination to the original server artifact location")
		}
		base, err := url.Parse(c.base)
		if err != nil {
			return "", false, err
		}
		if u.Host != "" {
			base.Host = u.Host
		}
		base.Path = path.Join(base.Path, "api/2.0/mlflow-artifacts/artifacts", u.Path)
		base.RawPath = ""
		base.RawQuery = ""
		base.Fragment = ""
		return strings.TrimRight(base.String(), "/"), true, nil
	case "http", "https":
		return strings.TrimRight(uri, "/"), true, nil
	case "file", "":
		if !c.opts.Local {
			return "", false, errors.New("the remote server returned a local artifact path; configure server artifact proxying or use a local target with access to that store")
		}
		return "", false, nil
	default:
		return "", false, nil
	}
}

func (c *Client) ListArtifacts(ctx context.Context, runID, p string) (core.ArtifactPage, error) {
	p, err := artifactPath(p)
	if err != nil {
		return core.ArtifactPage{}, err
	}
	r, err := c.GetRun(ctx, runID)
	if err != nil {
		return core.ArtifactPage{}, err
	}
	return c.listArtifacts(ctx, r, p)
}
func (c *Client) listArtifacts(ctx context.Context, r core.Run, p string) (core.ArtifactPage, error) {
	_, proxy, err := c.proxyURI(r.Info.ArtifactURI)
	if err != nil {
		return core.ArtifactPage{}, err
	}
	out := core.ArtifactPage{Files: []core.Artifact{}, RootURI: r.Info.ArtifactURI}
	if !proxy {
		if c.opts.ArtifactCLI == nil {
			return out, errors.New("direct artifact access needs MLflow CLI; configure a Python environment or install uv")
		}
		args := []string{"artifacts", "list", "--run-id", r.ID()}
		if p != "" {
			args = append(args, "--artifact-path", p)
		}
		b, err := c.opts.ArtifactCLI(ctx, args)
		if err != nil {
			return out, err
		}
		if err = decodeJSON(b, &out.Files); err != nil {
			return out, fmt.Errorf("invalid MLflow artifact list JSON: %w", err)
		}
	} else {
		token := ""
		seen := map[string]bool{}
		for {
			q := url.Values{"run_id": {r.ID()}}
			if p != "" {
				q.Set("path", p)
			}
			if token != "" {
				q.Set("page_token", token)
			}
			var page core.ArtifactPage
			if err := c.json(ctx, "GET", c.endpoint("artifacts/list", q), nil, &page); err != nil {
				return out, err
			}
			out.Files = append(out.Files, page.Files...)
			if page.NextPageToken == "" {
				break
			}
			if seen[page.NextPageToken] {
				return out, errors.New("MLflow repeated an artifact page token")
			}
			seen[page.NextPageToken] = true
			token = page.NextPageToken
		}
	}
	for _, f := range out.Files {
		clean, err := artifactPath(f.Path)
		if err != nil {
			return out, err
		}
		if clean == "" || path.Dir(clean) != parentDir(p) {
			return out, fmt.Errorf("server returned an artifact outside the requested directory: %q", f.Path)
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
func parentDir(p string) string {
	if p == "" {
		return "."
	}
	return p
}

// DownloadArtifact treats Destination as the exact final file/directory path.
// Downloads stage next to it, so a failed/canceled transfer cannot masquerade as
// a completed output and replacement stays on the same filesystem.
func (c *Client) DownloadArtifact(ctx context.Context, q core.DownloadRequest, progress func(core.Progress)) (core.DownloadResult, error) {
	var result core.DownloadResult
	p, err := artifactPath(q.Path)
	if err != nil {
		return result, err
	}
	if q.Destination == "" {
		return result, errors.New("download destination is required")
	}
	dest, err := filepath.Abs(q.Destination)
	if err != nil {
		return result, err
	}
	cwd, _ := os.Getwd()
	if dest == filepath.Dir(dest) || dest == cwd {
		return result, errors.New("destination must name an output file or directory, not the current directory or filesystem root")
	}
	if _, err = os.Lstat(dest); err == nil && !q.Overwrite {
		return result, fmt.Errorf("destination exists: %s (use overwrite explicitly)", dest)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	r, err := c.GetRun(ctx, q.RunID)
	if err != nil {
		return result, err
	}
	uri, proxy, err := c.proxyURI(r.Info.ArtifactURI)
	if err != nil {
		return result, err
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return result, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dest), ".lazymlflow-download-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	source := filepath.Join(stage, "payload")
	if proxy {
		isDir := p == ""
		if p != "" {
			parent := path.Dir(p)
			if parent == "." {
				parent = ""
			}
			listing, e := c.listArtifacts(ctx, r, parent)
			if e != nil {
				return result, e
			}
			found := false
			for _, f := range listing.Files {
				if f.Path == p {
					isDir = f.IsDir
					found = true
					break
				}
			}
			if !found {
				return result, fmt.Errorf("artifact %q not found", p)
			}
		}
		if isDir {
			err = c.downloadDirectory(ctx, r, uri, p, source, progress, &result)
		} else {
			err = c.downloadFile(ctx, uri, p, source, progress, &result)
		}
		if err != nil {
			return result, err
		}
	} else {
		if c.opts.ArtifactCLI == nil {
			return result, errors.New("direct artifact access needs MLflow CLI; configure Python or install uv")
		}
		args := []string{"artifacts", "download", "--run-id", q.RunID, "--dst-path", stage}
		if p != "" {
			args = append(args, "--artifact-path", p)
		}
		if progress != nil {
			progress(core.Progress{Path: p, Total: -1})
		}
		b, err := c.opts.ArtifactCLI(ctx, args)
		if err != nil {
			return result, err
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		source = strings.TrimSpace(lines[len(lines)-1])
		if source == "" {
			return result, errors.New("MLflow returned no artifact download path")
		}
		if !filepath.IsAbs(source) {
			source = filepath.Join(stage, source)
		}
		resolved, err := filepath.EvalSymlinks(source)
		if err != nil {
			return result, err
		}
		resolvedStage, err := filepath.EvalSymlinks(stage)
		if err != nil {
			return result, err
		}
		rel, err := filepath.Rel(resolvedStage, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return result, errors.New("MLflow returned a download path outside the staging directory")
		}
		source = resolved
		err = filepath.WalkDir(source, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return errors.New("download contains a symlink; refusing to publish it")
			}
			if !d.IsDir() {
				i, err := d.Info()
				if err != nil {
					return err
				}
				if !i.Mode().IsRegular() {
					return fmt.Errorf("download contains a non-regular file: %s", p)
				}
				result.Files++
				result.Bytes += i.Size()
			}
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = publishDownload(source, dest, q.Overwrite); err != nil {
		return result, err
	}
	result.Path = dest
	if progress != nil {
		progress(core.Progress{Path: p, Bytes: result.Bytes, Total: result.Bytes})
	}
	return result, nil
}
func (c *Client) downloadDirectory(ctx context.Context, r core.Run, uri, p, dest string, progress func(core.Progress), result *core.DownloadResult) error {
	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}
	list, err := c.listArtifacts(ctx, r, p)
	if err != nil {
		return err
	}
	for _, a := range list.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := filepath.Join(dest, path.Base(a.Path))
		if a.IsDir {
			err = c.downloadDirectory(ctx, r, uri, a.Path, child, progress, result)
		} else {
			err = c.downloadFile(ctx, uri, a.Path, child, progress, result)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func (c *Client) downloadFile(ctx context.Context, uri, p, dest string, progress func(core.Progress), result *core.DownloadResult) error {
	u, err := url.Parse(uri)
	if err != nil {
		return err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + p
	u.RawPath = ""
	resp, err := c.request(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	buf := make([]byte, 128*1024)
	var n int64
	for {
		read, e := resp.Body.Read(buf)
		if read > 0 {
			written, we := f.Write(buf[:read])
			n += int64(written)
			if we != nil {
				_ = f.Close()
				return we
			}
			if progress != nil {
				progress(core.Progress{Path: p, Bytes: n, Total: resp.ContentLength})
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			_ = f.Close()
			return e
		}
	}
	if err = f.Close(); err != nil {
		return err
	}
	result.Files++
	result.Bytes += n
	return nil
}
func publishDownload(source, dest string, overwrite bool) error {
	_, err := os.Lstat(dest)
	if errors.Is(err, os.ErrNotExist) {
		return os.Rename(source, dest)
	}
	if err != nil {
		return err
	}
	if !overwrite {
		return fmt.Errorf("destination appeared during download: %s", dest)
	}
	backup, err := os.MkdirTemp(filepath.Dir(dest), ".lazymlflow-backup-")
	if err != nil {
		return err
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			_ = os.RemoveAll(backup)
		}
	}()
	old := filepath.Join(backup, "original")
	if err = os.Rename(dest, old); err != nil {
		return err
	}
	if err = os.Rename(source, dest); err != nil {
		if rollbackErr := os.Rename(old, dest); rollbackErr != nil {
			keepBackup = true
			return fmt.Errorf("publish failed: %v; restore failed: %v; original at %s", err, rollbackErr, old)
		}
		return err
	}
	return nil
}
