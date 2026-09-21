package serverform

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/server"
)

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}
func TestFormReviewAndBack(t *testing.T) {
	m := New(t.TempDir()+"/stack", server.DefaultSpec())
	m.Init()
	m.set("id", "test")
	m.next()
	m.next()
	m.next()
	m.next()
	if m.step != 4 || m.Done || m.Err != nil {
		t.Fatalf("review: %+v", m)
	}
	m.Update(key("esc"))
	if m.step != 3 || m.values["id"] != "test" {
		t.Fatal("Back lost draft")
	}
	m.next()
	m.Update(key("enter"))
	if !m.Done || m.Result.Spec.ID != "test" {
		t.Fatal("review did not submit")
	}
}
func TestFormChoicesRecommendAndPermitLANOptOut(t *testing.T) {
	m := New("./stack", server.DefaultSpec())
	m.choose(field{key: "usage", choices: []string{"personal", "team", "remote-training"}}, 2)
	if m.values["access"] != "lan" || m.values["backend"] != "postgres" || m.values["auth"] != "native" {
		t.Fatal(m.values)
	}
	m.set("auth", "off")
	m.set("tls", "off")
	if err := m.collect(); err != nil {
		t.Fatal(err)
	}
	if m.Result.Spec.Auth != "off" {
		t.Fatal("explicit opt-out lost")
	}
}
func TestFormChangingProvidersClearsInactiveOptions(t *testing.T) {
	m := New("./stack", server.DefaultSpec())
	m.set("endpoint", "https://s3.example.com")
	m.set("bucket", "old-bucket")
	m.set("region", "old-region")
	m.set("nas_mount", "/old")
	m.set("cert", "/old/cert")
	m.set("key", "/old/key")
	if err := m.collect(); err != nil {
		t.Fatal(err)
	}
	if m.Result.Spec.S3Endpoint != "" || m.Result.Spec.NASMount != "" || m.Result.Spec.CertFile != "" {
		t.Fatal(m.Result.Spec)
	}
}
func TestFormTextOwnsKeysAndPureNarrowView(t *testing.T) {
	m := New("./stack", server.DefaultSpec())
	m.Init()
	before := m.values["id"]
	for _, s := range []string{"j", "q", "/"} {
		m.Update(key(s))
	}
	if m.Cancelled || m.Done || m.values["id"] == before || !strings.Contains(m.values["id"], "jq/") {
		t.Fatal("printable text became navigation")
	}
	beforeValues := map[string]string{}
	for k, v := range m.values {
		beforeValues[k] = v
	}
	width, height := m.width, m.height
	for _, size := range [][2]int{{80, 24}, {38, 12}, {12, 4}, {1, 1}} {
		v := m.View(size[0], size[1])
		for _, line := range strings.Split(v, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("too wide: %q", line)
			}
		}
		if len(strings.Split(v, "\n")) > size[1] {
			t.Fatal("too tall")
		}
	}
	if !reflect.DeepEqual(beforeValues, m.values) || width != m.width || height != m.height {
		t.Fatal("View mutated input state")
	}
}
func TestFormCancelAndInvalidReview(t *testing.T) {
	m := New("./stack", server.DefaultSpec())
	m.Update(key("esc"))
	if !m.Cancelled {
		t.Fatal("first Esc should cancel")
	}
	m = New("./stack", server.DefaultSpec())
	m.set("port", "not-a-port")
	m.step = 3
	m.next()
	if m.step != 3 || m.Err == nil || m.Done {
		t.Fatal("invalid request reached review")
	}
}

func TestReviewCanBeReadAtNarrowHeight(t *testing.T) {
	m := New("./stack", server.DefaultSpec())
	m.step = 3
	m.next()
	m.Update(tea.WindowSizeMsg{Width: 38, Height: 12})
	if !strings.Contains(m.View(38, 12), "Directory:") {
		t.Fatal("review did not begin with source/destination")
	}
	m.Update(key("G"))
	end := m.scroll
	if end == 0 {
		t.Fatal("long review did not scroll")
	}
	m.Update(key("k"))
	if m.scroll != end-1 {
		t.Fatal("End left an unbounded scroll offset")
	}
	if m.Done {
		t.Fatal("scroll submitted review")
	}
}
