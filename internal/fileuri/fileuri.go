// Package fileuri keeps native filesystem paths out of URL authority fields.
package fileuri

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Path is a URL path, not an escaped URI. net/url performs the escaping once.
func Path(native string) string {
	path := filepath.ToSlash(native)
	if len(path) >= 3 && path[1] == ':' && (path[2] == '/' || path[2] == '\\') {
		path = strings.ReplaceAll(path, `\`, "/")
		return "/" + path
	}
	return path
}
func Native(path string) string {
	if runtime.GOOS == "windows" && len(path) >= 4 && path[0] == '/' && path[2] == ':' && path[3] == '/' {
		path = path[1:]
	}
	return filepath.FromSlash(path)
}
