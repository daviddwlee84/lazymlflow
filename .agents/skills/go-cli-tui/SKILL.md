---
name: go-cli-tui
license: MIT
description: 'Build Go CLIs, terminal dashboards, and interactive wizards with Lazygit-inspired UX. Use when creating a Go command-line tool, adding a CLI/TUI feature, designing Bubble Tea or Charm interfaces, improving keyboard navigation or responsiveness, or adding guided setup and XDG configuration. Defaults to Cobra and the compatible Charm stack, with arrow/Vim navigation, shared operations, and real terminal verification.'
---

# Go CLI / TUI

Build around **see → select → act → see the result**. Keep context visible,
common actions discoverable, and navigation responsive while work runs. These
are opinionated defaults for new tools and features; respect existing contracts.

## Boundaries and defaults

- Use for Go commands, dashboards, pickers, wizards, and interaction fixes.
  A one-shot picker need not become a full-screen dashboard.
- Prefer Cobra for a command tree; Bubble Tea, Bubbles, and Lip Gloss for TUI;
  Huh when a form earns its complexity. A tiny CLI can use `flag`. Retain a
  working framework in an existing project.
- Read `go.mod` before examples. For greenfield work, choose compatible stable
  Charm versions, verify Go requirements, and pin `go.mod`/`go.sum`.
- Lazygit's implementation is an in-tree gocui fork, not Bubble Tea. Borrow
  interaction principles without copying its Git actions, layout, or every key.
- This is a development skill, not a Lazygit usage guide or general Go tutorial.
  Release-channel work can use the optional
  [cli-release-distribution guide](https://github.com/daviddwlee84/agent-skills/tree/main/skills/local/cli-release-distribution).
  No other skill is required to use this package.

## Workflow

- [ ] **1. Ground the task.** Read the command tree, services, config, terminal
  setup, and tests. Identify the repeated task, necessary state, target OSes,
  and whether it needs a command, picker, wizard, or dashboard. Infer from the
  repo; ask only about consequential product choices that remain unknown.
- [ ] **2. Define the interaction.** Sketch the primary screen and one complete
  action path using the compact contract below. Record focus, keyboard behavior,
  entry conditions, and failure/cancel behavior before wiring widgets.
- [ ] **3. Build a vertical slice.** Connect a real operation from CLI and UI to
  the same domain service, validation, and result. Keep handlers and views thin;
  add packages as responsibilities grow, rather than generating empty layers.
- [ ] **4. Complete the states.** Handle loading, successful empty results,
  errors, refresh, cancellation, resize, and return from overlays or external
  programs. Add configuration for actual recurring preferences.
- [ ] **5. Verify and finish.** Run relevant checks, fix failures, and exercise
  the interaction in a PTY. Report changes, evidence, and untested paths.
  Complete an authorized build request; do not stop after producing a design.

### Compact interaction contract

Use in working notes or the implementation explanation. Do not require a new
specification file for a small change; omit inapplicable rows.

```text
Task:           repeated action and state needed to decide
Surface:        command / inline picker / wizard / dashboard
Layout:         list, detail/preview, status; narrow-screen behavior
Navigation:     focus, arrows + Vim aliases, search, Back, help
Entry:          bare command, flags, --interactive, non-TTY/JSON
Operation:      shared service, validation, target, result, cancellation
State:          selection identity, async ownership, freshness, failure
Configuration:  preferences, paths, precedence, effective-config view
Acceptance:     input sequence and observable expected result
```

## Interaction invariants

1. **Stable context.** Keep pane roles, per-view selection, scroll, and filter.
   Refresh follows identity, not an old row number. Show focus separately from
   selection and status colors.
2. **Two navigation vocabularies.** Support arrows and `j/k`, Tab/Shift+Tab for
   focus, and context-appropriate `h/l`. Add `gg/G` for long lists. No required
   normal/insert mode. Advertise only applicable actions.
3. **Typing owns printable keys.** Route events through the modal and focused
   widget before navigation shortcuts. Typing `j`, `q`, `/`, or an action's
   mnemonic in a field must not invoke that action.
4. **Discoverable speed.** Provide a short contextual footer, `?` help, and an
   action menu when needed. Derive hints and dispatch from the same effective
   bindings and availability definitions.
5. **Immediate feedback.** Render available state before slow discovery; allow
   navigation during I/O. Show pending, success, and failure while retaining
   useful rows. Avoid decorative motion that competes with the task.
6. **Proportional confirmation.** Destructive actions name the exact target and
   consequence. Reversible navigation stays immediate. A mutating wizard ends
   with one meaningful review/submit step.

Read [interaction-design.md](references/interaction-design.md) when laying out
screens or implementing navigation, filtering, help, mouse behavior, or resize.

## CLI and wizard entry policy

Parse flags and validate syntax before deciding whether to prompt. Interactive
behavior follows intent, not merely the existence of a terminal.

| Invocation | Default |
|---|---|
| Bare app with a dashboard, interactive terminal | Open dashboard |
| Bare command explicitly designated wizard-capable, e.g. `create` | Open wizard |
| Bare ordinary command group | Show help |
| Partial business flags/arguments, required data missing | Usage error, example, help hint |
| Complete valid business flags/arguments | Execute command path |
| Explicit `--interactive` | Prefill from flags/config; collect remaining choices |
| Non-TTY or machine-output mode | Never prompt; produce data, help, or clear error |
| Invalid flag/value, even with `--interactive` | Report error; do not turn a typo into a wizard |

Global options such as `--config` and `--color` do not alone signal business
command intent. An explicit UI request without a usable TTY or combined with
JSON output is an error. Make equivalent operations scriptable; keep stdout
clean and diagnostics separate.

Read [cli-wizards-config.md](references/cli-wizards-config.md) for commands,
guided setup, validation, configuration, or shell integration.

## Implementation and verification references

- Read [charm-stack.md](references/charm-stack.md) when selecting dependencies,
  finding an existing component, or adapting version-sensitive examples. It maps
  needs to official libraries/tools; it is not a bulk-install instruction.
- Read [async-terminal.md](references/async-terminal.md) for background work,
  startup performance, or terminal handoffs. It includes dev-cli lessons with
  sources and performance evidence guidance.
- Read [agent-facing-cli.md](references/agent-facing-cli.md) when a tool needs
  an embedded operational skill or a dependable automation interface. Keep
  agent and human entry points on the same services; a bundled skill is optional.
- Read [go-distribution.md](references/go-distribution.md) when preparing a Go
  CLI for other users: start with the actual main-package install path, version
  reporting, and published tags; add package managers when the release needs them.
- Read [shell-completion.md](references/shell-completion.md) for native generators,
  user install/status, fpath activation, offline candidates and real shell tests.
- Read [shell-context.md](references/shell-context.md) when exporting a parent-shell
  environment, owning persistent connections, or generating consumer configuration.
- Read [self-update.md](references/self-update.md) when adding an upgrade command:
  choose source, release assets, or the owning package manager from evidence;
  verify and replace the running copy while preserving local builds by default.
- Read [verification.md](references/verification.md) before declaring an
  interaction implemented. Select applicable cases, use deterministic state
  tests, and exercise a real PTY; screenshots cannot prove input behavior.

## Gotchas

- **Charm v1/v2 examples are not interchangeable.** Bubble Tea v2 uses
  `charm.land/bubbletea/v2`, `tea.KeyPressMsg`, and `View() tea.View`; v1 uses
  the older module, `tea.KeyMsg`, and `View() string`. Huh/Bubbles must match
  the model generation. A UX fix is not permission to migrate the app.
- **Async wrappers can still freeze startup.** Blocking constructors, config
  probes, first-frame work, or synchronous `Update`/`View` work block input.
  Move the actual slow work into effects.
- **Late results remain late after cancellation.** Correlate results with the
  current request before changing state. Cancelled waiting also does not prove
  a remote write was rolled back.
- **Bytes/runes are not terminal widths.** Use ANSI-aware cell measurement and
  grapheme-safe truncation; test Chinese, combining marks, and emoji. Clamp
  sizes after subtracting actual borders/padding, including tiny resizes.
- **A character device is not necessarily a TTY.** Detect the actual input and
  output terminals. Machine-output intent suppresses UI even with a TTY.
- **A flag default is not an explicit override.** Preserve changed-flag
  information for explicit `false`, `0`, and empty values. Cobra validation
  hooks can run before `RunE`; check ordering before expecting a wizard to fill
  a required flag.
- **`os.UserConfigDir()` does not enforce XDG everywhere.** macOS uses
  Application Support. Implement the chosen XDG policy explicitly; persistent
  state belongs outside both disposable cache and the preferences file.
- **Nested terminal readers compete.** Integrate a form into the current model
  or release/suspend the outer program before running another reader. Restore
  terminal modes before children/editors and on handled exit paths.

This package contains instructions and references, not a generator or starter.
Preferences are original synthesis; project observations and official contracts
are identified in the references.
