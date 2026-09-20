// Package inspection collects reviewable, versioned MLflow evidence. Collectors
// perform reads only; rendering never fetches data or runs an agent.
package inspection

import (
	"fmt"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

const ContextVersion = 1

type HistoryMode string

const (
	HistoryNone    HistoryMode = "none"
	HistorySampled HistoryMode = "sampled"
	HistoryFull    HistoryMode = "full"
)

type Options struct {
	Metrics       []string    `json:"metrics,omitempty"`
	History       HistoryMode `json:"history"`
	SampleLimit   int         `json:"sample_limit"`
	IncludeSystem bool        `json:"include_system"`
}

func DefaultOptions() Options { return Options{History: HistorySampled, SampleLimit: 200} }
func (o Options) normalized() (Options, error) {
	if o.History == "" {
		o.History = HistorySampled
	}
	if o.History != HistoryNone && o.History != HistorySampled && o.History != HistoryFull {
		return o, fmt.Errorf("history must be none, sampled, or full")
	}
	if o.SampleLimit == 0 {
		o.SampleLimit = 200
	}
	if o.SampleLimit < 1 {
		return o, fmt.Errorf("sample limit must be positive")
	}
	o.Metrics = unique(o.Metrics)
	for _, key := range o.Metrics {
		if key == "" {
			return o, fmt.Errorf("metric names cannot be empty")
		}
	}
	return o, nil
}

type ExperimentOptions struct {
	Options
	Query       core.RunQuery
	DetailLimit int
	AllDetails  bool
}

type Source struct {
	Key         string `json:"key"`
	TargetID    string `json:"target_id"`
	Name        string `json:"name"`
	TrackingURI string `json:"tracking_uri"`
	SSHHost     string `json:"ssh_host,omitempty"`
}

type Warning struct {
	Subject string `json:"subject"`
	Section string `json:"section"`
	Message string `json:"message"`
}

type Snapshot struct {
	Version     int                `json:"version"`
	Kind        string             `json:"kind"`
	CollectedAt time.Time          `json:"collected_at"`
	Source      Source             `json:"source"`
	Options     Options            `json:"options"`
	Selection   Selection          `json:"selection"`
	Experiment  *ExperimentContext `json:"experiment,omitempty"`
	Runs        []RunContext       `json:"runs"`
	Warnings    []Warning          `json:"warnings"`
}

type Selection struct {
	Strategy        string   `json:"strategy"`
	RequestedRunIDs []string `json:"requested_run_ids,omitempty"`
	DetailedRunIDs  []string `json:"detailed_run_ids"`
	MatchingRuns    int      `json:"matching_runs"`
	DetailLimit     int      `json:"detail_limit,omitempty"`
	AllDetails      bool     `json:"all_details"`
}

type ExperimentContext struct {
	Overview         ExperimentOverview `json:"overview"`
	Experiment       core.Experiment    `json:"experiment"`
	Query            core.RunQuery      `json:"query"`
	MetadataComplete bool               `json:"metadata_complete"`
	// MatchingRuns contains every metadata row returned by the complete search,
	// independently of which rows were selected for detailed histories/artifacts.
	MatchingRuns []core.Run   `json:"matching_runs"`
	Notes        NotesContext `json:"notes"`
}

type RunContext struct {
	Run              core.Run         `json:"run"`
	MetadataComplete bool             `json:"metadata_complete"`
	SelectedMetrics  []string         `json:"selected_metrics"`
	Histories        []HistoryContext `json:"histories"`
	Artifacts        ArtifactsContext `json:"artifacts"`
	Notes            NotesContext     `json:"notes"`
	DatasetNotes     []NotesContext   `json:"dataset_notes"`
}

type HistoryContext struct {
	Observations *HistoryObservations `json:"observations,omitempty"`
	Key          string               `json:"key"`
	Status       string               `json:"status"` // complete, omitted, error
	Error        string               `json:"error,omitempty"`
	Summary      core.MetricSummary   `json:"summary"`
	Samples      []core.Metric        `json:"samples"`
	Sampled      bool                 `json:"sampled"`
}

type ArtifactsContext struct {
	Status  string          `json:"status"`
	Scope   string          `json:"scope"`
	RootURI string          `json:"root_uri,omitempty"`
	Files   []core.Artifact `json:"files"`
	Error   string          `json:"error,omitempty"`
}

type NotesContext struct {
	Subject core.Subject `json:"subject"`
	Status  string       `json:"status"`
	Notes   []core.Note  `json:"notes"`
	Error   string       `json:"error,omitempty"`
}
