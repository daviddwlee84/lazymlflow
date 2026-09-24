package artifactpreview

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestCompleteJSONPreservesNumbersKeysAndEscapedStrings(t *testing.T) {
	data := []byte(`{"z":9007199254740993123456789,"a":[true,{},[]],"s":"comma, bracket] brace} quote\" slash\\"}`)
	doc, err := Prepare(core.PreviewResult{Data: data, BytesRead: int64(len(data))}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var expected bytes.Buffer
	if err := json.Indent(&expected, data, "", "  "); err != nil {
		t.Fatal(err)
	}
	if !doc.Formatted || doc.Binary || doc.Text != expected.String() || doc.RawText != string(data) {
		t.Fatalf("JSON changed semantic content: %+v", doc)
	}
	raw, err := Prepare(core.PreviewResult{Data: data}, Options{Raw: true})
	if err != nil || raw.Formatted || raw.Text != string(data) {
		t.Fatal("raw JSON option ignored", err)
	}
}

func TestJSONFormattingStopsAtOutputBudget(t *testing.T) {
	data := []byte(strings.Repeat("[", 1600) + "0" + strings.Repeat("]", 1600))
	doc, err := Prepare(core.PreviewResult{Data: data}, Options{})
	if err != nil || doc.Formatted || doc.Text != string(data) || !strings.Contains(doc.Warning, "4 MiB") {
		t.Fatal("expanded JSON exceeded formatting budget or replaced raw evidence", err)
	}
}

func TestTruncatedTextAndInvalidJSONRemainRaw(t *testing.T) {
	for _, data := range []string{`{"partial": [1,2`, `{"a":1}`} {
		doc, err := Prepare(core.PreviewResult{Data: []byte(data), Truncated: true, BytesRead: int64(len(data) + 1)}, Options{})
		if err != nil || doc.Formatted || doc.Text != data || !doc.Truncated || !strings.Contains(doc.Warning, "prefix") {
			t.Fatal("truncated JSON presented as a complete formatted document", err)
		}
	}
	doc, err := Prepare(core.PreviewResult{Data: []byte("not JSON\n\tindented")}, Options{})
	if err != nil || doc.Binary || doc.Text != "not JSON\n\tindented" {
		t.Fatal("plain text formatting damaged lines/tabs", err)
	}
}

func TestPreviewSanitizesTerminalControlsAndUTF8Boundary(t *testing.T) {
	data := "first\r\n\t\x1b[31mred\x1b[0m\x1b]52;c;evil\a\nlast\x07\r"
	doc, err := Prepare(core.PreviewResult{Data: []byte(data)}, Options{})
	if err != nil || doc.Binary || doc.Text != "first\n\tred\nlast\\x07\\x0d" {
		t.Fatalf("terminal content not sanitized: %q %v", doc.Text, err)
	}
	utf, err := Prepare(core.PreviewResult{Data: []byte{'a', 0xe4, 0xb8}, Truncated: true}, Options{})
	if err != nil || utf.Binary || utf.Text != "a" || !strings.Contains(utf.Warning, "UTF-8") {
		t.Fatalf("partial Unicode became binary or replacement garbage: %+v %v", utf, err)
	}
	for _, data := range [][]byte{{0, 1, 2}, {0xff, 0xfe}, {0xe4, 0xb8}} {
		doc, err := Prepare(core.PreviewResult{Data: data}, Options{})
		if err != nil || !doc.Binary || doc.Text != "" {
			t.Fatalf("binary content rendered: %+v %v", doc, err)
		}
	}
}

func TestPagerFileContainsOnlyPrivateBoundedPreviewAndCleansUp(t *testing.T) {
	doc, _ := Prepare(core.PreviewResult{Info: core.PreviewInfo{Path: "logs/report.json"}, Data: []byte(`{"prefix":1}`), Truncated: true, BytesRead: 13}, Options{})
	file, err := PreparePager(doc)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Cleanup()
	data, err := os.ReadFile(file.Path)
	if err != nil || !strings.Contains(string(data), "TRUNCATED PREFIX") || !strings.HasSuffix(string(data), doc.Text) {
		t.Fatal("pager content is not the fetched prefix", err)
	}
	if file.Name != "logs/report.json" {
		t.Fatal("pager lost syntax filename")
	}
	for _, p := range []string{file.Path, filepath.Dir(file.Path)} {
		info, err := os.Stat(p)
		want := os.FileMode(0600)
		if info != nil && info.IsDir() {
			want = 0700
		}
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != want {
			t.Fatal("preview temp permissions", p, err)
		}
	}
	if err := file.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file.Path); !os.IsNotExist(err) {
		t.Fatal("pager preview temp survived cleanup")
	}
	if _, err := PreparePager(Document{Binary: true}); err == nil {
		t.Fatal("binary pager accepted")
	}
}
