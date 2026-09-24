// Package cli exposes the same MLflow operations as the dashboard to scripts.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/connection"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/localstate"
	"github.com/daviddwlee84/lazymlflow/internal/platform"
	"github.com/daviddwlee84/lazymlflow/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type Options struct {
	In         io.Reader
	Out        io.Writer
	Err        io.Writer
	Version    string
	Connector  core.Connector
	IsTerminal func() bool
	Dashboard  func(context.Context, tui.Options) error
	OpenURL    func(context.Context, string) error
	LoadConfig func(string) (*config.Config, error)
	State      core.StateStore
}

type app struct {
	options     Options
	configPath  string
	targetID    string
	trackingURI string
	jsonOutput  bool
	interactive bool
	mouse       bool
	connector   core.Connector
}

type usageError struct{ error }

func (e usageError) Unwrap() error { return e.error }

func usagef(format string, args ...any) error { return usageError{fmt.Errorf(format, args...)} }

// Execute owns diagnostics and cleanup, returning the documented process status.
func Execute(ctx context.Context, args []string, options Options) int {
	root := NewRoot(options)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var child *ChildExitError
	if errors.As(err, &child) {
		return child.Code
	}
	status, code := 1, "runtime_error"
	var usage usageError
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status, code = 130, "canceled"
	} else if errors.As(err, &usage) || strings.HasPrefix(err.Error(), "unknown command ") || strings.HasPrefix(err.Error(), "unknown flag:") {
		status, code = 2, "usage_error"
	}
	w := options.Err
	if w == nil {
		w = os.Stderr
	}
	jsonMode, _ := root.PersistentFlags().GetBool("json")
	if !jsonMode {
		jsonMode = jsonIntent(args)
	}
	if jsonMode {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": err.Error()}})
	} else {
		fmt.Fprintf(w, "Error: %s\n", cell(err.Error()))
	}
	return status
}

func jsonIntent(args []string) bool {
	value := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--json" || arg == "--json=true" {
			value = true
		}
		if arg == "--json=false" {
			value = false
		}
	}
	return value
}

func NewRoot(options Options) *cobra.Command {
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	if options.Err == nil {
		options.Err = os.Stderr
	}
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.LoadConfig == nil {
		options.LoadConfig = config.Load
	}
	if options.Dashboard == nil {
		options.Dashboard = tui.Run
	}
	if options.OpenURL == nil {
		options.OpenURL = platform.OpenURL
	}
	if options.IsTerminal == nil {
		options.IsTerminal = func() bool {
			in, okIn := options.In.(interface{ Fd() uintptr })
			out, okOut := options.Out.(interface{ Fd() uintptr })
			return okIn && okOut && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
		}
	}
	a := &app{options: options, connector: options.Connector}
	root := &cobra.Command{Use: "lazymlflow", Short: "Browse and compare MLflow experiments from your terminal", Version: options.Version, SilenceUsage: true, SilenceErrors: true}
	root.SetIn(options.In)
	root.SetOut(options.Out)
	root.SetErr(options.Err)
	root.SetVersionTemplate("{{if .Flags.GetBool \"json\"}}{\"version\":{{printf \"%q\" .Version}}}{{else}}{{.Name}} version {{.Version}}{{end}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "Config path (default: XDG lazymlflow/config.toml)")
	root.PersistentFlags().StringVar(&a.targetID, "target", "", "Configured target ID")
	root.PersistentFlags().StringVar(&a.trackingURI, "tracking-uri", "", "Temporary HTTP endpoint, local store, or SQLite URI")
	root.PersistentFlags().BoolVar(&a.jsonOutput, "json", false, "Write machine-readable JSON")
	root.PersistentFlags().BoolVar(&a.interactive, "interactive", false, "Open the dashboard or a supported target/server form")
	root.PersistentFlags().BoolVar(&a.mouse, "mouse", true, "Enable dashboard mouse controls (explicit value overrides saved preference)")
	root.MarkFlagsMutuallyExclusive("target", "tracking-uri")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("target") && cmd.Flags().Changed("tracking-uri") {
			return usagef("--target and --tracking-uri are mutually exclusive")
		}
		if cmd.Flags().Changed("target") && strings.TrimSpace(a.targetID) == "" {
			return usagef("--target cannot be empty")
		}
		if cmd.Flags().Changed("tracking-uri") && strings.TrimSpace(a.trackingURI) == "" {
			return usagef("--tracking-uri cannot be empty")
		}
		if a.interactive && (a.jsonOutput || !options.IsTerminal()) {
			return usagef("--interactive requires an input/output terminal and cannot be used with --json")
		}
		if a.interactive && cmd != root && !(cmd.Parent() != nil && ((cmd.Parent().Name() == "targets" && (cmd.Name() == "add" || cmd.Name() == "edit")) || (cmd.Parent().Name() == "server" && cmd.Name() == "init"))) {
			return usagef("--interactive is supported by the dashboard, targets add/edit, and server init")
		}
		return nil
	}
	root.Args = noArgs
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		if a.jsonOutput {
			return usagef("--json requires a data command; try lazymlflow experiments list --json")
		}
		if !options.IsTerminal() {
			return cmd.Help()
		}
		return a.dashboard(cmd.Context(), cmd.Flags().Changed("mouse"))
	}
	root.AddCommand(a.targetsCommand(), a.experimentsCommand(), a.runsCommand(), a.metricsCommand(), a.artifactsCommand(), a.openCommand(), a.configCommand(), a.doctorCommand(), a.viewCommand(), a.activityCommand(), a.datasetsCommand(), a.notesCommand(), a.promptCommand(), a.serverCommand(), a.modelsCommand())
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print the build version", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if a.jsonOutput {
			return a.output(cmd, map[string]string{"version": options.Version})
		}
		_, err := fmt.Fprintln(cmd.OutOrStdout(), options.Version)
		return err
	}})
	root.AddCommand(a.completionCommand())
	root.AddCommand(newUpgradeCommand(upgradeOptions{json: &a.jsonOutput, isTerminal: func(*cobra.Command) bool { return options.IsTerminal() }}))
	return root
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef("unexpected argument %q", args[0])
	}
	return nil
}
func exactArgs(count int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != count {
			return usagef("%s requires %d argument(s); see %s --help", cmd.CommandPath(), count, cmd.CommandPath())
		}
		return nil
	}
}
func rangeArgs(min, max int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < min || len(args) > max {
			return usagef("%s accepts %d–%d argument(s); see %s --help", cmd.CommandPath(), min, max, cmd.CommandPath())
		}
		return nil
	}
}

func (a *app) group(use, short string) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Args: noArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if a.jsonOutput {
			return usagef("%s --json requires a data subcommand", cmd.CommandPath())
		}
		return cmd.Help()
	}
	return cmd
}

func (a *app) load() (*config.Config, error) {
	cfg, err := a.options.LoadConfig(a.configPath)
	if err != nil {
		return nil, usageError{err}
	}
	return cfg, nil
}
func (a *app) resolve() (core.Target, error) {
	cfg, err := a.load()
	if err != nil {
		return core.Target{}, err
	}
	t, err := config.Resolve(cfg, a.targetID, a.trackingURI)
	if err != nil {
		return core.Target{}, usageError{err}
	}
	return t, nil
}
func (a *app) manager() core.Connector {
	if a.connector == nil {
		a.connector = connection.NewManager()
	}
	return a.connector
}

func (a *app) withSession(ctx context.Context, target core.Target, fn func(*core.Session) error) (err error) {
	m := a.manager()
	defer func() {
		if closeErr := m.Close(); err == nil {
			err = closeErr
		}
	}()
	s, err := m.Open(ctx, target)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := s.Close(); err == nil {
			err = closeErr
		}
	}()
	return fn(s)
}
func (a *app) session(ctx context.Context, fn func(*core.Session) error) error {
	t, err := a.resolve()
	if err != nil {
		return err
	}
	return a.withSession(ctx, t, fn)
}

func (a *app) output(cmd *cobra.Command, value any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func (a *app) dashboard(ctx context.Context, mouseExplicit bool) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	targets := make([]core.Target, 0, len(cfg.Targets)+1)
	for _, target := range cfg.Targets {
		normalized, err := config.NormalizeTarget(target, filepath.Dir(cfg.SourcePath()))
		if err != nil {
			return usageError{err}
		}
		targets = append(targets, normalized)
	}
	initial := ""
	if len(targets) > 0 || a.targetID != "" || a.trackingURI != "" || os.Getenv("LAZYMLFLOW_TARGET") != "" || os.Getenv("MLFLOW_TRACKING_URI") != "" {
		target, resolveErr := config.Resolve(cfg, a.targetID, a.trackingURI)
		if resolveErr != nil {
			return usageError{resolveErr}
		}
		if target.Transient {
			baseID := target.ID
			for suffix := 2; targetIndex(targets, target.ID) >= 0; suffix++ {
				target.ID = fmt.Sprintf("%s-%d", baseID, suffix)
			}
			targets = append(targets, target)
		}
		initial = target.ID
	}
	m := a.manager()
	defer m.Close()
	state := a.state()
	defer state.Close()
	var mouse *bool
	if mouseExplicit {
		mouse = &a.mouse
	}
	var configMu sync.Mutex
	return a.options.Dashboard(ctx, tui.Options{State: state, Mouse: mouse, Targets: targets, InitialTarget: initial, ConfigPath: cfg.SourcePath(), Connector: m, Input: a.options.In, Output: a.options.Out, MetricColumns: cfg.TUI.MetricColumns, ParameterColumns: cfg.TUI.ParameterColumns, RefreshSeconds: cfg.TUI.RefreshSeconds, PreviewMaxBytes: cfg.TUI.PreviewMaxBytes, Activity: cfg.Activity, Alerts: cfg.Alerts, SaveActivity: func(settings core.ActivitySettings, alerts core.AlertSettings) error {
		configMu.Lock()
		defer configMu.Unlock()
		previousSettings, previousAlerts := cfg.Activity, cfg.Alerts
		cfg.Activity, cfg.Alerts = settings, alerts
		if err := cfg.Save(a.configPath); err != nil {
			cfg.Activity, cfg.Alerts = previousSettings, previousAlerts
			return err
		}
		return nil
	}, SaveTargets: func(targets []core.Target, defaultID string) error {
		configMu.Lock()
		defer configMu.Unlock()
		cfg.Targets = nil
		for _, target := range targets {
			if !target.Transient {
				cfg.Targets = append(cfg.Targets, target)
			}
		}
		// Starting with --target changes only this session. The dashboard's
		// add/edit form does not express an intent to replace a saved default.
		if cfg.DefaultTarget != "" && targetIndex(cfg.Targets, cfg.DefaultTarget) >= 0 {
			defaultID = cfg.DefaultTarget
		}
		if targetIndex(cfg.Targets, defaultID) < 0 {
			defaultID = cfg.DefaultTarget
		}
		if targetIndex(cfg.Targets, defaultID) < 0 {
			defaultID = ""
		}
		cfg.DefaultTarget = defaultID
		return cfg.Save(a.configPath)
	}})
}

// Keep terminal control sequences in remote metadata from acting as terminal input.
func cell(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func (a *app) completionCommand() *cobra.Command {
	return &cobra.Command{Use: "completion [bash|zsh|fish|powershell]", Short: "Generate shell completion", Args: exactArgs(1), ValidArgs: []string{"bash", "zsh", "fish", "powershell"}, RunE: func(cmd *cobra.Command, args []string) error {
		if a.jsonOutput {
			return usagef("completion does not support --json")
		}
		switch args[0] {
		case "bash":
			return cmd.Root().GenBashCompletionV2(cmd.OutOrStdout(), true)
		case "zsh":
			return cmd.Root().GenZshCompletion(cmd.OutOrStdout())
		case "fish":
			return cmd.Root().GenFishCompletion(cmd.OutOrStdout(), true)
		case "powershell":
			return cmd.Root().GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
		default:
			return usagef("unknown shell %q", args[0])
		}
	}}
}

// state is deliberately constructed only by a dashboard or explicit local-view operation.
func (a *app) state() core.StateStore {
	if a.options.State != nil {
		return a.options.State
	}
	return localstate.New("")
}
