// Package artifactpreview prepares bounded artifact text for both CLI and TUI.
// It never executes artifact content or falls back to a full artifact download.
package artifactpreview

import (
	"context"
	"errors"
	"fmt"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

const DefaultLimit int64 = core.DefaultPreviewBytes
const MaxFormattedBytes = 4 << 20

type Options struct {
	MaxBytes        int64
	AllowLarge      bool
	AllowUnknown    bool
	AllowWholeFetch bool
	Raw             bool
}

type Document struct {
	Info      core.PreviewInfo `json:"info"`
	Text      string           `json:"text"`
	RawText   string           `json:"raw_text,omitempty"`
	Truncated bool             `json:"truncated"`
	Binary    bool             `json:"binary"`
	Formatted bool             `json:"formatted"`
	BytesRead int64            `json:"bytes_read"`
	Warning   string           `json:"warning,omitempty"`
}

func Inspect(ctx context.Context, backend core.Backend, runID, path string) (core.PreviewInfo, error) {
	previewer, ok := backend.(core.ArtifactPreviewer)
	if !ok {
		return core.PreviewInfo{RunID: runID, Path: path}, errors.New("this backend does not support bounded artifact previews; use the explicit download action")
	}
	return previewer.InspectArtifact(ctx, runID, path)
}

func Read(ctx context.Context, backend core.Backend, runID, path string, options Options) (Document, error) {
	previewer, ok := backend.(core.ArtifactPreviewer)
	if !ok {
		return Document{}, errors.New("this backend does not support bounded artifact previews; use the explicit download action")
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultLimit
	}
	if options.MaxBytes < 1 || options.MaxBytes > core.MaxPreviewBytes {
		return Document{}, fmt.Errorf("preview max_bytes must be between 1 and %d", core.MaxPreviewBytes)
	}
	result, err := previewer.ReadArtifactPreview(ctx, core.PreviewRequest{RunID: runID, Path: path, MaxBytes: options.MaxBytes, AllowLarge: options.AllowLarge, AllowUnknown: options.AllowUnknown, AllowWholeFetch: options.AllowWholeFetch})
	if err != nil {
		return Document{Info: result.Info}, err
	}
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	return Prepare(result, options)
}
