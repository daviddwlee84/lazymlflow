package cli

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
	"github.com/spf13/cobra"
)

func (a *app) artifactsCommand() *cobra.Command {
	group := a.group("artifacts", "Browse and download run artifacts")
	ls := &cobra.Command{Use: "ls RUN_ID [PATH]", Short: "List an artifact directory", Args: rangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		path := ""
		if len(args) > 1 {
			path = args[1]
		}
		return a.session(cmd.Context(), func(s *core.Session) error {
			page, err := s.Backend.ListArtifacts(cmd.Context(), args[0], path)
			if err != nil {
				return err
			}
			if page.Files == nil {
				page.Files = []core.Artifact{}
			}
			if a.jsonOutput {
				return a.output(cmd, page)
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "TYPE\tBYTES\tPATH")
			for _, f := range page.Files {
				kind := "file"
				size := fmt.Sprint(f.FileSize)
				if f.IsDir {
					kind = "dir"
					size = "—"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", kind, size, cell(f.Path))
			}
			return w.Flush()
		})
	}}
	var destination string
	var overwrite bool
	download := &cobra.Command{Use: "download RUN_ID [PATH] --dest OUTPUT_PATH", Short: "Download a file or directory to an exact output path", Args: rangeArgs(1, 2), Example: "  lazymlflow artifacts download RUN_ID model --dest ./downloaded-model\n  lazymlflow artifacts download RUN_ID metrics.json --dest ./metrics.json", RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(destination) == "" {
			return usagef("--dest is required and names the exact output file or directory")
		}
		path := ""
		if len(args) > 1 {
			path = args[1]
		}
		return a.session(cmd.Context(), func(s *core.Session) error {
			result, err := s.Backend.DownloadArtifact(cmd.Context(), core.DownloadRequest{RunID: args[0], Path: path, Destination: destination, Overwrite: overwrite}, nil)
			if err != nil {
				return err
			}
			if a.jsonOutput {
				return a.output(cmd, result)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Downloaded %d file(s), %d bytes → %s\n", result.Files, result.Bytes, cell(result.Path))
			return err
		})
	}}
	download.Flags().StringVar(&destination, "dest", "", "Exact output file or directory path (required)")
	download.Flags().BoolVar(&overwrite, "overwrite", false, "Replace an existing destination")
	group.AddCommand(ls, download)
	return group
}

func (a *app) openCommand() *cobra.Command {
	var experiment string
	var printOnly bool
	cmd := &cobra.Command{Use: "open [RUN_ID]", Short: "Open the tracking UI, an experiment, or a run", Args: rangeArgs(0, 1), Long: "Open the selected resource in a web browser. A managed local server or SSH tunnel\nstays alive until Ctrl+C; direct remote endpoints return immediately.\n--print is available only for direct remote targets.", RunE: func(cmd *cobra.Command, args []string) error {
		if a.jsonOutput && !printOnly {
			return usagef("open --json requires --print and a remote target")
		}
		t, err := a.resolve()
		if err != nil {
			return err
		}
		uri, err := url.Parse(t.TrackingURI)
		if err != nil {
			return usageError{err}
		}
		remote := uri.Scheme == "http" || uri.Scheme == "https"
		if printOnly && (!remote || t.SSHHost != "") {
			return usagef("--print requires a direct remote target; use open without --print to keep a local server or SSH tunnel running")
		}
		return a.withSession(cmd.Context(), t, func(s *core.Session) error {
			runID := ""
			experimentID := experiment
			if len(args) > 0 {
				r, err := s.Backend.GetRun(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				runID = r.ID()
				if experimentID != "" && experimentID != r.Info.ExperimentID {
					return usagef("run %s belongs to experiment %s, not %s", runID, r.Info.ExperimentID, experimentID)
				}
				experimentID = r.Info.ExperimentID
			}
			base := s.WebURL
			if base == "" {
				base = s.BaseURL
			}
			resource := platform.ResourceURL(base, experimentID, runID)
			if !printOnly {
				if err := a.options.OpenURL(cmd.Context(), resource); err != nil {
					return err
				}
			}
			if a.jsonOutput {
				if err := a.output(cmd, map[string]string{"url": resource}); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), resource); err != nil {
					return err
				}
			}
			if (s.Local || t.SSHHost != "") && !printOnly {
				fmt.Fprintln(cmd.ErrOrStderr(), "Managed MLflow connection is running. Press Ctrl+C to stop it.")
				<-cmd.Context().Done()
				return cmd.Context().Err()
			}
			return nil
		})
	}}
	cmd.Flags().StringVar(&experiment, "experiment", "", "Experiment ID (omit RUN_ID to open the experiment)")
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print a remote URL without opening a browser")
	return cmd
}

func (a *app) configCommand() *cobra.Command {
	group := a.group("config", "Inspect configuration")
	group.AddCommand(&cobra.Command{Use: "show", Short: "Show redacted configuration and target selection", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := a.load()
		if err != nil {
			return err
		}
		safe := config.Redacted(cfg)
		path := cfg.SourcePath()
		if path == "" {
			path = config.DefaultPath()
		}
		t, resolveErr := config.Resolve(cfg, a.targetID, a.trackingURI)
		result := map[string]any{"path": path, "state_path": localstate.DefaultPath(), "config": safe}
		if resolveErr == nil {
			result["effective_target"] = config.Redacted(&config.Config{Targets: []core.Target{t}}).Targets[0]
		} else {
			result["selection_error"] = resolveErr.Error()
		}
		if a.jsonOutput {
			return a.output(cmd, result)
		}
		w := table(cmd.OutOrStdout())
		fmt.Fprintf(w, "Config\t%s\nLocal preferences\t%s\nDefault target\t%s\n", cell(path), cell(localstate.DefaultPath()), cell(cfg.DefaultTarget))
		if resolveErr == nil {
			fmt.Fprintf(w, "Effective target\t%s\nTracking URI\t%s\n", cell(t.ID), cell(config.RedactURI(t.TrackingURI)))
		} else {
			fmt.Fprintf(w, "Selection\t%s\n", cell(resolveErr.Error()))
		}
		if err := w.Flush(); err != nil {
			return err
		}
		return a.output(cmd, safe)
	}})
	return group
}

func (a *app) doctorCommand() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{Use: "doctor", Short: "Inspect configuration, available runtimes, and tracking connectivity", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := a.load()
		if err != nil {
			return err
		}
		result := map[string]any{"version": a.options.Version, "platform": runtime.GOOS + "/" + runtime.GOARCH, "config_path": cfg.SourcePath(), "configured_targets": len(cfg.Targets), "state_path": localstate.DefaultPath()}
		for _, name := range []string{"uv", "python3", "ssh"} {
			path, err := exec.LookPath(name)
			if err != nil {
				result[name] = "not found"
			} else {
				result[name] = path
			}
		}
		t, resolveErr := config.Resolve(cfg, a.targetID, a.trackingURI)
		if resolveErr != nil {
			result["target_error"] = resolveErr.Error()
		} else {
			result["target"] = t.ID
			result["tracking_uri"] = config.RedactURI(t.TrackingURI)
		}
		var connectivityErr error
		if !offline && resolveErr == nil {
			connectivityErr = a.withSession(cmd.Context(), t, func(s *core.Session) error {
				_, err := s.Backend.SearchExperiments(cmd.Context(), core.ExperimentQuery{MaxResults: 1, ViewType: "ACTIVE_ONLY"})
				if err != nil {
					return err
				}
				result["connection"] = "ok"
				result["runtime_version"] = versionLabel(s.RuntimeVersion)
				result["local"] = s.Local
				return nil
			})
			if connectivityErr != nil {
				result["connection_error"] = connectivityErr.Error()
			}
		}
		if err := a.output(cmd, result); err != nil {
			return err
		}
		return connectivityErr
	}}
	cmd.Flags().BoolVar(&offline, "offline", false, "Skip connection and runtime preparation")
	return cmd
}
