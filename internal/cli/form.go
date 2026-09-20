package cli

import (
	"context"
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/core"
	"github.com/daviddwlee84/lazymlflow/internal/targetform"
)

// targetForm only owns the standalone terminal lifecycle. Input, validation,
// advanced settings, review, and cancellation are shared with the dashboard.
type targetForm struct {
	child         *targetform.Model
	width, height int
	mouse         bool
	validate      func(core.Target) error
}

func newTargetForm(draft core.Target, edit bool, path string) targetForm {
	return targetForm{child: targetform.New(draft, path, edit), width: 80, height: 24, mouse: true}
}

func (m targetForm) Init() tea.Cmd { return m.child.Init() }
func (m targetForm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.mouse {
		switch msg.(type) {
		case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.MouseWheelMsg:
			return m, nil
		}
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = size.Width, size.Height
	}
	var cmd tea.Cmd
	m.child, cmd = m.child.Update(msg)
	if m.child.Done && m.validate != nil {
		if err := m.validate(m.child.Target); err != nil {
			return m, m.child.Reject(err)
		}
	}
	if m.child.Done || m.child.Cancelled {
		return m, tea.Quit
	}
	return m, cmd
}
func (m targetForm) View() tea.View {
	v := tea.NewView(m.child.View(m.width, m.height))
	// Full-screen origin keeps shared form hit-testing aligned after shell output.
	v.AltScreen = true
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func runTargetForm(ctx context.Context, in io.Reader, out io.Writer, draft core.Target, edit bool, path string, mouse bool, validate func(core.Target) error) (core.Target, error) {
	initial := newTargetForm(draft, edit, path)
	initial.validate = validate
	initial.mouse = mouse
	model, err := tea.NewProgram(initial, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		if ctx.Err() != nil {
			return draft, ctx.Err()
		}
		return draft, err
	}
	result := model.(targetForm)
	if !result.child.Done {
		return draft, context.Canceled
	}
	return result.child.Target, nil
}
