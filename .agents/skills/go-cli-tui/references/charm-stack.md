# Charm stack and version selection

Read before choosing a library, adding a component, or adapting an example.
Start at [charm.land](https://charm.land/) and the
[official organization](https://github.com/charmbracelet). Discover capabilities
there, then read the selected package's version-matched documentation. This
reference is a routing map, not a dependency bundle.

## Contents

- [Default development stack](#default-development-stack)
- [Resolve versions before copying code](#resolve-versions-before-copying-code)
- [Optional libraries and tools](#optional-libraries-and-tools)
- [Relationship to Lazygit and existing skills](#relationship-to-lazygit-and-existing-skills)

## Default development stack

| Need | Default | Boundary |
|---|---|---|
| Command tree, flags, completion | [Cobra](https://github.com/spf13/cobra) | CLI adapter; domain rules remain testable outside handlers |
| Stateful terminal application | [Bubble Tea](https://github.com/charmbracelet/bubbletea) | Event/model/effect loop; render state without doing I/O |
| Inputs, lists, viewport, help, progress | [Bubbles](https://github.com/charmbracelet/bubbles) | Reuse widgets, then adapt focus/keymaps to the app |
| Styles, borders, layout | [Lip Gloss](https://github.com/charmbracelet/lipgloss) | Semantic styles and measured frames; not byte-based spacing |
| Forms and guided selection | [Huh](https://github.com/charmbracelet/huh) | App still owns draft, Back, review, apply, and cancellation |

Do not assume default component keymaps satisfy the interaction contract.
Inspect their focus behavior, filtering, pagination, and quit bindings. A single
input may be simpler with Bubbles than a full form. Huh supports standalone use
and integration into Bubble Tea; do not run competing programs on one terminal.

## Resolve versions before copying code

1. Read `go.mod`, the Go toolchain version, and any existing framework code.
2. Inspect the library's official release/tag, its `go.mod`, examples, and
   migration guide. In an existing module, `go list -m all` and `go doc` expose
   selected dependencies/APIs without guessing from a blog post.
3. For a new project, select compatible stable majors and Go requirements.
   Record exact versions in the module files. Verify a small compile slice
   before spreading API-specific code throughout the app.
4. Add only libraries required by the current feature. A request to improve
   navigation does not imply a dependency migration.

As checked on **2026-09-20**, the stable core stack has v2 releases. This is a
dated observation, not a permanently fixed version instruction. Verify the
latest compatible releases when implementing a new tool.

| Surface | Bubble Tea v1 | Bubble Tea v2 |
|---|---|---|
| Module | `github.com/charmbracelet/bubbletea` | `charm.land/bubbletea/v2` |
| View result | `string` | `tea.View` |
| Key press message | `tea.KeyMsg` | `tea.KeyPressMsg` |
| Mouse handling | Older common mouse message API | Distinct click, release, wheel, motion messages |
| Terminal view options | Many program options | Several become fields of `tea.View` |

Use the official [v2 upgrade guide](https://github.com/charmbracelet/bubbletea/blob/main/UPGRADE_GUIDE_V2.md)
for precise changes, including key matching. Do not mechanically append `/v2`
to old GitHub imports. Bubbles, Lip Gloss, and Huh also have v2 modules under
`charm.land`; verify the dependency graph so their `tea.Model` types agree.

Do not copy examples from `main` into a module pinned to a different release.
When maintaining v1, use v1 documentation and preserve behavior unless migration
is explicitly in scope. The dev-cli case study used Bubble Tea v1.3.10; its
interaction lessons carry over, but its concrete APIs are not v2 templates.

## Optional libraries and tools

Choose by the problem, not by the desire to use more of the ecosystem.

| Need | Official resource | Use / limit |
|---|---|---|
| Markdown inside help/detail | [Glamour](https://github.com/charmbracelet/glamour) | Render within available width; account for its styles and terminal assumptions |
| Structured/styled diagnostics | [Log](https://github.com/charmbracelet/log) | Send to a separate sink or UI message; never write across the renderer |
| ANSI-aware width/wrapping/truncation | [x/ansi](https://github.com/charmbracelet/x/tree/main/ansi) | Verify selected functions' grapheme/cell semantics with representative text |
| Actual terminal capability checks | [x/term](https://github.com/charmbracelet/x/tree/main/term) | Detect descriptors; do not equate character devices with terminals |
| A TUI served over SSH | [Wish](https://github.com/charmbracelet/wish) | Use when SSH serving is the product; not needed for a local tool that invokes SSH |
| Optional motion | [Harmonica](https://github.com/charmbracelet/harmonica) | Only where motion clarifies state; respect reduced-motion preferences |
| Shell-script interaction/prototype | [Gum](https://github.com/charmbracelet/gum) | A standalone tool; not the default subprocess implementation of Go form widgets |
| Read Markdown in the terminal | [Glow](https://github.com/charmbracelet/glow) | User tool/reference; Glamour is the rendering library |
| Reproducible terminal demonstrations | [VHS](https://github.com/charmbracelet/vhs) | Record actual workflows; a recording is not an assertion suite |
| Share a styled code/terminal image | [Freeze](https://github.com/charmbracelet/freeze) | Presentation artifact, not proof of keyboard behavior |

Crush, Mods, and other standalone applications visible on Charm's site are
useful ecosystem context, not prerequisites for writing a Go TUI. Consult a
tool only when the requested capability needs it. Do not add AI, a server,
animation, or an installer merely because the ecosystem offers one.

## Relationship to Lazygit and existing skills

Lazygit maintains its own in-tree `pkg/gocui`; see its
[architecture guidance](https://github.com/jesseduffield/lazygit/blob/master/AGENTS.md).
This skill adopts its state visibility, short action loops, contextual help,
and fast navigation as inspiration, not its framework.

The 2026-09-20 discovery pass checked skills.sh, `skills@1.7.0 find`, upstream
contents, and repository metadata. No candidate covered this complete contract:

| Candidate | Reviewed source | Assessment |
|---|---|---|
| `samber/cc-skills-golang@golang-cli` | [snapshot](https://github.com/samber/cc-skills-golang/tree/19a0626ae8565d27a7b7bdf59d8d99d94d7e284c/skills/golang-cli) | Useful CLI foundation; not a complete dashboard/wizard UX. Verify terminal detection and Cobra hook behavior rather than copying broad rules. |
| `hyperb1iss/hyperskills@tui-design` | [snapshot](https://github.com/hyperb1iss/hyperskills/tree/5c2f96185a7ea1f9a3e9e397b1687f674c4c8c36/skills/tui-design) | Closest UX overlap; framework-neutral and does not supply this Go/wizard/entry-policy integration. |
| `ggprompts/tfe@bubbletea` | [snapshot](https://github.com/ggprompts/tfe/tree/b71818c5c92d8b45980eda352969817e6284cd91/.claude/skills/bubbletea) | Layout ideas, but old APIs and a byte-slicing truncation example make direct reuse unsuitable for this Unicode-aware contract. |

These are evaluated alternatives, not runtime dependencies. This package is
original guidance; it does not copy upstream templates or vendor their content.
