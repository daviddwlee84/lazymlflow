package main

import (
	"runtime/debug"
	"strings"
)

func resolveVersion(injected string, info *debug.BuildInfo, ok bool) string {
	if version := strings.TrimSpace(injected); version != "" && version != "dev" && version != "(devel)" {
		return version
	}
	if ok && info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
