# Interaction design

Read when shaping a screen or changing input behavior. These are defaults for
new applications; preserve established bindings when extending one.

## Contents

- [Layout follows the task](#layout-follows-the-task)
- [Navigation and input ownership](#navigation-and-input-ownership)
- [Search, help, and continuity](#search-help-and-continuity)
- [Terminal presentation](#terminal-presentation)
- [Sources](#sources)

## Layout follows the task

Start with the repeated decision and the information needed to make it. For
resource management, use a list plus detail/preview as the starting point:

```text
┌ Resources ─────────┬ Details / preview ────────────────────┐
│ > selected target │ identity, status, relevant properties │
│   another target  │                                      │
│                   │ operation result or bounded log view │
└───────────────────┴──────────────────────────────────────┘
scope · freshness · pending operation
↑↓/jk select   Tab focus   Enter inspect   / filter   ? help
```

Use an inline picker for one value, a wizard for bounded configuration, and a
monitoring dashboard for measurements. Do not copy five Git panes into a task
with only one meaningful list.

- Keep pane positions and column meanings stable. Emphasize focus and selection
  without erasing status labels.
- Put frequent actions in the footer, less frequent ones in a contextual menu,
  and explanations in help. Do not expose every shortcut at once.
- Put pending/errors beside the affected object or operation. Retain results
  somewhere accessible after a transient toast disappears.
- Distinguish loading, empty, filtered-empty, failed refresh with previous
  data, and ready. Empty results should explain a useful next action.
- Separate row actions from view actions: creation works with an empty list;
  deletion needs a current target. The help/menu uses the same eligibility.

## Navigation and input ownership

| Context | Defaults |
|---|---|
| List | `↑/↓` and `k/j`; Home/End and `gg/G` for long lists |
| Pane focus | Tab / Shift+Tab with a visible focus indicator |
| Flat list with adjacent panes/tabs | `←/→` and `h/l` move between them |
| Tree | `←/h` collapse/parent, `→/l` expand/child; Tab changes pane |
| Wide detail viewport | Left/Right and `h/l` may pan; advertise locally |
| Inspect/select | Enter; Space only for documented toggle/multiselection |
| Filter/help | `/` and `?` outside text entry |
| Back | Esc handles the nearest active interaction |
| Quit/cancel | `q` only from navigation; deliberate Ctrl+C cancellation/exit |

Do not assign `h/l` to both scrolling and pane movement in one context. Prefer
the focused widget's meaning; Tab remains the unambiguous focus path. Keep
semantic action IDs, effective keys, labels, eligibility, dispatch, and help in
one registry. Reuse keys across disjoint contexts; reject active-scope conflicts.

Route to the active overlay and its focused component first. Otherwise route to
the focused base component, then remaining applicable global bindings. Consumed
input must not fall through:

- Printable keys type in fields, including `q`, `j/k`, `h/l`, `/`, and action
  mnemonics. Do not require a Vim insert mode.
- Arrow keys edit text except the explicit live-filter behavior below. Editing
  must not unexpectedly switch panes.
- Clear multi-key sequences when context changes. For `gg`, show pending `g`,
  allow Esc cancellation, and resolve a prefix without indefinitely blocking
  other input. Use the framework's parsing rather than raw escape matching.
- Bracketed paste is text; pasted newlines must not approve or submit actions.
  Handle press/repeat/release so a physical press does not activate twice.
- Legacy terminals may equate Tab/Ctrl+I and Enter/Ctrl+M. Do not require
  these pairs to represent distinct actions.

## Search, help, and continuity

`/` enters a local live filter. Up/Down may select visible results while keeping
input focus and its cursor; `j/k` stay text. Only changed query text resets the
selection to the first result. Cursor moves and no-op edits preserve selection.

Default: **Enter accepts the query and leaves input; Enter from navigation opens
the item**. Esc while filtering clears the query and returns to navigation.
Advertise this distinction. An action palette may execute on Enter; a remote
search may require submission. Label those surfaces clearly.

Maintain query, selected stable ID, and scroll per view. Refresh follows the ID
if present; after deletion, choose the nearest remaining position and clamp the
viewport. A failed refresh is not a successful empty list. Help, details, and
wizards restore the previous valid context when closed. Manual selection takes
precedence over delayed startup suggestions.

Help starts with current-view keys, explains symbols and unavailable actions,
and reveals workflow details progressively. Add search when content warrants it.
Opening help reads existing state, without fresh network probes. Key remapping
changes the hints as well as dispatch.

Mouse support complements keyboard access. First click selects; activation
behavior is explicit and reuses actions. Closing a modal with an outside click
must not activate the row behind it. Use the computed layout rectangles for
hit testing. Allow disabling mouse capture for native terminal text selection.

## Terminal presentation

- Compute sizes using terminal cells and actual style frame sizes. Clamp zero
  or negative content dimensions; resize preserves data and focus.
- Collapse secondary details or show the active pane on narrow screens. Keep
  switching and quitting available. Test 80×24, narrower, and wide layouts;
  the task determines the minimum usable size.
- Measure cells, not bytes/runes. Truncate at grapheme boundaries while keeping
  ANSI sequences intact. Wrap prose; scroll structured text. Exercise `專案`,
  `é`, and `👩🏽‍💻` in the supported terminal.
- Use semantic colors for text, focus, selection, pending, warning, error, and
  success; pair color with labels/shapes. Honor `NO_COLOR` in automatic mode;
  explicit color overrides follow the documented application policy.
- Avoid required Nerd Fonts. Make icons/motion optional; check light, dark,
  and default backgrounds. Offer a plain/linear path where full-screen layout
  is unsuitable for assistive technology.
- Sanitize external filenames/logs before display. App-generated ANSI styling
  does not authorize untrusted OSC or cursor-control sequences.

## Sources

Reviewed 2026-09-20. Keymap choices here are this skill's preferences, not a
claim that every Lazy-style application uses the same bindings.

- [Lazygit](https://github.com/jesseduffield/lazygit) and
  [custom keybindings](https://github.com/jesseduffield/lazygit/blob/master/docs/keybindings/Custom_Keybindings.md): contextual actions and configurable navigation.
- [dev-cli dashboard guide](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/docs/guides/tui-repos-bootstrap.md): live filtering, help, startup selection, and popup behavior. Domain-specific tabs/actions are not requirements here.
- [NO_COLOR](https://no-color.org/), [Unicode grapheme boundaries](https://unicode.org/reports/tr29/), and [kitty keyboard protocol](https://sw.kovidgoyal.net/kitty/keyboard-protocol/): capability references, not substitutes for terminal tests.
