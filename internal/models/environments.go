package models

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Environment inspection reads a bounded set of declared local data files. It
// never follows package indexes, installs dependencies, or imports Python code.
func (s *Service) inspectEnvironments(ctx context.Context, out *Inspection) {
	// References are checked by metadata listing only, never loaded.
	if len(out.Metadata.References) > 64 {
		out.Metadata.References = out.Metadata.References[:64]
		out.Warnings = append(out.Warnings, "Artifact reference inspection stopped at 64 entries")
	}
	for i := range out.Metadata.References {
		ref := &out.Metadata.References[i]
		loc := JoinLocation(out.Resolution.Location, ref.Path)
		parent := loc
		parent.Path = path.Dir(loc.Path)
		if parent.Path == "." {
			parent.Path = ""
		}
		page, e := s.models.ListModelArtifacts(ctx, parent)
		if e != nil {
			ref.Status = "unavailable"
			out.Warnings = append(out.Warnings, fmt.Sprintf("Artifact %s: %v", ref.Path, e))
			continue
		}
		ref.Status = "missing"
		for _, f := range page.Files {
			if f.Path == loc.Path {
				ref.Status = "present"
				break
			}
		}
		if ref.Status == "missing" {
			out.Warnings = append(out.Warnings, "Referenced model artifact is missing: "+ref.Path)
		}
	}
	refs := append([]string(nil), out.Metadata.EnvironmentFiles...)
	for _, f := range out.Files {
		name := path.Base(f.Path)
		if !f.IsDir && (name == "requirements.txt" || name == "python_env.yaml" || name == "conda.yaml") {
			refs = append(refs, name)
		}
		ext := strings.ToLower(path.Ext(name))
		if ext == ".pkl" || ext == ".pickle" || ext == ".py" || ext == ".pth" || ext == ".pt" || name == "code" {
			out.Metadata.Annotations = append(out.Metadata.Annotations, "Artifact listing includes code or serialized objects; none were loaded.")
		}
	}
	if len(out.Metadata.Flavors) > 0 {
		out.Metadata.Annotations = append(out.Metadata.Annotations, "Flavor declarations describe loaders; serving compatibility has not been executed.")
	}
	seen := map[string]bool{}
	var total int64
	for len(refs) > 0 {
		rel := refs[0]
		refs = refs[1:]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		if len(seen) > 8 {
			out.Warnings = append(out.Warnings, "Environment inspection stopped at eight files")
			break
		}
		env := EnvironmentFile{Path: rel, Status: "missing"}
		loc := JoinLocation(out.Resolution.Location, rel)
		parent := loc
		parent.Path = path.Dir(loc.Path)
		if parent.Path == "." {
			parent.Path = ""
		}
		listing, e := s.models.ListModelArtifacts(ctx, parent)
		if e != nil {
			env.Status = "unavailable"
			out.Warnings = append(out.Warnings, fmt.Sprintf("Environment %s: %v", rel, e))
			out.Metadata.Environments = append(out.Metadata.Environments, env)
			continue
		}
		found := false
		var size int64
		for _, f := range listing.Files {
			if f.Path == loc.Path && !f.IsDir {
				found = true
				size = f.FileSize
				break
			}
		}
		if !found {
			out.Metadata.Environments = append(out.Metadata.Environments, env)
			out.Warnings = append(out.Warnings, "Environment file is missing: "+rel)
			continue
		}
		remaining := 4*MetadataLimit - total
		readLimit := min(MetadataLimit, remaining)
		if size < 0 || readLimit <= 0 || size > readLimit {
			env.Status = "too-large"
			out.Metadata.Environments = append(out.Metadata.Environments, env)
			continue
		}
		b, e := s.models.ReadModelArtifact(ctx, loc, readLimit)
		if e != nil {
			env.Status = "unavailable"
			out.Warnings = append(out.Warnings, fmt.Sprintf("Environment %s: %v", rel, e))
			out.Metadata.Environments = append(out.Metadata.Environments, env)
			continue
		}
		total += int64(len(b))
		deps, includes, e := environmentDependencies(rel, b)
		if e != nil {
			env.Status = "invalid"
			out.Warnings = append(out.Warnings, fmt.Sprintf("Environment %s: %v", rel, e))
		} else {
			env.Status = "read"
			env.Dependencies = deps
			refs = append(refs, includes...)
		}
		out.Metadata.Environments = append(out.Metadata.Environments, env)
	}
	sort.Slice(out.Metadata.Environments, func(i, j int) bool { return out.Metadata.Environments[i].Path < out.Metadata.Environments[j].Path })
}
func environmentDependencies(name string, b []byte) ([]string, []string, error) {
	deps := []string{}
	includes := []string{}
	var lines []string
	if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
		m, e := metadataMapping(b)
		if e != nil {
			return nil, nil, e
		}
		var collect func(any)
		collect = func(v any) {
			switch v := v.(type) {
			case string:
				lines = append(lines, v)
			case []any:
				for _, item := range v {
					collect(item)
				}
			case map[string]any:
				if pip, ok := v["pip"]; ok {
					collect(pip)
				}
			}
		}
		collect(m["dependencies"])
		collect(m["build_dependencies"])
		if version, ok := m["python"]; ok {
			lines = append(lines, fmt.Sprint("python=", version))
		}
	} else {
		lines = strings.Split(string(b), "\n")
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		include := ""
		for _, prefix := range []string{"-r ", "--requirement "} {
			if strings.HasPrefix(line, prefix) {
				include = strings.TrimSpace(strings.TrimPrefix(line, prefix))
				break
			}
		}
		if include != "" {
			if SafePath(include) != nil || strings.Contains(include, ":") {
				return nil, nil, fmt.Errorf("unsafe requirements reference")
			}
			includes = append(includes, path.Join(path.Dir(name), include))
			continue
		}
		if strings.HasPrefix(line, "-") {
			deps = append(deps, "[installer option omitted]")
			continue
		}
		if index := strings.Index(line, "http://"); index >= 0 {
			line = line[:index] + SafeURI(line[index:])
		} else if index := strings.Index(line, "https://"); index >= 0 {
			line = line[:index] + SafeURI(line[index:])
		}
		if len(line) > 4096 {
			return nil, nil, fmt.Errorf("dependency line exceeds 4096 bytes")
		}
		deps = append(deps, line)
	}
	sort.Strings(deps)
	return deps, includes, nil
}
