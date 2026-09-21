# Async state and terminal lifecycle

Read for dashboard startup, background operations, slow I/O, or external tools.

## Contents

- [State and effects](#state-and-effects)
- [Startup and responsiveness](#startup-and-responsiveness)
- [Terminal ownership](#terminal-ownership)
- [SSH authentication handoffs](#ssh-authentication-handoffs)
- [Lessons from dev-cli](#lessons-from-dev-cli)

## State and effects

Use the framework's message loop as the UI state owner. Commands/effects call
services and return typed results; worker goroutines do not mutate model maps,
slices, or widgets concurrently. Copy or transfer ownership of mutable payloads
deliberately. `View` renders a snapshot without probing tools or mutating state.

```text
input / timer → Update → model snapshot → View
                  ↓
             effect(service, context, request ID)
                  ↓
             result message → accept if still current
```

Track a generation/request ID per independently refreshed view or query. Starting
a replacement read supersedes the previous generation. Include identity in every
result and reject stale success AND stale failure before changing visible state.
Cancel owned work when superseded or closed, but retain generation checks because
cancellation races with completion. Closed dialogs must not reappear on a reply.

When operations have dependencies, invalidate the dependent observation rather
than presenting it as current. Do not require every optional tab to finish
before one view becomes usable. Model these outcomes distinctly:

| Result | UI behavior |
|---|---|
| Loading with existing rows | Keep rows, show pending refresh |
| Accepted fresh result | Replace snapshot; restore selection by ID |
| Accepted successful empty result | Remove obsolete rows and show empty state |
| Refresh error | Keep usable snapshot with error and its provenance |
| Cached result | Label cached/stale; it is not completion of the live read |
| Unknown observation | Preserve unknown; do not render as false/clean/healthy |

Track pending mutations by operation/target identity so repeated input does not
start conflicting writes. A completion updates the affected state and exposes
the result. Retry reads when appropriate; do not blindly retry writes with an
unknown outcome. Reconcile state first. Cancelling a wait does not roll back
already-applied effects; show partial outcomes explicitly.

## Startup and responsiveness

Construct a useful initial shell before scans, network requests, subprocesses,
large cache decoding, or release checks. Load the active view first and optional
views on demand. Fresh local state can appear independently of remote state.
Avoid expensive initialization before `Program.Run()` as well as inside Update.

Use bounded concurrency and cancelable contexts for I/O. For log streams or large
collections, keep a bounded display buffer or virtualized viewport while keeping
required durable data elsewhere. Coalesce superseded snapshots/progress updates;
do not drop business events or mutation receipts. Log follow mode is explicit;
scrolling backward stops following until the user resumes it.

Choose refresh cadence from workload and measure it. Do not make a fixed FPS cap
the definition of smoothness or add sleeps to hide a backlog. Separate producer,
model-update, layout, and rendering costs before optimizing.

For a performance investigation, record startup stages, navigation/update time,
queue growth, rows processed, and refresh completion under representative data.
An initial `View` return is not proof that the terminal has painted a frame.
Use delayed fake services for deterministic behavior tests plus a real PTY for
perceived input latency. Report data size and terminal/multiplexer environment;
avoid claiming universal millisecond targets from one machine.

Diagnostic tracing should be opt-in and bounded. Prefer relative timing,
operation categories, and counts over sensitive raw paths, commands, or errors.
Write trace files outside the active screen stream, ideally after restoration.

## Terminal ownership

One component owns input parsing and terminal modes. Use the framework's supported
model composition and external-program execution mechanisms, checked against the
selected major version. Do not start a second reader/form over a running TUI.

Before an editor, pager, interactive child, or suspension, release/restore the
terminal; after return, reacquire it, refresh relevant state, and restore focus.
Handle ordinary completion, error, and cancellation through the same return path.
Keep child errors visible without abandoning a usable dashboard.

A dashboard can hand off to the same Cobra command and wizard used standalone
after releasing its reader (for example, Bubble Tea's `Exec` adapter). Forward the
chosen target/config and read-only state explicitly. Prevent a second handoff and
reject stale returns after target changes. Reload affected registrations and
reconnect when transport identity changes; setup must remain usable after the last
target is removed. Keep a child's printed result visible until acknowledgement
before reacquiring the dashboard. Cancellation before an operation needs no extra
pause. Verify release → wizard → result → return in a real PTY.

Use cleanup paths for raw mode, alternate screen, cursor visibility, mouse
capture, bracketed paste, and enabled keyboard protocols. Test normal quit,
startup failure, handled signals, and supported panic recovery. `os.Exit` skips
defers; keep it at the entrypoint after cleanup. SIGKILL cannot run cleanup.
In raw mode, explicitly handle Ctrl+C and any supported suspend/resume behavior.

Subprocesses use structured executable/argument calls and a context rather than
unescaped shell concatenation. Logs go to a separate sink or through messages
to a UI log view; a background `fmt.Println` can corrupt the screen.

## SSH authentication handoffs

Background discovery and refresh using `BatchMode=yes` cannot prompt for a
password or host-key trust. Return a typed authentication-required result and
expose an explicit action for that host instead of retrying indefinitely. JSON
and non-TTY calls return the requirement without launching a prompt.

For interactive authentication, release the TUI terminal to native SSH, then
reacquire it on success, failure or cancellation. Let SSH handle passwords,
keyboard-interactive challenges and host trust; do not collect or store SSH
passwords in app fields. Retry the intended operation after successful
authentication, bounded to that handoff. Failed or cancelled prompts remain
visible without reopening automatically. Tie completion to the target/request
generation so a late success cannot reconnect a newly selected target.

Distinguish credentials from connection lifetime. A TUI can reuse a connection
within its process, while separate CLI invocations do not share an in-memory
authentication map. Inspect effective OpenSSH settings with a bounded `ssh -G`
call when choosing a multiplexing strategy. Honor an explicitly configured
`ControlMaster`/`ControlPath`/`ControlPersist` policy rather than always replacing
it with a private socket or a shorter timeout. A private app-owned master can
provide scoped fallback reuse when no shared policy applies; document its
lifetime and cleanup. Reusing a persistent authenticated connection does not
mean the application cached the password.

An explicitly requested persistent shell tunnel has a different lifetime from an
API request. Read [shell-context.md](shell-context.md) before borrowing a master
whose idle policy may expire while only a forwarding listener remains.

Track ownership of the master separately from each forwarding. A master using
the user's sharing policy remains shared even if this app started it. On a
borrowed/shared master, cancel only the app's own forwards with their exact
forwarding specification; never send `-O exit` or `-O stop` during app cleanup.
Only an app-owned fallback master is eligible for app-wide shutdown.

Verify the handoff with a fake SSH child in a real PTY: native prompt input,
cancel/failure return, stale target changes and a successful single retry.
Verify configured-master reuse and forwarding cleanup separately; report a
real-host check only when it was actually performed. The relevant upstream
contracts are [BatchMode and multiplexing settings](https://man.openbsd.org/ssh_config)
and [SSH control commands](https://man.openbsd.org/ssh); the UI and ownership
policies above are design guidance, not an SSH password-management API.

## Lessons from dev-cli

Observed in local checkout `689836cdea61d46ee86cdfa4a0df42ac324e6561` on
2026-09-20. Public source links identify the inspected files; no local absolute
paths or installed `dev` command are needed by this skill.

| Source | Transferable lesson |
|---|---|
| [Dashboard guide](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/docs/guides/tui-repos-bootstrap.md) | First view precedes discovery; views publish independently; distinguish cache acceptance from live completion. |
| [Readiness regression tests](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/internal/tui/readiness_internal_test.go) | Superseded generations must not commit prepared state; dependencies need explicit invalidation. |
| [Dashboard actions](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/docs/guides/dashboard-actions.md) | Reuse full CLI workflows and return to the dashboard after completion/cancellation/error. |
| [Prompt implementation](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/internal/cli/prompt.go) | Detect the actual terminal pair; preserve line-editing and completed prompt display. |

The generic policies here are synthesis, not a request to copy dev's task states,
SSH trust model, registry, eight tabs, or entire safety architecture. Match the
size of the implementation to the new application's actual risks and workload.
Use [Bubble Tea's examples and docs](https://github.com/charmbracelet/bubbletea)
for the chosen version's effect and terminal-handoff APIs.

## Overlays that outlive background discovery

Keep ownership of each asynchronous result explicit. A late discovery result
may update available targets, but must not replace a pending operation's review
or result surface. Process an operation's own completion even if another
surface is visible; otherwise its pending flag can remain stuck forever.
After a write or an unknown outcome, mark affected cached views stale and
refresh them before claiming the displayed state is current. Preserve useful
old data with a stale marker while the refresh is pending.

This is a lazyclash workbench regression case, reviewed 2026-09-20; apply it
where an application has overlapping discovery, forms and remote operations.
