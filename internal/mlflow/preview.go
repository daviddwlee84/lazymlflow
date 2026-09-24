package mlflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/fileuri"
)

var _ core.ArtifactPreviewer = (*Client)(nil)

type previewSource struct {
	info      core.PreviewInfo
	localRoot string
	url       string
	headers   http.Header
	signed    bool
}

func (c *Client) InspectArtifact(ctx context.Context, runID, artifact string) (core.PreviewInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	source, err := c.resolvePreview(ctx, runID, artifact)
	return source.info, err
}

func (c *Client) resolvePreview(ctx context.Context, runID, artifact string) (previewSource, error) {
	source := previewSource{info: core.PreviewInfo{RunID: runID, Path: artifact}}
	p, err := artifactPath(artifact)
	if err != nil {
		return source, err
	}
	source.info.Path = p
	if p == "" {
		source.info.IsDir = true
		return source, nil
	}
	run, err := c.GetRun(ctx, runID)
	if err != nil {
		return source, err
	}
	if run.ID() != runID {
		return source, errors.New("artifact lookup returned a different or missing run ID")
	}
	u, err := url.Parse(run.Info.ArtifactURI)
	if err != nil {
		return source, errors.New("invalid artifact storage URI")
	}
	if u.Scheme == "file" || u.Scheme == "" {
		if !c.opts.Local {
			return source, errors.New("remote local-path artifacts cannot be previewed from this machine; enable server artifact proxying")
		}
		if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
			return source, errors.New("artifact file URI must refer to this machine")
		}
		root := fileuri.Native(u.Path)
		if u.Scheme == "" {
			root = run.Info.ArtifactURI
		}
		if !filepath.IsAbs(root) {
			return source, errors.New("local artifact preview requires an absolute artifact root")
		}
		source.localRoot = root
		f, err := openPreviewLocal(root, p)
		if err != nil {
			return source, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return source, err
		}
		source.info.IsDir = info.IsDir()
		source.info.Supported = info.Mode().IsRegular()
		if source.info.Supported {
			size := info.Size()
			source.info.Size = &size
		} else {
			source.info.Reason = "preview requires a regular file"
		}
		return source, nil
	}
	base, proxy, err := c.proxyURI(run.Info.ArtifactURI)
	if err != nil {
		return source, err
	}
	if !proxy {
		source.info.Reason = "this direct storage backend has no bounded reader; use the explicit download action"
		return source, nil
	}
	parent := path.Dir(p)
	if parent == "." {
		parent = ""
	}
	// Listing is metadata-only. A prefix preview never calls an artifact
	// download subprocess or prepares an SDK for an unsupported direct store.
	listing, err := c.listArtifacts(ctx, run, parent)
	if err != nil {
		return source, err
	}
	found := false
	for _, entry := range listing.Files {
		if entry.Path != p {
			continue
		}
		found, source.info.IsDir = true, entry.IsDir
		if !entry.IsDir && entry.FileSize >= 0 && (entry.FileSizeKnown || entry.FileSize > 0) {
			size := entry.FileSize
			source.info.Size = &size
		}
		break
	}
	if !found {
		return source, fmt.Errorf("artifact %q not found", p)
	}
	if source.info.IsDir {
		return source, nil
	}
	source.url, err = storagePath(base, p)
	if err != nil {
		return source, err
	}
	source.info.Supported = true
	full, err := url.Parse(source.url)
	if err != nil {
		return source, errors.New("invalid artifact download location")
	}
	const artifactPrefix = "/api/2.0/mlflow-artifacts/artifacts/"
	if i := strings.Index(full.Path, artifactPrefix); i >= 0 {
		// MLflow 3.16+ can return direct storage URLs. Older servers and local
		// repositories legitimately lack this capability; never substitute an
		// SDK full download for this metadata probe.
		source.info.MayDownloadWhole = true
		probe := *full
		probe.Path = full.Path[:i] + "/api/2.0/mlflow-artifacts/presigned/" + full.Path[i+len(artifactPrefix):]
		probe.RawPath, probe.RawQuery, probe.Fragment = "", "", ""
		var signed struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Size    *int64            `json:"file_size"`
		}
		err := c.json(ctx, "GET", probe.String(), nil, &signed)
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 405 || apiErr.Status == 501) {
				return source, nil
			}
			return source, err
		}
		storage, err := url.Parse(signed.URL)
		if err != nil || storage.Host == "" || storage.User != nil || storage.Scheme != "http" && storage.Scheme != "https" {
			return source, errors.New("server returned an invalid presigned artifact URL")
		}
		if full.Scheme == "https" && storage.Scheme != "https" {
			return source, errors.New("refusing HTTPS downgrade for presigned artifact preview")
		}
		source.url, source.signed = storage.String(), true
		source.headers = http.Header{}
		for name, value := range signed.Headers {
			if strings.ContainsAny(name+value, "\r\n") {
				return source, errors.New("server returned invalid storage request headers")
			}
			switch strings.ToLower(name) {
			case "host", "range", "accept-encoding", "connection", "content-length", "transfer-encoding":
				continue
			}
			source.headers.Set(name, value)
		}
		if signed.Size != nil && *signed.Size >= 0 {
			source.info.Size = signed.Size
		}
		source.info.MayDownloadWhole = false
	}
	return source, nil
}

func openPreviewLocal(root, artifact string) (*os.File, error) {
	directory, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open artifact root: %w", err)
	}
	defer directory.Close()
	info, err := directory.Stat(filepath.FromSlash(artifact))
	if err != nil {
		return nil, fmt.Errorf("inspect artifact preview: %w", err)
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return nil, errors.New("artifact preview requires a regular file")
	}
	f, err := directory.Open(filepath.FromSlash(artifact))
	if err != nil {
		return nil, fmt.Errorf("open artifact preview: %w", err)
	}
	return f, nil
}

func (c *Client) ReadArtifactPreview(ctx context.Context, request core.PreviewRequest) (core.PreviewResult, error) {
	result := core.PreviewResult{}
	if request.MaxBytes == 0 {
		request.MaxBytes = core.DefaultPreviewBytes
	}
	if request.MaxBytes < 1 || request.MaxBytes > core.MaxPreviewBytes {
		return result, fmt.Errorf("preview max_bytes must be between 1 and %d", core.MaxPreviewBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	source, err := c.resolvePreview(ctx, request.RunID, request.Path)
	result.Info = source.info
	if err != nil {
		return result, err
	}
	if err := core.CheckPreviewApproval(source.info, request); err != nil {
		return result, err
	}
	if source.localRoot != "" {
		f, err := openPreviewLocal(source.localRoot, source.info.Path)
		if err != nil {
			return result, err
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			return result, err
		}
		size := stat.Size()
		result.Info.Size, result.Info.IsDir, result.Info.Supported = &size, stat.IsDir(), stat.Mode().IsRegular()
		if err := core.CheckPreviewApproval(result.Info, request); err != nil {
			return result, err
		}
		return readPreviewBody(ctx, f, result, request)
	}
	response, err := c.previewHTTP(ctx, source, request.MaxBytes)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	result.Info.ContentType = response.Header.Get("Content-Type")
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable && response.Header.Get("Content-Range") == "bytes */0" {
		zero := int64(0)
		result.Info.Size = &zero
		return result, nil
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent && response.StatusCode != http.StatusNoContent {
		return result, &APIError{Status: response.StatusCode, Message: "artifact preview request failed"}
	}
	if response.StatusCode == http.StatusPartialContent {
		total, err := previewRangeSize(response.Header.Get("Content-Range"))
		if err != nil {
			return result, err
		}
		result.Info.Size = total
		result.Truncated = total == nil || response.ContentLength >= 0 && *total > response.ContentLength
	} else if response.ContentLength >= 0 {
		size := response.ContentLength
		result.Info.Size = &size
	}
	// The listing can be stale. Inspect the actual representation's headers
	// before reading its body, and apply the same explicit approval policy.
	if err := core.CheckPreviewApproval(result.Info, request); err != nil {
		return result, err
	}
	return readPreviewBody(ctx, response.Body, result, request)
}

func previewRangeSize(header string) (*int64, error) {
	value := strings.TrimPrefix(header, "bytes ")
	rangePart, totalPart, ok := strings.Cut(value, "/")
	start, end, okRange := strings.Cut(rangePart, "-")
	last, err := strconv.ParseInt(end, 10, 64)
	if !strings.HasPrefix(header, "bytes ") || !ok || !okRange || start != "0" || err != nil || last < 0 {
		return nil, errors.New("artifact server returned an invalid prefix Content-Range")
	}
	if totalPart == "*" {
		return nil, nil
	}
	total, err := strconv.ParseInt(totalPart, 10, 64)
	if err != nil || total <= last {
		return nil, errors.New("artifact server returned an invalid total Content-Range")
	}
	return &total, nil
}

type contextPreviewReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextPreviewReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func readPreviewBody(ctx context.Context, reader io.Reader, result core.PreviewResult, request core.PreviewRequest) (core.PreviewResult, error) {
	data, err := io.ReadAll(io.LimitReader(contextPreviewReader{ctx, reader}, request.MaxBytes+1))
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.BytesRead = int64(len(data))
	result.Truncated = result.Truncated || result.BytesRead > request.MaxBytes || result.Info.Size != nil && *result.Info.Size > result.BytesRead
	if result.Info.Size != nil && *result.Info.Size < result.BytesRead {
		result.Info.Size = nil
	}
	if result.Truncated && !request.AllowLarge {
		return result, &core.PreviewApprovalError{Info: result.Info, Large: true, WholeFetch: result.Info.MayDownloadWhole && !request.AllowWholeFetch}
	}
	if result.Truncated {
		data = data[:min(int64(len(data)), request.MaxBytes)]
	} else {
		size := int64(len(data))
		result.Info.Size = &size
	}
	result.Data = data
	return result, nil
}

func (c *Client) previewHTTP(ctx context.Context, source previewSource, limit int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", source.url, nil)
	if err != nil {
		return nil, errors.New("invalid artifact preview request")
	}
	req.Header = source.headers.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "lazymlflow/0.1")
	req.Header.Set("Range", "bytes=0-"+strconv.FormatInt(limit, 10))
	if source.info.Size != nil && *source.info.Size == 0 {
		req.Header.Del("Range")
	}
	if !source.signed {
		origin, _ := url.Parse(c.base)
		if origin != nil && sameOrigin(origin, req.URL) {
			if c.opts.Username != "" {
				req.SetBasicAuth(c.opts.Username, c.opts.Password)
			} else if c.opts.Token != "" {
				req.Header.Set("Authorization", "Bearer "+c.opts.Token)
			}
		}
	}
	client := *c.http
	if source.signed {
		client.Jar = nil
	}
	priorRedirect := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if len(via) > 0 && !sameOrigin(next.URL, via[0].URL) {
			next.Header.Del("Authorization")
			next.Header.Del("Cookie")
			for name := range source.headers {
				next.Header.Del(name)
			}
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && next.URL.Scheme != "https" {
			return errors.New("refusing HTTPS downgrade redirect")
		}
		if priorRedirect != nil {
			return priorRedirect(next, via)
		}
		return nil
	}
	response, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		// URL errors contain presigned query strings. Keep only the underlying
		// transport error; never echo the storage URL or returned auth headers.
		var urlErr *url.Error
		failedURL := ""
		if errors.As(err, &urlErr) {
			failedURL = urlErr.URL
			err = urlErr.Err
		}
		message := c.redact(err.Error())
		message = strings.ReplaceAll(message, source.url, "[artifact URL]")
		if failedURL != "" {
			message = strings.ReplaceAll(message, failedURL, "[artifact URL]")
		}
		for _, values := range source.headers {
			for _, value := range values {
				if value != "" {
					message = strings.ReplaceAll(message, value, "[redacted]")
				}
			}
		}
		return nil, fmt.Errorf("artifact preview transfer failed: %s", message)
	}
	return response, nil
}
