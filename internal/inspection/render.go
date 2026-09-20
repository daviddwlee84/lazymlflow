package inspection

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

// RenderMarkdown is deterministic for a captured snapshot. It reports observed
// values and collection limits without inferring model quality or convergence.
func RenderMarkdown(s Snapshot) string {
	var b strings.Builder
	title := "Run summary"
	if len(s.Runs) > 1 {
		title = "Run comparison summary"
	}
	if s.Experiment != nil {
		title = "Experiment summary"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "Source: %s (`%s`), %s\n\nCollected: %s · context version %d\n\n", md(s.Source.Name), md(s.Source.TargetID), md(config.RedactURI(s.Source.TrackingURI)), s.CollectedAt.UTC().Format(time.RFC3339), s.Version)
	fmt.Fprintf(&b, "Selection: %s. %d matching runs; %d detailed runs.\n\n", md(s.Selection.Strategy), s.Selection.MatchingRuns, len(s.Runs))
	fmt.Fprintf(&b, "History policy: %s; sample budget %d per selected metric. Statistics use full successfully fetched histories. Latest values remain separate from history extrema.\n\n", md(string(s.Options.History)), s.Options.SampleLimit)
	if !s.Options.IncludeSystem && len(s.Options.Metrics) == 0 {
		b.WriteString("Automatically selected histories exclude `system/` metrics; all latest metrics remain included.\n\n")
	}
	if s.Experiment != nil {
		e := s.Experiment
		fmt.Fprintf(&b, "## Experiment %s\n\nID: `%s` · lifecycle: %s · metadata scan complete: %t\n\n", md(e.Experiment.Name), md(e.Experiment.ID), md(e.Experiment.LifecycleStage), e.MetadataComplete)
		fmt.Fprintf(&b, "Filter: %s\n\nOrder: %s · lifecycle query: %s\n\n", md(display(e.Query.Filter)), md(strings.Join(e.Query.OrderBy, ", ")), md(e.Query.ViewType))
		writeExperimentOverview(&b, e.Overview)
		fmt.Fprintf(&b, "All %d matching metadata rows are included in JSON. Detailed histories/artifacts/notes cover the %d selected run IDs below; other runs have metadata only.\n\n", len(e.MatchingRuns), len(s.Runs))
		b.WriteString("| Run ID | Name | Status | Started (UTC) | Detailed |\n|---|---|---|---|---|\n")
		selected := map[string]bool{}
		for _, id := range s.Selection.DetailedRunIDs {
			selected[id] = true
		}
		for _, r := range e.MatchingRuns {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %t |\n", md(r.ID()), md(r.Name()), md(r.Info.Status), md(date(r.Info.StartTime)), selected[r.ID()])
		}
		b.WriteByte('\n')
		writeNotes(&b, "Experiment notes", e.Notes)
	}
	if len(s.Runs) > 1 {
		writeComparison(&b, s.Runs)
	}
	for _, r := range s.Runs {
		writeRun(&b, r)
	}
	b.WriteString("## Collection limits\n\n")
	b.WriteString("- Artifact evidence lists root entries only; file contents are not included.\n- Dataset schema/profile are the logged values, not an independent validation of the underlying data.\n- A finished run, last value, minimum, or maximum alone does not establish convergence, generalization, or a preferred optimization direction.\n- The snapshot captures reads over time; concurrent tracking updates can change subsequent results.\n")
	if len(s.Warnings) > 0 {
		b.WriteString("\n### Partial or unavailable evidence\n\n")
		for _, w := range s.Warnings {
			fmt.Fprintf(&b, "- %s / %s: %s\n", md(w.Subject), md(w.Section), md(w.Message))
		}
	}
	return b.String()
}
func writeExperimentOverview(b *strings.Builder, overview ExperimentOverview) {
	fmt.Fprintf(b, "### Complete metadata overview\n\nCoverage below uses all %d matching runs, including runs outside the detailed selection. Counts are per run and exact logged key; metric ranges use finite server-latest values, not history extrema.\n\n", overview.RunCount)
	b.WriteString("| Status | Runs |\n|---|---|\n")
	for _, status := range overview.Statuses {
		fmt.Fprintf(b, "| %s | %d |\n", md(display(status.Status)), status.Runs)
	}
	b.WriteString("\n| Metric | Logged runs | Missing | Finite | Non-finite | Latest minimum | Latest maximum |\n|---|---|---|---|---|---|---|\n")
	for _, metric := range overview.Metrics {
		fmt.Fprintf(b, "| %s | %d | %d | %d | %d | %s | %s |\n", md(metric.Key), metric.LoggedRuns, metric.MissingRuns, metric.FiniteRuns, metric.NonFiniteRuns, md(metricObservation(metric.LatestMin)), md(metricObservation(metric.LatestMax)))
	}
	b.WriteString("\n| Parameter | Logged runs | Missing | Distinct values | Examples (up to 3) |\n|---|---|---|---|---|\n")
	for _, param := range overview.Parameters {
		examples := make([]string, 0, len(param.Examples))
		for _, value := range param.Examples {
			quoted, _ := json.Marshal(value)
			examples = append(examples, string(quoted))
		}
		fmt.Fprintf(b, "| %s | %d | %d | %d | %s |\n", md(param.Key), param.LoggedRuns, param.MissingRuns, param.DistinctValues, md(strings.Join(examples, ", ")))
	}
	b.WriteString("\n| Dataset | Digest | Variant identity | Runs using variant | Contexts |\n|---|---|---|---|---|\n")
	for _, dataset := range overview.Datasets {
		contexts := make([]string, 0, len(dataset.Contexts))
		for _, value := range dataset.Contexts {
			contexts = append(contexts, display(value))
		}
		fmt.Fprintf(b, "| %s | %s | %s | %d | %s |\n", md(dataset.Name), md(dataset.Digest), md(dataset.ID), dataset.Runs, md(strings.Join(contexts, ", ")))
	}
	b.WriteByte('\n')
}
func metricObservation(point *MetricObservation) string {
	if point == nil {
		return "—"
	}
	return fmt.Sprintf("%s (run %s; step %d; timestamp %d)", point.Value.String(), point.RunID, point.Step, point.Timestamp)
}
func writeHistoryObservations(b *strings.Builder, h HistoryContext) {
	if h.Observations == nil {
		return
	}
	b.WriteString("Numeric history observations (no optimization direction implied):\n\n| Difference | Value | From step / timestamp | To step / timestamp |\n|---|---|---|---|\n")
	for _, row := range []struct {
		label    string
		delta    NumericDelta
		from, to *core.Metric
	}{
		{"Last − first", h.Observations.FirstToLast, h.Summary.First, h.Summary.Last},
		{"Last − minimum", h.Observations.LastMinusMinimum, h.Summary.Min, h.Summary.Last},
		{"Last − maximum", h.Observations.LastMinusMaximum, h.Summary.Max, h.Summary.Last},
	} {
		value := "Unavailable: " + row.delta.Unavailable
		if row.delta.Value != nil {
			value = row.delta.Value.String()
		}
		coordinates := func(point *core.Metric) string {
			if point == nil {
				return "—"
			}
			return fmt.Sprintf("%d / %d", point.Step, point.Timestamp)
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n", row.label, md(value), coordinates(row.from), coordinates(row.to))
	}
	b.WriteByte('\n')
}

func writeComparison(b *strings.Builder, runs []RunContext) {
	b.WriteString("## Latest metric comparison\n\n| Metric")
	for _, r := range runs {
		fmt.Fprintf(b, " | %s (%s)", md(r.Run.Name()), md(r.Run.ID()))
	}
	b.WriteString(" |\n|---")
	for range runs {
		b.WriteString("|---")
	}
	b.WriteString("|\n")
	keys := []string{}
	for _, r := range runs {
		for _, m := range r.Run.Data.Metrics {
			keys = append(keys, m.Key)
		}
	}
	keys = unique(keys)
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(b, "| %s", md(key))
		for _, r := range runs {
			value := "—"
			if v, ok := r.Run.Metric(key); ok {
				value = v.String()
			}
			fmt.Fprintf(b, " | %s", md(value))
		}
		b.WriteString(" |\n")
	}
	b.WriteByte('\n')
}
func writeRun(b *strings.Builder, r RunContext) {
	run := r.Run
	fmt.Fprintf(b, "## Run %s\n\nID: `%s` · experiment: `%s` · status: %s · lifecycle: %s\n\n", md(run.Name()), md(run.ID()), md(run.Info.ExperimentID), md(run.Info.Status), md(run.Info.LifecycleStage))
	fmt.Fprintf(b, "Started: %s · ended: %s · parent: %s\n\n", md(date(run.Info.StartTime)), md(date(run.Info.EndTime)), md(display(run.ParentID())))
	b.WriteString("### Latest metrics\n\n| Metric | Latest | Step | Timestamp (ms) | Dataset |\n|---|---|---|---|---|\n")
	metrics := append([]core.Metric(nil), run.Data.Metrics...)
	sort.SliceStable(metrics, func(i, j int) bool { return metrics[i].Key < metrics[j].Key })
	for _, m := range metrics {
		fmt.Fprintf(b, "| %s | %s | %d | %d | %s / %s |\n", md(m.Key), md(m.Value.String()), m.Step, m.Timestamp, md(m.DatasetName), md(m.DatasetDigest))
	}
	b.WriteByte('\n')
	writeValues(b, "Parameters", run.Data.Params)
	writeValues(b, "Tags", run.Data.Tags)
	b.WriteString("### Selected metric histories\n\n")
	if len(r.Histories) == 0 {
		b.WriteString("No metric histories selected.\n\n")
	}
	for _, h := range r.Histories {
		fmt.Fprintf(b, "#### %s\n\n", md(h.Key))
		if h.Status != "complete" {
			fmt.Fprintf(b, "Status: %s. %s\n\n", md(h.Status), md(h.Error))
			continue
		}
		summary := h.Summary
		fmt.Fprintf(b, "Samples: %d total, %d finite, %d non-finite; %d included in this context; sampled: %t.\n\n", summary.Count, summary.FiniteCount, summary.NonFiniteCount, len(h.Samples), h.Sampled)
		if summary.Count > 0 {
			fmt.Fprintf(b, "Step range: %d–%d · timestamp range: %d–%d ms.\n\n", summary.MinStep, summary.MaxStep, summary.StartTimestamp, summary.EndTimestamp)
		}
		b.WriteString("| Statistic (finite values) | Value | Step | Timestamp (ms) |\n|---|---|---|---|\n")
		for _, row := range []struct {
			name string
			m    *core.Metric
		}{{"First", summary.First}, {"Last", summary.Last}, {"Minimum", summary.Min}, {"Maximum", summary.Max}} {
			if row.m == nil {
				fmt.Fprintf(b, "| %s | — | — | — |\n", row.name)
			} else {
				fmt.Fprintf(b, "| %s | %s | %d | %d |\n", row.name, md(row.m.Value.String()), row.m.Step, row.m.Timestamp)
			}
		}
		b.WriteByte('\n')
		writeHistoryObservations(b, h)
		if len(h.Samples) > 0 {
			b.WriteString("Included history points:\n\n| Step | Timestamp (ms) | Value |\n|---|---|---|\n")
			for _, point := range h.Samples {
				fmt.Fprintf(b, "| %d | %d | %s |\n", point.Step, point.Timestamp, md(point.Value.String()))
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString("### Datasets\n\n")
	if len(run.Inputs.DatasetInputs) == 0 {
		b.WriteString("No dataset inputs logged.\n\n")
	}
	for _, input := range run.Inputs.DatasetInputs {
		d := input.Dataset
		fmt.Fprintf(b, "#### %s\n\nDigest: `%s` · source type: %s\n\nSource (logged): %s\n\n", md(d.Name), md(d.Digest), md(d.SourceType), md(d.Source))
		writeValues(b, "Dataset input tags / context", input.Tags)
		if d.Schema != "" {
			schema := core.ParseDatasetSchema(d.Schema)
			fmt.Fprintf(b, "Schema: kind %s, %d columns, %d leaves", md(schema.Kind), schema.Columns, schema.Leaves)
			if schema.Error != "" {
				fmt.Fprintf(b, "; parsing warning: %s", md(schema.Error))
			}
			b.WriteString(".\n\n")
			writeLiteral(b, "Logged schema", d.Schema)
		} else {
			b.WriteString("Schema: not logged.\n\n")
		}
		if d.Profile != "" {
			writeLiteral(b, "Logged profile", d.Profile)
		} else {
			b.WriteString("Profile: not logged.\n\n")
		}
	}
	for _, notes := range r.DatasetNotes {
		writeNotes(b, "Dataset notes: "+notes.Subject.Label, notes)
	}
	b.WriteString("### Artifact root\n\n")
	fmt.Fprintf(b, "Status: %s · %s\n\nURI: %s\n\n", md(r.Artifacts.Status), md(r.Artifacts.Scope), md(config.RedactURI(r.Artifacts.RootURI)))
	if r.Artifacts.Error != "" {
		fmt.Fprintf(b, "Collection warning: %s\n\n", md(r.Artifacts.Error))
	}
	b.WriteString("| Path | Kind | Bytes |\n|---|---|---|\n")
	for _, a := range r.Artifacts.Files {
		kind := "file"
		if a.IsDir {
			kind = "directory"
		}
		fmt.Fprintf(b, "| %s | %s | %d |\n", md(a.Path), kind, a.FileSize)
	}
	b.WriteByte('\n')
	writeNotes(b, "Local run notes", r.Notes)
}
func writeValues(b *strings.Builder, title string, values []core.KeyValue) {
	fmt.Fprintf(b, "### %s\n\n", md(title))
	if len(values) == 0 {
		b.WriteString("None logged.\n\n")
		return
	}
	b.WriteString("| Key | Value |\n|---|---|\n")
	values = append([]core.KeyValue(nil), values...)
	sort.SliceStable(values, func(i, j int) bool { return values[i].Key < values[j].Key })
	for _, v := range values {
		fmt.Fprintf(b, "| %s | %s |\n", md(v.Key), md(v.Value))
	}
	b.WriteByte('\n')
}
func writeNotes(b *strings.Builder, title string, n NotesContext) {
	fmt.Fprintf(b, "### %s\n\n", md(title))
	if n.Status != "complete" {
		fmt.Fprintf(b, "Status: %s. %s\n\n", md(n.Status), md(n.Error))
		return
	}
	if len(n.Notes) == 0 {
		b.WriteString("No active local notes.\n\n")
		return
	}
	for _, note := range n.Notes {
		fmt.Fprintf(b, "Note `%s` · revision %d · updated %s\n\n", md(note.ID), note.Revision, md(date(note.UpdatedAt)))
		writeLiteral(b, "Local note", note.Body)
	}
}
func writeLiteral(b *strings.Builder, label, value string) {
	// JSON quoting ensures newlines, controls, or code fences inside metadata
	// cannot impersonate report structure. No content is interpreted as markup.
	safe, _ := json.Marshal(value)
	fence := strings.Repeat("`", max(3, longestRun(string(safe), '`')+1))
	fmt.Fprintf(b, "%s (literal JSON string):\n\n%sjson\n%s\n%s\n\n", md(label), fence, string(safe), fence)
}
func md(s string) string {
	s = ansi.Strip(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = html.EscapeString(s)
	return strings.NewReplacer("\\", "\\\\", "|", "\\|", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "#", "\\#").Replace(s)
}
func longestRun(s string, char rune) int {
	longest, current := 0, 0
	for _, r := range s {
		if r == char {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	return longest
}
func date(ms int64) string {
	if ms == 0 {
		return "—"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}
func display(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ContextJSON preserves the versioned machine interface, including non-finite
// metrics represented as JSON strings by core.Number.
func ContextJSON(s Snapshot) (string, error) {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
