package inspection

import (
	"math"
	"sort"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

// ExperimentOverview counts the complete search population, independently of
// the bounded set of runs whose histories and artifacts were inspected.
type ExperimentOverview struct {
	RunCount   int                 `json:"run_count"`
	Statuses   []StatusCount       `json:"statuses"`
	Metrics    []MetricCoverage    `json:"metrics"`
	Parameters []ParameterCoverage `json:"parameters"`
	Datasets   []DatasetCoverage   `json:"datasets"`
}
type StatusCount struct {
	Status string `json:"status"`
	Runs   int    `json:"runs"`
}
type MetricCoverage struct {
	Key           string             `json:"key"`
	LoggedRuns    int                `json:"logged_runs"`
	MissingRuns   int                `json:"missing_runs"`
	FiniteRuns    int                `json:"finite_runs"`
	NonFiniteRuns int                `json:"non_finite_runs"`
	LatestMin     *MetricObservation `json:"latest_min,omitempty"`
	LatestMax     *MetricObservation `json:"latest_max,omitempty"`
}
type MetricObservation struct {
	RunID     string      `json:"run_id"`
	Value     core.Number `json:"value"`
	Step      int64       `json:"step"`
	Timestamp int64       `json:"timestamp"`
}
type ParameterCoverage struct {
	Key            string   `json:"key"`
	LoggedRuns     int      `json:"logged_runs"`
	MissingRuns    int      `json:"missing_runs"`
	DistinctValues int      `json:"distinct_values"`
	Examples       []string `json:"examples"` // First three distinct values in lexical order.
}
type DatasetCoverage struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Digest     string   `json:"digest"`
	SourceType string   `json:"source_type"`
	Runs       int      `json:"runs"`
	Contexts   []string `json:"contexts"`
}

type NumericDelta struct {
	Value       *core.Number `json:"value,omitempty"`
	Unavailable string       `json:"unavailable,omitempty"`
}
type HistoryObservations struct {
	FirstToLast      NumericDelta `json:"first_to_last"`
	LastMinusMinimum NumericDelta `json:"last_minus_minimum"`
	LastMinusMaximum NumericDelta `json:"last_minus_maximum"`
}

func historyObservations(summary core.MetricSummary) *HistoryObservations {
	return &HistoryObservations{FirstToLast: metricDelta(summary.First, summary.Last), LastMinusMinimum: metricDelta(summary.Min, summary.Last), LastMinusMaximum: metricDelta(summary.Max, summary.Last)}
}
func metricDelta(from, to *core.Metric) NumericDelta {
	if from == nil || to == nil {
		return NumericDelta{Unavailable: "no finite history samples"}
	}
	value := float64(to.Value) - float64(from.Value)
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return NumericDelta{Unavailable: "difference exceeds finite floating-point range"}
	}
	if value == 0 {
		value = 0
	} // Normalize negative zero without treating missing data as zero.
	result := core.Number(value)
	return NumericDelta{Value: &result}
}

func summarizeExperimentMetadata(source string, runs []core.Run) ExperimentOverview {
	overview := ExperimentOverview{RunCount: len(runs), Statuses: []StatusCount{}, Metrics: []MetricCoverage{}, Parameters: []ParameterCoverage{}, Datasets: []DatasetCoverage{}}
	statuses := map[string]int{}
	metrics := map[string]*MetricCoverage{}
	parameters := map[string]*ParameterCoverage{}
	values := map[string]map[string]bool{}
	datasets := map[string]*DatasetCoverage{}
	datasetRuns := map[string]map[string]bool{}
	contexts := map[string]map[string]bool{}
	for _, run := range runs {
		statuses[run.Info.Status]++
		seenMetrics := map[string]bool{}
		for _, metric := range run.Data.Metrics {
			// Match Run.Metric's scalar-key semantics if a backend repeats a key.
			// The raw metadata still retains every record and its dataset/model fields.
			if seenMetrics[metric.Key] {
				continue
			}
			seenMetrics[metric.Key] = true
			entry := metrics[metric.Key]
			if entry == nil {
				entry = &MetricCoverage{Key: metric.Key}
				metrics[metric.Key] = entry
			}
			entry.LoggedRuns++
			value := float64(metric.Value)
			if math.IsNaN(value) || math.IsInf(value, 0) {
				entry.NonFiniteRuns++
				continue
			}
			entry.FiniteRuns++
			point := MetricObservation{RunID: run.ID(), Value: metric.Value, Step: metric.Step, Timestamp: metric.Timestamp}
			if entry.LatestMin == nil || metric.Value < entry.LatestMin.Value || (metric.Value == entry.LatestMin.Value && run.ID() < entry.LatestMin.RunID) {
				copy := point
				entry.LatestMin = &copy
			}
			if entry.LatestMax == nil || metric.Value > entry.LatestMax.Value || (metric.Value == entry.LatestMax.Value && run.ID() < entry.LatestMax.RunID) {
				copy := point
				entry.LatestMax = &copy
			}
		}
		seenParameters := map[string]bool{}
		for _, param := range run.Data.Params {
			if seenParameters[param.Key] {
				continue
			}
			seenParameters[param.Key] = true
			entry := parameters[param.Key]
			if entry == nil {
				entry = &ParameterCoverage{Key: param.Key}
				parameters[param.Key] = entry
				values[param.Key] = map[string]bool{}
			}
			entry.LoggedRuns++
			values[param.Key][param.Value] = true
		}
		for _, input := range run.Inputs.DatasetInputs {
			dataset := input.Dataset
			id := core.DatasetIdentity(source, dataset)
			entry := datasets[id]
			if entry == nil {
				entry = &DatasetCoverage{ID: id, Name: dataset.Name, Digest: dataset.Digest, SourceType: dataset.SourceType}
				datasets[id] = entry
				datasetRuns[id] = map[string]bool{}
				contexts[id] = map[string]bool{}
			}
			datasetRuns[id][run.ID()] = true
			contexts[id][core.DatasetContext(input)] = true
		}
	}
	for status, count := range statuses {
		overview.Statuses = append(overview.Statuses, StatusCount{Status: status, Runs: count})
	}
	sort.Slice(overview.Statuses, func(i, j int) bool { return overview.Statuses[i].Status < overview.Statuses[j].Status })
	for _, entry := range metrics {
		entry.MissingRuns = len(runs) - entry.LoggedRuns
		overview.Metrics = append(overview.Metrics, *entry)
	}
	sort.Slice(overview.Metrics, func(i, j int) bool { return overview.Metrics[i].Key < overview.Metrics[j].Key })
	for key, entry := range parameters {
		entry.MissingRuns = len(runs) - entry.LoggedRuns
		entry.DistinctValues = len(values[key])
		entry.Examples = make([]string, 0, len(values[key]))
		for value := range values[key] {
			entry.Examples = append(entry.Examples, value)
		}
		sort.Strings(entry.Examples)
		entry.Examples = entry.Examples[:min(3, len(entry.Examples))]
		overview.Parameters = append(overview.Parameters, *entry)
	}
	sort.Slice(overview.Parameters, func(i, j int) bool { return overview.Parameters[i].Key < overview.Parameters[j].Key })
	for id, entry := range datasets {
		entry.Runs = len(datasetRuns[id])
		entry.Contexts = make([]string, 0, len(contexts[id]))
		for value := range contexts[id] {
			entry.Contexts = append(entry.Contexts, value)
		}
		sort.Strings(entry.Contexts)
		overview.Datasets = append(overview.Datasets, *entry)
	}
	sort.Slice(overview.Datasets, func(i, j int) bool {
		a, b := overview.Datasets[i], overview.Datasets[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Digest != b.Digest {
			return a.Digest < b.Digest
		}
		return a.ID < b.ID
	})
	return overview
}
