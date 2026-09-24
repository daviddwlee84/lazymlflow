package artifactpreview

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

// PagerCommand builds argv directly. Artifact names and PAGER contents are
// never interpolated into a shell command or expanded as shell expressions.
func PagerCommand(file PagerFile) ([]string, error) {
	return pagerCommand(file, os.Getenv("PAGER"), exec.LookPath)
}

func pagerCommand(file PagerFile, pager string, lookup func(string) (string, error)) ([]string, error) {
	for _, name := range []string{"bat", "batcat"} {
		if executable, err := lookup(name); err == nil {
			return []string{executable, "--paging=always", "--style=plain", "--file-name", file.Name, "--", file.Path}, nil
		}
	}
	if strings.TrimSpace(pager) != "" {
		args, err := pagerArguments(pager)
		if err != nil {
			return nil, err
		}
		executable, err := lookup(args[0])
		if err != nil {
			return nil, fmt.Errorf("PAGER executable unavailable: %w", err)
		}
		args[0] = executable
		// The private absolute filename cannot be interpreted as a flag. Avoid
		// assuming that every user-configured pager recognizes a -- separator.
		return append(args, file.Path), nil
	}
	for _, name := range []string{"less", "more"} {
		if executable, err := lookup(name); err == nil {
			if strings.EqualFold(strings.TrimSuffix(filepath.Base(executable), filepath.Ext(executable)), "less") {
				return []string{executable, "-R", "--", file.Path}, nil
			}
			return []string{executable, file.Path}, nil
		}
	}
	return nil, errors.New("no pager found; install bat or less, or set PAGER to an executable")
}

func pagerArguments(value string) ([]string, error) {
	var result []string
	var part strings.Builder
	var quote rune
	escaped, started := false, false
	runes := []rune(value)
	for i, r := range runes {
		if escaped {
			part.WriteRune(r)
			escaped, started = false, true
			continue
		}
		if r == '\\' && quote != '\'' {
			if i+1 < len(runes) {
				next := runes[i+1]
				if next != '\\' && next != '"' && !(quote == 0 && (next == '\'' || unicode.IsSpace(next))) {
					part.WriteRune(r)
					started = true
					continue
				}
			}
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				part.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote, started = r, true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				result = append(result, part.String())
				part.Reset()
				started = false
			}
			continue
		}
		part.WriteRune(r)
		started = true
	}
	if quote != 0 || escaped {
		return nil, errors.New("PAGER has an unfinished quote or escape")
	}
	if started {
		result = append(result, part.String())
	}
	if len(result) == 0 || result[0] == "" {
		return nil, errors.New("PAGER must name an executable")
	}
	return result, nil
}
