package models

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
)

type Reference struct{ Kind, RunID, Path, Name, Version, Alias, ModelID, URI string }

func SafePath(p string) error {
	if p == "" {
		return nil
	}
	if strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("unsafe relative artifact path %q", p)
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return errors.New("artifact path contains a control character")
		}
	}
	return nil
}
func ParseSource(raw string) (Reference, error) {
	var r Reference
	u, err := url.Parse(raw)
	if err != nil {
		return r, errors.New("invalid model source URI")
	}
	if u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || !strings.HasPrefix(u.Path, "/") {
		return r, errors.New("source must be a runs:/ or models:/ URI without authority, credentials, query or fragment")
	}
	p := strings.TrimPrefix(u.Path, "/")
	if err := SafePath(p); err != nil {
		return r, err
	}
	parts := strings.Split(p, "/")
	r.URI = raw
	switch u.Scheme {
	case "runs":
		if len(parts) < 2 || parts[0] == "" {
			return r, errors.New("run source requires runs:/RUN_ID/ARTIFACT_PATH")
		}
		r.Kind = "run-artifact"
		r.RunID = parts[0]
		r.Path = strings.Join(parts[1:], "/")
	case "models":
		if len(parts) == 1 && strings.HasPrefix(parts[0], "m-") && len(parts[0]) > 2 && !strings.Contains(parts[0], "@") {
			r.Kind = "logged-model"
			r.ModelID = parts[0]
			break
		}
		if len(parts) == 1 && strings.Contains(parts[0], "@") {
			at := strings.LastIndex(parts[0], "@")
			r.Name, r.Alias = parts[0][:at], parts[0][at+1:]
			if r.Name == "" || r.Alias == "" {
				return Reference{}, errors.New("model alias source requires models:/NAME@ALIAS")
			}
			r.Kind = "registered-model"
			break
		}
		if len(parts) != 2 || parts[0] == "" {
			return Reference{}, errors.New("model source requires models:/NAME/VERSION, models:/NAME@ALIAS, or models:/m-ID")
		}
		n, e := strconv.ParseUint(parts[1], 10, 64)
		if e != nil || n == 0 || strconv.FormatUint(n, 10) != parts[1] {
			return Reference{}, errors.New("model version must be a positive numeric version; stages and latest are not supported")
		}
		r.Kind = "registered-model"
		r.Name = parts[0]
		r.Version = parts[1]
	default:
		return r, errors.New("source must use runs:/ or models:/")
	}
	if r.Kind == "registered-model" && strings.Contains(r.Name, ":") {
		return Reference{}, errors.New("registered model names cannot contain ':'")
	}
	return r, nil
}
func sourceURI(p string) string { return "models:" + (&url.URL{Path: "/" + p}).EscapedPath() }
func SafeURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[unavailable URI]"
	}
	if u.Scheme == "file" || u.Scheme == "" {
		return "[local artifact storage]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
func JoinLocation(l ArtifactLocation, p string) ArtifactLocation {
	l.Path = path.Join(l.Path, p)
	return l
}
