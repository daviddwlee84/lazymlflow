package cli

import (
	"fmt"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/spf13/cobra"
)

func validLocalVisibility(v string) bool {
	return v == "normal" || v == "hidden" || v == "archived" || v == "all"
}
func (a *app) listRuns(cmd *cobra.Command, q queryFlags, ids, metrics, params, columns, groups []string, useView bool, localVisibility string) error {
	lifecycle, err := q.validate()
	if err != nil {
		return err
	}
	if useView && len(ids) != 1 {
		return usagef("--use-view requires exactly one explicit experiment ID; use runs list EXPERIMENT_ID --use-view")
	}
	if cmd.Flags().Changed("visibility") && !useView {
		return usagef("--visibility requires --use-view")
	}
	if cmd.Flags().Changed("visibility") && !validLocalVisibility(localVisibility) {
		return usagef("--visibility must be normal, hidden, archived, or all")
	}
	parsedColumns, err := parseColumns(columns)
	if err != nil {
		return err
	}
	target, err := a.resolve()
	if err != nil {
		return err
	}
	v := core.DefaultView(metrics, params)
	custom := useView || cmd.Flags().Changed("column") || cmd.Flags().Changed("group-by")
	var visibility map[string]core.Visibility
	localSort := false
	if useView {
		var fallback core.ExperimentView
		target, fallback, err = a.viewContext()
		if err != nil {
			return err
		}
		state := a.state()
		defer state.Close()
		v, _, err = a.loadExperimentView(cmd.Context(), state, target, ids[0], fallback)
		if err != nil {
			return err
		}
		visibility, err = state.ListVisibility(cmd.Context(), core.SourceKey(target))
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("visibility") {
			v.Visibility = localVisibility
		}
		if !cmd.Flags().Changed("filter") {
			q.filter = v.Filter
		}
		if !cmd.Flags().Changed("order-by") {
			if order, ok := core.ServerOrder(v.Sort); ok {
				q.order = order
			} else {
				localSort = true
			}
		}
	}
	if cmd.Flags().Changed("column") {
		v.Columns = parsedColumns
	}
	// Existing metrics/params options still add their exact fields, including when
	// a saved view is in use. JSON keeps full runs instead of dropping hidden keys.
	if custom {
		for _, p := range params {
			v.Columns = appendColumn(v.Columns, core.ColumnSpec{Kind: "param", Key: p})
		}
		for _, m := range metrics {
			v.Columns = appendColumn(v.Columns, core.ColumnSpec{Kind: "metric", Key: m})
		}
	}
	if cmd.Flags().Changed("group-by") {
		v.GroupBy = nil
		for _, g := range groups {
			if g != "" {
				v.GroupBy = append(v.GroupBy, g)
			}
		}
		if len(v.GroupBy) > 0 {
			v.Mode = "grouped"
		} else {
			v.Mode = "flat"
		}
		v.Expansion = "all"
	}
	if custom && cmd.Flags().Changed("order-by") {
		v.Sort = nil
		for _, raw := range q.order {
			sort, err := core.ParseSort(raw)
			if err != nil {
				return usageError{err}
			}
			v.Sort = append(v.Sort, sort)
		}
	}
	if custom {
		v.Filter = q.filter
		v = core.NormalizeView(v)
		if err := core.ValidateView(v); err != nil {
			return usageError{err}
		}
	}
	return a.withSession(cmd.Context(), target, func(s *core.Session) error {
		if len(ids) == 0 {
			experimentView := "ACTIVE_ONLY"
			if lifecycle != "ACTIVE_ONLY" {
				experimentView = "ALL"
			}
			page, err := collectExperiments(cmd.Context(), s.Backend, core.ExperimentQuery{ViewType: experimentView, MaxResults: 1000}, true)
			if err != nil {
				return err
			}
			for _, e := range page.Experiments {
				ids = append(ids, e.ID)
			}
		}
		page := core.RunPage{Runs: []core.Run{}}
		if len(ids) > 0 {
			page, err = collectRuns(cmd.Context(), s.Backend, core.RunQuery{ExperimentIDs: ids, Filter: q.filter, OrderBy: q.order, ViewType: lifecycle, MaxResults: q.limit, PageToken: q.token}, q.all)
			if err != nil {
				return err
			}
		}
		if useView {
			rows := make([]core.Run, 0, len(page.Runs))
			for _, r := range page.Runs {
				if visible(visibility, "run", r.ID(), v.Visibility) {
					rows = append(rows, r)
				}
			}
			page.Runs = rows
		}
		if localSort && len(page.Runs) > 1 {
			page.Runs = core.SortRuns(page.Runs, v.Sort)
		}
		if a.jsonOutput {
			if custom {
				scope := "all_matches"
				if localSort && (page.NextPageToken != "" || q.token != "") {
					scope = "loaded_rows"
				}
				rows := core.BuildRunRows(page.Runs, v, visibility)
				if rows == nil {
					rows = []core.RunRow{}
				}
				return a.output(cmd, map[string]any{"runs": page.Runs, "next_page_token": page.NextPageToken, "view": v, "loaded_runs": len(page.Runs), "complete": page.NextPageToken == "" && q.token == "", "sort_scope": scope, "rows": rows})
			}
			return a.output(cmd, page)
		}
		if custom {
			if localSort && (page.NextPageToken != "" || q.token != "") {
				fmt.Fprintf(cmd.ErrOrStderr(), "Local sorting covers %d loaded runs; use --all to sort all matching results.\n", len(page.Runs))
			}
			if err := printRunView(cmd, page.Runs, v, visibility); err != nil {
				return err
			}
		} else {
			if err := printLegacyRuns(cmd, page.Runs, metrics, params); err != nil {
				return err
			}
		}
		return pageHint(cmd, page.NextPageToken)
	})
}
func appendColumn(columns []core.ColumnSpec, c core.ColumnSpec) []core.ColumnSpec {
	for _, old := range columns {
		if sameColumn(old, c) {
			return columns
		}
	}
	return append(columns, c)
}
func printLegacyRuns(cmd *cobra.Command, runs []core.Run, metrics, params []string) error {
	w := table(cmd.OutOrStdout())
	fmt.Fprint(w, "RUN ID\tNAME\tEXPERIMENT\tSTATUS\tSTARTED")
	for _, key := range metrics {
		fmt.Fprintf(w, "\t%s", cell(key))
	}
	for _, key := range params {
		fmt.Fprintf(w, "\tparam:%s", cell(key))
	}
	fmt.Fprintln(w)
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s", cell(r.ID()), cell(r.Name()), cell(r.Info.ExperimentID), cell(r.Info.Status), timestamp(r.Info.StartTime))
		for _, key := range metrics {
			value := "—"
			if metric, ok := r.Metric(key); ok {
				value = metric.String()
			}
			fmt.Fprintf(w, "\t%s", value)
		}
		for _, key := range params {
			fmt.Fprintf(w, "\t%s", cell(lookup(r.Data.Params, key)))
		}
		fmt.Fprintln(w)
	}
	return w.Flush()
}
func printRunView(cmd *cobra.Command, runs []core.Run, v core.ExperimentView, visibility map[string]core.Visibility) error {
	w := table(cmd.OutOrStdout())
	fmt.Fprint(w, "RUN ID")
	for _, c := range v.Columns {
		label := c.Label
		if label == "" {
			label = core.ColumnID(c)
		}
		fmt.Fprintf(w, "\t%s", cell(label))
	}
	fmt.Fprintln(w)
	for _, row := range core.BuildRunRows(runs, v, visibility) {
		prefix := strings.Repeat("  ", row.Depth)
		if row.Run == nil {
			note := ""
			if row.Note != "" {
				note = " · " + cell(row.Note)
			}
			fmt.Fprintf(w, "%s%s (%d loaded)%s\n", prefix, cell(row.Label), row.Count, note)
			continue
		}
		fmt.Fprintf(w, "%s%s", prefix, cell(row.Run.ID()))
		for _, c := range v.Columns {
			fmt.Fprintf(w, "\t%s", cell(core.DisplayValue(*row.Run, c)))
		}
		fmt.Fprintln(w)
	}
	return w.Flush()
}
