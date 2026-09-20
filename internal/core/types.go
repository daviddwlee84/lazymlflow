// Package core contains the contracts shared by the command line, terminal UI,
// and MLflow connection implementations.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
)

type Target struct {
	ID                   string   `toml:"id" json:"id"`
	Name                 string   `toml:"name,omitempty" json:"name,omitempty"`
	TrackingURI          string   `toml:"tracking_uri" json:"tracking_uri"`
	WebURL               string   `toml:"web_url,omitempty" json:"web_url,omitempty"`
	WorkingDir           string   `toml:"working_dir,omitempty" json:"working_dir,omitempty"`
	Python               string   `toml:"python,omitempty" json:"python,omitempty"`
	MLflowVersion        string   `toml:"mlflow_version,omitempty" json:"mlflow_version,omitempty"`
	ArtifactsDestination string   `toml:"artifacts_destination,omitempty" json:"artifacts_destination,omitempty"`
	TokenEnv             string   `toml:"token_env,omitempty" json:"token_env,omitempty"`
	UsernameEnv          string   `toml:"username_env,omitempty" json:"username_env,omitempty"`
	PasswordEnv          string   `toml:"password_env,omitempty" json:"password_env,omitempty"`
	CAFile               string   `toml:"ca_file,omitempty" json:"ca_file,omitempty"`
	ExtraPackages        []string `toml:"extra_packages,omitempty" json:"extra_packages,omitempty"`
	// Env maps child environment variable names to source environment names.
	Env       map[string]string `toml:"env,omitempty" json:"env,omitempty"`
	Transient bool              `toml:"-" json:"-"`
}

func (t Target) Label() string {
	if t.Name != "" {
		return t.Name
	}
	return t.ID
}

type Number float64

func (n Number) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) {
		return []byte(`"NaN"`), nil
	}
	if math.IsInf(f, 1) {
		return []byte(`"Infinity"`), nil
	}
	if math.IsInf(f, -1) {
		return []byte(`"-Infinity"`), nil
	}
	return json.Marshal(f)
}
func (n *Number) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("invalid metric value %q: %w", s, err)
	}
	*n = Number(f)
	return nil
}
func (n Number) String() string { return strconv.FormatFloat(float64(n), 'g', -1, 64) }

type KeyValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type Experiment struct {
	ID               string     `json:"experiment_id"`
	Name             string     `json:"name"`
	ArtifactLocation string     `json:"artifact_location"`
	LifecycleStage   string     `json:"lifecycle_stage"`
	CreationTime     int64      `json:"creation_time,omitempty"`
	LastUpdateTime   int64      `json:"last_update_time,omitempty"`
	Tags             []KeyValue `json:"tags,omitempty"`
}
type RunInfo struct {
	RunID          string `json:"run_id"`
	RunUUID        string `json:"run_uuid,omitempty"`
	ExperimentID   string `json:"experiment_id"`
	RunName        string `json:"run_name,omitempty"`
	Status         string `json:"status"`
	StartTime      int64  `json:"start_time"`
	EndTime        int64  `json:"end_time,omitempty"`
	ArtifactURI    string `json:"artifact_uri"`
	LifecycleStage string `json:"lifecycle_stage"`
	UserID         string `json:"user_id,omitempty"`
}
type Metric struct {
	Key           string `json:"key"`
	Value         Number `json:"value"`
	Timestamp     int64  `json:"timestamp"`
	Step          int64  `json:"step"`
	ModelID       string `json:"model_id,omitempty"`
	DatasetName   string `json:"dataset_name,omitempty"`
	DatasetDigest string `json:"dataset_digest,omitempty"`
}
type RunData struct {
	Metrics []Metric   `json:"metrics"`
	Params  []KeyValue `json:"params"`
	Tags    []KeyValue `json:"tags"`
}
type Run struct {
	Info RunInfo `json:"info"`
	Data RunData `json:"data"`
}

func (r Run) ID() string {
	if r.Info.RunID != "" {
		return r.Info.RunID
	}
	return r.Info.RunUUID
}
func (r Run) Name() string {
	if r.Info.RunName != "" {
		return r.Info.RunName
	}
	for _, v := range r.Data.Tags {
		if v.Key == "mlflow.runName" {
			return v.Value
		}
	}
	return r.ID()
}
func (r Run) Metric(key string) (Number, bool) {
	for _, v := range r.Data.Metrics {
		if v.Key == key {
			return v.Value, true
		}
	}
	return 0, false
}

type ExperimentQuery struct {
	Filter     string   `json:"filter,omitempty"`
	OrderBy    []string `json:"order_by,omitempty"`
	ViewType   string   `json:"view_type,omitempty"`
	MaxResults int      `json:"max_results,omitempty"`
	PageToken  string   `json:"page_token,omitempty"`
}
type RunQuery struct {
	ExperimentIDs []string `json:"experiment_ids"`
	Filter        string   `json:"filter,omitempty"`
	OrderBy       []string `json:"order_by,omitempty"`
	ViewType      string   `json:"run_view_type,omitempty"`
	MaxResults    int      `json:"max_results,omitempty"`
	PageToken     string   `json:"page_token,omitempty"`
}
type ExperimentPage struct {
	Experiments   []Experiment `json:"experiments"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}
type RunPage struct {
	Runs          []Run  `json:"runs"`
	NextPageToken string `json:"next_page_token,omitempty"`
}
type Artifact struct {
	Path     string `json:"path"`
	IsDir    bool   `json:"is_dir"`
	FileSize int64  `json:"file_size,omitempty"`
}
type ArtifactPage struct {
	Files         []Artifact `json:"files"`
	RootURI       string     `json:"root_uri,omitempty"`
	NextPageToken string     `json:"next_page_token,omitempty"`
}
type DownloadRequest struct {
	RunID       string `json:"run_id"`
	Path        string `json:"path"`
	Destination string `json:"destination"`
	Overwrite   bool   `json:"overwrite"`
}
type Progress struct {
	Path  string
	Bytes int64
	Total int64
}
type DownloadResult struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

type Backend interface {
	SearchExperiments(context.Context, ExperimentQuery) (ExperimentPage, error)
	GetExperiment(context.Context, string) (Experiment, error)
	SearchRuns(context.Context, RunQuery) (RunPage, error)
	GetRun(context.Context, string) (Run, error)
	MetricHistory(context.Context, string, string) ([]Metric, error)
	ListArtifacts(context.Context, string, string) (ArtifactPage, error)
	DownloadArtifact(context.Context, DownloadRequest, func(Progress)) (DownloadResult, error)
}

type Session struct {
	Backend        Backend
	Target         Target
	BaseURL        string
	WebURL         string
	RuntimeVersion string
	Local          bool
	CloseFunc      func() error
	closeOnce      sync.Once
	closeErr       error
}

func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		if s.CloseFunc != nil {
			s.closeErr = s.CloseFunc()
		}
	})
	return s.closeErr
}

type Connector interface {
	Open(context.Context, Target) (*Session, error)
	Close() error
}

// Compare preserves caller order and fails instead of silently omitting a run.
func Compare(ctx context.Context, b Backend, ids []string) ([]Run, error) {
	result := make([]Run, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		r, err := b.GetRun(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}
