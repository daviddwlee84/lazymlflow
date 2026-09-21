// Package models inspects MLflow model metadata and exports verifiable byte bundles.
// It never imports model code or deserializes model weights.
package models

import (
	"context"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

const ManifestSchema = "lazymlflow-model-bundle/v1"
const MetadataLimit int64 = 1 << 20

type RegisteredModel struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	CreationTimestamp    int64           `json:"creation_timestamp,omitempty"`
	LastUpdatedTimestamp int64           `json:"last_updated_timestamp,omitempty"`
	Tags                 []core.KeyValue `json:"tags,omitempty"`
	Aliases              []Alias         `json:"aliases,omitempty"`
	LatestVersions       []ModelVersion  `json:"latest_versions,omitempty"`
}
type Alias struct {
	Alias   string `json:"alias"`
	Version string `json:"version"`
}
type ModelVersion struct {
	Name                 string          `json:"name"`
	Version              string          `json:"version"`
	Source               string          `json:"source,omitempty"`
	RunID                string          `json:"run_id,omitempty"`
	ModelID              string          `json:"model_id,omitempty"`
	Status               string          `json:"status,omitempty"`
	StatusMessage        string          `json:"status_message,omitempty"`
	Description          string          `json:"description,omitempty"`
	Aliases              []string        `json:"aliases,omitempty"`
	Tags                 []core.KeyValue `json:"tags,omitempty"`
	CreationTimestamp    int64           `json:"creation_timestamp,omitempty"`
	LastUpdatedTimestamp int64           `json:"last_updated_timestamp,omitempty"`
}
type Registration struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type LoggedModel struct {
	Info LoggedModelInfo `json:"info"`
	Data LoggedModelData `json:"data"`
}
type LoggedModelInfo struct {
	ModelID              string          `json:"model_id"`
	ExperimentID         string          `json:"experiment_id,omitempty"`
	Name                 string          `json:"name,omitempty"`
	ArtifactURI          string          `json:"artifact_uri,omitempty"`
	SourceRunID          string          `json:"source_run_id,omitempty"`
	Status               string          `json:"status,omitempty"`
	StatusMessage        string          `json:"status_message,omitempty"`
	ModelType            string          `json:"model_type,omitempty"`
	CreationTimestamp    int64           `json:"creation_timestamp_ms,omitempty"`
	LastUpdatedTimestamp int64           `json:"last_updated_timestamp_ms,omitempty"`
	Tags                 []core.KeyValue `json:"tags,omitempty"`
	Registrations        []Registration  `json:"registrations,omitempty"`
}
type LoggedModelData struct {
	Params  []core.KeyValue `json:"params,omitempty"`
	Metrics []core.Metric   `json:"metrics,omitempty"`
}
type Query struct {
	Filter     string   `json:"filter,omitempty"`
	MaxResults int      `json:"max_results,omitempty"`
	PageToken  string   `json:"page_token,omitempty"`
	OrderBy    []string `json:"order_by,omitempty"`
}
type RegisteredPage struct {
	Models        []RegisteredModel `json:"registered_models"`
	NextPageToken string            `json:"next_page_token,omitempty"`
}
type VersionPage struct {
	Versions      []ModelVersion `json:"model_versions"`
	NextPageToken string         `json:"next_page_token,omitempty"`
}

// ArtifactLocation is a resolved storage root, never an unresolved models:/ alias.
// Its routing fields are internal and excluded from exported receipts.
type ArtifactLocation struct {
	URI           string `json:"-"`
	RunID         string `json:"-"`
	LoggedModelID string `json:"-"`
	Path          string `json:"-"`
}
type ArtifactRequest struct {
	Location    ArtifactLocation
	Destination string
	Overwrite   bool
}
type Backend interface {
	SearchRegisteredModels(context.Context, Query) (RegisteredPage, error)
	SearchModelVersions(context.Context, Query) (VersionPage, error)
	GetModelVersion(context.Context, string, string) (ModelVersion, error)
	GetModelVersionByAlias(context.Context, string, string) (ModelVersion, error)
	GetModelVersionDownloadURI(context.Context, string, string) (string, error)
	GetLoggedModel(context.Context, string) (LoggedModel, error)
	ListModelArtifacts(context.Context, ArtifactLocation) (core.ArtifactPage, error)
	DownloadModelArtifacts(context.Context, ArtifactRequest, func(core.Progress)) (core.DownloadResult, error)
	ReadModelArtifact(context.Context, ArtifactLocation, int64) ([]byte, error)
}
type Resolution struct {
	SourceKey      string           `json:"source_key,omitempty"`
	RequestedURI   string           `json:"requested_uri"`
	ResolvedURI    string           `json:"resolved_uri"`
	Kind           string           `json:"kind"`
	ResolvedAt     time.Time        `json:"resolved_at"`
	RunID          string           `json:"run_id,omitempty"`
	ArtifactPath   string           `json:"artifact_path,omitempty"`
	RegisteredName string           `json:"registered_name,omitempty"`
	Version        string           `json:"version,omitempty"`
	Alias          string           `json:"alias,omitempty"`
	LoggedModelID  string           `json:"logged_model_id,omitempty"`
	Status         string           `json:"status,omitempty"`
	StorageURI     string           `json:"storage_uri,omitempty"`
	Location       ArtifactLocation `json:"-"`
	Registered     *ModelVersion    `json:"registered,omitempty"`
	Logged         *LoggedModel     `json:"logged,omitempty"`
}
type ArtifactReference struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}
type EnvironmentFile struct {
	Path         string   `json:"path"`
	Status       string   `json:"status"`
	Dependencies []string `json:"dependencies,omitempty"`
}
type Metadata struct {
	References        []ArtifactReference `json:"artifact_references,omitempty"`
	Status            string              `json:"status"`
	RuntimeValidation string              `json:"runtime_validation"`
	Environments      []EnvironmentFile   `json:"environments,omitempty"`
	Annotations       []string            `json:"annotations,omitempty"`
	Flavors           []string            `json:"flavors"`
	Signature         any                 `json:"signature,omitempty"`
	EnvironmentFiles  []string            `json:"environment_files,omitempty"`
	Serving           string              `json:"serving"`
	// Metadata is descriptive. Candidate means python_function is declared, not tested.
}
type Inspection struct {
	Resolution Resolution      `json:"resolution"`
	Files      []core.Artifact `json:"files"`
	Metadata   Metadata        `json:"metadata"`
	Warnings   []string        `json:"warnings"`
}
type RelatedModel struct {
	Role   string       `json:"role"`
	Step   *int64       `json:"step,omitempty"`
	Source string       `json:"source"`
	Model  *LoggedModel `json:"model,omitempty"`
	Error  string       `json:"error,omitempty"`
}
type RelatedResult struct {
	RunID    string         `json:"run_id"`
	Models   []RelatedModel `json:"models"`
	Complete bool           `json:"complete"`
}
type ExportOptions struct {
	Source      string
	Destination string
	Overwrite   bool
}
type FileDigest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Schema     string       `json:"schema"`
	Source     Resolution   `json:"source"`
	ExportedAt time.Time    `json:"exported_at"`
	Files      []FileDigest `json:"files"`
}
type ExportResult struct {
	Path     string     `json:"path"`
	Manifest string     `json:"manifest"`
	Files    int        `json:"files"`
	Bytes    int64      `json:"bytes"`
	Source   Resolution `json:"source"`
}
type VerifyResult struct {
	Valid       bool     `json:"valid"`
	Files       int      `json:"files"`
	Bytes       int64    `json:"bytes"`
	Differences []string `json:"differences"`
}
