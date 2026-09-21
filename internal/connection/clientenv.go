package connection

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/config"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

// ClientEnvPlan is a portable, secret-free environment recipe. References are
// resolved by the receiving shell/process, never included in its JSON output.
type ClientEnvPlan struct {
	Version            int               `json:"version"`
	Target             string            `json:"target"`
	Set                map[string]string `json:"set"`
	References         map[string]string `json:"references"`
	NonEmptyReferences []string          `json:"nonempty_references,omitempty"`
	Unset              []string          `json:"unset"`
	Warnings           []string          `json:"warnings,omitempty"`
}

var clientManagedVariables = []string{
	"MLFLOW_TRACKING_URI", "MLFLOW_REGISTRY_URI", "MLFLOW_TRACKING_USERNAME",
	"MLFLOW_TRACKING_PASSWORD", "MLFLOW_TRACKING_TOKEN", "MLFLOW_TRACKING_INSECURE_TLS",
	"MLFLOW_TRACKING_SERVER_CERT_PATH", "MLFLOW_BACKEND_STORE_URI",
	"MLFLOW_TRACKING_CLIENT_CERT_PATH", "MLFLOW_TRACKING_AUTH", "MLFLOW_TRACKING_AWS_SIGV4",
	"MLFLOW_ARTIFACTS_DESTINATION", "MLFLOW_DEFAULT_ARTIFACT_ROOT", "MLFLOW_ALLOW_FILE_STORE",
	"MLFLOW_ENABLE_WORKSPACES", "MLFLOW_WORKSPACE_STORE_URI", "MLFLOW_WORKSPACE",
	"MLFLOW_AUTH_CONFIG_PATH", "MLFLOW_AUTH_ADMIN_USERNAME", "MLFLOW_AUTH_ADMIN_PASSWORD",
	"MLFLOW_FLASK_SERVER_SECRET_KEY",
}

func managedClientVariable(name string) bool {
	if strings.HasPrefix(name, "_MLFLOW_") || strings.HasPrefix(name, "MLFLOW_SERVER_") {
		return true
	}
	for _, key := range clientManagedVariables {
		if key == name {
			return true
		}
	}
	return false
}

// ValidateExperimentTarget excludes the readonly local-store browsing adapter.
// A persistent server created by `server init` is an ordinary HTTP(S) target.
func ValidateExperimentTarget(t core.Target) error {
	u, err := url.Parse(t.TrackingURI)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("experiment environments require an HTTP(S) tracking server; local file/SQLite targets are readonly browsing adapters; create a persistent server with lazymlflow server init")
	}
	return nil
}

// ClientEnvironmentPlan generates exports for stable direct endpoints, without
// opening a connection or looking up secret values. Inherited names are used
// only to find stale server settings and transient authentication references.
func ClientEnvironmentPlan(t core.Target) (ClientEnvPlan, error) {
	if t.SSHHost != "" {
		return ClientEnvPlan{}, errors.New("an SSH target needs a live tunnel; use lazymlflow targets exec " + t.ID + " -- COMMAND instead of exporting a temporary URL")
	}
	return clientEnvironmentPlan(t, t.TrackingURI, os.Environ())
}

func clientEnvironmentPlan(t core.Target, endpoint string, inherited []string) (ClientEnvPlan, error) {
	t, err := config.NormalizeTarget(t, "")
	if err != nil {
		return ClientEnvPlan{}, err
	}
	if err := ValidateExperimentTarget(t); err != nil {
		return ClientEnvPlan{}, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ClientEnvPlan{}, errors.New("client endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	p := ClientEnvPlan{Version: 1, Target: t.ID, Set: map[string]string{
		"MLFLOW_TRACKING_URI": endpoint, "MLFLOW_REGISTRY_URI": endpoint,
	}, References: map[string]string{}, Unset: []string{}}
	if !t.Transient && (t.UsernameEnv != "" || t.PasswordEnv != "") {
		if t.UsernameEnv == "" || t.PasswordEnv == "" {
			return ClientEnvPlan{}, errors.New("experiment clients require username_env and password_env together; an incomplete Basic pair can make the MLflow SDK use a different credential source")
		}
		p.NonEmptyReferences = []string{"MLFLOW_TRACKING_PASSWORD", "MLFLOW_TRACKING_USERNAME"}
	} else {
		p.Warnings = []string{"The MLflow SDK can use Basic credentials from ~/.mlflow/credentials ahead of a token; environment exports do not control SDK credential files."}
	}
	for dest, source := range t.Env {
		// Routing, authentication and runtime controls have dedicated semantics.
		// Do not let generic artifact-provider settings bypass their isolation.
		if managedClientVariable(dest) {
			return ClientEnvPlan{}, fmt.Errorf("target env mapping %s conflicts with managed MLflow connection/runtime settings; use the dedicated target fields", dest)
		}
		p.References[dest] = source
	}
	present := map[string]bool{}
	for _, item := range inherited {
		name, _, _ := strings.Cut(item, "=")
		present[name] = true
	}
	for _, auth := range []struct{ dest, source string }{
		{"MLFLOW_TRACKING_USERNAME", t.UsernameEnv},
		{"MLFLOW_TRACKING_PASSWORD", t.PasswordEnv},
		{"MLFLOW_TRACKING_TOKEN", t.TokenEnv},
	} {
		if auth.source != "" {
			p.References[auth.dest] = auth.source
		} else if t.Transient && present[auth.dest] {
			p.References[auth.dest] = auth.dest
		}
	}
	if t.Transient {
		// A temporary URI intentionally keeps the SDK's ambient/default auth
		// behavior. Named profiles instead isolate these alternative mechanisms.
		for _, name := range []string{"MLFLOW_TRACKING_AUTH", "MLFLOW_TRACKING_AWS_SIGV4", "MLFLOW_TRACKING_CLIENT_CERT_PATH"} {
			if present[name] {
				p.References[name] = name
			}
		}
	}
	if t.CAFile != "" {
		p.Set["MLFLOW_TRACKING_SERVER_CERT_PATH"] = t.CAFile
	}
	unset := map[string]bool{}
	for _, name := range clientManagedVariables {
		unset[name] = true
	}
	for name := range present {
		if managedClientVariable(name) {
			unset[name] = true
		}
	}
	for name := range p.Set {
		delete(unset, name)
	}
	for name := range p.References {
		delete(unset, name)
	}
	for name := range unset {
		p.Unset = append(p.Unset, name)
	}
	sort.Strings(p.Unset)
	return p, nil
}

func quoteShellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// RenderSH supports POSIX sh, bash and zsh. A single export statement resolves
// every reference against the original environment before changing any source;
// cleanup comes afterward, preserving cycles, aliases and self-references.
func (p ClientEnvPlan) RenderSH() string {
	names := make([]string, 0, len(p.Set)+len(p.References))
	for name := range p.Set {
		names = append(names, name)
	}
	for name := range p.References {
		if _, exists := p.Set[name]; !exists {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, warning := range p.Warnings {
		b.WriteString("# " + warning + "\n")
	}
	b.WriteString("export")
	for _, name := range names {
		b.WriteString(" " + "\\" + "\n  " + name + "=")
		if value, ok := p.Set[name]; ok {
			b.WriteString(quoteShellLiteral(value))
		} else {
			source := p.References[name]
			if p.nonEmpty(name) {
				b.WriteString("\"${" + source + ":?" + source + " is required and must not be empty}\"")
			} else {
				b.WriteString("\"${" + source + "?" + source + " is required}\"")
			}
		}
	}
	b.WriteByte('\n')
	if len(p.Unset) > 0 {
		b.WriteString("unset")
		for _, name := range p.Unset {
			b.WriteString(" " + quoteShellLiteral(name))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (p ClientEnvPlan) nonEmpty(name string) bool {
	for _, required := range p.NonEmptyReferences {
		if required == name {
			return true
		}
	}
	return false
}

// ResolveClientEnvironment expands references simultaneously, then applies
// cleanup/overrides. Unrelated environment entries (including provider settings)
// remain inherited; no managed-server runtime defaults are injected.
func ResolveClientEnvironment(p ClientEnvPlan, inherited []string) ([]string, error) {
	original := map[string]string{}
	for _, item := range inherited {
		if key, value, ok := strings.Cut(item, "="); ok {
			original[key] = value
		}
	}
	resolved := map[string]string{}
	for name, source := range p.References {
		value, ok := original[source]
		if !ok {
			return nil, fmt.Errorf("environment variable %s is not set (required for %s)", source, name)
		}
		if value == "" && p.nonEmpty(name) {
			return nil, fmt.Errorf("environment variable %s must not be empty for %s; refusing MLflow SDK credential fallback", source, name)
		}
		resolved[name] = value
	}
	for name, value := range p.Set {
		resolved[name] = value
	}
	for _, name := range p.Unset {
		delete(original, name)
	}
	for name, value := range resolved {
		original[name] = value
	}
	names := make([]string, 0, len(original))
	for name := range original {
		names = append(names, name)
	}
	sort.Strings(names)
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+original[name])
	}
	return env, nil
}

// ExecClientEnvironment creates an experiment child's environment. For SSH,
// endpoint must be the owned proxy URL: that proxy handles upstream TLS/auth.
func ExecClientEnvironment(t core.Target, endpoint string, inherited []string) ([]string, error) {
	p, err := clientEnvironmentPlan(t, endpoint, inherited)
	if err != nil {
		return nil, err
	}
	// Resolve all references before stripping proxy-owned authentication, so a
	// misconfigured target fails consistently and provider aliases still work.
	env, err := ResolveClientEnvironment(p, inherited)
	if err != nil {
		return nil, err
	}
	if t.SSHHost != "" {
		for _, name := range []string{"MLFLOW_TRACKING_USERNAME", "MLFLOW_TRACKING_PASSWORD", "MLFLOW_TRACKING_TOKEN", "MLFLOW_TRACKING_SERVER_CERT_PATH"} {
			var filtered []string
			for _, item := range env {
				if !strings.HasPrefix(item, name+"=") {
					filtered = append(filtered, item)
				}
			}
			env = filtered
		}
		env = bypassLoopbackProxy(env)
	}
	return env, nil
}
