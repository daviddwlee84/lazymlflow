// Package serverform supplies the same setup draft and review to CLI and TUI.
package serverform

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazymlflow/internal/server"
)

type Result struct {
	Directory             string
	Spec                  server.ServerSpec
	Start, RegisterTarget bool
	AdminPasswordEnv      string
}

type field struct {
	key, label, help string
	choices          []string
}
type Model struct {
	Result                     Result
	Done, Cancelled            bool
	Err                        error
	values                     map[string]string
	inputs                     map[string]textinput.Model
	step, focus, width, height int
	scroll                     int
	pressed                    string
}

func New(directory string, spec server.ServerSpec) *Model {
	if directory == "" {
		directory = "./mlflow-server"
	}
	if spec.ID == "" {
		spec.ID = "mlflow"
	}
	m := &Model{Result: Result{Directory: directory, Spec: spec}, width: 80, height: 24, inputs: map[string]textinput.Model{}, values: map[string]string{
		"id": spec.ID, "dir": directory, "backend": spec.Backend, "artifacts": spec.Artifacts, "access": spec.Access,
		"hostname": spec.Hostname, "bind": spec.BindAddress, "port": strconv.Itoa(spec.Port), "tls": spec.TLS, "auth": spec.Auth,
		"artifact_path": spec.ArtifactPath, "nas_mount": spec.NASMount, "cert": spec.CertFile, "key": spec.KeyFile,
		"endpoint": spec.S3Endpoint, "bucket": spec.S3Bucket, "region": spec.S3Region, "access_env": spec.S3AccessKeyEnv, "secret_env": spec.S3SecretKeyEnv,
		"admin": spec.AdminUsername, "admin_env": "", "start": "no", "register": "no",
		"usage": "personal",
	}}
	if spec.Backend == "postgres" {
		m.values["usage"] = "team"
	}
	if spec.Access == "lan" {
		m.values["usage"] = "remote-training"
	}
	for k, v := range m.values {
		i := textinput.New()
		i.Prompt = ""
		i.CharLimit = 4096
		i.SetValue(v)
		m.inputs[k] = i
	}
	return m
}
func (m *Model) Init() tea.Cmd { return m.focusField() }

// PrefillFinish preserves explicitly supplied CLI choices in the shared draft.
func (m *Model) PrefillFinish(start, register bool, adminEnv string) {
	if start {
		m.set("start", "yes")
	}
	if register {
		m.set("register", "yes")
	}
	m.set("admin_env", adminEnv)
}
func (m *Model) fields() []field {
	switch m.step {
	case 0:
		return []field{
			{"id", "Stack name", "Used for this stack and optional target ID", nil},
			{"dir", "Output directory", "New directory; existing projects are preserved", nil},
			{"usage", "Training setup", "Choose your needs to prefill a recommendation; all choices remain editable", []string{"personal", "team", "remote-training"}},
			{"backend", "Metadata database", "SQLite: personal use. PostgreSQL: shared/concurrent training", []string{"sqlite", "postgres"}},
			{"artifacts", "Artifact storage", "Local first; NAS when available; RustFS is the suggested managed S3", []string{"local", "nas", "rustfs", "seaweedfs", "s3"}},
			{"access", "Who connects?", "LAN chooses HTTPS and native permissions; next page can override", []string{"loopback", "lan"}},
		}
	case 1:
		f := []field{{"hostname", "Server hostname", "Resolvable from training machines; included in Host validation", nil}, {"bind", "Bind address", "Interface on the server host", nil}, {"port", "Published port", "Client endpoint port", nil}, {"tls", "TLS", "Internal CA, existing certificate, or explicit off", []string{"off", "internal", "provided"}}, {"auth", "Authentication", "Native MLflow users/roles, or explicit off", []string{"off", "native"}}}
		if m.values["tls"] == "provided" {
			f = append(f, field{"cert", "Certificate PEM", "Existing server-side certificate file", nil}, field{"key", "Private-key PEM", "Mounted read-only; its content is never shown", nil})
		}
		return f
	case 2:
		var f []field
		switch m.values["artifacts"] {
		case "local":
			f = append(f, field{"artifact_path", "Artifact directory", "Optional existing host directory; blank uses a persistent Docker volume", nil})
		case "nas":
			f = append(f, field{"artifact_path", "Artifact directory", "Dedicated directory on an already mounted NAS", nil}, field{"nas_mount", "NAS mount root", "Required mount identity is checked before starting", nil})
		case "s3":
			f = append(f, field{"endpoint", "S3 endpoint", "Blank for AWS S3; otherwise an HTTP(S) endpoint", nil}, field{"access_env", "Access-key environment", "Name of an existing environment variable; never paste a key", nil}, field{"secret_env", "Secret-key environment", "Name of an existing environment variable; never paste a secret", nil})
		}
		if a := m.values["artifacts"]; a == "s3" || a == "rustfs" || a == "seaweedfs" {
			f = append(f, field{"bucket", "S3 bucket", "External buckets must already exist", nil}, field{"region", "S3 region", "Usually us-east-1 for a local S3 service", nil})
		}
		if m.values["auth"] == "native" {
			f = append(f, field{"admin", "Bootstrap administrator", "Stable username; subsequent up never resets its password", nil}, field{"admin_env", "Admin password environment", "Optional variable name; blank generates a private bootstrap password file", nil})
		}
		return f
	case 3:
		return []field{{"start", "Start after creation", "Docker is required only when starting", []string{"no", "yes"}}, {"register", "Add connection target", "Save a new profile without replacing your current default", []string{"no", "yes"}}}
	}
	return nil
}
func (m *Model) focusField() tea.Cmd {
	for k, i := range m.inputs {
		i.Blur()
		m.inputs[k] = i
	}
	f := m.fields()
	m.focus = max(0, min(m.focus, len(f)))
	if m.focus < len(f) && len(f[m.focus].choices) == 0 {
		i := m.inputs[f[m.focus].key]
		cmd := i.Focus()
		m.inputs[f[m.focus].key] = i
		return cmd
	}
	return nil
}
func (m *Model) set(key, value string) {
	m.values[key] = value
	i := m.inputs[key]
	i.SetValue(value)
	m.inputs[key] = i
}
func (m *Model) choose(f field, delta int) {
	i := 0
	for n, v := range f.choices {
		if v == m.values[f.key] {
			i = n
		}
	}
	i = (i + delta + len(f.choices)) % len(f.choices)
	m.set(f.key, f.choices[i])
	if f.key == "usage" {
		r := server.Recommend(server.Requirements{Team: m.values["usage"] != "personal", RemoteTraining: m.values["usage"] == "remote-training", NeedObjectStorage: m.values["artifacts"] == "rustfs" || m.values["artifacts"] == "seaweedfs", Provider: m.values["artifacts"]})
		s := r.Spec
		m.set("backend", s.Backend)
		m.set("access", s.Access)
		m.set("bind", s.BindAddress)
		m.set("port", strconv.Itoa(s.Port))
		m.set("tls", s.TLS)
		m.set("auth", s.Auth)
		if s.Access == "lan" && (m.values["hostname"] == "" || m.values["hostname"] == "localhost") {
			m.set("hostname", "mlflow.internal")
		}
	}
	if f.key == "access" {
		if m.values[f.key] == "lan" {
			m.set("bind", "0.0.0.0")
			m.set("port", "8443")
			m.set("tls", "internal")
			m.set("auth", "native")
			if m.values["hostname"] == "" || m.values["hostname"] == "localhost" {
				m.set("hostname", "mlflow.internal")
			}
			m.set("backend", "postgres")
		} else {
			m.set("bind", "127.0.0.1")
			m.set("hostname", "localhost")
			m.set("port", "8000")
			m.set("tls", "off")
			m.set("auth", "off")
		}
	}
	if f.key == "artifacts" && m.values[f.key] != "local" {
		m.set("backend", "postgres")
	}
}
func (m *Model) collect() error {
	s := m.Result.Spec
	s.ID = strings.TrimSpace(m.values["id"])
	s.Backend = m.values["backend"]
	s.Artifacts = m.values["artifacts"]
	s.Access = m.values["access"]
	s.TLS = m.values["tls"]
	s.Auth = m.values["auth"]
	s.Hostname = strings.TrimSpace(m.values["hostname"])
	s.BindAddress = strings.TrimSpace(m.values["bind"])
	p, err := strconv.Atoi(m.values["port"])
	if err != nil {
		return fmt.Errorf("Published port must be a number")
	}
	s.Port = p
	s.ArtifactPath = strings.TrimSpace(m.values["artifact_path"])
	s.NASMount = strings.TrimSpace(m.values["nas_mount"])
	s.CertFile = strings.TrimSpace(m.values["cert"])
	s.KeyFile = strings.TrimSpace(m.values["key"])
	s.S3Endpoint = strings.TrimSpace(m.values["endpoint"])
	s.S3Bucket = strings.TrimSpace(m.values["bucket"])
	s.S3Region = strings.TrimSpace(m.values["region"])
	s.S3AccessKeyEnv = strings.TrimSpace(m.values["access_env"])
	s.S3SecretKeyEnv = strings.TrimSpace(m.values["secret_env"])
	s.AdminUsername = strings.TrimSpace(m.values["admin"])
	if s.TLS != "provided" {
		s.CertFile = ""
		s.KeyFile = ""
	}
	if s.Artifacts != "nas" {
		s.NASMount = ""
	}
	if s.Artifacts != "nas" && s.Artifacts != "local" {
		s.ArtifactPath = ""
	}
	if s.Artifacts != "s3" {
		s.S3Endpoint = ""
		s.S3AccessKeyEnv = ""
		s.S3SecretKeyEnv = ""
	}
	if s.Artifacts == "local" || s.Artifacts == "nas" {
		s.S3Bucket = ""
		s.S3Region = ""
	}
	s, err = server.Normalize(s)
	if err != nil {
		return err
	}
	d := strings.TrimSpace(m.values["dir"])
	if d == "" {
		return fmt.Errorf("Output directory is required")
	}
	m.Result = Result{Directory: d, Spec: s, Start: m.values["start"] == "yes", RegisterTarget: m.values["register"] == "yes", AdminPasswordEnv: strings.TrimSpace(m.values["admin_env"])}
	return nil
}
func (m *Model) next() tea.Cmd {
	m.Err = nil
	if m.step == 3 {
		if err := m.collect(); err != nil {
			m.Err = err
			return nil
		}
	}
	if m.step == 4 {
		m.Done = true
		return nil
	}
	m.step++
	m.focus = 0
	m.scroll = 0
	return m.focusField()
}
func (m *Model) back() tea.Cmd {
	m.Err = nil
	if m.step == 0 {
		m.Cancelled = true
		return nil
	}
	m.step--
	m.focus = 0
	return m.focusField()
}
func (m *Model) clampScroll() { lines, hits := m.lines(); m.scroll = m.viewport(lines, hits) }
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	if m.Done || m.Cancelled {
		return m, nil
	}
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, v.Width)
		m.height = max(1, v.Height)
		m.pressed = ""
		for k, i := range m.inputs {
			i.SetWidth(max(1, m.width-6))
			m.inputs[k] = i
		}
		if m.step == 4 {
			m.clampScroll()
		}
		return m, nil
	case tea.MouseClickMsg:
		m.pressed = ""
		if v.Button == tea.MouseLeft {
			m.pressed = m.hit(v.X, v.Y)
		}
		return m, nil
	case tea.MouseReleaseMsg:
		p := m.pressed
		m.pressed = ""
		if v.Button != tea.MouseLeft || p == "" || p != m.hit(v.X, v.Y) {
			return m, nil
		}
		if p == "next" {
			return m, m.next()
		}
		if p == "back" {
			return m, m.back()
		}
		var i int
		if _, e := fmt.Sscanf(p, "field:%d", &i); e == nil && i < len(m.fields()) {
			m.focus = i
			f := m.fields()[i]
			if len(f.choices) > 0 {
				m.choose(f, 1)
			}
			return m, m.focusField()
		}
		return m, nil
	case tea.MouseWheelMsg:
		if m.step == 4 {
			if v.Button == tea.MouseWheelUp {
				m.scroll = max(0, m.scroll-1)
			} else {
				m.scroll++
			}
			m.clampScroll()
		}
		return m, nil
	case tea.MouseMotionMsg:
		return m, nil
	case tea.KeyPressMsg:
		m.pressed = ""
		k := v.String()
		if k == "ctrl+c" {
			m.Cancelled = true
			return m, nil
		}
		if k == "esc" {
			return m, m.back()
		}
		if m.step == 4 {
			switch k {
			case "up", "k":
				m.scroll = max(0, m.scroll-1)
			case "down", "j":
				m.scroll++
			case "pgdown", "ctrl+d":
				m.scroll += 10
			case "pgup", "ctrl+u":
				m.scroll = max(0, m.scroll-10)
			case "home":
				m.scroll = 0
			case "end", "G":
				m.scroll = 1 << 30
			}
			m.clampScroll()
			if k == "enter" {
				return m, m.next()
			}
			if k == "shift+tab" || k == "left" {
				return m, m.back()
			}
			return m, nil
		}
		f := m.fields()
		if k == "ctrl+n" {
			return m, m.next()
		}
		if k == "tab" || k == "shift+tab" || k == "enter" {
			if k == "enter" && m.focus == len(f) {
				return m, m.next()
			}
			if k == "shift+tab" {
				m.focus = (m.focus + len(f)) % (len(f) + 1)
			} else {
				m.focus = (m.focus + 1) % (len(f) + 1)
			}
			return m, m.focusField()
		}
		if m.focus < len(f) && len(f[m.focus].choices) > 0 {
			switch k {
			case "left", "up", "h", "k":
				m.choose(f[m.focus], -1)
			case "right", "down", "l", "j", "space":
				m.choose(f[m.focus], 1)
			}
			return m, nil
		}
	}
	f := m.fields()
	if m.focus < len(f) && len(f[m.focus].choices) == 0 {
		k := f[m.focus].key
		i := m.inputs[k]
		var cmd tea.Cmd
		i, cmd = i.Update(msg)
		m.inputs[k] = i
		m.values[k] = i.Value()
		return m, cmd
	}
	return m, nil
}
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
func (m *Model) lines() ([]string, map[int]string) {
	titles := []string{"Choose a setup", "Connections and protection", "Storage and credentials", "Finish options", "Review and create"}
	lines := []string{"MLflow server setup · " + titles[m.step], ""}
	hits := map[int]string{}
	if m.step == 4 {
		r := m.Result
		s := r.Spec
		lines = append(lines, "Directory: "+safe(r.Directory), "Stack: "+s.ID, "Metadata: "+s.Backend+" · artifacts: "+s.Artifacts, "Access: "+s.Access+" · "+safe(s.Hostname)+":"+strconv.Itoa(s.Port), "TLS: "+s.TLS+" · authentication: "+s.Auth, "Start: "+strconv.FormatBool(r.Start)+" · add target: "+strconv.FormatBool(r.RegisterTarget), "", "Creates a new project. Existing data and target defaults are preserved.")
		if s.Access == "lan" && (s.TLS == "off" || s.Auth == "off") {
			lines = append(lines, "Selected LAN opt-out: "+s.TLS+" TLS / "+s.Auth+" authentication")
		}
		if s.Auth == "native" {
			if r.AdminPasswordEnv == "" {
				lines = append(lines, "Bootstrap password: generated into a private file")
			} else {
				lines = append(lines, "Bootstrap password environment: "+safe(r.AdminPasswordEnv))
			}
		}
	} else {
		for n, f := range m.fields() {
			mark := "  "
			if n == m.focus {
				mark = "> "
			}
			hits[len(lines)] = fmt.Sprintf("field:%d", n)
			lines = append(lines, mark+f.label)
			value := safe(m.values[f.key])
			if len(f.choices) > 0 {
				value = "‹ " + value + " ›"
			} else if n == m.focus {
				value = m.inputs[f.key].View()
			}
			hits[len(lines)] = fmt.Sprintf("field:%d", n)
			lines = append(lines, "  "+value)
			if n == m.focus {
				lines = append(lines, "  "+f.help)
			}
		}
	}
	if m.Err != nil {
		lines = append(lines, "", "Error: "+safe(m.Err.Error()))
	}
	lines = append(lines, "")
	mark := "  "
	if m.focus == len(m.fields()) || m.step == 4 {
		mark = "> "
	}
	label := "Next"
	if m.step == 4 {
		label = "Create project"
	}
	hits[len(lines)] = "next"
	lines = append(lines, mark+"[ "+label+" ]")
	hits[len(lines)] = "back"
	lines = append(lines, "  [ Back / cancel ]")
	return lines, hits
}
func (m *Model) viewport(lines []string, hits map[int]string) int {
	capacity := max(1, m.height-2)
	if m.step == 4 {
		return max(0, min(m.scroll, len(lines)-capacity))
	}
	if len(lines) <= capacity {
		return 0
	}
	wanted := len(lines) - 2
	if m.step < 4 && m.focus < len(m.fields()) {
		for i := 0; i < len(lines); i++ {
			if hits[i] == fmt.Sprintf("field:%d", m.focus) {
				wanted = i
				break
			}
		}
	}
	return max(0, min(wanted-capacity+4, len(lines)-capacity))
}
func (m *Model) hit(x, y int) string {
	if x < 0 || x >= m.width || y < 0 || y >= m.height-2 {
		return ""
	}
	l, h := m.lines()
	return h[y+m.viewport(l, h)]
}
func (original *Model) View(width, height int) string {
	copy := *original
	m := &copy
	m.width = max(1, width)
	m.height = max(1, height)
	lines, hits := m.lines()
	start := m.viewport(lines, hits)
	capacity := max(1, height-2)
	out := make([]string, 0, max(1, height))
	for i := start; i < min(len(lines), start+capacity); i++ {
		out = append(out, ansi.Truncate(lines[i], max(1, width), "…"))
	}
	for len(out) < capacity {
		out = append(out, "")
	}
	if height > 1 {
		hint := "Tab next field · ←/→ choices · Ctrl+N next · Esc back"
		if m.step == 4 {
			hint = "↑↓/jk scroll review · Enter create · Esc back"
		}
		out = append(out, ansi.Truncate(hint, max(1, width), "…"))
	}
	if height > 2 {
		out = append(out, ansi.Truncate("Enter activates Next / Create · Ctrl+C cancels", max(1, width), "…"))
	}
	return strings.Join(out[:min(len(out), max(1, height))], "\n")
}
