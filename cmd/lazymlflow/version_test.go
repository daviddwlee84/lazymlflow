package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionSources(t *testing.T) {
	for _, test := range []struct {
		injected, module string
		ok               bool
		want             string
	}{
		{"v0.2.0", "v0.1.0", true, "v0.2.0"},
		{"dev", "v0.1.0", true, "v0.1.0"},
		{"", "v0.1.0-alpha.1", true, "v0.1.0-alpha.1"},
		{"dev", "v0.0.0-20260920120000-abcdef123456", true, "v0.0.0-20260920120000-abcdef123456"},
		{"dev", "(devel)", true, "dev"},
		{"dev", "", true, "dev"},
		{"dev", "v0.1.0", false, "dev"},
	} {
		info := &debug.BuildInfo{Main: debug.Module{Version: test.module}}
		if got := resolveVersion(test.injected, info, test.ok); got != test.want {
			t.Fatalf("%+v: got %q", test, got)
		}
	}
	if got := resolveVersion("dev", nil, true); got != "dev" {
		t.Fatal(got)
	}
}
