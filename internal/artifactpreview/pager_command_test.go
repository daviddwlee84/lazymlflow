package artifactpreview

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPagerCommandKeepsArtifactAndShellSyntaxAsArguments(t *testing.T) {
	file := PagerFile{Path: "/private/tmp/preview;$(unsafe).txt", Name: "report name.json"}
	lookup := func(name string) (string, error) {
		if name == "bat" {
			return "/tools/bat", nil
		}
		return "", errors.New("missing")
	}
	argv, err := pagerCommand(file, "sh -c ignored", lookup)
	if err != nil || !reflect.DeepEqual(argv, []string{"/tools/bat", "--paging=always", "--style=plain", "--file-name", file.Name, "--", file.Path}) {
		t.Fatalf("argv=%q err=%v", argv, err)
	}
	lookup = func(name string) (string, error) {
		if name == "my pager" {
			return "/tools/my pager", nil
		}
		return "", errors.New("missing")
	}
	argv, err = pagerCommand(file, `"my pager" --flag '$(touch never)'`, lookup)
	if err != nil || !reflect.DeepEqual(argv, []string{"/tools/my pager", "--flag", "$(touch never)", file.Path}) {
		t.Fatalf("shell syntax was not literal: %q %v", argv, err)
	}
}

func TestPagerMissingAndInvalidQuotesFailClearly(t *testing.T) {
	lookup := func(string) (string, error) { return "", errors.New("missing") }
	if _, err := pagerCommand(PagerFile{}, "", lookup); err == nil || !strings.Contains(err.Error(), "no pager") {
		t.Fatalf("missing tools: %v", err)
	}
	if _, err := pagerCommand(PagerFile{}, `"unfinished`, lookup); err == nil || !strings.Contains(err.Error(), "unfinished") {
		t.Fatalf("bad quote: %v", err)
	}
}

func TestPagerArgumentsPreserveQuotedWindowsExecutable(t *testing.T) {
	argv, err := pagerArguments(`"C:\Program Files\less.exe" -R`)
	if err != nil || !reflect.DeepEqual(argv, []string{`C:\Program Files\less.exe`, "-R"}) {
		t.Fatalf("Windows executable lost path separators: %q %v", argv, err)
	}
}
