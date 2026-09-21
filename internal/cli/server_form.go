package cli

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazymlflow/internal/server"
	"github.com/daviddwlee84/lazymlflow/internal/serverform"
)

type setupForm struct {
	child         *serverform.Model
	width, height int
	mouse         bool
}

func (m setupForm) Init() tea.Cmd { return m.child.Init() }
func (m setupForm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.mouse {
		switch msg.(type) {
		case tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg, tea.MouseWheelMsg:
			return m, nil
		}
	}
	if v, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = v.Width
		m.height = v.Height
	}
	var cmd tea.Cmd
	m.child, cmd = m.child.Update(msg)
	if m.child.Done || m.child.Cancelled {
		return m, tea.Quit
	}
	return m, cmd
}
func (m setupForm) View() tea.View {
	v := tea.NewView(m.child.View(m.width, m.height))
	v.AltScreen = true
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}
func (a *app) runServerForm(ctx context.Context, dir string, spec server.ServerSpec, prefill ...serverform.Result) (serverform.Result, error) {
	initial := setupForm{child: serverform.New(dir, spec), width: 80, height: 24, mouse: a.mouse}
	if len(prefill) > 0 {
		initial.child.PrefillFinish(prefill[0].Start, prefill[0].RegisterTarget, prefill[0].AdminPasswordEnv)
	}
	result, err := tea.NewProgram(initial, tea.WithContext(ctx), tea.WithInput(a.options.In), tea.WithOutput(a.options.Out)).Run()
	if err != nil {
		if ctx.Err() != nil {
			return serverform.Result{}, ctx.Err()
		}
		return serverform.Result{}, err
	}
	r := result.(setupForm)
	if !r.child.Done {
		return serverform.Result{}, context.Canceled
	}
	return r.child.Result, nil
}
