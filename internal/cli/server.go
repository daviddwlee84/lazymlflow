package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/server"
	"github.com/daviddwlee84/lazymlflow/internal/serverform"
	"github.com/spf13/cobra"
)

func (a *app) serverCommand() *cobra.Command {
	g := a.group("server", "Recommend, generate and manage an explicitly owned persistent MLflow stack")
	g.AddCommand(a.serverRecommendCommand(), a.serverInitCommand())
	for _, action := range []string{"up", "status", "logs", "down", "ca-export", "spec"} {
		g.AddCommand(a.serverLifecycleCommand(action))
	}
	return g
}

func (a *app) serverRecommendCommand() *cobra.Command {
	var needs server.Requirements
	cmd := &cobra.Command{Use: "recommend", Short: "Recommend a setup from training and storage needs (no writes)", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if needs.Provider != "" && needs.Provider != "rustfs" && needs.Provider != "seaweedfs" {
			return usagef("provider must be rustfs or seaweedfs")
		}
		r := server.Recommend(needs)
		if a.jsonOutput {
			return a.output(cmd, r)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Suggested setup: %s + %s artifacts\nAccess: %s; TLS: %s; authentication: %s\n", r.Spec.Backend, r.Spec.Artifacts, r.Spec.Access, r.Spec.TLS, r.Spec.Auth)
		for _, s := range r.Reasons {
			fmt.Fprintln(cmd.OutOrStdout(), "- "+s)
		}
		for _, s := range r.Caveats {
			fmt.Fprintln(cmd.OutOrStdout(), "Note: "+s)
		}
		return nil
	}}
	f := cmd.Flags()
	f.BoolVar(&needs.Team, "team", false, "Multiple users or concurrent training jobs")
	f.BoolVar(&needs.RemoteTraining, "remote-training", false, "Training clients connect from another machine")
	f.StringVar(&needs.ExistingNAS, "nas", "", "Existing NAS artifact directory")
	f.BoolVar(&needs.NeedObjectStorage, "object-storage", false, "Need an S3 object-storage service")
	f.StringVar(&needs.S3Endpoint, "s3-endpoint", "", "Existing S3-compatible endpoint")
	f.StringVar(&needs.Provider, "provider", "", "Managed S3 preference: rustfs or seaweedfs")
	return cmd
}

func (a *app) serverInitCommand() *cobra.Command {
	spec := server.DefaultSpec()
	var dir, fromSpec, adminEnv string
	var start, register, dryRun bool
	cmd := &cobra.Command{Use: "init [ID]", Short: "Generate a new stack directory; bare terminal invocation opens setup", Args: rangeArgs(0, 1), Example: "  lazymlflow server init personal --dir ./mlflow-server\n  lazymlflow server init team --dir ./team-mlflow --backend postgres --artifacts rustfs --access lan --hostname mlflow.example.internal --start\n  lazymlflow server init --interactive"}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", "", "New output directory (must not exist)")
	f.StringVar(&fromSpec, "from-spec", "", "Read a ServerSpec JSON document; do not combine with layout flags")
	f.StringVar(&spec.Backend, "backend", spec.Backend, "Metadata backend: sqlite or postgres")
	f.StringVar(&spec.Artifacts, "artifacts", spec.Artifacts, "Artifact storage: local, nas, s3, rustfs or seaweedfs")
	f.StringVar(&spec.Access, "access", spec.Access, "Access: loopback or lan (LAN defaults to HTTPS/native auth)")
	f.StringVar(&spec.TLS, "tls", spec.TLS, "TLS: off, internal or provided; off is an explicit opt-out")
	f.StringVar(&spec.Auth, "auth", spec.Auth, "Authentication: off or native; off is an explicit opt-out")
	f.StringVar(&spec.Hostname, "hostname", "", "Client-reachable DNS name or IP for TLS and Host validation")
	f.StringVar(&spec.BindAddress, "bind", spec.BindAddress, "Published interface IP address")
	f.IntVar(&spec.Port, "port", spec.Port, "Published port (HTTP 8000; HTTPS 8443)")
	f.StringVar(&spec.ArtifactPath, "artifact-path", "", "Existing local/NAS artifact directory; blank local uses a Docker volume")
	f.StringVar(&spec.NASMount, "nas-mount", "", "Exact existing NAS mount point (checked before start)")
	f.StringVar(&spec.CertFile, "cert-file", "", "Existing PEM certificate for provided TLS")
	f.StringVar(&spec.KeyFile, "key-file", "", "Existing PEM private key for provided TLS")
	f.StringVar(&spec.S3Endpoint, "s3-endpoint", "", "External S3 HTTP(S) endpoint; blank uses AWS S3")
	f.StringVar(&spec.S3Bucket, "s3-bucket", "", "Artifact bucket (default mlflow)")
	f.StringVar(&spec.S3Region, "s3-region", "", "S3 region (default us-east-1)")
	f.StringVar(&spec.S3AccessKeyEnv, "s3-access-key-env", "", "Environment variable with external S3 access key")
	f.StringVar(&spec.S3SecretKeyEnv, "s3-secret-key-env", "", "Environment variable with external S3 secret key")
	f.StringVar(&spec.AdminUsername, "admin-username", spec.AdminUsername, "Stable initial native-auth administrator username")
	f.StringVar(&adminEnv, "admin-password-env", "", "Optional environment variable containing bootstrap password; blank generates a private file")
	f.BoolVar(&start, "start", false, "Start the generated stack after writing files")
	f.BoolVar(&register, "register-target", false, "Add a connection target without changing the saved default")
	f.BoolVar(&dryRun, "dry-run", false, "Validate and display the spec without creating files or starting services")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		business := len(args) > 0
		for _, name := range append(append([]string{}, serverLayoutFlags...), "dir", "from-spec", "start", "register-target", "dry-run", "admin-password-env") {
			if f.Changed(name) {
				business = true
			}
		}
		wizard := a.interactive || (!business && a.options.IsTerminal() && !a.jsonOutput)
		if wizard && dryRun {
			return usagef("--dry-run cannot be combined with interactive setup")
		}
		if fromSpec != "" {
			conflict := len(args) > 0
			for _, name := range serverLayoutFlags {
				if f.Changed(name) {
					conflict = true
				}
			}
			if conflict {
				return usagef("--from-spec cannot be combined with ID or layout flags")
			}
			data, err := os.ReadFile(fromSpec)
			if err != nil {
				return usageError{err}
			}
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(&spec); err != nil {
				return usagef("invalid server spec: %v", err)
			}
			if err = decoder.Decode(&struct{}{}); err != io.EOF {
				return usagef("server spec must contain exactly one JSON object")
			}
		} else {
			if len(args) > 0 {
				spec.ID = args[0]
			}
			if spec.Access == "lan" {
				if !f.Changed("tls") {
					spec.TLS = "internal"
				}
				if !f.Changed("auth") {
					spec.Auth = "native"
				}
				if !f.Changed("bind") {
					spec.BindAddress = "0.0.0.0"
				}
			}
			if spec.TLS != "off" && !f.Changed("port") {
				spec.Port = 8443
			}
		}
		if f.Changed("port") && (spec.Port < 1 || spec.Port > 65535) {
			return usagef("port must be between 1 and 65535")
		}
		validation := spec
		if wizard {
			if validation.ID == "" {
				validation.ID = "draft"
			}
			if validation.Hostname == "" && (validation.TLS != "off" || validation.Access == "lan") {
				validation.Hostname = "mlflow.invalid"
			}
			if validation.Artifacts == "nas" {
				if validation.ArtifactPath == "" {
					validation.ArtifactPath = "/mnt/nas"
				}
				if validation.NASMount == "" {
					validation.NASMount = "/mnt/nas"
				}
			}
			if validation.TLS == "provided" {
				if validation.CertFile == "" {
					validation.CertFile = "/tmp/server.crt"
				}
				if validation.KeyFile == "" {
					validation.KeyFile = "/tmp/server.key"
				}
			}
		}
		if !wizard && (spec.ID == "" || dir == "") {
			return usagef("server init requires ID and --dir; example: lazymlflow server init personal --dir ./mlflow-server")
		}
		if _, err := server.Normalize(validation); err != nil {
			return usageError{err}
		}
		result := serverform.Result{Directory: dir, Spec: spec, Start: start, RegisterTarget: register, AdminPasswordEnv: adminEnv}
		if wizard {
			var err error
			result, err = a.runServerForm(cmd.Context(), dir, spec, result)
			if err != nil {
				return err
			}
		} else if spec.ID == "" || dir == "" {
			return usagef("server init requires ID and --dir; example: lazymlflow server init personal --dir ./mlflow-server")
		}
		if dryRun {
			normalized, err := server.Normalize(result.Spec)
			if err != nil {
				return usageError{err}
			}
			return a.output(cmd, map[string]any{"dry_run": true, "directory": dir, "spec": normalized, "start": start, "register_target": register})
		}
		stack, err := a.applyServerInit(cmd.Context(), result)
		if err != nil {
			return err
		}
		if a.jsonOutput {
			return a.output(cmd, map[string]any{"directory": stack.Dir, "project": stack.Project, "tracking_uri": stack.TrackingURI, "spec": stack.Spec, "files": stack.Files, "started": result.Start, "target_registered": result.RegisterTarget, "admin_password_file": func() string {
				if stack.Spec.Auth == "native" {
					return stack.AdminPasswordPath()
				}
				return ""
			}()})
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Created stack %s in %s\nTracking URL: %s\n", cell(stack.Spec.ID), cell(stack.Dir), cell(stack.TrackingURI))
		if result.Start {
			fmt.Fprintln(cmd.OutOrStdout(), "Stack started; it continues running after lazymlflow exits.")
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Start: lazymlflow server up --dir %s\n", "'"+strings.ReplaceAll(stack.Dir, "'", "'\"'\"'")+"'")
		}
		if stack.Spec.Auth == "native" {
			fmt.Fprintf(cmd.OutOrStdout(), "Bootstrap username: %s\nInitial password file: %s\nManage members and roles at %s/admin\n", cell(stack.Spec.AdminUsername), cell(stack.AdminPasswordPath()), cell(stack.TrackingURI))
		}
		if stack.Spec.Auth == "off" {
			fmt.Fprintln(cmd.OutOrStdout(), "Authentication: explicitly off.")
		}
		if stack.Spec.TLS == "off" {
			fmt.Fprintln(cmd.OutOrStdout(), "TLS: explicitly off.")
		}
		return nil
	}
	return cmd
}

var serverLayoutFlags = []string{"backend", "artifacts", "access", "tls", "auth", "hostname", "bind", "port", "artifact-path", "nas-mount", "cert-file", "key-file", "s3-endpoint", "s3-bucket", "s3-region", "s3-access-key-env", "s3-secret-key-env", "admin-username"}

func (a *app) applyServerInit(ctx context.Context, result serverform.Result) (*server.Stack, error) {
	var cfg *config.Config
	if result.RegisterTarget {
		var err error
		cfg, err = a.load()
		if err != nil {
			if a.configPath != "" && errors.Is(err, os.ErrNotExist) {
				cfg = config.New(a.configPath)
			} else {
				return nil, err
			}
		}
		if targetIndex(cfg.Targets, result.Spec.ID) >= 0 {
			return nil, usagef("target %q already exists; choose a different stack ID or omit --register-target", result.Spec.ID)
		}
	}
	stack, err := server.Init(ctx, result.Directory, result.Spec, server.InitOptions{AdminPasswordEnv: result.AdminPasswordEnv})
	if err != nil {
		return nil, err
	}
	if result.Start {
		if err = server.NewManager().Up(ctx, stack); err != nil {
			return stack, fmt.Errorf("stack files remain at %s; starting failed: %w", stack.Dir, err)
		}
	}
	if result.RegisterTarget {
		cfg.Targets = append(cfg.Targets, stack.Target())
		if err = cfg.Save(a.configPath); err != nil {
			return stack, fmt.Errorf("stack files remain at %s; target registration failed: %w", stack.Dir, err)
		}
	}
	return stack, nil
}

func (a *app) serverLifecycleCommand(action string) *cobra.Command {
	var dir, destination, service string
	var follow bool
	var tail int
	short := map[string]string{"up": "Build and start this generated stack; services persist after exit", "status": "Show this stack's service states", "logs": "Show redacted service logs", "down": "Stop this stack; preserve all data volumes", "ca-export": "Export only the internal TLS public root CA", "spec": "Print this stack's public configuration"}[action]
	cmd := &cobra.Command{Use: action, Short: short, Args: noArgs}
	cmd.Flags().StringVar(&dir, "dir", "", "Generated stack directory")
	if action == "logs" {
		cmd.Flags().BoolVar(&follow, "follow", false, "Follow logs until interrupted")
		cmd.Flags().IntVar(&tail, "tail", 100, "Lines per service (1–10000)")
		cmd.Flags().StringVar(&service, "service", "", "Limit logs to a service")
	}
	if action == "ca-export" {
		cmd.Flags().StringVar(&destination, "output", "", "New certificate destination (default: stack/ca.crt)")
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if dir == "" {
			return usagef("server %s requires --dir", action)
		}
		if action == "logs" && a.jsonOutput {
			return usagef("server logs writes a text stream and does not support --json")
		}
		stack, err := server.Load(dir)
		if err != nil {
			return err
		}
		manager := server.NewManager()
		switch action {
		case "up":
			err = manager.Up(cmd.Context(), stack)
		case "down":
			err = manager.Down(cmd.Context(), stack)
		case "status":
			status, e := manager.Status(cmd.Context(), stack)
			if e != nil {
				return e
			}
			if a.jsonOutput {
				return a.output(cmd, status)
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "SERVICE\tSTATE\tHEALTH\tEXIT CODE")
			for _, s := range status.Services {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", cell(s.Service), cell(s.State), cell(s.Health), s.ExitCode)
			}
			if len(status.Services) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No containers are running for this stack.")
			}
			return w.Flush()
		case "logs":
			return manager.Logs(cmd.Context(), stack, follow, tail, service, cmd.OutOrStdout())
		case "ca-export":
			path, e := manager.ExportCA(cmd.Context(), stack, destination)
			if e != nil {
				return e
			}
			fingerprint, e := server.CAFingerprint(path)
			if e != nil {
				return e
			}
			if a.jsonOutput {
				return a.output(cmd, map[string]string{"ca_file": path, "fingerprint_sha256": fingerprint})
			}
			_, e = fmt.Fprintf(cmd.OutOrStdout(), "%s\nSHA256 (DER): %s\n", path, fingerprint)
			return e
		case "spec":
			return a.output(cmd, stack.Spec)
		}
		if err != nil {
			return err
		}
		if a.jsonOutput {
			return a.output(cmd, map[string]any{"action": action, "project": stack.Project, "tracking_uri": stack.TrackingURI, "data_preserved": true})
		}
		if action == "down" {
			fmt.Fprintln(cmd.OutOrStdout(), "Stack stopped. All data volumes were preserved.")
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Stack running at %s\n", cell(stack.TrackingURI))
			if stack.Spec.TLS == "internal" {
				fmt.Fprintf(cmd.OutOrStdout(), "Public CA: %s\n", cell(stack.CAPath()))
			}
		}
		return nil
	}
	return cmd
}
