package cli

import (
	"fmt"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/models"
	"github.com/spf13/cobra"
)

func (a *app) modelsCommand() *cobra.Command {
	group := a.group("models", "Inspect model provenance and export verifiable artifact bundles")
	service := func(cmd *cobra.Command, fn func(*models.Service) error) error {
		return a.session(cmd.Context(), func(s *core.Session) error {
			m, e := models.New(s.Backend)
			if e != nil {
				return e
			}
			m.SourceKey = core.SourceKey(s.Target)
			return fn(m)
		})
	}
	var filter, token string
	var limit int
	var all bool
	list := &cobra.Command{Use: "list", Short: "List registered models (read-only)", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if limit < 1 {
			return usagef("--limit must be positive")
		}
		return service(cmd, func(s *models.Service) error {
			p, e := s.List(cmd.Context(), models.Query{Filter: filter, MaxResults: limit, PageToken: token}, all)
			if e != nil {
				return e
			}
			if a.jsonOutput {
				return a.output(cmd, p)
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "REGISTERED MODEL\tDESCRIPTION")
			for _, m := range p.Models {
				fmt.Fprintf(w, "%s\t%s\n", cell(m.Name), cell(m.Description))
			}
			if e = w.Flush(); e != nil {
				return e
			}
			return pageHint(cmd, p.NextPageToken)
		})
	}}
	list.Flags().StringVar(&filter, "filter", "", "MLflow registered-model filter")
	list.Flags().IntVar(&limit, "limit", 100, "Maximum results per page")
	list.Flags().StringVar(&token, "page-token", "", "Continuation token")
	list.Flags().BoolVar(&all, "all", false, "Fetch all pages")
	var versionLimit int
	var versionToken string
	var versionsAll bool
	versions := &cobra.Command{Use: "versions NAME", Short: "List all versions of a registered model", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(args[0]) == "" || versionLimit < 1 {
			return usagef("model name and a positive --limit are required")
		}
		return service(cmd, func(s *models.Service) error {
			p, e := s.Versions(cmd.Context(), args[0], models.Query{MaxResults: versionLimit, PageToken: versionToken}, versionsAll)
			if e != nil {
				return e
			}
			if a.jsonOutput {
				return a.output(cmd, p)
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "VERSION\tSTATUS\tRUN\tLOGGED MODEL\tALIASES")
			for _, v := range p.Versions {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", cell(v.Version), cell(v.Status), cell(v.RunID), cell(v.ModelID), cell(strings.Join(v.Aliases, ", ")))
			}
			if e = w.Flush(); e != nil {
				return e
			}
			return pageHint(cmd, p.NextPageToken)
		})
	}}
	versions.Flags().IntVar(&versionLimit, "limit", 100, "Maximum results per page")
	versions.Flags().StringVar(&versionToken, "page-token", "", "Continuation token")
	versions.Flags().BoolVar(&versionsAll, "all", false, "Fetch all pages")
	related := &cobra.Command{Use: "related RUN_ID", Short: "Inspect a run's logged model inputs and outputs", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return service(cmd, func(s *models.Service) error {
			r, queryErr := s.Related(cmd.Context(), args[0])
			if a.jsonOutput {
				if e := a.output(cmd, r); e != nil {
					return e
				}
				return queryErr
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "ROLE\tSTEP\tSOURCE\tSTATUS")
			for _, m := range r.Models {
				step := "—"
				if m.Step != nil {
					step = fmt.Sprint(*m.Step)
				}
				status := m.Error
				if m.Model != nil {
					status = m.Model.Info.Status
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", cell(m.Role), step, cell(m.Source), cell(status))
			}
			if e := w.Flush(); e != nil {
				return e
			}
			return queryErr
		})
	}}
	inspect := &cobra.Command{Use: "inspect SOURCE", Short: "Inspect metadata without loading model code or weights", Args: exactArgs(1), Example: "  lazymlflow models inspect models:/classifier@champion --json\n  lazymlflow models inspect runs:/RUN_ID/checkpoint", RunE: func(cmd *cobra.Command, args []string) error {
		if _, e := models.ParseSource(args[0]); e != nil {
			return usageError{e}
		}
		return service(cmd, func(s *models.Service) error {
			r, e := s.Inspect(cmd.Context(), args[0])
			if e != nil {
				return e
			}
			if a.jsonOutput {
				return a.output(cmd, r)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\nResolved: %s\nKind: %s\nStatus: %s\nMLmodel: %s\nFlavors: %s\nServing: %s\n", cell(r.Resolution.RequestedURI), cell(r.Resolution.ResolvedURI), cell(r.Resolution.Kind), cell(r.Resolution.Status), r.Metadata.Status, cell(strings.Join(r.Metadata.Flavors, ", ")), r.Metadata.Serving)
			for _, warning := range r.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", cell(warning))
			}
			w := table(cmd.OutOrStdout())
			fmt.Fprintln(w, "TYPE\tBYTES\tPATH")
			for _, f := range r.Files {
				kind := "file"
				if f.IsDir {
					kind = "dir"
				}
				fmt.Fprintf(w, "%s\t%d\t%s\n", kind, f.FileSize, cell(f.Path))
			}
			return w.Flush()
		})
	}}
	var dest string
	var overwrite bool
	export := &cobra.Command{Use: "export SOURCE --dest DIRECTORY", Short: "Export original artifact bytes and a SHA-256 receipt", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if _, e := models.ParseSource(args[0]); e != nil {
			return usageError{e}
		}
		if strings.TrimSpace(dest) == "" {
			return usagef("--dest is required and names the exact bundle directory")
		}
		return service(cmd, func(s *models.Service) error {
			r, e := s.Export(cmd.Context(), models.ExportOptions{Source: args[0], Destination: dest, Overwrite: overwrite}, nil)
			if e != nil {
				return e
			}
			if a.jsonOutput {
				return a.output(cmd, r)
			}
			_, e = fmt.Fprintf(cmd.OutOrStdout(), "Exported %d files, %d bytes → %s\nManifest: %s\n", r.Files, r.Bytes, cell(r.Path), cell(r.Manifest))
			return e
		})
	}}
	export.Flags().StringVar(&dest, "dest", "", "Exact bundle output directory (required)")
	export.Flags().BoolVar(&overwrite, "overwrite", false, "Replace an existing bundle after a successful export")
	var manifest string
	verify := &cobra.Command{Use: "verify BUNDLE --manifest EXPECTED_MANIFEST", Short: "Verify bundle bytes against an independently reviewed manifest, offline", Args: exactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(manifest) == "" {
			return usagef("--manifest is required and names the independently reviewed expected manifest")
		}
		r, verifyErr := models.Verify(cmd.Context(), args[0], manifest)
		if a.jsonOutput {
			if e := a.output(cmd, r); e != nil {
				return e
			}
			return verifyErr
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Verified: %t; %d files, %d bytes\n", r.Valid, r.Files, r.Bytes)
		for _, d := range r.Differences {
			fmt.Fprintln(cmd.OutOrStdout(), cell(d))
		}
		return verifyErr
	}}
	verify.Flags().StringVar(&manifest, "manifest", "", "Independently reviewed expected manifest (required)")
	group.AddCommand(list, versions, related, inspect, export, verify)
	return group
}
