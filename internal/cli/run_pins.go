package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/spf13/cobra"
)

func (a *app) withRunPins(cmd *cobra.Command, action func(core.Target, core.RunPinStore) error) error {
	target, err := a.resolve()
	if err != nil {
		return err
	}
	state := a.state()
	defer state.Close()
	pins, ok := state.(core.RunPinStore)
	if !ok {
		return fmt.Errorf("local state store does not support run pins")
	}
	return action(target, pins)
}

func (a *app) runsPinnedCommand() *cobra.Command {
	return &cobra.Command{Use: "pinned", Short: "List this target's pinned runs using cached metadata (no connection)", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return a.withRunPins(cmd, func(target core.Target, store core.RunPinStore) error {
			source := core.SourceKey(target)
			pins, err := store.LoadRunPins(cmd.Context(), source)
			if err != nil {
				return err
			}
			if pins == nil {
				pins = []core.RunPin{}
			}
			if a.jsonOutput {
				return a.output(cmd, map[string]any{"target": target.ID, "source": source, "pins": pins, "cached": true, "local_only": true})
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Pinned runs for %s (cached metadata)\n", cell(target.Label())); err != nil {
				return err
			}
			if len(pins) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No pinned runs.")
				return err
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "RUN ID\tNAME\tEXPERIMENT\tSTATUS\tPINNED\tOBSERVED")
			for _, pin := range pins {
				experiment := pin.ExperimentName
				if experiment == "" {
					experiment = pin.ExperimentID
				} else {
					experiment += " (" + pin.ExperimentID + ")"
				}
				status := pin.Status
				if pin.LifecycleStage == "deleted" {
					status += " / deleted"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", cell(pin.RunID), cell(pin.RunName), cell(experiment), cell(status), timestamp(pin.PinnedAt), timestamp(pin.ObservedAt))
			}
			return w.Flush()
		})
	}}
}

func runPinArgs(cmd *cobra.Command, args []string) error {
	if err := exactArgs(1)(cmd, args); err != nil {
		return err
	}
	if strings.TrimSpace(args[0]) == "" {
		return usagef("run ID cannot be empty")
	}
	return nil
}

func (a *app) runPinCommand() *cobra.Command {
	return &cobra.Command{Use: "pin RUN_ID", Short: "Pin a run locally across experiments on the current target", Args: runPinArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return a.withRunPins(cmd, func(target core.Target, store core.RunPinStore) error {
			var pin core.RunPin
			if err := a.withSession(cmd.Context(), target, func(session *core.Session) error {
				run, err := session.Backend.GetRun(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if run.ID() != args[0] || strings.TrimSpace(run.Info.ExperimentID) == "" {
					return fmt.Errorf("run lookup returned a different or missing run/experiment identity")
				}
				observed := time.Now().UnixMilli()
				experiment, err := session.Backend.GetExperiment(cmd.Context(), run.Info.ExperimentID)
				if err != nil {
					return err
				}
				if experiment.ID != run.Info.ExperimentID {
					return fmt.Errorf("experiment lookup returned a different or missing experiment identity")
				}
				pin = core.RunPin{RunID: run.ID(), ExperimentID: experiment.ID, RunName: run.Name(), ExperimentName: experiment.Name, Status: run.Info.Status, StartTime: run.Info.StartTime, EndTime: run.Info.EndTime, LifecycleStage: run.Info.LifecycleStage, ObservedAt: observed}
				return store.SaveRunPin(cmd.Context(), core.SourceKey(target), pin)
			}); err != nil {
				return err
			}
			if a.jsonOutput {
				return a.output(cmd, map[string]any{"target": target.ID, "run_id": pin.RunID, "pinned": true, "local_only": true})
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Pinned %s (%s) locally on %s.\n", cell(pin.RunName), cell(pin.RunID), cell(target.Label()))
			return err
		})
	}}
}

func (a *app) runUnpinCommand() *cobra.Command {
	return &cobra.Command{Use: "unpin RUN_ID", Short: "Remove a local run pin without connecting", Args: runPinArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return a.withRunPins(cmd, func(target core.Target, store core.RunPinStore) error {
			if err := store.DeleteRunPin(cmd.Context(), core.SourceKey(target), args[0]); err != nil {
				return err
			}
			if a.jsonOutput {
				return a.output(cmd, map[string]any{"target": target.ID, "run_id": args[0], "pinned": false, "local_only": true})
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "Removed local pin for %s on %s.\n", cell(args[0]), cell(target.Label()))
			return err
		})
	}}
}
