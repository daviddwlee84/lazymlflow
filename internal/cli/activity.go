package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/activity"
	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/spf13/cobra"
)

func (a *app) activityCommand() *cobra.Command {
	group := a.group("activity", "Browse cross-experiment activity and manage the local inbox")
	var view string
	var refresh, all, includeHidden bool
	var limit int
	list := &cobra.Command{Use: "list", Short: "List cached runs; --refresh polls the selected target", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !validActivityView(view) {
			return usagef("--view must be running, recent, unread, alerts, acknowledged, or all")
		}
		if limit < 1 {
			return usagef("--limit must be positive")
		}
		return a.withActivity(cmd, func(cfg *config.Config, target core.Target, state core.StateStore, store core.ActivityStore) error {
			var snapshot core.ActivitySnapshot
			var err error
			if refresh {
				snapshot, err = a.refreshActivity(cmd.Context(), cfg, target, state, store, activity.Options{RecentLimit: limit})
			} else {
				snapshot, err = store.LoadActivity(cmd.Context(), core.SourceKey(target))
			}
			if err != nil {
				return err
			}
			rows := core.ActivityRecords(snapshot, view)
			if !includeHidden {
				visibility, err := state.ListVisibility(cmd.Context(), snapshot.Source)
				if err != nil {
					return err
				}
				visibleRows := make([]core.ActivityRecord, 0, len(rows))
				for _, row := range rows {
					if visible(visibility, "experiment", row.ExperimentID, "normal") && visible(visibility, "run", row.RunID, "normal") {
						visibleRows = append(visibleRows, row)
					}
				}
				rows = visibleRows
			}
			total := len(rows)
			if !all && len(rows) > limit {
				rows = rows[:limit]
			}
			return a.printActivity(cmd, target, snapshot, view, rows, total, !refresh)
		})
	}}
	list.Flags().StringVar(&view, "view", "unread", "View: running, recent, unread, alerts, acknowledged, all")
	list.Flags().BoolVar(&refresh, "refresh", false, "Poll the target before listing (otherwise reads local state only)")
	list.Flags().IntVar(&limit, "limit", 100, "Maximum rows to display and recent runs to fetch")
	list.Flags().BoolVar(&all, "all", false, "Display all cached matching rows")
	list.Flags().BoolVar(&includeHidden, "include-hidden", false, "Include locally hidden and archived experiments and runs")
	_ = list.RegisterFlagCompletionFunc("view", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"running", "recent", "unread", "alerts", "acknowledged", "all"}, cobra.ShellCompDirectiveNoFileComp
	})
	var full bool
	refreshCmd := &cobra.Command{Use: "refresh", Short: "Poll activity; --full reconciles all historical runs and counts", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return a.withActivity(cmd, func(cfg *config.Config, target core.Target, state core.StateStore, store core.ActivityStore) error {
			snapshot, err := a.refreshActivity(cmd.Context(), cfg, target, state, store, activity.Options{Full: full})
			if err != nil {
				return err
			}
			if a.jsonOutput {
				return a.output(cmd, map[string]any{"target": target.ID, "full": full, "activity": snapshot})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Refreshed %s: %d tracked runs, %d experiments; last poll %s; last full scan %s\n", cell(target.Label()), len(snapshot.Records), len(snapshot.Experiments), activityTime(snapshot.Checkpoint), activityTime(snapshot.LastFullScan))
			return err
		})
	}}
	refreshCmd.Flags().BoolVar(&full, "full", false, "Reconcile every historical run, including old edits and deletions")
	group.AddCommand(list, refreshCmd, a.activityMarkCommand("read"), a.activityMarkCommand("unread"), a.activityMarkCommand("acknowledge"))
	return group
}

func validActivityView(view string) bool {
	switch view {
	case "running", "recent", "unread", "alerts", "acknowledged", "all":
		return true
	}
	return false
}

func (a *app) withActivity(cmd *cobra.Command, run func(*config.Config, core.Target, core.StateStore, core.ActivityStore) error) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	target, err := config.Resolve(cfg, a.targetID, a.trackingURI)
	if err != nil {
		return usageError{err}
	}
	state := a.state()
	defer state.Close()
	store, ok := state.(core.ActivityStore)
	if !ok {
		return fmt.Errorf("local state store does not support activity")
	}
	return run(cfg, target, state, store)
}

func (a *app) refreshActivity(ctx context.Context, cfg *config.Config, target core.Target, state core.StateStore, store core.ActivityStore, options activity.Options) (snapshot core.ActivitySnapshot, err error) {
	options.Settings, options.Alerts = cfg.Activity, cfg.Alerts
	options.Policy = func(ctx context.Context, experiment string) (core.ActivityPolicy, error) {
		view, found, err := state.LoadView(ctx, core.SourceKey(target), experiment)
		if err != nil || !found || view.Activity == nil {
			return core.ActivityPolicy{}, err
		}
		return *core.CloneActivityPolicy(view.Activity), nil
	}
	err = a.withSession(ctx, target, func(session *core.Session) error {
		var refreshErr error
		snapshot, refreshErr = activity.Refresh(ctx, session.Backend, store, core.SourceKey(target), options)
		return refreshErr
	})
	return snapshot, err
}

func (a *app) activityMarkCommand(action string) *cobra.Command {
	var all, restore bool
	var since string
	short := map[string]string{"read": "Mark observed runs read locally", "unread": "Mark observed runs unread, or rediscover a recent window", "acknowledge": "Acknowledge current anomaly episodes locally"}[action]
	cmd := &cobra.Command{Use: action + " [RUN_ID...]", Short: short, RunE: func(cmd *cobra.Command, args []string) error {
		modes := 0
		if len(args) > 0 {
			modes++
		}
		if all {
			modes++
		}
		if cmd.Flags().Changed("since") {
			modes++
		}
		if modes != 1 {
			return usagef("specify run IDs or --all%s", map[bool]string{true: " or --since 7d"}[action == "unread"])
		}
		for _, id := range args {
			if strings.TrimSpace(id) == "" {
				return usagef("run IDs cannot be empty")
			}
		}
		days := 0
		if cmd.Flags().Changed("since") {
			var err error
			days, err = activitySinceDays(since)
			if err != nil {
				return err
			}
		}
		return a.withActivity(cmd, func(cfg *config.Config, target core.Target, state core.StateStore, store core.ActivityStore) error {
			source := core.SourceKey(target)
			if days > 0 {
				snapshot, err := a.refreshActivity(cmd.Context(), cfg, target, state, store, activity.Options{UnreadDays: days})
				if err != nil {
					return err
				}
				return a.output(cmd, map[string]any{"target": target.ID, "action": action, "since": since, "unread": len(core.ActivityRecords(snapshot, "unread")), "local_only": true})
			}
			snapshot, err := store.LoadActivity(cmd.Context(), source)
			if err != nil {
				return err
			}
			ids := append([]string(nil), args...)
			if all {
				view := "all"
				if action == "read" {
					view = "unread"
				}
				if action == "acknowledge" {
					view = "alerts"
					if restore {
						view = "acknowledged"
					}
				}
				for _, row := range core.ActivityRecords(snapshot, view) {
					ids = append(ids, row.RunID)
				}
				sort.Strings(ids)
			}
			receipts := make([]core.ActivityReceipt, 0, len(ids))
			unique := make([]string, 0, len(ids))
			seen := map[string]bool{}
			for _, id := range ids {
				if seen[id] {
					continue
				}
				row, found := snapshot.Records[id]
				if !found {
					return usagef("run %q is not in the local activity index; run activity refresh first", id)
				}
				seen[id] = true
				unique = append(unique, id)
				receipts = append(receipts, core.ActivityReceipt{RunID: id, Revision: row.Revision})
			}
			if len(receipts) > 0 {
				switch action {
				case "read":
					err = store.MarkActivityRead(cmd.Context(), source, receipts)
				case "unread":
					err = store.MarkActivityUnread(cmd.Context(), source, unique)
				case "acknowledge":
					err = store.AcknowledgeActivity(cmd.Context(), source, receipts, !restore)
				}
			}
			if err != nil {
				return err
			}
			return a.output(cmd, map[string]any{"target": target.ID, "action": action, "runs": unique, "count": len(unique), "restored": restore, "local_only": true})
		})
	}}
	cmd.Flags().BoolVar(&all, "all", false, "Apply to all observed runs in the selected target")
	if action == "unread" {
		cmd.Flags().StringVar(&since, "since", "", "Poll and mark runs started or ended within this many days unread, e.g. 7d")
	}
	if action == "acknowledge" {
		cmd.Flags().BoolVar(&restore, "restore", false, "Restore acknowledged current episodes to the alerts inbox")
	}
	return cmd
}

func activitySinceDays(value string) (int, error) {
	if !strings.HasSuffix(value, "d") {
		return 0, usagef("--since must be a positive number of days, e.g. 7d")
	}
	days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
	if err != nil || days < 1 || days > 36500 {
		return 0, usagef("--since must be between 1d and 36500d")
	}
	return days, nil
}

func activityTime(ms int64) string {
	if ms <= 0 {
		return "never"
	}
	return time.UnixMilli(ms).Local().Format(time.RFC3339)
}

func (a *app) printActivity(cmd *cobra.Command, target core.Target, snapshot core.ActivitySnapshot, view string, rows []core.ActivityRecord, total int, cached bool) error {
	type row struct {
		core.ActivityRecord
		Unread   bool `json:"unread"`
		Alerting bool `json:"alerting"`
	}
	result := make([]row, 0, len(rows))
	for _, value := range rows {
		result = append(result, row{value, value.Unread(), value.HasAlerts()})
	}
	if a.jsonOutput {
		return a.output(cmd, map[string]any{"target": target.ID, "source": snapshot.Source, "view": view, "runs": result, "total": total, "cached": cached, "initialized": snapshot.Initialized, "complete": snapshot.Complete, "last_poll": snapshot.Checkpoint, "last_full_scan": snapshot.LastFullScan, "counts": snapshot.Counts, "errors": snapshot.Errors})
	}
	w := table(cmd.OutOrStdout())
	fmt.Fprintln(w, "UNREAD\tALERT\tEXPERIMENT\tRUN\tSTATUS\tENDED\tRUN ID")
	for _, record := range rows {
		unread, alert := "", ""
		if record.Unread() {
			unread = "*"
		}
		if record.HasAlerts() {
			alert = "!"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", unread, alert, cell(record.ExperimentName), cell(record.RunName), cell(record.Status), activityTime(record.EndTime), cell(record.RunID))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%d/%d %s · last poll %s · last full scan %s\n", len(rows), total, view, activityTime(snapshot.Checkpoint), activityTime(snapshot.LastFullScan))
	if err != nil {
		return err
	}
	for _, message := range snapshot.Errors {
		fmt.Fprintln(cmd.ErrOrStderr(), cell(message))
	}
	return nil
}
