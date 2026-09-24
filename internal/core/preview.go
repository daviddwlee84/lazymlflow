package core

import (
	"context"
	"fmt"
	"strings"
)

const DefaultPreviewBytes int64 = 1 << 20
const MaxPreviewBytes int64 = 64 << 20

// PreviewInfo contains displayable metadata only. Storage URLs and credentials
// must never appear in preview results, confirmation prompts or JSON output.
type PreviewInfo struct {
	RunID            string `json:"run_id"`
	Path             string `json:"path"`
	Size             *int64 `json:"size,omitempty"`
	IsDir            bool   `json:"is_dir"`
	Supported        bool   `json:"supported"`
	MayDownloadWhole bool   `json:"may_download_whole"`
	ContentType      string `json:"content_type,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

type PreviewRequest struct {
	RunID           string
	Path            string
	MaxBytes        int64
	AllowLarge      bool
	AllowUnknown    bool
	AllowWholeFetch bool
}

type PreviewResult struct {
	Info      PreviewInfo `json:"info"`
	Data      []byte      `json:"-"`
	Truncated bool        `json:"truncated"`
	BytesRead int64       `json:"bytes_read"`
}

// ArtifactPreviewer is optional: existing backends retain complete download
// support without promising that an SDK can fetch a bounded byte prefix.
type ArtifactPreviewer interface {
	InspectArtifact(context.Context, string, string) (PreviewInfo, error)
	ReadArtifactPreview(context.Context, PreviewRequest) (PreviewResult, error)
}

type PreviewApprovalError struct {
	Info       PreviewInfo
	Large      bool
	Unknown    bool
	WholeFetch bool
}

func (e *PreviewApprovalError) Error() string {
	reasons := []string{}
	if e.Large {
		if e.Info.Size != nil {
			reasons = append(reasons, fmt.Sprintf("artifact is %d bytes", *e.Info.Size))
		} else {
			reasons = append(reasons, "artifact exceeds the preview byte limit")
		}
	}
	if e.Unknown {
		reasons = append(reasons, "artifact size is unknown")
	}
	if e.WholeFetch {
		reasons = append(reasons, "this server may fetch the whole cloud object before serving a prefix")
	}
	return "preview confirmation required: " + strings.Join(reasons, "; ")
}

func CheckPreviewApproval(info PreviewInfo, request PreviewRequest) error {
	if request.MaxBytes < 1 || request.MaxBytes > MaxPreviewBytes {
		return fmt.Errorf("preview max_bytes must be between 1 and %d", MaxPreviewBytes)
	}
	if info.IsDir {
		return fmt.Errorf("artifact %q is a directory; select a file to preview", info.Path)
	}
	if !info.Supported {
		if info.Reason != "" {
			return fmt.Errorf("artifact preview unavailable: %s", info.Reason)
		}
		return fmt.Errorf("artifact preview unavailable for this storage backend")
	}
	e := &PreviewApprovalError{Info: info}
	e.Large = info.Size != nil && *info.Size > request.MaxBytes && !request.AllowLarge
	e.Unknown = info.Size == nil && !request.AllowUnknown
	e.WholeFetch = info.MayDownloadWhole && (info.Size == nil || *info.Size > request.MaxBytes) && !request.AllowWholeFetch
	if e.Large || e.Unknown || e.WholeFetch {
		return e
	}
	return nil
}
