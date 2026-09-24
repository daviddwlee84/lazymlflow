package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Save edits only fields whose values changed, preserving unrelated TOML,
// comments, and unknown future options. It rejects intervening edits and uses
// a same-directory temporary file plus rename for atomic replacement.
func (c *Config) Save(path string) error {
	if path == "" {
		path = c.path
	}
	if path == "" {
		path = DefaultPath()
	}
	if path == "" {
		return errors.New("cannot determine config path")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if c.path != "" {
		originalPath, _ := filepath.Abs(c.path)
		if originalPath != path {
			return errors.New("load the destination config before saving to a different path")
		}
	}
	requestedPath := path
	// Dotfile managers commonly expose config through a symlink. Replace the
	// actual file atomically, retaining the user's link and selected path.
	if c.existed {
		if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
			path = resolved
		}
	}
	if err = c.Validate(); err != nil {
		return err
	}
	b, err := c.render()
	if err != nil {
		return err
	}
	if err = checkUnchanged(path, c.original, c.existed); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Cooperating writers hold this for the entire compare-and-replace. The
	// second content comparison also detects ordinary editor saves during write.
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("config is being edited (lock %s.lock); retry after the other writer finishes: %w", path, err)
	}
	lock.Close()
	defer os.Remove(path + ".lock")
	if err = checkUnchanged(path, c.original, c.existed); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	mode := os.FileMode(0600)
	if info, e := os.Stat(path); e == nil {
		mode = info.Mode().Perm()
	}
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = checkUnchanged(path, c.original, c.existed); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	if dir, e := os.Open(filepath.Dir(path)); e == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	c.path = requestedPath
	c.original = bytes.Clone(b)
	c.existed = true
	return nil
}

func checkUnchanged(path string, original []byte, existed bool) error {
	current, err := os.ReadFile(path)
	if os.IsNotExist(err) && !existed {
		return nil
	}
	if err != nil {
		return fmt.Errorf("config changed since it was loaded; reload before saving: %w", err)
	}
	if !existed || !bytes.Equal(current, original) {
		return errors.New("config changed since it was loaded; reload before saving")
	}
	return nil
}

type assignment struct {
	key        string
	start, end int
}
type section struct {
	name       string
	target     int
	start, end int
	entries    []assignment
}
type edit struct {
	start, end int
	text       string
}

func sections(b []byte) ([]section, error) {
	result := []section{{target: -1, start: 0, end: len(b)}}
	target := -1
	var p unstable.Parser
	p.Reset(b)
	for p.NextExpression() {
		n := p.Expression()
		if n.Kind != unstable.Table && n.Kind != unstable.ArrayTable && n.Kind != unstable.KeyValue {
			continue
		}
		it := n.Key()
		var keys []string
		offset := 0
		for it.Next() {
			if len(keys) == 0 {
				offset = int(it.Node().Raw.Offset)
			}
			keys = append(keys, string(it.Node().Data))
		}
		start := bytes.LastIndexByte(b[:offset], '\n') + 1
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			if n.Kind == unstable.ArrayTable && len(keys) == 1 && keys[0] == "targets" {
				target++
			}
			t := -1
			if len(keys) > 0 && keys[0] == "targets" {
				t = target
			}
			result[len(result)-1].end = start
			result = append(result, section{name: strings.Join(keys, "."), target: t, start: start, end: len(b)})
		} else {
			end := valueEnd(b, offset)
			result[len(result)-1].entries = append(result[len(result)-1].entries, assignment{key: strings.Join(keys, "."), start: offset, end: end})
		}
	}
	return result, p.Error()
}

// valueEnd finds a TOML assignment's end while respecting multiline strings,
// arrays, and inline tables. The parser above has already validated syntax.
func valueEnd(b []byte, start int) int {
	depth := 0
	quote := byte(0)
	triple := false
	escaped := false
	for i := start; i < len(b); i++ {
		c := b[i]
		if quote != 0 {
			if quote == '"' && escaped {
				escaped = false
				continue
			}
			if quote == '"' && c == '\\' {
				escaped = true
				continue
			}
			if c == quote {
				if triple {
					if i+2 < len(b) && b[i+1] == quote && b[i+2] == quote {
						quote = 0
						triple = false
						i += 2
					}
				} else {
					quote = 0
				}
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			triple = i+2 < len(b) && b[i+1] == c && b[i+2] == c
			if triple {
				i += 2
			}
			continue
		}
		if c == '[' || c == '{' {
			depth++
		}
		if c == ']' || c == '}' {
			depth--
		}
		if c == '#' {
			if depth == 0 {
				return i
			}
			for i < len(b) && b[i] != '\n' {
				i++
			}
			continue
		}
		if (c == '\n' || c == '\r') && depth == 0 {
			return i
		}
	}
	return len(b)
}

func fields(v any) map[string]any {
	out := map[string]any{}
	r := reflect.ValueOf(v)
	t := r.Type()
	for i := 0; i < r.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("toml"), ",")[0]
		if tag != "" && tag != "-" {
			if r.Field(i).Kind() == reflect.Pointer && r.Field(i).IsNil() {
				continue
			}
			out[tag] = r.Field(i).Interface()
		}
	}
	return out
}
func scalar(v any) (string, error) {
	if m, ok := v.(map[string]string); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(m))
		for _, k := range keys {
			key, _ := scalar(k)
			val, _ := scalar(m[k])
			pairs = append(pairs, key+" = "+val)
		}
		return "{ " + strings.Join(pairs, ", ") + " }", nil
	}
	b, e := toml.Marshal(map[string]any{"value": v})
	if e != nil {
		return "", e
	}
	s := string(b)
	if len(s) < 8 {
		return "", errors.New("unsupported TOML value")
	}
	return strings.TrimSpace(strings.TrimPrefix(s, "value = ")), nil
}

func (c *Config) render() ([]byte, error) {
	if len(c.original) == 0 {
		return toml.Marshal(c)
	}
	old := defaultConfig("")
	if err := toml.Unmarshal(c.original, old); err != nil {
		return nil, err
	}
	ss, err := sections(c.original)
	if err != nil {
		return nil, err
	}
	byID := map[string]core.Target{}
	for _, t := range c.Targets {
		byID[t.ID] = t
	}
	oldIDs := map[string]bool{}
	for _, t := range old.Targets {
		oldIDs[t.ID] = true
	}
	var changes []edit
	var appendix strings.Builder
	hasTUI := false
	hasActivity, hasAlerts := false, false
	targetSections := 0
	hasDefault := false
	for _, s := range ss {
		var before, after map[string]any
		switch s.name {
		case "":
			before = map[string]any{"default_target": old.DefaultTarget}
			after = map[string]any{"default_target": c.DefaultTarget}
			for _, a := range s.entries {
				if a.key == "default_target" {
					hasDefault = true
				}
			}
		case "tui":
			hasTUI = true
			before = fields(old.TUI)
			after = fields(c.TUI)
		case "activity":
			hasActivity = true
			before, after = fields(old.Activity), fields(c.Activity)
		case "alerts":
			hasAlerts = true
			before, after = fields(old.Alerts), fields(c.Alerts)
		case "targets", "targets.env":
			if s.name == "targets" {
				targetSections++
			}
			if s.target < 0 || s.target >= len(old.Targets) {
				return nil, errors.New("config uses an unsupported targets layout; edit this file manually")
			}
			previous := old.Targets[s.target]
			current, exists := byID[previous.ID]
			if !exists {
				changes = append(changes, edit{s.start, s.end, ""})
				continue
			}
			if s.name == "targets" {
				before = fields(previous)
				after = fields(current)
				for _, sub := range ss {
					if sub.target == s.target && sub.name == "targets.env" {
						delete(before, "env")
						delete(after, "env")
					}
				}
			} else {
				before = map[string]any{}
				after = map[string]any{}
				for k, v := range previous.Env {
					before[k] = v
				}
				for k, v := range current.Env {
					after[k] = v
				}
			}
		default:
			if s.target >= 0 && s.target < len(old.Targets) {
				if _, exists := byID[old.Targets[s.target].ID]; !exists {
					changes = append(changes, edit{s.start, s.end, ""})
				}
			}
			continue
		}
		seen := map[string]bool{}
		for _, a := range s.entries {
			prev, known := before[a.key]
			next, desired := after[a.key]
			if !known && !desired {
				continue
			}
			seen[a.key] = true
			if known && desired && reflect.DeepEqual(prev, next) {
				continue
			}
			if !desired {
				changes = append(changes, edit{a.start, a.end, ""})
				continue
			}
			value, e := scalar(next)
			if e != nil {
				return nil, e
			}
			key, _ := scalar(a.key)
			// Keep the existing key spelling and whitespace; only its value changes.
			prefix := c.original[a.start:a.end]
			equal := bytes.IndexByte(prefix, '=')
			if equal >= 0 {
				changes = append(changes, edit{a.start, a.end, string(prefix[:equal+1]) + " " + value + " "})
			} else {
				changes = append(changes, edit{a.start, a.end, key + " = " + value + " "})
			}
		}
		var added strings.Builder
		keys := make([]string, 0, len(after))
		for k := range after {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := after[key]
			if seen[key] || reflect.DeepEqual(before[key], next) {
				continue
			}
			if key == "env" && len(next.(map[string]string)) == 0 {
				continue
			}
			value, e := scalar(next)
			if e != nil {
				return nil, e
			}
			quoted, _ := scalar(key)
			fmt.Fprintf(&added, "%s = %s\n", quoted, value)
		}
		if added.Len() > 0 {
			prefix := ""
			if s.end > 0 && c.original[s.end-1] != '\n' {
				prefix = "\n"
			}
			changes = append(changes, edit{s.end, s.end, prefix + added.String()})
		}
	}
	if targetSections == 0 && len(old.Targets) > 0 && !reflect.DeepEqual(old.Targets, c.Targets) {
		return nil, errors.New("config targets use inline or dotted TOML; edit manually to preserve the existing layout")
	}
	if !hasDefault && old.DefaultTarget != "" && c.DefaultTarget != old.DefaultTarget {
		return nil, errors.New("config default_target uses a complex TOML layout; edit it manually")
	}
	if !hasTUI && !reflect.DeepEqual(old.TUI, c.TUI) {
		b, e := toml.Marshal(struct {
			TUI Preferences `toml:"tui"`
		}{c.TUI})
		if e != nil {
			return nil, e
		}
		appendix.Write(b)
	}
	if !hasActivity && !reflect.DeepEqual(old.Activity, c.Activity) {
		b, e := toml.Marshal(struct {
			Activity core.ActivitySettings `toml:"activity"`
		}{c.Activity})
		if e != nil {
			return nil, e
		}
		appendix.WriteByte('\n')
		appendix.Write(b)
	}
	if !hasAlerts && !reflect.DeepEqual(old.Alerts, c.Alerts) {
		b, e := toml.Marshal(struct {
			Alerts core.AlertSettings `toml:"alerts"`
		}{c.Alerts})
		if e != nil {
			return nil, e
		}
		appendix.WriteByte('\n')
		appendix.Write(b)
	}
	for _, t := range c.Targets {
		if !oldIDs[t.ID] {
			b, e := toml.Marshal(struct {
				Targets []core.Target `toml:"targets"`
			}{[]core.Target{t}})
			if e != nil {
				return nil, e
			}
			appendix.WriteByte('\n')
			appendix.Write(b)
		}
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].start == changes[j].start {
			return changes[i].end < changes[j].end
		}
		return changes[i].start < changes[j].start
	})
	var out bytes.Buffer
	position := 0
	for _, e := range changes {
		if e.start < position {
			return nil, errors.New("config edit overlaps; edit the file manually")
		}
		out.Write(c.original[position:e.start])
		out.WriteString(e.text)
		position = e.end
	}
	out.Write(c.original[position:])
	if appendix.Len() > 0 {
		out.WriteByte('\n')
		out.WriteString(appendix.String())
	}
	validate := defaultConfig("")
	if err = toml.Unmarshal(out.Bytes(), validate); err != nil {
		return nil, fmt.Errorf("cannot preserve config layout; edit it manually: %w", err)
	}
	actual, actualErr := toml.Marshal(validate)
	wanted, wantedErr := toml.Marshal(c)
	if actualErr != nil || wantedErr != nil || !bytes.Equal(actual, wanted) {
		return nil, errors.New("config uses a layout that cannot safely be updated; edit it manually")
	}
	return out.Bytes(), nil
}
