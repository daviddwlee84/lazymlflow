// Package config implements lazymlflow profiles without creating files on reads.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/pelletier/go-toml/v2"
)

const DefaultMLflowVersion = "3.16.1"

type Preferences struct {
	MetricColumns    []string `toml:"metric_columns,omitempty" json:"metric_columns,omitempty"`
	ParameterColumns []string `toml:"parameter_columns,omitempty" json:"parameter_columns,omitempty"`
	RefreshSeconds   int      `toml:"refresh_seconds,omitempty" json:"refresh_seconds,omitempty"`
}

type Config struct {
	DefaultTarget string        `toml:"default_target,omitempty" json:"default_target,omitempty"`
	Targets       []core.Target `toml:"targets,omitempty" json:"targets"`
	TUI           Preferences   `toml:"tui" json:"tui"`
	path          string
	original      []byte
	existed       bool
}

// DefaultPath deliberately honors XDG on macOS as well as Linux. A relative
// XDG_CONFIG_HOME is invalid under XDG and is ignored.
func DefaultPath() string {
	if p := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(p) {
		return filepath.Join(p, "lazymlflow", "config.toml")
	}
	if runtime.GOOS == "windows" {
		if p, err := os.UserConfigDir(); err == nil && p != "" {
			return filepath.Join(p, "lazymlflow", "config.toml")
		}
	}
	if p, err := os.UserHomeDir(); err == nil && p != "" {
		return filepath.Join(p, ".config", "lazymlflow", "config.toml")
	}
	return ""
}

// Load accepts a missing default file. An explicitly selected missing file is
// an error, so a typo cannot silently switch a profile.
func Load(path string) (*Config, error) {
	explicit := path != ""
	if path == "" {
		path = DefaultPath()
	}
	if path == "" {
		return nil, errors.New("cannot determine config path: set HOME or XDG_CONFIG_HOME")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c := &Config{path: path}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) && !explicit {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err = toml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.original = bytes.Clone(b)
	c.existed = true
	if err = c.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	for i, target := range c.Targets {
		c.Targets[i], _ = NormalizeTarget(target, filepath.Dir(path))
	}
	return c, nil
}

// New creates a config intended for an explicitly selected new path. Save
// still refuses to overwrite a file created by someone else in the meantime.
func New(path string) *Config {
	if path == "" {
		path = DefaultPath()
	}
	return &Config{path: path}
}
func (c *Config) SourcePath() string { return c.path }

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var version = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([A-Za-z0-9.+-]*)$`)

func (c *Config) Validate() error {
	if c.TUI.RefreshSeconds < 0 {
		return errors.New("tui.refresh_seconds must be zero or positive")
	}
	seen := map[string]bool{}
	for _, t := range c.Targets {
		if seen[t.ID] {
			return fmt.Errorf("duplicate target id %q", t.ID)
		}
		seen[t.ID] = true
		if _, err := NormalizeTarget(t, filepath.Dir(c.path)); err != nil {
			return fmt.Errorf("target %q: %w", t.ID, err)
		}
	}
	if c.DefaultTarget != "" && !seen[c.DefaultTarget] {
		return fmt.Errorf("default_target %q does not exist", c.DefaultTarget)
	}
	return nil
}

// NormalizeTarget fixes local paths at the time a profile is added, and makes
// hand-written relative paths deterministic when read from a config file.
func NormalizeTarget(t core.Target, baseDir string) (core.Target, error) {
	if !identifier.MatchString(t.ID) {
		return t, errors.New("target id must contain only letters, digits, dots, underscores and hyphens")
	}
	if strings.TrimSpace(t.TrackingURI) == "" {
		return t, errors.New("tracking_uri is required")
	}
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return t, err
		}
	}
	baseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return t, err
	}
	if t.WorkingDir != "" {
		t.WorkingDir, err = absolutePath(t.WorkingDir, baseDir)
		if err != nil {
			return t, err
		}
		baseDir = t.WorkingDir
	}
	uri := t.TrackingURI
	switch {
	case strings.HasPrefix(uri, "http://"), strings.HasPrefix(uri, "https://"):
		u, e := url.Parse(uri)
		if e != nil || u.Host == "" {
			return t, errors.New("tracking_uri must be a valid HTTP(S) URL")
		}
		if u.User != nil {
			return t, errors.New("tracking_uri must not contain credentials; use username_env/password_env or token_env")
		}
		if u.RawQuery != "" || u.Fragment != "" {
			return t, errors.New("tracking_uri must not contain a query or fragment")
		}
		t.TrackingURI = strings.TrimRight(uri, "/")
	case strings.HasPrefix(uri, "sqlite:"):
		p, e := SQLitePath(uri, baseDir)
		if e != nil {
			return t, e
		}
		t.TrackingURI = "sqlite:///" + filepath.ToSlash(p)
		if t.WorkingDir == "" {
			t.WorkingDir = baseDir
		}
	case strings.HasPrefix(uri, "file:"):
		p, e := FilePath(uri, baseDir)
		if e != nil {
			return t, e
		}
		t.TrackingURI = (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String()
		if t.WorkingDir == "" {
			t.WorkingDir = baseDir
		}
	case strings.Contains(uri, "://") || strings.HasPrefix(uri, "databricks:"):
		return t, errors.New("unsupported tracking URI: use an HTTP(S) server, a local directory, or sqlite:///path")
	default:
		p, e := absolutePath(uri, baseDir)
		if e != nil {
			return t, e
		}
		t.TrackingURI = (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String()
		if t.WorkingDir == "" {
			t.WorkingDir = baseDir
		}
	}
	if t.WebURL != "" {
		u, e := url.Parse(t.WebURL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return t, errors.New("web_url must be an HTTP(S) URL without credentials, query or fragment")
		}
		t.WebURL = strings.TrimRight(t.WebURL, "/")
	}
	if t.MLflowVersion != "" && !version.MatchString(t.MLflowVersion) {
		return t, errors.New("mlflow_version must be an exact version such as 3.16.1")
	}
	if t.CAFile != "" {
		t.CAFile, err = absolutePath(t.CAFile, baseDir)
		if err != nil {
			return t, err
		}
	}
	pythonIsDir := false
	if t.Python != "" {
		info, e := os.Stat(filepath.Join(baseDir, t.Python))
		pythonIsDir = e == nil && info.IsDir()
	}
	if t.Python != "" && (strings.ContainsAny(t.Python, "/\\") || strings.HasPrefix(t.Python, "~") || t.Python == ".venv" || pythonIsDir) {
		t.Python, err = absolutePath(t.Python, baseDir)
		if err != nil {
			return t, err
		}
	}
	if t.ArtifactsDestination != "" {
		if strings.HasPrefix(t.ArtifactsDestination, "file:") {
			p, e := FilePath(t.ArtifactsDestination, baseDir)
			if e != nil {
				return t, e
			}
			t.ArtifactsDestination = (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String()
		} else if !strings.Contains(t.ArtifactsDestination, "://") {
			t.ArtifactsDestination, err = absolutePath(t.ArtifactsDestination, baseDir)
			if err != nil {
				return t, err
			}
		}
	}
	for _, name := range []string{t.TokenEnv, t.UsernameEnv, t.PasswordEnv} {
		if name != "" && !envName.MatchString(name) {
			return t, fmt.Errorf("invalid credential environment variable %q", name)
		}
	}
	for child, source := range t.Env {
		if !envName.MatchString(child) || !envName.MatchString(source) {
			return t, errors.New("env must map valid environment variable names to source variable names")
		}
	}
	for _, pkg := range t.ExtraPackages {
		if strings.TrimSpace(pkg) == "" || strings.HasPrefix(pkg, "-") || strings.ContainsAny(pkg, "\r\n\x00") {
			return t, fmt.Errorf("invalid extra package %q", pkg)
		}
	}
	return t, nil
}

func absolutePath(p, base string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		p = filepath.Join(h, strings.TrimPrefix(p, "~/"))
		if p == filepath.Join(h, "~") {
			p = h
		}
	}
	if strings.IndexByte(p, 0) >= 0 {
		return "", errors.New("path contains NUL")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Abs(p)
}

func FilePath(uri, base string) (string, error) {
	if !strings.HasPrefix(uri, "file:") {
		return absolutePath(uri, base)
	}
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Host != "" && u.Host != "localhost" {
		return "", errors.New("file URI must reference the local host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("file URI must not contain a query or fragment")
	}
	p := u.Path
	if p == "" {
		p, err = url.PathUnescape(u.Opaque)
		if err != nil {
			return "", err
		}
	}
	if p == "" {
		return "", errors.New("file URI is missing its directory")
	}
	return absolutePath(filepath.FromSlash(p), base)
}

// SQLitePath accepts SQLAlchemy's relative (three slashes) and absolute (four
// slashes) SQLite forms. Options are rejected rather than dropping a supplied
// mode or silently opening another file.
func SQLitePath(uri, base string) (string, error) {
	if !strings.HasPrefix(uri, "sqlite:///") {
		return "", errors.New("SQLite URI must use sqlite:///path.db")
	}
	p := strings.TrimPrefix(uri, "sqlite:///")
	if p == "" || p == ":memory:" || strings.ContainsAny(p, "?#") {
		return "", errors.New("SQLite URI must name an existing database file without query options")
	}
	return absolutePath(filepath.FromSlash(p), base)
}

// Resolve follows flags, app target env, MLflow URI env, configured default,
// then first profile. A selected but broken profile never falls back.
func Resolve(c *Config, targetID, trackingURI string) (core.Target, error) {
	if targetID != "" && trackingURI != "" {
		return core.Target{}, errors.New("--target and --tracking-uri are mutually exclusive")
	}
	if targetID == "" && trackingURI == "" {
		targetID = os.Getenv("LAZYMLFLOW_TARGET")
		if targetID == "" {
			trackingURI = os.Getenv("MLFLOW_TRACKING_URI")
		}
	}
	if trackingURI != "" {
		return NormalizeTarget(core.Target{ID: "temporary", Name: "Temporary", TrackingURI: trackingURI, Transient: true}, "")
	}
	if c == nil {
		c = &Config{}
	}
	if targetID == "" {
		targetID = c.DefaultTarget
	}
	if targetID == "" && len(c.Targets) > 0 {
		targetID = c.Targets[0].ID
	}
	if targetID == "" {
		return core.Target{}, errors.New("no tracking target configured; run lazymlflow targets add or pass --tracking-uri")
	}
	for _, t := range c.Targets {
		if t.ID == targetID {
			return NormalizeTarget(t, filepath.Dir(c.path))
		}
	}
	return core.Target{}, fmt.Errorf("target %q not found; run lazymlflow targets list", targetID)
}

func RedactURI(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "<invalid URI>"
	}
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	q := u.Query()
	for k := range q {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "key") {
			q.Set(k, "REDACTED")
		}
	}
	if u.RawQuery != "" {
		u.RawQuery = q.Encode()
	}
	return u.String()
}
func Redacted(c *Config) Config {
	r := *c
	r.Targets = append([]core.Target(nil), c.Targets...)
	for i := range r.Targets {
		r.Targets[i].TrackingURI = RedactURI(r.Targets[i].TrackingURI)
		r.Targets[i].WebURL = RedactURI(r.Targets[i].WebURL)
		r.Targets[i].ArtifactsDestination = RedactURI(r.Targets[i].ArtifactsDestination)
	}
	return r
}
