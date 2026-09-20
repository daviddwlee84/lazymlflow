package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/spf13/cobra"
)

func (a *app) targetsCommand() *cobra.Command {
	group := a.group("targets", "Manage tracking servers and local stores")
	group.AddCommand(&cobra.Command{Use: "list", Short: "List configured targets", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := a.load()
		if err != nil {
			return err
		}
		safe := config.Redacted(cfg)
		if a.jsonOutput {
			return a.output(cmd, map[string]any{"default_target": cfg.DefaultTarget, "targets": safe.Targets})
		}
		w := table(cmd.OutOrStdout())
		fmt.Fprintln(w, "ID\tNAME\tDEFAULT\tTRACKING URI\tSSH HOST")
		for _, t := range safe.Targets {
			mark := ""
			if t.ID == cfg.DefaultTarget {
				mark = "*"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", cell(t.ID), cell(t.Label()), mark, cell(t.TrackingURI), cell(t.SSHHost))
		}
		return w.Flush()
	}})
	group.AddCommand(a.targetWriteCommand(false), a.targetWriteCommand(true))
	group.AddCommand(&cobra.Command{Use: "remove ID", Short: "Remove a target from local config (MLflow data is untouched)", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := a.load()
		if err != nil {
			return err
		}
		index := targetIndex(cfg.Targets, args[0])
		if index < 0 {
			return usagef("target %q does not exist", args[0])
		}
		cfg.Targets = append(cfg.Targets[:index], cfg.Targets[index+1:]...)
		if cfg.DefaultTarget == args[0] {
			cfg.DefaultTarget = ""
		}
		if err := cfg.Save(a.configPath); err != nil {
			return err
		}
		if a.jsonOutput {
			return a.output(cmd, map[string]any{"removed": args[0]})
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed target %s from config.\n", cell(args[0]))
		return err
	}})
	group.AddCommand(&cobra.Command{Use: "default ID", Short: "Choose the default configured target", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := a.load()
		if err != nil {
			return err
		}
		if targetIndex(cfg.Targets, args[0]) < 0 {
			return usagef("target %q does not exist", args[0])
		}
		cfg.DefaultTarget = args[0]
		if err := cfg.Save(a.configPath); err != nil {
			return err
		}
		if a.jsonOutput {
			return a.output(cmd, map[string]string{"default_target": args[0]})
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Default target: %s\n", cell(args[0]))
		return err
	}})
	group.AddCommand(&cobra.Command{Use: "test [ID]", Short: "Connect and verify experiment access", Args: rangeArgs(0, 1), RunE: func(cmd *cobra.Command, args []string) error {
		var t core.Target
		var err error
		if len(args) > 0 {
			if a.trackingURI != "" {
				return usagef("targets test ID cannot be combined with --tracking-uri")
			}
			cfg, e := a.load()
			if e != nil {
				return e
			}
			t, err = config.Resolve(cfg, args[0], "")
			if err != nil {
				return usageError{err}
			}
		} else {
			t, err = a.resolve()
			if err != nil {
				return err
			}
		}
		return a.withSession(cmd.Context(), t, func(s *core.Session) error {
			_, err := s.Backend.SearchExperiments(cmd.Context(), core.ExperimentQuery{MaxResults: 1, ViewType: "ACTIVE_ONLY"})
			if err != nil {
				return err
			}
			if a.jsonOutput {
				return a.output(cmd, map[string]any{"ok": true, "target": t.ID, "tracking_uri": config.RedactURI(t.TrackingURI), "base_url": config.RedactURI(s.BaseURL), "runtime_version": s.RuntimeVersion, "local": s.Local})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Connected to %s (%s)\nMLflow: %s\n", cell(t.Label()), cell(config.RedactURI(s.BaseURL)), cell(versionLabel(s.RuntimeVersion)))
			return err
		})
	}})
	return group
}

func (a *app) targetWriteCommand(edit bool) *cobra.Command {
	verb := "add"
	short := "Add a tracking target (opens a form when called without arguments)"
	if edit {
		verb = "edit"
		short = "Edit a target; omitted flags keep existing values"
	}
	var supplied core.Target
	var makeDefault bool
	cmd := &cobra.Command{Use: verb + " [ID]", Short: short, Args: rangeArgs(0, 1), Example: "  lazymlflow targets " + verb + " local --uri ./mlruns\n  lazymlflow targets " + verb + " staging --uri https://mlflow.example.com --token-env MLFLOW_TRACKING_TOKEN\n  lazymlflow targets " + verb + " --interactive"}
	f := cmd.Flags()
	f.StringVar(&supplied.TrackingURI, "uri", "", "Tracking HTTP endpoint, local folder, file URI, or SQLite URI")
	f.StringVar(&supplied.SSHHost, "ssh-host", "", "OpenSSH host/alias; tracking URI is reached from that host")
	f.StringVar(&supplied.Name, "name", "", "Display name")
	f.StringVar(&supplied.WebURL, "web-url", "", "Browser URL when different from the tracking endpoint")
	f.StringVar(&supplied.WorkingDir, "working-dir", "", "Original working directory for local artifacts")
	f.StringVar(&supplied.Python, "python", "", "Existing Python executable or virtual environment (never modified)")
	f.StringVar(&supplied.MLflowVersion, "mlflow-version", "", "MLflow version for managed uv runtime")
	f.StringVar(&supplied.ArtifactsDestination, "artifacts-destination", "", "Original local/cloud destination for mlflow-artifacts URIs")
	f.StringVar(&supplied.TokenEnv, "token-env", "", "Environment variable holding bearer token")
	f.StringVar(&supplied.UsernameEnv, "username-env", "", "Environment variable holding HTTP basic username")
	f.StringVar(&supplied.PasswordEnv, "password-env", "", "Environment variable holding HTTP basic password")
	f.StringVar(&supplied.CAFile, "ca-file", "", "Custom CA certificate file")
	f.StringSliceVar(&supplied.ExtraPackages, "extra-package", nil, "Additional Python artifact provider packages for managed uv runtime")
	f.StringToStringVar(&supplied.Env, "env", nil, "Child environment variable to source environment variable, e.g. AWS_PROFILE=MY_AWS_PROFILE")
	f.BoolVar(&makeDefault, "default", false, "Make this the default target")
	// Keep supplied values separate from existing settings so explicit empty values
	// clear optional fields while omitted flags leave those fields unchanged.
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := a.load()
		if err != nil {
			if !edit && a.configPath != "" && errors.Is(err, os.ErrNotExist) {
				cfg = config.New(a.configPath)
			} else {
				return err
			}
		}
		business := len(args) > 0
		for _, name := range targetFlagNames {
			if cmd.Flags().Changed(name) {
				business = true
			}
		}
		if cmd.Flags().Changed("default") || cmd.Flags().Changed("tracking-uri") {
			business = true
		}
		wizard := a.interactive || (!business && a.options.IsTerminal() && !a.jsonOutput)
		if edit && len(args) == 1 && !hasTargetChanges(cmd) && a.options.IsTerminal() && !a.jsonOutput {
			wizard = true
		}
		if wizard && (!a.options.IsTerminal() || a.jsonOutput) {
			return usagef("the target form requires a terminal and cannot use --json")
		}
		if edit && len(args) == 0 {
			return usagef("targets edit requires an ID; see lazymlflow targets list")
		}
		draft := core.Target{}
		if len(args) > 0 {
			draft.ID = args[0]
		}
		index := targetIndex(cfg.Targets, draft.ID)
		if edit {
			if index < 0 {
				return usagef("target %q does not exist", draft.ID)
			}
			draft, err = config.NormalizeTarget(cfg.Targets[index], filepath.Dir(cfg.SourcePath()))
			if err != nil {
				return usageError{err}
			}
		} else if index >= 0 {
			return usagef("target %q already exists; use targets edit", draft.ID)
		}
		applyTargetFlags(cmd, &draft, supplied)
		if cmd.Flags().Changed("tracking-uri") {
			if cmd.Flags().Changed("uri") {
				return usagef("use either --uri or --tracking-uri, not both")
			}
			draft.TrackingURI = a.trackingURI
		}
		if !wizard && (draft.ID == "" || draft.TrackingURI == "") {
			return usagef("target ID and --uri are required; example: lazymlflow targets add local --uri ./mlruns (or use --interactive)")
		}
		if edit && !wizard && !hasTargetChanges(cmd) {
			return usagef("no target changes supplied; use --interactive or provide a field flag")
		}
		// Reject invalid supplied values before opening a form. Missing values
		// receive placeholders only for this syntax check; the submitted draft
		// still goes through the complete validator before saving.
		validationDraft := draft
		if validationDraft.ID == "" {
			validationDraft.ID = "new-target"
		}
		if validationDraft.TrackingURI == "" {
			if cmd.Flags().Changed("uri") {
				return usagef("--uri cannot be empty")
			}
			validationDraft.TrackingURI = "https://example.invalid"
		}
		if _, err := config.NormalizeTarget(validationDraft, ""); err != nil {
			return usageError{err}
		}
		if wizard {
			path := cfg.SourcePath()
			if path == "" {
				path = config.DefaultPath()
			}
			draft, err = runTargetForm(cmd.Context(), a.options.In, a.options.Out, draft, edit, path, a.mouse, func(target core.Target) error {
				if !edit && targetIndex(cfg.Targets, target.ID) >= 0 {
					return usagef("target %q already exists; choose another ID", target.ID)
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if !edit && targetIndex(cfg.Targets, draft.ID) >= 0 {
			return usagef("target %q already exists", draft.ID)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		draft, err = config.NormalizeTarget(draft, cwd)
		if err != nil {
			return usageError{err}
		}
		if edit {
			cfg.Targets[index] = draft
		} else {
			cfg.Targets = append(cfg.Targets, draft)
		}
		if makeDefault {
			cfg.DefaultTarget = draft.ID
		}
		if err := cfg.Save(a.configPath); err != nil {
			return err
		}
		safe := config.Redacted(&config.Config{Targets: []core.Target{draft}}).Targets[0]
		if a.jsonOutput {
			return a.output(cmd, safe)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved target %s → %s\n", cell(draft.ID), cell(config.RedactURI(draft.TrackingURI)))
		return err
	}
	return cmd
}

var targetFlagNames = []string{"uri", "ssh-host", "name", "web-url", "working-dir", "python", "mlflow-version", "artifacts-destination", "token-env", "username-env", "password-env", "ca-file", "extra-package", "env"}

func hasTargetChanges(cmd *cobra.Command) bool {
	for _, key := range targetFlagNames {
		if cmd.Flags().Changed(key) {
			return true
		}
	}
	return cmd.Flags().Changed("default") || cmd.Flags().Changed("tracking-uri")
}
func applyTargetFlags(cmd *cobra.Command, dst *core.Target, src core.Target) {
	fields := []struct {
		name  string
		to    *string
		value string
	}{{"uri", &dst.TrackingURI, src.TrackingURI}, {"ssh-host", &dst.SSHHost, src.SSHHost}, {"name", &dst.Name, src.Name}, {"web-url", &dst.WebURL, src.WebURL}, {"working-dir", &dst.WorkingDir, src.WorkingDir}, {"python", &dst.Python, src.Python}, {"mlflow-version", &dst.MLflowVersion, src.MLflowVersion}, {"artifacts-destination", &dst.ArtifactsDestination, src.ArtifactsDestination}, {"token-env", &dst.TokenEnv, src.TokenEnv}, {"username-env", &dst.UsernameEnv, src.UsernameEnv}, {"password-env", &dst.PasswordEnv, src.PasswordEnv}, {"ca-file", &dst.CAFile, src.CAFile}}
	for _, field := range fields {
		if cmd.Flags().Changed(field.name) {
			*field.to = field.value
		}
	}
	if cmd.Flags().Changed("extra-package") {
		dst.ExtraPackages = src.ExtraPackages
	}
	if cmd.Flags().Changed("env") {
		dst.Env = src.Env
	}
}
func targetIndex(targets []core.Target, id string) int {
	for i, t := range targets {
		if t.ID == id {
			return i
		}
	}
	return -1
}
func versionLabel(version string) string {
	if strings.TrimSpace(version) == "" {
		return "version not reported by server"
	}
	return version
}
