package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ParseMetadata parses data only. No YAML tags, aliases, loaders, or environment
// commands are executed, and no model framework is imported.
func ParseMetadata(data []byte) (Metadata, error) {
	out := Metadata{Status: "present", RuntimeValidation: "not_run", Flavors: []string{}, Serving: "custom-or-unknown"}
	model, err := metadataMapping(data)
	if err != nil {
		return out, err
	}
	if signature, ok := model["signature"]; ok {
		out.Signature = signature
		if raw, ok := signature.(string); ok {
			var parsed any
			if json.Unmarshal([]byte(raw), &parsed) == nil {
				out.Signature = parsed
			}
		}
	}
	if flavors, ok := model["flavors"].(map[string]any); ok {
		envs := map[string]bool{}
		for name, v := range flavors {
			out.Flavors = append(out.Flavors, name)
			if name == "python_function" {
				out.Serving = "pyfunc-candidate-unverified"
			}
			config, ok := v.(map[string]any)
			if !ok {
				return out, fmt.Errorf("MLmodel flavor %q must be a mapping", name)
			}
			for _, key := range []string{"code", "data", "python_model", "pickled_model", "model_data", "model_path", "model_code_path"} {
				var refs []string
				switch value := config[key].(type) {
				case string:
					refs = append(refs, value)
				case []any:
					for _, item := range value {
						if value, ok := item.(string); ok {
							refs = append(refs, value)
						}
					}
				}
				for _, p := range refs {
					if p == "" {
						continue
					}
					if SafePath(p) != nil || strings.Contains(p, ":") {
						return out, fmt.Errorf("MLmodel %s reference is not a safe relative path", key)
					}
					out.References = append(out.References, ArtifactReference{Path: p, Kind: key, Status: "declared"})
				}
			}
			if artifacts, ok := config["artifacts"].(map[string]any); ok {
				for _, item := range artifacts {
					if artifact, ok := item.(map[string]any); ok {
						if p, ok := artifact["path"].(string); ok {
							if p == "" || SafePath(p) != nil || strings.Contains(p, ":") {
								return out, errors.New("MLmodel artifact reference is not a safe relative path")
							}
							out.References = append(out.References, ArtifactReference{Path: p, Kind: "artifact", Status: "declared"})
						}
					}
				}
			}
			var paths []string
			switch env := config["env"].(type) {
			case string:
				paths = append(paths, env)
			case map[string]any:
				for _, v := range env {
					if p, ok := v.(string); ok {
						paths = append(paths, p)
					}
				}
			}
			for _, p := range paths {
				if SafePath(p) != nil || strings.Contains(p, ":") || p == "" {
					return out, errors.New("MLmodel environment reference is not a safe relative path")
				}
				envs[p] = true
			}
		}
		for p := range envs {
			out.EnvironmentFiles = append(out.EnvironmentFiles, p)
		}
	}
	sort.Slice(out.References, func(i, j int) bool {
		if out.References[i].Path != out.References[j].Path {
			return out.References[i].Path < out.References[j].Path
		}
		return out.References[i].Kind < out.References[j].Kind
	})
	sort.Strings(out.Flavors)
	sort.Strings(out.EnvironmentFiles)
	return out, nil
}

func metadataMapping(data []byte) (map[string]any, error) {
	if int64(len(data)) > MetadataLimit {
		return nil, errors.New("MLmodel exceeds the 1 MiB metadata limit")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if e := decoder.Decode(&doc); e != nil {
		return nil, fmt.Errorf("invalid MLmodel YAML: %w", e)
	}
	var extra yaml.Node
	if e := decoder.Decode(&extra); e != io.EOF {
		return nil, errors.New("MLmodel must contain exactly one YAML document")
	}
	count := 0
	var visit func(*yaml.Node, int) error
	visit = func(n *yaml.Node, depth int) error {
		count++
		if depth > 32 || count > 10000 {
			return errors.New("MLmodel exceeds the YAML structure limit")
		}
		if n.Kind == yaml.AliasNode || n.Anchor != "" {
			return errors.New("MLmodel YAML aliases and anchors are not supported")
		}
		switch n.Tag {
		case "", "!!map", "!!seq", "!!str", "!!int", "!!float", "!!bool", "!!null", "!!timestamp":
		default:
			return errors.New("MLmodel contains a nonstandard YAML tag")
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
					return errors.New("MLmodel mapping keys must be strings")
				}
				if seen[k.Value] {
					return fmt.Errorf("duplicate MLmodel key %q", k.Value)
				}
				seen[k.Value] = true
			}
		}
		for _, c := range n.Content {
			if e := visit(c, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if e := visit(&doc, 0); e != nil {
		return nil, e
	}
	var model map[string]any
	if e := doc.Decode(&model); e != nil {
		return nil, errors.New("MLmodel must contain a mapping")
	}
	if model == nil {
		return nil, errors.New("MLmodel must contain a mapping")
	}

	return model, nil
}
