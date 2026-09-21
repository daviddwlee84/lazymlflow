// Package mlflow implements the public tracking REST API. Storage formats and
// database schemas remain the responsibility of the official MLflow server.
package mlflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

const api = "/api/2.0/mlflow/"
const maxJSON = 128 << 20

type Options struct {
	HTTPClient                *http.Client
	Username, Password, Token string
	Local                     bool
	ArtifactCLI               func(context.Context, []string) ([]byte, error)
	ArtifactDestination       string
	OriginalTrackingURI       string
}
type Client struct {
	base string
	http *http.Client
	opts Options
}

func New(base string, opts Options) *Client {
	h := opts.HTTPClient
	if h == nil {
		h = &http.Client{}
	}
	clone := *h
	priorRedirect := clone.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("refusing HTTPS downgrade redirect")
		}
		if priorRedirect != nil {
			return priorRedirect(req, via)
		}
		return nil
	}
	return &Client{base: strings.TrimRight(base, "/"), http: &clone, opts: opts}
}

type APIError struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
	Status    int    `json:"status"`
}

func (e *APIError) Error() string {
	if e.ErrorCode != "" {
		return fmt.Sprintf("MLflow %s (HTTP %d): %s", e.ErrorCode, e.Status, e.Message)
	}
	return fmt.Sprintf("MLflow HTTP %d: %s", e.Status, e.Message)
}
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Host, b.Host) && strings.EqualFold(a.Scheme, b.Scheme)
}
func (c *Client) request(ctx context.Context, method, endpoint string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "lazymlflow/0.1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	origin, _ := url.Parse(c.base)
	if origin != nil && sameOrigin(origin, req.URL) {
		if c.opts.Username != "" {
			req.SetBasicAuth(c.opts.Username, c.opts.Password)
		} else if c.opts.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.opts.Token)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.redactError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		e := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(b, e)
		e.Status = resp.StatusCode
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
			if e.Message == "" {
				e.Message = http.StatusText(resp.StatusCode)
			}
		}
		e.Message = c.redact(e.Message)
		return nil, e
	}
	return resp, nil
}
func (c *Client) redact(s string) string {
	for _, v := range []string{c.opts.Token, c.opts.Password} {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}
func (c *Client) redactError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New(c.redact(err.Error()))
}
func (c *Client) json(ctx context.Context, method, endpoint string, payload, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.request(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil {
		return err
	}
	if len(b) > maxJSON {
		return errors.New("MLflow JSON response exceeds 128 MiB; narrow the query")
	}
	if err = decodeJSON(b, out); err != nil {
		return fmt.Errorf("invalid MLflow JSON response: %w", err)
	}
	return nil
}

// Protobuf JSON from different MLflow versions encodes int64s either as strings
// or numbers. Normalize only integer fields, never IDs, params or tag values.
func decodeJSON(b []byte, out any) error {
	var tree any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&tree); err != nil {
		return err
	}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, item := range x {
				switch k {
				case "timestamp", "step", "start_time", "end_time", "creation_time", "last_update_time", "file_size", "creation_timestamp", "last_updated_timestamp", "creation_timestamp_ms", "last_updated_timestamp_ms":
					if s, ok := item.(string); ok {
						if _, err := strconv.ParseInt(s, 10, 64); err == nil {
							x[k] = json.Number(s)
						}
					}
				}
				visit(item)
			}
		case []any:
			for _, item := range x {
				visit(item)
			}
		}
	}
	visit(tree)
	normalized, err := json.Marshal(tree)
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, out)
}
func (c *Client) endpoint(resource string, q url.Values) string {
	u := c.base + api + resource
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}
func (c *Client) SearchExperiments(ctx context.Context, q core.ExperimentQuery) (core.ExperimentPage, error) {
	if q.MaxResults <= 0 {
		q.MaxResults = 100
	}
	if q.ViewType == "" {
		q.ViewType = "ACTIVE_ONLY"
	}
	if len(q.OrderBy) == 0 {
		q.OrderBy = []string{"name ASC"}
	}
	var page core.ExperimentPage
	err := c.json(ctx, "POST", c.endpoint("experiments/search", nil), q, &page)
	if page.Experiments == nil {
		page.Experiments = []core.Experiment{}
	}
	return page, err
}
func (c *Client) GetExperiment(ctx context.Context, id string) (core.Experiment, error) {
	var out struct {
		Experiment core.Experiment `json:"experiment"`
	}
	if id == "" {
		return out.Experiment, errors.New("experiment ID is required")
	}
	err := c.json(ctx, "GET", c.endpoint("experiments/get", url.Values{"experiment_id": {id}}), nil, &out)
	return out.Experiment, err
}
func (c *Client) SearchRuns(ctx context.Context, q core.RunQuery) (core.RunPage, error) {
	if len(q.ExperimentIDs) == 0 {
		return core.RunPage{}, errors.New("at least one experiment ID is required")
	}
	if q.MaxResults <= 0 {
		q.MaxResults = 100
	}
	if q.ViewType == "" {
		q.ViewType = "ACTIVE_ONLY"
	}
	if len(q.OrderBy) == 0 {
		q.OrderBy = []string{"attributes.start_time DESC", "attributes.run_id ASC"}
	}
	var page core.RunPage
	err := c.json(ctx, "POST", c.endpoint("runs/search", nil), q, &page)
	if page.Runs == nil {
		page.Runs = []core.Run{}
	}
	return page, err
}
func (c *Client) GetRun(ctx context.Context, id string) (core.Run, error) {
	var out struct {
		Run core.Run `json:"run"`
	}
	if id == "" {
		return out.Run, errors.New("run ID is required")
	}
	err := c.json(ctx, "GET", c.endpoint("runs/get", url.Values{"run_id": {id}}), nil, &out)
	return out.Run, err
}
func (c *Client) MetricHistory(ctx context.Context, id, key string) ([]core.Metric, error) {
	if id == "" || key == "" {
		return nil, errors.New("run ID and metric key are required")
	}
	result := []core.Metric{}
	token := ""
	seen := map[string]bool{}
	for {
		q := url.Values{"run_id": {id}, "metric_key": {key}, "max_results": {"10000"}}
		if token != "" {
			q.Set("page_token", token)
		}
		var page struct {
			Metrics []core.Metric `json:"metrics"`
			Next    string        `json:"next_page_token"`
		}
		if err := c.json(ctx, "GET", c.endpoint("metrics/get-history", q), nil, &page); err != nil {
			return nil, err
		}
		result = append(result, page.Metrics...)
		if page.Next == "" {
			return result, nil
		}
		if seen[page.Next] {
			return nil, errors.New("MLflow repeated a metric-history page token")
		}
		seen[page.Next] = true
		token = page.Next
	}
}
func (c *Client) ServerVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := c.request(ctx, "GET", c.base+"/version", nil)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 256))
	return strings.Trim(strings.TrimSpace(string(b)), "\""), err
}

var _ core.Backend = (*Client)(nil)
