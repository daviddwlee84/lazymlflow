package models

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

type Service struct {
	backend   core.Backend
	models    Backend
	SourceKey string
}

func New(b core.Backend) (*Service, error) {
	m, ok := b.(Backend)
	if !ok {
		return nil, errors.New("this backend does not support model inspection")
	}
	return &Service{backend: b, models: m}, nil
}
func (s *Service) List(ctx context.Context, q Query, all bool) (RegisteredPage, error) {
	out := RegisteredPage{Models: []RegisteredModel{}}
	seen := map[string]bool{q.PageToken: true}
	for {
		p, e := s.models.SearchRegisteredModels(ctx, q)
		if e != nil {
			return out, e
		}
		out.Models = append(out.Models, p.Models...)
		out.NextPageToken = p.NextPageToken
		if !all || p.NextPageToken == "" {
			return out, nil
		}
		if seen[p.NextPageToken] {
			return out, errors.New("server repeated a registered-model page token")
		}
		seen[p.NextPageToken] = true
		q.PageToken = p.NextPageToken
	}
}
func (s *Service) Versions(ctx context.Context, name string, q Query, all bool) (VersionPage, error) {
	if name == "" {
		return VersionPage{}, errors.New("registered model name is required")
	}
	escaped := strings.ReplaceAll(strings.ReplaceAll(name, "\\", "\\\\"), "'", "\\'")
	q.Filter = "name = '" + escaped + "'"
	out := VersionPage{Versions: []ModelVersion{}}
	seen := map[string]bool{q.PageToken: true}
	for {
		p, e := s.models.SearchModelVersions(ctx, q)
		if e != nil {
			return out, e
		}
		out.Versions = append(out.Versions, p.Versions...)
		out.NextPageToken = p.NextPageToken
		if !all || p.NextPageToken == "" {
			return out, nil
		}
		if seen[p.NextPageToken] {
			return out, errors.New("server repeated a model-version page token")
		}
		seen[p.NextPageToken] = true
		q.PageToken = p.NextPageToken
	}
}
func (s *Service) Related(ctx context.Context, id string) (RelatedResult, error) {
	out := RelatedResult{RunID: id, Models: []RelatedModel{}, Complete: true}
	r, err := s.backend.GetRun(ctx, id)
	if err != nil {
		return out, err
	}
	for _, v := range r.Inputs.ModelInputs {
		out.Models = append(out.Models, RelatedModel{Role: "input", Source: sourceURI(v.ModelID)})
	}
	for _, v := range r.Outputs.ModelOutputs {
		out.Models = append(out.Models, RelatedModel{Role: "output", Step: v.Step, Source: sourceURI(v.ModelID)})
	}
	cache := map[string]LoggedModel{}
	errs := map[string]error{}
	var first error
	for i := range out.Models {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		v := &out.Models[i]
		ref, err := ParseSource(v.Source)
		if err != nil {
			return out, err
		}
		m, ok := cache[ref.ModelID]
		if !ok && errs[ref.ModelID] == nil {
			m, err = s.models.GetLoggedModel(ctx, ref.ModelID)
			if err != nil {
				errs[ref.ModelID] = err
			} else {
				cache[ref.ModelID] = m
			}
		}
		if e := errs[ref.ModelID]; e != nil {
			v.Error = e.Error()
			out.Complete = false
			if first == nil {
				first = e
			}
			continue
		}
		m.Info.ArtifactURI = SafeURI(m.Info.ArtifactURI)
		v.Model = &m
	}
	return out, first
}
func (s *Service) Resolve(ctx context.Context, source string) (Resolution, error) {
	ref, err := ParseSource(source)
	if err != nil {
		return Resolution{}, err
	}
	out := Resolution{RequestedURI: source, ResolvedURI: source, Kind: ref.Kind, ResolvedAt: time.Now().UTC(), SourceKey: s.SourceKey}
	switch ref.Kind {
	case "run-artifact":
		r, e := s.backend.GetRun(ctx, ref.RunID)
		if e != nil {
			return out, e
		}
		out.RunID = r.ID()
		out.ArtifactPath = ref.Path
		out.Status = r.Info.Status
		out.Location = ArtifactLocation{URI: r.Info.ArtifactURI, RunID: r.ID(), Path: ref.Path}
	case "logged-model":
		m, e := s.models.GetLoggedModel(ctx, ref.ModelID)
		if e != nil {
			return out, e
		}
		if m.Info.ModelID != ref.ModelID {
			return out, errors.New("server returned a different logged model")
		}
		out.LoggedModelID = m.Info.ModelID
		out.RunID = m.Info.SourceRunID
		out.Status = m.Info.Status
		out.Location = ArtifactLocation{URI: m.Info.ArtifactURI, LoggedModelID: m.Info.ModelID}
		m.Info.ArtifactURI = SafeURI(m.Info.ArtifactURI)
		out.Logged = &m
	case "registered-model":
		var m ModelVersion
		if ref.Alias != "" {
			m, err = s.models.GetModelVersionByAlias(ctx, ref.Name, ref.Alias)
		} else {
			m, err = s.models.GetModelVersion(ctx, ref.Name, ref.Version)
		}
		if err != nil {
			return out, err
		}
		if m.Name != ref.Name || m.Version == "" || (ref.Version != "" && m.Version != ref.Version) {
			return out, errors.New("server returned a different model version")
		}
		out.RegisteredName = m.Name
		out.Version = m.Version
		out.Alias = ref.Alias
		out.RunID = m.RunID
		out.LoggedModelID = m.ModelID
		out.Status = m.Status
		out.ResolvedURI = sourceURI(m.Name + "/" + m.Version)
		if _, e := ParseSource(out.ResolvedURI); e != nil {
			return out, fmt.Errorf("server returned invalid model version: %w", e)
		}
		uri, e := s.models.GetModelVersionDownloadURI(ctx, m.Name, m.Version)
		if e != nil {
			return out, e
		}
		out.Location, err = s.storageLocation(ctx, uri, map[string]bool{}, 0)
		if err != nil {
			return out, err
		}
		m.Source = SafeURI(m.Source)
		out.Registered = &m
		if m.ModelID != "" {
			logged, e := s.models.GetLoggedModel(ctx, m.ModelID)
			if e != nil {
				return out, e
			}
			logged.Info.ArtifactURI = SafeURI(logged.Info.ArtifactURI)
			out.Logged = &logged
		}
	}
	if out.Location.URI == "" {
		return out, errors.New("model has no artifact storage URI")
	}
	out.StorageURI = SafeURI(out.Location.URI)
	return out, nil
}
func (s *Service) storageLocation(ctx context.Context, raw string, seen map[string]bool, depth int) (ArtifactLocation, error) {
	if depth > 8 || seen[raw] {
		return ArtifactLocation{}, errors.New("cyclic model artifact location")
	}
	seen[raw] = true
	u, e := url.Parse(raw)
	if e != nil {
		return ArtifactLocation{}, errors.New("invalid artifact storage URI")
	}
	if u.Scheme == "runs" {
		ref, e := ParseSource(raw)
		if e != nil {
			return ArtifactLocation{}, e
		}
		r, e := s.backend.GetRun(ctx, ref.RunID)
		if e != nil {
			return ArtifactLocation{}, e
		}
		return ArtifactLocation{URI: r.Info.ArtifactURI, RunID: r.ID(), Path: ref.Path}, nil
	}
	if u.Scheme == "models" {
		ref, e := ParseSource(raw)
		if e != nil {
			return ArtifactLocation{}, e
		}
		if ref.Kind == "logged-model" {
			m, e := s.models.GetLoggedModel(ctx, ref.ModelID)
			if e != nil {
				return ArtifactLocation{}, e
			}
			return ArtifactLocation{URI: m.Info.ArtifactURI, LoggedModelID: m.Info.ModelID}, nil
		}
		if ref.Alias != "" {
			return ArtifactLocation{}, errors.New("registry download URI unexpectedly contains a mutable alias")
		}
		next, e := s.models.GetModelVersionDownloadURI(ctx, ref.Name, ref.Version)
		if e != nil {
			return ArtifactLocation{}, e
		}
		return s.storageLocation(ctx, next, seen, depth+1)
	}
	return ArtifactLocation{URI: raw}, nil
}
func (s *Service) Inspect(ctx context.Context, source string) (Inspection, error) {
	out := Inspection{Files: []core.Artifact{}, Warnings: []string{}, Metadata: Metadata{Status: "absent", RuntimeValidation: "not_run", Flavors: []string{}, Serving: "custom-or-unknown"}}
	r, e := s.Resolve(ctx, source)
	if e != nil {
		return out, e
	}
	out.Resolution = r
	page, e := s.models.ListModelArtifacts(ctx, r.Location)
	if e != nil {
		return out, e
	}
	out.Files = page.Files
	// A run source can name a single raw file. Listing then returns no children.
	expected := path.Join(r.Location.Path, "MLmodel")
	for _, f := range page.Files {
		if f.Path != expected || f.IsDir {
			continue
		}
		if f.FileSize < 0 || f.FileSize > MetadataLimit {
			out.Metadata.Status = "unavailable"
			out.Warnings = append(out.Warnings, "MLmodel exceeds the 1 MiB metadata limit")
			return out, nil
		}
		b, e := s.models.ReadModelArtifact(ctx, JoinLocation(r.Location, "MLmodel"), MetadataLimit)
		if e != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Metadata.Status = "unavailable"
			out.Warnings = append(out.Warnings, e.Error())
			return out, nil
		}
		m, e := ParseMetadata(b)
		if e != nil {
			out.Metadata.Status = "invalid"
			out.Warnings = append(out.Warnings, e.Error())
			return out, nil
		}
		out.Metadata = m
		s.inspectEnvironments(ctx, &out)
		return out, ctx.Err()
	}
	return out, nil
}
func ready(r Resolution) bool {
	if r.Kind == "run-artifact" {
		return true
	}
	if r.Status != "READY" && r.Status != "LOGGED_MODEL_READY" {
		return false
	}
	return r.Logged == nil || r.Logged.Info.Status == "READY" || r.Logged.Info.Status == "LOGGED_MODEL_READY"
}
func sortedFiles(files []FileDigest) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}
