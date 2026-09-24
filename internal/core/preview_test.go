package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestArtifactSizeDecodingDistinguishesUnknownAndEmpty(t *testing.T) {
	for _, test := range []struct {
		json  string
		size  int64
		known bool
	}{
		{`{"path":"a"}`, 0, false},
		{`{"path":"a","file_size":null}`, 0, false},
		{`{"path":"a","file_size":0}`, 0, true},
		{`{"path":"a","file_size":"0"}`, 0, true},
		{`{"path":"a","file_size":12}`, 12, true},
		{`{"path":"a","file_size":-1}`, -1, false},
	} {
		var artifact Artifact
		if err := json.Unmarshal([]byte(test.json), &artifact); err != nil {
			t.Fatal(err)
		}
		if artifact.FileSize != test.size || artifact.FileSizeKnown != test.known {
			t.Fatalf("size metadata %s => %+v", test.json, artifact)
		}
		encoded, err := json.Marshal(artifact)
		if err != nil {
			t.Fatal(err)
		}
		if test.known && test.size == 0 && !strings.Contains(string(encoded), `"file_size":0`) {
			t.Fatal("empty file lost known-zero size")
		}
	}
}

func TestPreviewApprovalSeparatesSizeAndProxyCost(t *testing.T) {
	small, large := int64(5), int64(100)
	q := PreviewRequest{MaxBytes: 10}
	if err := CheckPreviewApproval(PreviewInfo{Supported: true, Size: &small, MayDownloadWhole: true}, q); err != nil {
		t.Fatal("small old-server proxy should work automatically", err)
	}
	for _, info := range []PreviewInfo{{Supported: true, Size: &large, MayDownloadWhole: true}, {Supported: true, MayDownloadWhole: true}} {
		var approval *PreviewApprovalError
		if err := CheckPreviewApproval(info, q); !errors.As(err, &approval) || !approval.WholeFetch {
			t.Fatal("missing explicit proxy cost approval", err)
		}
		if err := CheckPreviewApproval(info, PreviewRequest{MaxBytes: 10, AllowLarge: true, AllowUnknown: true, AllowWholeFetch: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckPreviewApproval(PreviewInfo{Supported: false, Reason: "no bounded reader"}, PreviewRequest{MaxBytes: 10, AllowLarge: true, AllowUnknown: true, AllowWholeFetch: true}); err == nil {
		t.Fatal("approval made unsupported transport supported")
	}
}
