# Verification and acceptance cases

Read before handing off an interaction. This is a reusable acceptance guide,
not a claim that a generated application has already passed these checks.

## Contents

- [Verification sequence](#verification-sequence)
- [Behavior cases](#behavior-cases)
- [Unicode emulator limits](#unicode-emulator-limits)
- [Three development walkthroughs](#three-development-walkthroughs)
- [Evidence](#evidence)

## Verification sequence

1. Select cases relevant to the changed behavior; use the project's actual
   command names and existing verification tools.
2. Test state transitions and request validation with fake services, controlled
   clocks/completions, and captured I/O. Exercise races deterministically rather
   than relying on arbitrary sleeps. Include layout boundaries where changed.
3. Run formatting checks, `go vet ./...`, and appropriate `go test` targets.
   For concurrent changes, run race tests where the target supports them.
   Build the affected executable using the declared Go version.
4. Exercise CLI help, intentionally bad input, configured output, and applicable
   dry-run/config inspection against disposable state. Parse JSON as JSON.
   For a distributed CLI, find its upgrade entry point in help and exercise
   read-only check plus the supported existing-install upgrade path. Record a
   deliberate product exemption rather than silently omitting this UX.
5. Run a real PTY session for changed interaction/terminal behavior. Launch,
   type, paste, switch, resize, open/close overlays, simulate slow/error I/O,
   and exit. Verify shell echo and cursor state afterward.
6. Fix failures and rerun the affected gates. Report any unavailable PTY or
   target-OS check as unverified, not as passed from snapshots or cross-builds.

Use an isolated environment for runtime tests: child-process HOME/XDG directories,
fake backends, test config, and disposable data. Do not repurpose the invoking
shell's HOME or operate on real user resources for a generic smoke test.
Build/test the project's supported OSes; a cross-compiled binary does not prove
its terminal interaction works on that OS.

## Behavior cases

| Scenario | Observable result |
|---|---|
| Navigate the same list with arrows and j/k | Same row/action eligibility; focus stays visible |
| Tab/Shift+Tab; h/l in tree or detail | Focus cycles predictably; local left/right meaning is advertised |
| Type `jkhql/?` and paste a multiline string into a field | Text stays text; no shortcut, submit, or destructive action fires |
| Live filter: edit, move text cursor, Up/Down, Enter | Changed query resets results; cursor-only changes preserve selection; Enter leaves input without opening |
| Open and close help/modal/wizard | Prior valid view, filter, selection, and scroll are restored |
| Refresh with reorder, selected deletion, and empty success | Follow stable ID; clamp after deletion; successful empty removes obsolete rows |
| Start request A, then B; finish B before A | A's success/error cannot overwrite B or reopen a closed surface |
| Delay/fail one optional view while using another | Initial UI and navigation stay usable; failed refresh retains labeled prior data |
| Repeated activation while mutation is pending | No duplicate conflicting operation; visible pending and final/partial result |
| Empty list or filter hides every row | Creation/help still work; row actions cannot use an absent target |
| Bare wizard command vs partial business flags | Bare TTY entry guides; missing data after supplied business flags produces usage error |
| Global --config on a bare wizard entry | Still opens wizard, using the selected config |
| Unknown flag/invalid value with --interactive | Error before prompting |
| Pipe/JSON and explicit interactive conflicts | No prompts/progress/ANSI in data; contradictory modes fail clearly |
| Published CLI installed through a supported package manager | Help exposes upgrade/check; apply invokes the exact owning manager under normal approval policy |
| Manager requires the installed process to exit | Handoff is observable, safe status polling does not block the manager, and completion is verified independently of acceptance |
| Windows junction/ACL, another running instance, manager error with zero exit | Actual ownership and outcome are verified on Windows; neither Unix mode bits nor process exit code alone prove success |
| Upgrade check, broken product config, unavailable managed backend | Check does not mutate; upgrading the CLI does not require its application backend |
| Manager no-op, lagging formula, nonzero exit or cancellation | Actual installed result or failure is reported; no fallback to a different installer |
| Flags, env, file, defaults; explicit false and zero | Expected precedence; missing default config succeeds, explicit missing config fails |
| XDG override and relative XDG variable | Absolute override used; invalid relative value ignored; state survives cache deletion |
| Wizard invalid field, Back, upstream-choice change | Answers remain; errors are local; dependent answers are revalidated |
| Cancel before submit / cancel after external work begins | No pre-submit apply; later cancellation reports actual or unknown effects truthfully |
| Remap a key or define an active-scope conflict | Dispatch and help agree; conflict rejected with a useful error |
| 80×24, narrow, wide, rapid/zero-sized resize | No panic or negative dimensions; main task and quit remain reachable |
| Chinese, combining text, emoji, NO_COLOR, light/dark | No broken truncation; meaning and focus do not depend only on color |
| External editor/child success, failure, cancellation | Terminal restored/reacquired; current context and child outcome remain usable |
| Dashboard → shared CLI wizard → review/cancel or result → dashboard | One reader; result remains until acknowledgement; target identity and affected data refresh correctly |
| No search matches; clipped review buttons | Hidden old choices cannot submit; invisible Apply regions cannot receive clicks |
| Mouse press/release, drag off button, layout/target change while pressed | Only the same still-enabled semantic action can execute; no stale coordinate activation |
| Modal outside click/wheel; click field then type action mnemonic | No click-through; field keeps text ownership; draft remains unsaved until submit |
| Mouse disabled or toggled; wheel over inactive pane | Native selection can be restored; hovered pane scroll/focus is predictable |
| Timestamped graphs after disconnect, target switch, source failure and counter reset | Gaps/stale source age remain truthful; history and aggregates stay bounded |
| Normal exit, handled interruption, supported recovery | Shell cursor, echo, and terminal modes restored |

For a CLI-only change, skip irrelevant full-screen cases. For a navigation or
wizard change, arrow/Vim equivalence, text ownership, Back/cancel, and real input
checks are central. A resize snapshot alone cannot validate key handling.

## Mouse in a real PTY

Model tests prove semantic dispatch; also drive the terminal parser and reporting
mode in a real PTY. For SGR mouse reporting, coordinates on the wire are one-based:
`ESC [ < 0 ; X ; Y M` presses the left button and the final `m` releases it.
Wheel codes are 64/65. Send bytes to the PTY, not directly to `Update`, and assert
observable navigation/action results. Check capture enable/disable output and
terminal cleanup on exit. Include resize-between-press-and-release and a modal
click-through case in deterministic tests even when the PTY scenario stays short.

A replay emulator need not understand mouse reporting modes to display the
resulting screen; inspect raw control sequences separately when needed. Snapshot
appearance alone does not prove a mouse event reached the intended action.

## Unicode emulator limits

During lazyclash's terminal checks, pyte 0.8.2 truncated draw chunks at VS16/ZWJ,
making later columns appear absent even when the application's `View` retained
the text. Its replay snapshot therefore was not a reliable Unicode layout
oracle for that case. Do not remove supported glyphs or change product layout
solely to satisfy this emulator artifact.

Combine direct `View` checks of complete grapheme-bearing rows and ANSI-aware
cell widths with actual PTY input, resize, and terminal-restoration checks.
When visual appearance remains disputed, inspect a Unicode-capable terminal;
neither cell-width assertions nor a limited emulator prove final glyph rendering.
Keep the limitation specific to the tool/version and observed input.

Source: lazyclash [PTY harness](https://github.com/daviddwlee84/lazyclash/blob/b0a6564403e9794bc5c3e0634207f96a30b78eb7/scripts/pty_smoke.py)
and [View regression tests](https://github.com/daviddwlee84/lazyclash/blob/b0a6564403e9794bc5c3e0634207f96a30b78eb7/internal/tui/model_test.go),
reviewed 2026-09-20.

## Three development walkthroughs

Use these to check whether the skill produces concrete engineering decisions.
They are scenario specifications, not bundled starter projects or benchmark
results. For a measured skill evaluation, compare the same task with/without
the skill in isolated workspaces and retain actual outcomes.

### A. New resource dashboard

Prompt: "Build a Go resource manager with a list, detail preview, refresh, and
restart. Make it feel like Lazygit with arrow and Vim navigation."

Expected approach: compatible Charm stack, a shared list/restart service, stable
IDs, per-view generations, semantic actions/help, clear restart result, and
initial rendering independent of remote loading. Test a delayed list service,
out-of-order refresh, removed selection, repeated restart, and narrow Chinese
labels. Observe keyboard operation while the fake service is held pending.

Reject: synchronous I/O in View, row-index authority, a new process for each
pane, animation used as proof of speed, or JSON output polluted by UI logs.

### B. Guided configuration

Prompt: "Add `hosts add` with flags and a wizard so users do not have to memorize
arguments; support a custom config path."

Expected approach: declare the bare command wizard-capable, parse supplied
values first, distinguish global options from business flags, and share the
complete validator. Use prefills, searchable choices, Back, review, and an
explicit save to the selected file. Test `hosts add`, `--name staging` alone,
`--interactive --name staging`, a misspelled flag, redirected input, and JSON.

Reject: silently repairing typos through prompting, overwriting unrelated
config, dropping answers on Back, or claiming cancellation undoes a saved file.

### C. Existing Bubble Tea v1 feature

Prompt: "Add a searchable action menu to this existing Bubble Tea v1 app."

Expected approach: inspect the selected module and existing conventions; keep
v1 APIs; integrate the menu into the current loop; restore focus on close;
derive help from effective actions; allow action execution on palette Enter
without changing the list-filter Enter contract. Test typing action mnemonics,
Esc, a vanished target, and existing keybindings.

Reject: an unsolicited v2 migration, mixed tea.Model generations, a nested
terminal reader, or global `q` interception while the menu query is edited.

## Evidence

Record the meaningful result, not only the commands invoked:

```text
Changed behavior:
Automated checks and outcomes:
PTY sequence, terminal/size, and observed outcome:
Slow/failure path exercised:
Unverified platform or behavior:
```

VHS can record a reviewed scenario; it does not assert correctness by itself.
Do not report these walkthroughs as executed Go applications when only the skill
documents were reviewed. Structural skill lint, documentation builds, and
scenario coverage are distinct from runtime and comparative skill evaluations.
