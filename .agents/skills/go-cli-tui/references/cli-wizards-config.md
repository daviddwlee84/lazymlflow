# CLI, wizards, and configuration

Read when implementing commands, guided setup, preferences, or shell integration.
This contract is a default for new tools; preserve existing public behavior.

## Contents

- [Resolve invocation intent](#resolve-invocation-intent)
- [One operation, multiple entry points](#one-operation-multiple-entry-points)
- [Wizard experience](#wizard-experience)
- [Configuration](#configuration)
- [Sources](#sources)

## Resolve invocation intent

Declare which commands support a wizard. An ordinary noun group such as `hosts`
shows help; a designated `hosts add` can guide the user when called bare.
Do not infer a wizard for every missing argument.

Resolve in this order:

1. Parse syntax, unknown flags, invalid supplied values, and conflicting modes.
   `--help`/version remain usable without initializing a UI, network, or domain
   services. Config problems should not prevent access to basic help.
2. Check explicit output/interaction intent. `--json`, disabled input, or
   non-TTY execution suppress prompts. Explicit UI plus machine output or
   missing usable terminals is an error, not a silent fallback.
3. If `--interactive` is explicit, prefill a wizard with validated supplied
   values. If the designated command is bare in a TTY, enter its wizard.
4. Otherwise validate the operation as a command. Missing required data is a
   concise usage error naming the fields, one example, and the help command.

Business arguments/flags express command intent, including explicit values
equal to defaults. Global presentation/config options alone do not. A bare
dashboard app opens its UI only in interactive mode. Without that capability,
bare roots/groups show help; a bare machine-output invocation without a data
operation reports a usage error instead of printing prose into JSON.

```text
forgebox                             # dashboard, if one exists and TTY
forgebox hosts                       # command-group help
forgebox hosts add                   # designated wizard, TTY only
forgebox --config ./test.toml hosts add  # still a bare wizard entry
forgebox hosts add --name staging    # missing required address: usage error
forgebox hosts add --interactive --name staging  # prefilled wizard
forgebox hosts add --adress host     # unknown flag: error, never a wizard
```

Inspect Cobra's hook order before implementing. Required-flag checks and `Args`
validation may execute before `RunE`. Do not mark a wizard-filled field required
unconditionally and expect the wizard to run. Separate supplied-value syntax
validation from completeness validation; both entry paths finish with the same
complete request validator. Root `PersistentPreRunE` is not a guarantee when a
child overrides the persistent hook; use an explicit shared initialization path
or the library's verified hook traversal contract.

## One operation, multiple entry points

Keep request construction, domain validation, execution, and presentation
separate. CLI flags, a standalone wizard, and a dashboard action all produce
the same typed request and call the same service. Tests can inject readers,
writers, services, and cancellation without launching a real terminal.

- In Cobra, return errors from handlers and write through `OutOrStdout()` and
  `ErrOrStderr()`. Let the entrypoint choose an exit status after cleanup.
- Write command data to stdout; diagnostics to stderr. UI rendering must have
  exclusive ownership of its terminal stream. JSON contains no progress/ANSI.
- Default exit contract: success 0, runtime failure 1, usage/config error 2,
  interrupted/cancelled requested operation 130. Closing an idle dashboard is
  success. Preserve child status when the command's purpose is transparent
  execution. Document intentional variations.
- Where an operation requires confirmation, provide a documented noninteractive
  approval path; missing approval fails instead of waiting for input. Scale
  confirmations to consequences; do not add prompts to harmless commands.
- Add `--dry-run` for consequential mutations using the real validation/plan
  path. Do not execute probes with hidden writes or create state during preview.
- Provide relevant help examples and shell completion for commands/values.
  Keep slow or authenticated discovery out of ordinary help/completion.
- A child cannot change its parent shell's cwd. If navigation requires a shell
  wrapper, use a narrow structured handoff, not `eval` of general command output.

Authentication-required is not a missing wizard field. For SSH, keep background
checks noninteractive and let an explicit human action hand the terminal to
native SSH; machine output returns a structured requirement. Preserve the draft
across cancellation. See [SSH authentication handoffs](async-terminal.md#ssh-authentication-handoffs)
for terminal restoration, session lifetime and shared-master ownership.

## Wizard experience

Use guided choice to remove syntax memorization, not to ask every possible flag.
Prefill known values, explain defaults, offer searchable selects for long lists,
and reveal advanced choices only when selected or needed. Keep optional fields
skippable. Show the current step/task; avoid an inaccurate fixed step count when
branches change the flow.

Maintain a draft request separate from applied state. Back preserves answers;
changing an upstream choice invalidates or revalidates dependent answers.
Validate locally on field/step completion, then validate the complete request
through the domain service. Errors stay near the field and keep user input.

For prefilled forms that only save local registrations or preferences, offer
an explicit Save shortcut such as Ctrl+S from any field. Visiting every field
is not a prerequisite; retain an optional Review action. Synchronize the active
input into the draft, then use the same complete validation and save path.
Failed validation or persistence keeps the draft and useful field focus.
Connectivity tests inform the user but need not block saving an otherwise valid
offline target. Cancel or invalidate pending tests so late results cannot replace
the saved form. Direct Save still performs required owner inspection and does
not bypass confirmation or review for core changes, remote applies, or other
consequential operations.

| Event | Default |
|---|---|
| Tab / Shift+Tab | Move between controls; input letters remain text |
| Up/Down or j/k in a non-text selector | Select options |
| Back button / Esc | Close nested selector first, then previous step |
| Esc at first step / Cancel / Ctrl+C | Cancel the wizard; do not submit |
| Ctrl+S, where direct local Save is offered | Validate and save the current draft without traversing remaining fields |
| Review / consequential submit | Review target, resolved values, and effects before applying |
| Finish/cancel from dashboard | Restore originating view/filter/selection |

For a simple one-field prompt, Esc cancels directly. Do not override text
editing with selector aliases. A form library supplies widgets, not the full
application's draft, Back, review, or cancellation policy.

Verify Save from early and late fields, validation failures, and stale test
results. Send the raw Ctrl+S byte (`\x13`) through a real PTY; on POSIX terminals,
check that IXON flow control is disabled while the TUI owns input and restored
on exit. A model-level key test alone cannot detect Ctrl+S freezing terminal
output before the application receives it.

Guard search and mouse submission too. An empty filtered selection must not submit
an older hidden choice. Clipped/offscreen buttons are not clickable; derive hits
from visible geometry and invalidate presses after resize/layout changes. Start a
destructive review on Cancel, and test Enter before changing that selection.

Before apply, revalidate the current target when concurrent changes could make
the review obsolete. Report partial effects separately from remaining work.
Cancellation after external execution began stops waiting/work where supported;
it does not imply rollback. A safe reproducible CLI equivalent can help users
learn, but omit secrets and do not auto-execute a printed command.

In a running TUI, embed the form model or release the outer terminal before a
standalone form. Reuse the full wizard; avoid a second reduced dashboard-only
implementation. Secret fields are masked and excluded from summaries, logs,
effective-config output, and command previews.

## Configuration

Start with useful defaults and no required file. Add settings for demonstrated
preferences, such as key bindings, theme, mouse capture, or refresh cadence.
Use TOML for a new tool unless its surrounding project already uses another
format. Do not introduce Viper solely to read a small typed file; add layering
machinery when its benefits justify the dependency.

Default path policy:

| Purpose | macOS/Linux | Windows |
|---|---|---|
| Preferences | `$XDG_CONFIG_HOME/<app>/config.toml`, fallback `~/.config/<app>/config.toml` | `os.UserConfigDir()` + `<app>/config.toml` |
| Durable user data | `$XDG_DATA_HOME/<app>`, fallback `~/.local/share/<app>` | LocalAppData + `<app>/data` |
| Session/history state | `$XDG_STATE_HOME/<app>`, fallback `~/.local/state/<app>` | LocalAppData + `<app>/state` |
| Rebuildable cache | `$XDG_CACHE_HOME/<app>`, fallback `~/.cache/<app>` | `os.UserCacheDir()` + `<app>/cache` |

Resolve LocalAppData through the platform's known-folder contract; do not
hard-code a drive/user path. Cache cleanup removes only the cache subdirectory.
This intentionally uses XDG on macOS instead of Go's native config default.
Ignore relative XDG variables as required by the specification; empty variables
use defaults. Never turn a missing HOME into a write in the working directory.
Only create directories for an actual write, not help, listing, or a dry run.

Precedence: **explicit flags → documented app-prefixed environment variables →
selected config file → built-in defaults**. Only flags actually supplied
override lower layers, including `false` and `0`. Honor `--config <path>` for
both reads and wizard writes. A missing default file is fine; an explicit
missing file, malformed content, or invalid field produces an actionable error.
Do not silently load `./config.toml` as a trusted override.

For an external-editor command, resolve the settings path independently of
parsing so malformed files can be repaired. Suppress editor launch and file
creation for JSON/non-TTY intent. Parse quoted editor arguments without shell
evaluation, use the platform's editor precedence, create missing state only on
an actual edit, and validate afterward without discarding invalid user edits.

Expose a redacted effective-config view (`config show` or `--print-config`) and
its selected path. Bind configuration to semantic actions, not row numbers or
screen coordinates. Validate keymap conflicts per scope and regenerate help
from effective bindings. Keep a usable Back/help/quit route after remapping.

Keep the destination file and save scope visible; offer a changes review and
require it when the operation's consequences warrant it. Preserve
unrelated fields/comments when editing an existing file, or offer an explicit
manual edit if the writer cannot do so. Detect intervening edits before replacing
the file and use an appropriate atomic write. Preferences are not a database
for changing selections/logs. No hot reload is required for the first version.

## Multi-target and externally owned state

For cross-target operations, make source and destination explicit and resolve
credentials independently. Reuse one preview/apply service from CLI and TUI.
Bind a reviewed plan to the relevant state when concurrent edits can invalidate
it, reread before apply, and report partial/unknown effects per operation.
A digest detects stale observations; it does not make multiple remote writes
an atomic transaction or justify automatic rollback.

When another application owns persistent configuration, distinguish saved
source, regenerated/applied runtime, and observed behavior. A file-write receipt
must not imply that a native app reloaded it. Expose the owner's activation step
and a separate verification action if there is no supported external API.

For completion setup and candidate queries, see
[shell-completion.md](shell-completion.md).

## Sources

Reviewed 2026-09-20. Invocation rules, TOML preference, and exit defaults are
design decisions of this skill, not mandates of the libraries.

- [Cobra source and guide](https://github.com/spf13/cobra): parsing, validation, hooks, I/O, completion. Check the target release's `command.go` for order.
- [Huh](https://github.com/charmbracelet/huh): fields, validation, accessibility, and Bubble Tea integration.
- [XDG specification](https://specifications.freedesktop.org/basedir/latest/) and [Go UserConfigDir](https://pkg.go.dev/os#UserConfigDir): directory contracts.
- [dev-cli dashboard actions](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/docs/guides/dashboard-actions.md): reuse of CLI wizards and terminal handoffs. The entry policy above is a new preference, not a claim about every dev command.
