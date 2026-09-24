package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/spf13/cobra"
)

type queryFlags struct {
	filter string
	order  []string
	view   string
	limit  int
	token  string
	all    bool
}

func (q *queryFlags) bind(cmd *cobra.Command, order []string) {
	cmd.Flags().StringVar(&q.filter, "filter", "", "MLflow server-side filter expression")
	cmd.Flags().StringSliceVar(&q.order, "order-by", order, "Server-side ordering; repeat for multiple fields")
	cmd.Flags().StringVar(&q.view, "view", "active", "Lifecycle view: active, deleted, all")
	cmd.Flags().IntVar(&q.limit, "limit", 100, "Maximum rows per server page")
	cmd.Flags().StringVar(&q.token, "page-token", "", "Continuation token returned by a previous query")
	cmd.Flags().BoolVar(&q.all, "all", false, "Fetch all matching pages (limit remains the page size)")
}

func (q queryFlags) validate() (string, error) {
	if q.limit < 1 {
		return "", usagef("--limit must be positive")
	}
	switch strings.ToLower(q.view) {
	case "active", "active_only":
		return "ACTIVE_ONLY", nil
	case "deleted", "deleted_only":
		return "DELETED_ONLY", nil
	case "all":
		return "ALL", nil
	default:
		return "", usagef("--view must be active, deleted, or all")
	}
}

func collectExperiments(ctx context.Context, b core.Backend, q core.ExperimentQuery, all bool) (core.ExperimentPage, error) {
	result := core.ExperimentPage{Experiments: []core.Experiment{}}
	seen := map[string]bool{q.PageToken: true}
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		page, err := b.SearchExperiments(ctx, q)
		if err != nil {
			return result, err
		}
		result.Experiments = append(result.Experiments, page.Experiments...)
		result.NextPageToken = page.NextPageToken
		if !all || page.NextPageToken == "" {
			return result, nil
		}
		if seen[page.NextPageToken] {
			return result, fmt.Errorf("server repeated an experiment page token")
		}
		seen[page.NextPageToken] = true
		q.PageToken = page.NextPageToken
	}
}

func collectRuns(ctx context.Context, b core.Backend, q core.RunQuery, all bool) (core.RunPage, error) {
	result := core.RunPage{Runs: []core.Run{}}
	seen := map[string]bool{q.PageToken: true}
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		page, err := b.SearchRuns(ctx, q)
		if err != nil {
			return result, err
		}
		result.Runs = append(result.Runs, page.Runs...)
		result.NextPageToken = page.NextPageToken
		if !all || page.NextPageToken == "" {
			return result, nil
		}
		if seen[page.NextPageToken] {
			return result, fmt.Errorf("server repeated a run page token")
		}
		seen[page.NextPageToken] = true
		q.PageToken = page.NextPageToken
	}
}

func (a *app) experimentsCommand() *cobra.Command {
	group := a.group("experiments", "List and inspect experiments")
	q := queryFlags{}
	var useView bool
	var localVisibility string
	list := &cobra.Command{Use: "list", Short: "Search experiments on the tracking server", Args: noArgs, Example: `  lazymlflow experiments list --filter "name LIKE 'training%'" --all --json`, RunE: func(cmd *cobra.Command, _ []string) error {
		view, err := q.validate()
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("visibility") && !useView {
			return usagef("--visibility requires --use-view")
		}
		target, err := a.resolve()
		if err != nil {
			return err
		}
		var visibility map[string]core.Visibility
		if useView {
			state := a.state()
			defer state.Close()
			layout, _, err := state.LoadLayout(cmd.Context())
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("visibility") {
				localVisibility = layout.ExperimentVisibility
			}
			if localVisibility == "" {
				localVisibility = "normal"
			}
			if !validLocalVisibility(localVisibility) {
				return usagef("--visibility must be normal, hidden, archived, or all")
			}
			visibility, err = state.ListVisibility(cmd.Context(), core.SourceKey(target))
			if err != nil {
				return err
			}
		}
		return a.withSession(cmd.Context(), target, func(s *core.Session) error {
			page, err := collectExperiments(cmd.Context(), s.Backend, core.ExperimentQuery{Filter: q.filter, OrderBy: q.order, ViewType: view, MaxResults: q.limit, PageToken: q.token}, q.all)
			if err != nil {
				return err
			}
			if useView {
				rows := make([]core.Experiment, 0, len(page.Experiments))
				for _, e := range page.Experiments {
					if visible(visibility, "experiment", e.ID, localVisibility) {
						rows = append(rows, e)
					}
				}
				page.Experiments = rows
			}
			if a.jsonOutput {
				return a.output(cmd, page)
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "ID\tNAME\tLIFECYCLE\tARTIFACT LOCATION")
			for _, e := range page.Experiments {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", cell(e.ID), cell(e.Name), cell(e.LifecycleStage), cell(e.ArtifactLocation))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			return pageHint(cmd, page.NextPageToken)
		})
	}}
	q.bind(list, []string{"name ASC"})
	list.Flags().BoolVar(&useView, "use-view", false, "Apply personal hidden/archived experiment visibility")
	list.Flags().StringVar(&localVisibility, "visibility", "normal", "Local visibility with --use-view: normal, hidden, archived, all")
	get := &cobra.Command{Use: "get EXPERIMENT_ID", Short: "Get one experiment", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return a.session(cmd.Context(), func(s *core.Session) error {
			e, err := s.Backend.GetExperiment(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.jsonOutput {
				return a.output(cmd, e)
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintf(w, "ID\t%s\nName\t%s\nLifecycle\t%s\nArtifacts\t%s\n", cell(e.ID), cell(e.Name), cell(e.LifecycleStage), cell(e.ArtifactLocation))
			for _, tag := range e.Tags {
				fmt.Fprintf(w, "Tag %s\t%s\n", cell(tag.Key), cell(tag.Value))
			}
			return w.Flush()
		})
	}}
	group.AddCommand(list, get, a.experimentsSummaryCommand())
	return group
}

func (a *app) runsCommand() *cobra.Command {
	group := a.group("runs", "Search, inspect, and compare runs")
	q := queryFlags{}
	var experimentIDs, metrics, params, columns, groupBy []string
	var useView bool
	var localVisibility string
	list := &cobra.Command{Use: "list [EXPERIMENT_ID...]", Short: "Search runs (all experiments when none are specified)", Example: `  lazymlflow runs list 1 2 --filter "metrics.accuracy > 0.9" --order-by "metrics.accuracy DESC" --metrics accuracy,loss --all
  lazymlflow runs list --experiment 1 --json`, RunE: func(cmd *cobra.Command, args []string) error {
		return a.listRuns(cmd, q, unique(append(append([]string(nil), experimentIDs...), args...)), metrics, params, columns, groupBy, useView, localVisibility)

	}}
	q.bind(list, []string{"attributes.start_time DESC"})
	list.Flags().StringSliceVar(&experimentIDs, "experiment", nil, "Experiment ID; repeat or use comma-separated IDs")
	list.Flags().StringSliceVar(&metrics, "metrics", nil, "Metric columns in the text table")
	list.Flags().StringSliceVar(&params, "params", nil, "Parameter columns in the text table")
	list.Flags().StringArrayVar(&columns, "column", nil, "Display exact attribute/metric/param/tag/dataset field, e.g. metric:loss; repeat")
	list.Flags().StringArrayVar(&groupBy, "group-by", nil, "Group loaded runs by exact parameter key; repeat")
	list.Flags().BoolVar(&useView, "use-view", false, "Apply the saved local view (requires one explicit experiment)")
	list.Flags().StringVar(&localVisibility, "visibility", "normal", "Override local visibility with --use-view: normal, hidden, archived, all")
	get := &cobra.Command{Use: "get RUN_ID", Short: "Inspect a run, including latest metrics, parameters, and tags", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return a.session(cmd.Context(), func(s *core.Session) error {
			r, err := s.Backend.GetRun(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if a.jsonOutput {
				return a.output(cmd, r)
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintf(w, "Run ID\t%s\nName\t%s\nExperiment\t%s\nStatus\t%s\nStarted\t%s\nArtifacts\t%s\n", cell(r.ID()), cell(r.Name()), cell(r.Info.ExperimentID), cell(r.Info.Status), timestamp(r.Info.StartTime), cell(r.Info.ArtifactURI))
			for _, m := range r.Data.Metrics {
				fmt.Fprintf(w, "Metric %s\t%s\n", cell(m.Key), m.Value.String())
			}
			for _, p := range r.Data.Params {
				fmt.Fprintf(w, "Param %s\t%s\n", cell(p.Key), cell(p.Value))
			}
			for _, t := range r.Data.Tags {
				fmt.Fprintf(w, "Tag %s\t%s\n", cell(t.Key), cell(t.Value))
			}
			return w.Flush()
		})
	}}
	var differences bool
	compare := &cobra.Command{Use: "compare RUN_ID RUN_ID...", Short: "Compare runs from the current target, across experiments", Args: func(cmd *cobra.Command, args []string) error {
		if len(unique(args)) < 2 {
			return usagef("runs compare requires at least two distinct run IDs")
		}
		return nil
	}, RunE: func(cmd *cobra.Command, args []string) error {
		return a.session(cmd.Context(), func(s *core.Session) error {
			runs, err := core.Compare(cmd.Context(), s.Backend, args)
			if err != nil {
				return err
			}
			result := comparison(runs, differences)
			if a.jsonOutput {
				return a.output(cmd, result)
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprint(w, "FIELD")
			for _, r := range runs {
				fmt.Fprintf(w, "\t%s (%s)", cell(r.Name()), cell(r.ID()))
			}
			fmt.Fprintln(w)
			for _, row := range result.Rows {
				fmt.Fprint(w, cell(row.Field))
				for _, v := range row.Values {
					if v == nil {
						fmt.Fprint(w, "\t—")
					} else {
						fmt.Fprintf(w, "\t%s", cell(*v))
					}
				}
				fmt.Fprintln(w)
			}
			return w.Flush()
		})
	}}
	compare.Flags().BoolVar(&differences, "differences", false, "Show only fields whose values differ")
	group.AddCommand(list, get, compare, a.runsSummaryCommand(), a.runsPinnedCommand(), a.runPinCommand(), a.runUnpinCommand())
	return group
}

type comparisonRow struct {
	Field     string    `json:"field"`
	Values    []*string `json:"values"`
	Different bool      `json:"different"`
}
type comparisonResult struct {
	Runs []core.Run      `json:"runs"`
	Rows []comparisonRow `json:"rows"`
}

func comparison(runs []core.Run, differences bool) comparisonResult {
	result := comparisonResult{Runs: runs, Rows: []comparisonRow{}}
	values := make([]map[string]string, len(runs))
	keys := map[string]bool{}
	for i, r := range runs {
		values[i] = map[string]string{"run:experiment": r.Info.ExperimentID, "run:status": r.Info.Status}
		for _, m := range r.Data.Metrics {
			values[i]["metric:"+m.Key] = strconv.FormatFloat(float64(m.Value), 'g', -1, 64)
		}
		for _, p := range r.Data.Params {
			values[i]["param:"+p.Key] = p.Value
		}
		for _, t := range r.Data.Tags {
			values[i]["tag:"+t.Key] = t.Value
		}
		for key := range values[i] {
			keys[key] = true
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		row := comparisonRow{Field: key, Values: make([]*string, len(runs))}
		baseline, exists := values[0][key]
		for i := range runs {
			v, ok := values[i][key]
			if ok {
				row.Values[i] = &v
			}
			if ok != exists || v != baseline {
				row.Different = true
			}
		}
		if !differences || row.Different {
			result.Rows = append(result.Rows, row)
		}
	}
	return result
}

func (a *app) metricsCommand() *cobra.Command {
	group := a.group("metrics", "Read complete metric histories")
	group.AddCommand(&cobra.Command{Use: "history RUN_ID METRIC_KEY", Short: "Read all metric samples, preserving repeated steps", Args: exactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return a.session(cmd.Context(), func(s *core.Session) error {
			metrics, err := s.Backend.MetricHistory(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			if metrics == nil {
				metrics = []core.Metric{}
			}
			if a.jsonOutput {
				return a.output(cmd, map[string]any{"run_id": args[0], "metric_key": args[1], "metrics": metrics})
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "STEP\tVALUE\tTIMESTAMP\tMODEL\tDATASET")
			for _, m := range metrics {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\n", m.Step, m.Value.String(), timestamp(m.Timestamp), cell(m.ModelID), cell(m.DatasetName))
			}
			return w.Flush()
		})
	}})
	return group
}

func table(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0) }
func pageHint(cmd *cobra.Command, token string) error {
	if token != "" {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "More results available. Use --page-token %q or --all.\n", token)
		return err
	}
	return nil
}
func timestamp(ms int64) string {
	if ms == 0 {
		return "—"
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
func lookup(values []core.KeyValue, key string) string {
	for _, v := range values {
		if v.Key == key {
			return v.Value
		}
	}
	return "—"
}
func unique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result
}
