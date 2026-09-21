# Shell completion as a product feature

Read when adding completion setup, dynamic candidates, or update integration.
Generation, installation, shell activation, and candidate lookup are separate
operations. A working generator alone does not make Tab work in a user's shell.

## Product and shell ownership

Keep raw output such as `tool completion zsh` usable for package managers and
dotfile tools. If users need setup help, offer install/status commands that
write a user directory and show the activation snippet. Do not silently edit
an arbitrary framework's rc file, run sudo, or replace package-owned completions.
Detect foreign files and intervening changes before an atomic replacement.

For zsh, the completion directory must be in `fpath` before `compinit`.
Frameworks such as Oh My Zsh usually run compinit themselves; avoid duplicate
initialization and per-startup regeneration. An existing directory such as
~/.zfunc can be more appropriate than the generic XDG data default when a
dotfile manager already owns activation. A child command can report installed
files, but cannot prove its parent interactive shell loaded them.

Model installation channels separately. A source install does not normally
install shell files. Homebrew owns its completion directories. A dotfiles
manifest can register a Go tool and feed its native generator into an existing
completion pipeline without requiring a formula or a custom handwritten script.
Keep install-only platform policy aligned with the explicit upgrade consumer.

## Candidates and freshness

Cobra 1.10.2's native zsh bridge calls the invoked executable's `__complete`
path at completion time. Updating that binary usually exposes new commands and
values without regenerating the bridge. A changed generator still needs refresh.
Do not generalize this to a different shell/generator: legacy Bash output can
contain a static command tree.

Supply useful enums and saved local IDs. Respect the command's selection
precedence: a temporary endpoint must not complete IDs from an unrelated saved
default target. For completion, read only enough local state to resolve IDs;
do not reuse helpers that discover servers or resolve credentials.

Tab should remain useful offline and with broken settings. Return static
commands/flags, omit unavailable dynamic values, and avoid writes, authentication,
remote calls or starting a TUI. Use NoFileComp for logical IDs and value flags;
allow native path completion only where the value really is a local path.
Do not suggest local files for paths that belong to a remote host.

## Verify the actual shell path

Test candidates through `__complete`, but also launch an isolated interactive
zsh with a temporary ZDOTDIR, install the generated script, initialize completion,
and send real Tab input through a PTY. Use disposable registrations and assert
the resulting command buffer/candidate, not merely the presence of a script.
Check both missing/broken settings and new binary commands behind an existing
bridge. Never source the user's full shell rc for a generic smoke test.

Distinguish the evidence: generator syntax, file ownership checks, direct
candidate tests and actual shell activation are different validations.

## Sources

Reviewed 2026-09-20. Setup commands and offline candidate policy are design
choices informed by lazyclash, not requirements imposed by Cobra.

- [Cobra 1.10.2 completion guide](https://github.com/spf13/cobra/blob/v1.10.2/site/content/completions/_index.md)
  and [zsh generator](https://github.com/spf13/cobra/blob/v1.10.2/zsh_completions.go):
  callback directives and the current-binary bridge.
- [zsh completion initialization](https://zsh.sourceforge.io/Doc/Release/Completion-System.html#Initialization):
  fpath, autoload and compinit.
- [Go source installation](https://go.dev/ref/mod#go-install):
  executable package and destination semantics.
- [lazyclash operating knowledge](https://github.com/daviddwlee84/lazyclash/tree/main/docs):
  product install/status and independently managed dotfiles integration.
