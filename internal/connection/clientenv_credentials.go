package connection

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

// CheckClientCredentialFile prevents a named token-only/anonymous profile from
// silently becoming SDK Basic authentication. MLflow 3.16's credential reader
// has a fixed ~/.mlflow/credentials path and prefers its Basic pair to a token.
// Explicit Basic profiles and temporary URIs retain their declared semantics.
func CheckClientCredentialFile(t core.Target) error {
	if t.Transient || t.UsernameEnv != "" || t.PasswordEnv != "" {
		return nil
	}
	if t.SSHHost != "" && t.TokenEnv != "" && os.Getenv(t.TokenEnv) != "" {
		// The owned Go proxy replaces any SDK-generated Authorization header
		// with this explicit nonempty token. An empty token gives no such gate.
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("cannot locate the MLflow SDK credential file; configure an explicit Basic target or provide the normal user home directory")
	}
	return checkClientCredentialFileAt(filepath.Join(home, ".mlflow", "credentials"))
}

func checkClientCredentialFileAt(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return errors.New("cannot safely inspect ~/.mlflow/credentials; configure explicit Basic target references or resolve the SDK credential-file configuration before targets exec")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot read ~/.mlflow/credentials; resolve the SDK credential-file configuration before targets exec")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return errors.New("cannot safely read ~/.mlflow/credentials; resolve the SDK credential-file configuration before targets exec")
	}
	configured, err := sdkFileBasicPair(string(b))
	if err != nil {
		// Parsing errors may contain credential text. Keep the diagnostic fixed.
		return errors.New("cannot interpret ~/.mlflow/credentials safely; repair the SDK credential file or configure explicit Basic target references before targets exec")
	}
	if configured {
		return errors.New("~/.mlflow/credentials contains a Basic credential pair that the MLflow SDK would prefer over this named target's token/no-auth settings; remove that conflicting SDK file configuration or select a target with explicit username_env/password_env references (temporary --tracking-uri intentionally uses SDK defaults)")
	}
	return nil
}

// sdkFileBasicPair reads only enough ConfigParser syntax to determine whether
// the SDK can acquire a nonempty Basic pair. Defaults, continuations and basic
// interpolation are included; values never leave this private boolean check.
func sdkFileBasicPair(text string) (bool, error) {
	sections := map[string]map[string]string{}
	section, previous, previousIndent := "", "", 0
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 1024), 64<<10)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if previous != "" && indent > previousIndent {
			sections[section][previous] += "\n" + trimmed
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			end := strings.LastIndex(trimmed, "]")
			if end < 2 {
				return false, errors.New("invalid section")
			}
			section = trimmed[1:end]
			if _, duplicate := sections[section]; duplicate {
				return false, errors.New("duplicate section")
			}
			sections[section] = map[string]string{}
			previous = ""
			continue
		}
		if section == "" {
			return false, errors.New("missing section")
		}
		index := strings.IndexAny(trimmed, "=:")
		if index <= 0 {
			return false, errors.New("invalid option")
		}
		key := strings.ToLower(strings.TrimSpace(trimmed[:index]))
		if _, duplicate := sections[section][key]; duplicate {
			return false, errors.New("duplicate option")
		}
		sections[section][key] = strings.TrimSpace(trimmed[index+1:])
		previous, previousIndent = key, indent
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	selected, exists := sections["mlflow"]
	if !exists {
		return false, nil
	}
	values := map[string]string{}
	for key, value := range sections["DEFAULT"] {
		values[key] = value
	}
	for key, value := range selected {
		values[key] = value
	}
	username, err := sdkInterpolate(values["mlflow_tracking_username"], values, 0)
	if err != nil {
		return false, err
	}
	password, err := sdkInterpolate(values["mlflow_tracking_password"], values, 0)
	return username != "" && password != "", err
}

func sdkInterpolate(value string, values map[string]string, depth int) (string, error) {
	if depth > 10 {
		return "", errors.New("interpolation depth")
	}
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '%' {
			out.WriteByte(value[i])
			i++
			continue
		}
		if i+1 < len(value) && value[i+1] == '%' {
			out.WriteByte('%')
			i += 2
			continue
		}
		if i+1 >= len(value) || value[i+1] != '(' {
			return "", errors.New("invalid interpolation")
		}
		end := strings.Index(value[i+2:], ")s")
		if end < 0 {
			return "", errors.New("invalid interpolation")
		}
		end += i + 2
		key := strings.ToLower(value[i+2 : end])
		raw, ok := values[key]
		if !ok {
			return "", errors.New("missing interpolation key")
		}
		resolved, err := sdkInterpolate(raw, values, depth+1)
		if err != nil {
			return "", err
		}
		out.WriteString(resolved)
		i = end + 2
	}
	return out.String(), nil
}
