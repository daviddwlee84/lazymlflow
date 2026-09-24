package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/spf13/cobra"
)

// viewContext resolves the configured source without starting MLflow or SSH.
func (a *app) viewContext() (core.Target, core.ExperimentView, error) {
	cfg, err := a.load()
	if err != nil {
		return core.Target{}, core.ExperimentView{}, err
	}
	target, err := config.Resolve(cfg, a.targetID, a.trackingURI)
	if err != nil {
		return target, core.ExperimentView{}, usageError{err}
	}
	return target, core.DefaultView(cfg.TUI.MetricColumns, cfg.TUI.ParameterColumns), nil
}
func (a *app) loadExperimentView(ctx context.Context, state core.StateStore, target core.Target, experiment string, fallback core.ExperimentView) (core.ExperimentView, bool, error) {
	view, found, err := state.LoadView(ctx, core.SourceKey(target), experiment)
	if err != nil {
		return view, false, err
	}
	if !found {
		return fallback, false, nil
	}
	return core.NormalizeView(view), true, nil
}
func (a *app) viewCommand() *cobra.Command {
	group := a.group("view", "Manage local views and visibility (never modifies MLflow)")
	group.AddCommand(&cobra.Command{Use: "show EXPERIMENT_ID", Short: "Show the effective saved view without connecting", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		target, fallback, err := a.viewContext()
		if err != nil {
			return err
		}
		state := a.state()
		defer state.Close()
		v, found, err := a.loadExperimentView(cmd.Context(), state, target, args[0], fallback)
		if err != nil {
			return err
		}
		return a.output(cmd, map[string]any{"experiment_id": args[0], "target": target.ID, "saved": found, "view": v})
	}})
	group.AddCommand(a.viewSetCommand())
	group.AddCommand(&cobra.Command{Use: "reset EXPERIMENT_ID", Short: "Restore configured defaults for an experiment's local view", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		t, err := a.resolve()
		if err != nil {
			return err
		}
		state := a.state()
		defer state.Close()
		if err := state.ResetView(cmd.Context(), core.SourceKey(t), args[0]); err != nil {
			return err
		}
		return a.output(cmd, map[string]any{"target": t.ID, "experiment_id": args[0], "reset": true})
	}})
	for _, entry := range []struct {
		verb  string
		value core.Visibility
	}{{"hide", core.VisibilityHidden}, {"archive", core.VisibilityArchived}, {"restore", core.VisibilityNormal}} {
		verb, value := entry.verb, entry.value
		var experiment, run string
		cmd := &cobra.Command{Use: verb + " (--experiment ID | --run ID)", Short: strings.Title(verb) + " an experiment or run locally", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("experiment") && cmd.Flags().Changed("run") {
				return usagef("--experiment and --run are mutually exclusive")
			}
			if (strings.TrimSpace(experiment) == "") == (strings.TrimSpace(run) == "") {
				return usagef("specify exactly one nonempty --experiment ID or --run ID")
			}
			kind, id := "experiment", experiment
			if run != "" {
				kind, id = "run", run
			}
			t, err := a.resolve()
			if err != nil {
				return err
			}
			state := a.state()
			defer state.Close()
			if err := state.SetVisibility(cmd.Context(), core.SourceKey(t), kind, id, value); err != nil {
				return err
			}
			return a.output(cmd, map[string]any{"target": t.ID, "kind": kind, "id": id, "visibility": value, "local_only": true})
		}}
		cmd.Flags().StringVar(&experiment, "experiment", "", "Experiment ID; its run visibility entries are unchanged")
		cmd.Flags().StringVar(&run, "run", "", "Run ID; child run visibility entries are unchanged")
		group.AddCommand(cmd)
	}
	group.AddCommand(&cobra.Command{Use: "visibility", Short: "List local hidden and archived resources", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		t, err := a.resolve()
		if err != nil {
			return err
		}
		state := a.state()
		defer state.Close()
		values, err := state.ListVisibility(cmd.Context(), core.SourceKey(t))
		if err != nil {
			return err
		}
		if a.jsonOutput {
			return a.output(cmd, values)
		}
		w := table(cmd.OutOrStdout())
		fmt.Fprintln(w, "RESOURCE\tVISIBILITY")
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(w, "%s\t%s\n", cell(key), values[key])
		}
		return w.Flush()
	}})
	return group
}
func (a *app) viewSetCommand() *cobra.Command {
	var columns, sorts, groups, numeric []string
	var metricPins, subscriptions []string
	var mode, expansion, visibility, filter string
	var metricUpdates, readOn string
	cmd := &cobra.Command{Use: "set EXPERIMENT_ID", Short: "Save columns, ordering and presentation for one experiment", Args: exactArgs(1), Example: `  lazymlflow view set 11 --column attribute:name --column dataset:name --column 'param:learning rate' --column metric:loss --sort 'metric:loss ASC'
  lazymlflow view set 11 --group-by optimizer --group-by 'learning rate' --mode grouped`, RunE: func(cmd *cobra.Command, args []string) error {
		changed := false
		for _, name := range []string{"column", "sort", "group-by", "numeric", "mode", "expansion", "visibility", "filter", "metric-pin", "notify-metric", "metric-updates", "read-on"} {
			changed = changed || cmd.Flags().Changed(name)
		}
		if !changed {
			return usagef("provide a view setting; see lazymlflow view set --help")
		}
		// Parse all explicit flags before opening a state database.
		parsedColumns, err := parseColumns(columns)
		if err != nil {
			return err
		}
		pins := nonemptyStrings(metricPins)
		if err := core.ValidateMetricPins(pins); err != nil {
			return usageError{err}
		}
		subs, err := parseMetricSubscriptions(subscriptions)
		if err != nil {
			return err
		}
		updates, err := parseInheritedBool(metricUpdates)
		if err != nil {
			return usagef("--metric-updates: %v", err)
		}
		if readOn != "" && readOn != "open" && readOn != "select" && readOn != "inherit" {
			return usagef("--read-on must be open, select, or inherit")
		}
		parsedSort := make([]core.SortSpec, 0, len(sorts))
		for _, raw := range sorts {
			value, err := core.ParseSort(raw)
			if err != nil {
				return usageError{err}
			}
			parsedSort = append(parsedSort, value)
		}
		numericColumns, err := parseColumns(numeric)
		if err != nil {
			return err
		}
		t, fallback, err := a.viewContext()
		if err != nil {
			return err
		}
		state := a.state()
		defer state.Close()
		v, _, err := a.loadExperimentView(cmd.Context(), state, t, args[0], fallback)
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("column") {
			v.Columns = parsedColumns
		}
		if cmd.Flags().Changed("sort") {
			v.Sort = parsedSort
		}
		if cmd.Flags().Changed("group-by") {
			v.GroupBy = nil
			for _, g := range groups {
				if g != "" {
					v.GroupBy = append(v.GroupBy, g)
				}
			}
			if !cmd.Flags().Changed("mode") {
				if len(v.GroupBy) > 0 {
					v.Mode = "grouped"
				} else if v.Mode == "grouped" {
					v.Mode = "flat"
				}
			}
		}
		if cmd.Flags().Changed("numeric") {
			for i := range v.Columns {
				v.Columns[i].Numeric = false
			}
			for i := range v.Sort {
				v.Sort[i].Column.Numeric = false
			}
			for _, c := range numericColumns {
				found := false
				for i := range v.Columns {
					if sameColumn(v.Columns[i], c) {
						v.Columns[i].Numeric = true
						found = true
					}
				}
				for i := range v.Sort {
					if sameColumn(v.Sort[i].Column, c) {
						v.Sort[i].Column.Numeric = true
						found = true
					}
				}
				if !found {
					return usagef("--numeric %s:%s must name a selected or sorted column", c.Kind, c.Key)
				}
			}
		}
		for i := range v.Sort {
			for _, c := range v.Columns {
				if sameColumn(c, v.Sort[i].Column) && c.Numeric {
					v.Sort[i].Column.Numeric = true
				}
			}
		}
		if cmd.Flags().Changed("mode") {
			v.Mode = mode
		}
		if cmd.Flags().Changed("expansion") {
			v.Expansion = expansion
		}
		if cmd.Flags().Changed("visibility") {
			v.Visibility = visibility
		}
		if cmd.Flags().Changed("filter") {
			v.Filter = filter
		}
		if cmd.Flags().Changed("metric-pin") {
			v.MetricPins = pins
		}
		if cmd.Flags().Changed("notify-metric") || cmd.Flags().Changed("metric-updates") || cmd.Flags().Changed("read-on") {
			if v.Activity == nil {
				v.Activity = &core.ActivityPolicy{}
			}
			if cmd.Flags().Changed("notify-metric") {
				v.Activity.Subscriptions = subs
			}
			if cmd.Flags().Changed("metric-updates") {
				v.Activity.MetricUpdates = updates
			}
			if cmd.Flags().Changed("read-on") {
				v.Activity.ReadOn = readOn
				if readOn == "inherit" {
					v.Activity.ReadOn = ""
				}
			}
		}
		if err := core.ValidateView(v); err != nil {
			return usageError{err}
		}
		v = core.NormalizeView(v)
		if err := state.SaveView(cmd.Context(), core.SourceKey(t), args[0], v); err != nil {
			return err
		}
		return a.output(cmd, map[string]any{"target": t.ID, "experiment_id": args[0], "saved": true, "view": v})
	}}
	cmd.Flags().StringArrayVar(&columns, "column", nil, "Replace columns: attribute:name, metric:loss, param:key, tag:key, dataset:name; repeat")
	cmd.Flags().StringArrayVar(&sorts, "sort", nil, "Replace ordering, e.g. 'metric:loss ASC'; repeat for secondary keys")
	cmd.Flags().StringArrayVar(&groups, "group-by", nil, "Exact parameter key; repeat; an empty value clears grouping")
	cmd.Flags().StringArrayVar(&numeric, "numeric", nil, "Treat a selected/sorted column numerically, e.g. param:batch_size; repeat")
	cmd.Flags().StringVar(&mode, "mode", "", "Presentation: auto, flat, tree, grouped")
	cmd.Flags().StringVar(&expansion, "expansion", "", "Initial expansion: collapsed, first, all")
	cmd.Flags().StringVar(&visibility, "visibility", "", "Local visibility: normal, hidden, archived, all")
	cmd.Flags().StringVar(&filter, "filter", "", "Saved MLflow server filter (empty clears)")
	cmd.Flags().StringArrayVar(&metricPins, "metric-pin", nil, "Replace ordered exact metric pins; repeat; empty clears")
	cmd.Flags().StringArrayVar(&subscriptions, "notify-metric", nil, "Replace subscriptions: exact KEY or KEY=value|sample; repeat; empty clears")
	cmd.Flags().StringVar(&metricUpdates, "metric-updates", "inherit", "Notify on any metric sample update: true, false, inherit")
	cmd.Flags().StringVar(&readOn, "read-on", "inherit", "Mark a run read on open, select, or inherit the global setting")
	return cmd
}

func nonemptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func parseInheritedBool(value string) (*bool, error) {
	if value == "" || value == "inherit" {
		return nil, nil
	}
	if value != "true" && value != "false" {
		return nil, fmt.Errorf("must be true, false, or inherit")
	}
	v, err := strconv.ParseBool(value)
	return &v, err
}

func parseMetricSubscriptions(values []string) ([]core.MetricSubscription, error) {
	result := make([]core.MetricSubscription, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		key, mode := value, "value"
		if i := strings.LastIndexByte(value, '='); i >= 0 {
			key, mode = value[:i], value[i+1:]
		}
		result = append(result, core.MetricSubscription{Key: key, Mode: mode})
	}
	if err := core.ValidateActivityPolicy(core.ActivityPolicy{Subscriptions: result}); err != nil {
		return nil, usageError{err}
	}
	return result, nil
}
func parseColumns(values []string) ([]core.ColumnSpec, error) {
	result := make([]core.ColumnSpec, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		if raw == "" {
			continue
		}
		c, err := core.ParseColumn(raw)
		if err != nil {
			return nil, usageError{err}
		}
		key := core.ColumnID(c)
		if !seen[key] {
			seen[key] = true
			result = append(result, c)
		}
	}
	return result, nil
}
func sameColumn(a, b core.ColumnSpec) bool {
	return a.Kind == b.Kind && a.Key == b.Key && a.DatasetContext == b.DatasetContext
}
func visible(values map[string]core.Visibility, kind, id, filter string) bool {
	if filter == "all" {
		return true
	}
	value := values[core.VisibilityKey(kind, id)]
	if value == "" {
		value = core.VisibilityNormal
	}
	return string(value) == filter
}
