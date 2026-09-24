package artifactpreview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func Prepare(result core.PreviewResult, options Options) (Document, error) {
	document := Document{Info: result.Info, Truncated: result.Truncated, BytesRead: result.BytesRead}
	data := bytes.TrimPrefix(result.Data, []byte{0xef, 0xbb, 0xbf})
	warnings := []string{}
	if result.Truncated && !utf8.Valid(data) {
		for n := 1; n < utf8.UTFMax && n <= len(data); n++ {
			if utf8.Valid(data[:len(data)-n]) && !utf8.FullRune(data[len(data)-n:]) {
				data = data[:len(data)-n]
				warnings = append(warnings, "incomplete trailing UTF-8 character omitted")
				break
			}
		}
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		document.Binary = true
		document.Warning = "Binary or non-UTF-8 artifact; use the explicit download action to inspect it"
		return document, nil
	}
	raw, limited := sanitizeBounded(string(data), MaxFormattedBytes)
	document.RawText, document.Text = raw, raw
	if limited {
		document.Truncated = true
		warnings = append(warnings, "display limited to 4 MiB after sanitizing")
	}
	if !options.Raw && !result.Truncated && !limited && json.Valid(data) {
		if pretty, ok := formatJSON(data, MaxFormattedBytes); ok {
			formatted, clipped := sanitizeBounded(string(pretty), MaxFormattedBytes)
			if !clipped {
				document.Text, document.Formatted = formatted, formatted != raw
			} else {
				warnings = append(warnings, "formatted JSON exceeds 4 MiB; showing original text")
			}
		} else {
			warnings = append(warnings, "formatted JSON exceeds 4 MiB; showing original text")
		}
	}
	if result.Truncated {
		warnings = append(warnings, "showing a bounded prefix; the artifact was not read completely")
	}
	document.Warning = strings.Join(warnings, "; ")
	return document, nil
}

// Sanitize preserves indentation and lines while removing terminal escape
// sequences and showing other control characters as inert escape text.
func Sanitize(text string) string {
	value, _ := sanitizeBounded(text, MaxFormattedBytes)
	return value
}

func sanitizeBounded(text string, limit int) (string, bool) {
	text = ansi.Strip(strings.ReplaceAll(text, "\r\n", "\n"))
	var out strings.Builder
	out.Grow(min(len(text), limit))
	for _, r := range text {
		value := string(r)
		if r != '\n' && r != '\t' && (unicode.IsControl(r) || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069) {
			if r <= 0xff {
				value = fmt.Sprintf("\\x%02x", r)
			} else {
				value = fmt.Sprintf("\\u%04x", r)
			}
		}
		if out.Len()+len(value) > limit {
			return out.String(), true
		}
		out.WriteString(value)
	}
	return out.String(), false
}

// formatJSON preserves key order, numeric lexemes and escaped strings. Unlike
// json.Indent it can stop before deeply nested input expands past the budget.
func formatJSON(data []byte, limit int) ([]byte, bool) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, false
	}
	data = compact.Bytes()
	out := make([]byte, 0, min(len(data)+len(data)/4, limit))
	appendBytes := func(value ...byte) bool {
		if len(out)+len(value) > limit {
			return false
		}
		out = append(out, value...)
		return true
	}
	depth := 0
	newline := func() bool {
		if len(out)+1+depth*2 > limit {
			return false
		}
		out = append(out, '\n')
		for range depth * 2 {
			out = append(out, ' ')
		}
		return true
	}
	quoted, escaped := false, false
	for i, c := range data {
		if quoted {
			if !appendBytes(c) {
				return nil, false
			}
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
			if !appendBytes(c) {
				return nil, false
			}
		case '{', '[':
			if !appendBytes(c) {
				return nil, false
			}
			depth++
			if i+1 < len(data) && data[i+1] != '}' && data[i+1] != ']' {
				if !newline() {
					return nil, false
				}
			}
		case '}', ']':
			depth--
			if i > 0 && data[i-1] != '{' && data[i-1] != '[' {
				if !newline() {
					return nil, false
				}
			}
			if !appendBytes(c) {
				return nil, false
			}
		case ',':
			if !appendBytes(c) || !newline() {
				return nil, false
			}
		case ':':
			if !appendBytes(':', ' ') {
				return nil, false
			}
		default:
			if !appendBytes(c) {
				return nil, false
			}
		}
	}
	return out, true
}
