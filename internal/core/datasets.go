package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DatasetIdentity identifies a variant, excluding per-use context and profile.
// Canonical JSON means harmless source/schema formatting changes retain notes.
func DatasetIdentity(source string, d Dataset) string {
	b, _ := json.Marshal([]string{"dataset-v1", source, d.Name, d.Digest, d.SourceType, canonicalSourceJSON(d.Source), canonicalSchemaJSON(d.Schema)})
	sum := sha256.Sum256(b)
	return "dataset-v1:" + hex.EncodeToString(sum[:])
}

type DatasetUse struct {
	RunID          string     `json:"run_id"`
	RunName        string     `json:"run_name"`
	ExperimentID   string     `json:"experiment_id"`
	ExperimentName string     `json:"experiment_name"`
	Status         string     `json:"status"`
	Context        string     `json:"context,omitempty"`
	InputIndex     int        `json:"input_index"`
	Profile        string     `json:"profile,omitempty"`
	Tags           []KeyValue `json:"tags,omitempty"`
}
type DatasetEntry struct {
	ID      string        `json:"id"`
	Dataset Dataset       `json:"dataset"`
	Schema  DatasetSchema `json:"schema"`
	Uses    []DatasetUse  `json:"uses"`
}
type CatalogError struct {
	ExperimentID string `json:"experiment_id,omitempty"`
	Message      string `json:"message"`
}
type DatasetCatalog struct {
	Source             string         `json:"source"`
	Scope              string         `json:"scope"`
	ViewType           string         `json:"view_type"`
	ExperimentIDs      []string       `json:"experiment_ids"`
	Entries            []DatasetEntry `json:"entries"`
	Complete           bool           `json:"complete"`
	Cancelled          bool           `json:"cancelled"`
	StartedAt          int64          `json:"started_at"`
	UpdatedAt          int64          `json:"updated_at"`
	ExperimentsScanned int            `json:"experiments_scanned"`
	RunsScanned        int            `json:"runs_scanned"`
	Errors             []CatalogError `json:"errors"`
}
type DatasetScanOptions struct {
	ExperimentIDs []string
	ViewType      string
}

func newCatalog(source, scope, view string) DatasetCatalog {
	now := time.Now().UnixMilli()
	return DatasetCatalog{Source: source, Scope: scope, ViewType: view, ExperimentIDs: []string{}, Entries: []DatasetEntry{}, Errors: []CatalogError{}, StartedAt: now, UpdatedAt: now}
}

func DatasetContext(input DatasetInput) string {
	for _, t := range input.Tags {
		if t.Key == "mlflow.data.context" {
			return t.Value
		}
	}
	return ""
}

type catalogBuilder struct {
	value DatasetCatalog
	index map[string]int
	uses  map[string]bool
	runs  map[string]bool
}

func newCatalogBuilder(c DatasetCatalog) *catalogBuilder {
	return &catalogBuilder{value: c, index: map[string]int{}, uses: map[string]bool{}, runs: map[string]bool{}}
}
func (b *catalogBuilder) addRun(e Experiment, r Run) {
	if !b.runs[r.ID()] {
		b.value.RunsScanned++
		b.runs[r.ID()] = true
	}
	for i, input := range r.Inputs.DatasetInputs {
		id := DatasetIdentity(b.value.Source, input.Dataset)
		useKey := r.ID() + "\x00" + fmt.Sprint(i)
		if b.uses[useKey] {
			continue
		}
		b.uses[useKey] = true
		index, found := b.index[id]
		if !found {
			index = len(b.value.Entries)
			b.index[id] = index
			d := input.Dataset
			d.Profile = ""
			b.value.Entries = append(b.value.Entries, DatasetEntry{ID: id, Dataset: d, Schema: ParseDatasetSchema(d.Schema), Uses: []DatasetUse{}})
		}
		experimentID := r.Info.ExperimentID
		if experimentID == "" {
			experimentID = e.ID
		}
		b.value.Entries[index].Uses = append(b.value.Entries[index].Uses, DatasetUse{RunID: r.ID(), RunName: r.Name(), ExperimentID: experimentID, ExperimentName: e.Name, Status: r.Info.Status, Context: DatasetContext(input), InputIndex: i + 1, Profile: input.Dataset.Profile, Tags: append([]KeyValue(nil), input.Tags...)})
	}
}

// snapshot owns its arrays and nested schema values, so consumers can safely
// sort, filter, or retain progress while the scan continues.
func (b *catalogBuilder) snapshot() DatasetCatalog {
	b.value.UpdatedAt = time.Now().UnixMilli()
	result := b.value
	result.ExperimentIDs = append([]string{}, b.value.ExperimentIDs...)
	result.Errors = append([]CatalogError{}, b.value.Errors...)
	result.Entries = make([]DatasetEntry, len(b.value.Entries))
	for i, e := range b.value.Entries {
		e.Uses = append([]DatasetUse{}, e.Uses...)
		for j := range e.Uses {
			e.Uses[j].Tags = append([]KeyValue(nil), e.Uses[j].Tags...)
		}
		e.Schema.Fields = cloneSchemaFields(e.Schema.Fields)
		result.Entries[i] = e
	}
	sort.SliceStable(result.Entries, func(i, j int) bool {
		a, c := result.Entries[i], result.Entries[j]
		if a.Dataset.Name != c.Dataset.Name {
			return a.Dataset.Name < c.Dataset.Name
		}
		if a.Dataset.Digest != c.Dataset.Digest {
			return a.Dataset.Digest < c.Dataset.Digest
		}
		return a.ID < c.ID
	})
	return result
}
func cloneSchemaFields(fields []SchemaField) []SchemaField {
	out := append([]SchemaField{}, fields...)
	for i := range out {
		out[i].Shape = append([]int64(nil), out[i].Shape...)
		if out[i].Required != nil {
			v := *out[i].Required
			out[i].Required = &v
		}
		if out[i].Dimensions != nil {
			v := *out[i].Dimensions
			out[i].Dimensions = &v
		}
		out[i].Children = cloneSchemaFields(out[i].Children)
	}
	return out
}

func CatalogFromRuns(source string, experiments []Experiment, runs []Run) DatasetCatalog {
	b := newCatalogBuilder(newCatalog(source, "loaded_runs", "ACTIVE_ONLY"))
	byID := map[string]Experiment{}
	for _, e := range experiments {
		byID[e.ID] = e
		b.value.ExperimentIDs = append(b.value.ExperimentIDs, e.ID)
	}
	for _, r := range runs {
		b.addRun(byID[r.Info.ExperimentID], r)
	}
	b.value.ExperimentsScanned = len(experiments)
	return b.snapshot()
}

// ScanDatasets searches every public API page. Failures retain successful
// experiments and make completeness explicit; no local visibility is applied.
func ScanDatasets(ctx context.Context, backend Backend, source string, options DatasetScanOptions, progress func(DatasetCatalog)) (DatasetCatalog, error) {
	view := strings.ToUpper(options.ViewType)
	if view == "" {
		view = "ACTIVE_ONLY"
	}
	switch view {
	case "ACTIVE_ONLY", "DELETED_ONLY", "ALL":
	default:
		return DatasetCatalog{}, fmt.Errorf("invalid dataset lifecycle %q", options.ViewType)
	}
	scope := "target"
	if len(options.ExperimentIDs) > 0 {
		scope = "experiments"
	}
	b := newCatalogBuilder(newCatalog(source, scope, view))
	emit := func() {
		if progress != nil {
			progress(b.snapshot())
		}
	}
	fail := func(id string, err error) {
		b.value.Errors = append(b.value.Errors, CatalogError{ExperimentID: id, Message: err.Error()})
	}
	finish := func(err error) (DatasetCatalog, error) {
		b.value.Cancelled = errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		b.value.Complete = err == nil && len(b.value.Errors) == 0
		result := b.snapshot()
		if progress != nil {
			progress(b.snapshot())
		}
		return result, err
	}
	experiments := []Experiment{}
	seenExperiments := map[string]bool{}
	if len(options.ExperimentIDs) > 0 {
		for _, id := range options.ExperimentIDs {
			if strings.TrimSpace(id) == "" {
				return finish(errors.New("experiment ID cannot be empty"))
			}
			if seenExperiments[id] {
				continue
			}
			seenExperiments[id] = true
			if err := ctx.Err(); err != nil {
				return finish(err)
			}
			e, err := backend.GetExperiment(ctx, id)
			if err != nil {
				if ctx.Err() != nil {
					return finish(ctx.Err())
				}
				fail(id, err)
				continue
			}
			experiments = append(experiments, e)
			b.value.ExperimentIDs = append(b.value.ExperimentIDs, id)
		}
	} else {
		// Deleted runs can belong to active experiments, therefore a deleted run
		// scan must discover all experiments before filtering runs.
		expView := view
		if view == "DELETED_ONLY" {
			expView = "ALL"
		}
		token := ""
		seenTokens := map[string]bool{}
		for {
			if err := ctx.Err(); err != nil {
				return finish(err)
			}
			page, err := backend.SearchExperiments(ctx, ExperimentQuery{ViewType: expView, MaxResults: 100, PageToken: token, OrderBy: []string{"name ASC"}})
			if err != nil {
				if ctx.Err() != nil {
					return finish(ctx.Err())
				}
				fail("", err)
				break
			}
			for _, e := range page.Experiments {
				if !seenExperiments[e.ID] {
					experiments = append(experiments, e)
					b.value.ExperimentIDs = append(b.value.ExperimentIDs, e.ID)
					seenExperiments[e.ID] = true
				}
			}
			if page.NextPageToken == "" {
				break
			}
			if page.NextPageToken == token || seenTokens[page.NextPageToken] {
				fail("", errors.New("experiment pagination token repeated"))
				break
			}
			token = page.NextPageToken
			seenTokens[token] = true
		}
	}
	emit()
	for _, e := range experiments {
		token := ""
		seenTokens := map[string]bool{}
		for {
			if err := ctx.Err(); err != nil {
				return finish(err)
			}
			page, err := backend.SearchRuns(ctx, RunQuery{ExperimentIDs: []string{e.ID}, ViewType: view, MaxResults: 100, PageToken: token, OrderBy: []string{"attributes.start_time DESC"}})
			if err != nil {
				if ctx.Err() != nil {
					return finish(ctx.Err())
				}
				fail(e.ID, err)
				break
			}
			for _, r := range page.Runs {
				b.addRun(e, r)
			}
			emit()
			if page.NextPageToken == "" {
				break
			}
			if page.NextPageToken == token || seenTokens[page.NextPageToken] {
				fail(e.ID, errors.New("run pagination token repeated"))
				break
			}
			token = page.NextPageToken
			seenTokens[token] = true
		}
		b.value.ExperimentsScanned++
		emit()
	}
	if len(b.value.Errors) > 0 {
		return finish(fmt.Errorf("dataset scan incomplete: %d request(s) failed", len(b.value.Errors)))
	}
	return finish(nil)
}
