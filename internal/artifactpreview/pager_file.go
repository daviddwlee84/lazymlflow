package artifactpreview

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PagerFile struct {
	Path string
	Name string
	dir  string
}

func (f PagerFile) Cleanup() error {
	if f.dir == "" {
		return nil
	}
	return os.RemoveAll(f.dir)
}

// PreparePager writes only the already fetched and sanitized preview. Native
// pagers never acquire a larger artifact or execute its contents.
func PreparePager(document Document) (PagerFile, error) {
	if document.Binary {
		return PagerFile{}, errors.New("binary artifacts cannot be opened in the text pager")
	}
	directory, err := os.MkdirTemp("", "lazymlflow-preview-")
	if err != nil {
		return PagerFile{}, err
	}
	result := PagerFile{Path: filepath.Join(directory, "preview.txt"), Name: Sanitize(document.Info.Path), dir: directory}
	cleanup := true
	defer func() {
		if cleanup {
			_ = result.Cleanup()
		}
	}()
	result.Name = strings.ReplaceAll(strings.ReplaceAll(result.Name, "\n", " "), "\t", " ")
	file, err := os.OpenFile(result.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return PagerFile{}, err
	}
	size := "unknown size"
	if document.Info.Size != nil {
		size = fmt.Sprintf("%d bytes total", *document.Info.Size)
	}
	status := "complete preview"
	if document.Truncated {
		status = "TRUNCATED PREFIX"
	}
	banner := fmt.Sprintf("lazymlflow · %s · %s · %s\n", result.Name, size, status)
	if document.Warning != "" {
		banner += Sanitize(document.Warning) + "\n"
	}
	_, err = file.WriteString(banner + "\n" + document.Text)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return PagerFile{}, err
	}
	cleanup = false
	return result, nil
}
