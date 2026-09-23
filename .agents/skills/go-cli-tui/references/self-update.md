# Go CLI self-update

Read when making a CLI installable, preparing its first release, or implementing
an explicit `upgrade`/`update` command. Include the existing user's path to the
next version in the product UX: normally expose an upgrade entry point and a
read-only check. An intentional omission needs a documented product reason and
supported external upgrade instructions. A source release
does not need a release-asset pipeline just to offer a useful updater. Select the
strategy from the project's published artifacts and the executable being run.
These are implementation defaults; preserve the product's supported platforms
and existing command contract.

## Choose the update path from evidence

| Current situation | Suitable behavior |
|---|---|
| Pure-Go project publishes source tags, no binary assets | Build the selected exact module version with installed native Go, verify, and replace the running copy |
| Standalone installation; release publishes a matching platform archive and checksums | Download that exact archive, verify its checksum and executable identity, then replace the running copy |
| Release intentionally omits this platform | Offer a source build only when the project explicitly supports that fallback and its native dependencies |
| A supported package manager owns the resolved executable | Preview its exact command during check; invoke it during explicit apply under the command's normal confirmation policy |
| Ownership is ambiguous or its manager has no supported adapter | Give actionable manager-specific guidance without mutating or switching install methods |
| Local, modified, replaced-module, or unrecognized build | Preserve it by default; explain any supported explicit replacement path |

Failure to fetch an expected asset, a missing required checksum, or a checksum
mismatch is a failed update. It is not evidence that a release lacks platform
coverage, and must not silently switch to another install method. Do not assume
that “Go project” means `CGO_ENABLED=0` works; inspect its build requirements.

## Separate build provenance from installation ownership

Inspect the running executable through `os.Executable`, resolve symlinks, and
retain both the reported executable path and resolved path. Searching `PATH` can
reveal duplicate copies for diagnostics, but must not choose a different update
destination.
`os.Executable` is not a permanent file-identity guarantee, so recheck the target
before replacement.

Go build information identifies the main package, module version, replacements,
and available VCS settings. It does **not** record who installed the file. A
versioned module binary copied from `~/go/bin` into `~/.local/bin` still has the
same build metadata. Updating it by running an ordinary `go install` with today's
`GOBIN`, then claiming success, can leave the invoked copy unchanged. Stage the
build with a temporary `GOBIN` and replace the resolved current copy instead.

Use package ownership records and the resolved package layout when available.
A directory name, `$GOPATH/bin`, or module version alone is only a clue. Preserve
package-owned files even when their directory is writable; do not overwrite a
Homebrew Cellar binary, Nix store path, or another manager's payload directly.
If ownership remains ambiguous, report it instead of guessing an updater. `--force`
must not bypass package ownership, checksum checks, or changed-file detection.

Keep release labels separate from provenance. An injected `v1.2.3` can describe a
local VCS build with uncommitted changes; equality with the latest tag does not
make that copy disposable. Inspect available `vcs.revision`, `vcs.modified`,
module version and replacement metadata. Preserve local/development builds by
default, and require the product's explicit development-replacement option if
supported. Unknown metadata stays unknown.

## Keep checking separate from applying

Use a shared check/plan service for CLI and any TUI action. A useful result names
the current version, candidate version, resolved executable, build provenance,
install owner, selected strategy, and whether applying is supported. Resolve a
candidate once and carry its exact tag through download/build/verification;
`@latest` must not resolve a second, possibly different release during apply.

`upgrade --check` may fetch release metadata, but does not replace files, create
staging directories, refresh installed skills, or modify configuration. It works
in read-only mode. Applying observes the product's read-only policy. JSON and
non-TTY calls never prompt: require explicit apply intent where confirmation is
part of the command contract, keep stdout machine-readable, and route build
progress to stderr. Help, version, and embedded skill output remain offline.
Avoid adding network checks to ordinary startup as a side effect of this feature.

## Delegate supported package managers

When the owner is supported, `tool upgrade` should complete the handoff instead
of asking the user to copy a command that the tool already knows how to run.
Keep the existing prompt/`--yes`, non-TTY and JSON contracts; do not add a second
confirmation layer around an already explicit apply policy. Check mode displays
the owner, exact target and command but never runs the upgrade.

Identify the installed package from the resolved executable and manager records.
For Homebrew, use the installed keg/receipt and formula identity, including its
tap when available. Verify that the selected `brew` uses that same Cellar; a
second Homebrew on PATH must not redirect the update to another installation.
Pass one formula as argv to `brew upgrade`, rather than launching a shell or
upgrading every installed package. Never overwrite the Cellar payload directly.

Let the manager decide its available version. A newer GitHub tag does not prove
that the formula is updated, and a manager-owned apply must not depend on a
separate GitHub latest-release lookup. Pins, already-current packages and tap
lag are valid manager outcomes. Report the effective installed version after
the command, without claiming the GitHub release was installed merely because
the manager exited zero. Follow the manager's stable installation link after
replacement; the running process may still refer to an old keg, and another
binary earlier on PATH may be unrelated.

Forward progress according to the CLI's output contract, propagate failures
and cancellation, and leave rollback to the manager. A failed manager command
does not authorize a standalone download, a source install or `sudo`.

## Windows Scoop handoff

Scoop checks for processes whose executable lives under the installed app's
folder and can skip their update. Calling it synchronously while that same
binary remains alive cannot complete the normal upgrade. Choose an explicit
external upgrade path or an approved handoff appropriate to the product; do not
turn off Scoop's running-process protection or terminate unrelated instances.

For an asynchronous handoff, put the helper and its working directory outside
the installed package, bind the reviewed owner/receipt and the initiating
process identity, and wait for that exact process to exit. Authorize the request
once. Distinguish accepted/running work from completed, blocked, failed or
interrupted work, with a durable result and discoverable read-only query.
A query should not itself start the package being replaced: an out-of-package
status command or reading the result file avoids that conflict. A host job may
retain lifetime control; report that limitation instead of claiming the helper
is guaranteed to survive terminal closure.

Verify the effective `current` target, bucket and actual product/version after
Scoop returns. Its exit code alone is insufficient: inspected Scoop source can
print an `ERROR` (including failed archive hash verification) and finish the
outer update command with zero. Treat explicit manager errors and unverified
outcomes as failures, not an already-current package; retain bounded diagnostic
logs without exposing credentials. An unknown/partial change is not rollback.

Windows junctions and ACLs need native verification. Unix execute/permission
bits do not establish Windows executability or privacy. Keep embedded scripts,
skills and checksum fixtures byte-stable across Git checkout line endings.
Use native Windows process/terminal tests in addition to PE/header and cross-
compilation checks. Keep actual developer installations outside test scope.

## Prepare source builds

For source builds, check the required native toolchain and dependencies before
preparing an update. When the contract promises to use installed Go only, set
`GOTOOLCHAIN=local`; Go's automatic selection can otherwise download a toolchain.
If preserving the user's normal `GOTOOLCHAIN` policy instead, document that Go
itself may obtain a compatible toolchain. Do not bootstrap a missing Go command,
run an installer script, or invoke `sudo` on the user's behalf.
Missing tools or unwritable destinations produce actionable manual instructions.
Run the version-qualified build outside the user's checkout with task-owned
staging, `GOWORK=off`, and deliberate build flags. Keep module verification
enabled and avoid changing persistent `go env` settings.

## Replace only a verified executable

Treat apply as a transaction around one resolved destination:

1. Capture the destination's identity and acquire a lock for that destination.
   Another updater must not publish concurrently. Validate regular-file and
   ownership requirements before expensive preparation.
2. Create a private staging location on the destination filesystem. Build the
   exact version there or download the selected archive with bounded size/time.
   Extraction must reject unsafe paths and unexpected payloads.
3. Verify required checksums before extracting or executing downloaded content.
   Validate the staged program's expected package/module, platform, executable
   mode and version; run a bounded offline version check. A release label alone
   is insufficient to identify the intended program.
4. Honor cancellation and revalidate the destination and any launch symlink
   before publishing. Check file identity and relevant metadata/content, not
   merely whether a path with the same name still exists. A lock coordinates
   cooperating updaters; it does not replace this check.
5. Publish with an atomic replacement mechanism supported by the platform. On
   systems that cannot replace a running executable directly, implement and test
   the platform's deferred replacement path or report that apply is unsupported.
6. Preserve the old executable on download, build, verification, cancellation,
   identity, permission, or replacement failure. Clean only owned staging/locks;
   never delete the old file first or retry by switching install methods.
7. Report the actual updated path and verified version. A secondary action such
   as refreshing an external skill copy must report its own failure without
   claiming that an already published binary update was rolled back.

For package-managed updates, let the manager own its transaction and verify the
effective installation afterward. Avoid claiming the currently invoked copy was
updated merely because an unrelated installation command exited successfully.

## Embedded knowledge follows the binary

An embedded `--skill` guide changes with the updated executable. End users do
not need `npx skills` to refresh that embedded content. A separately installed
operational skill is an independent copy: refresh it only through a documented,
scoped feature. Development skills such as `go-cli-tui` belong to the project's
contributor workflow and are not part of an end-user binary upgrade.

## Verification cases

Exercise the updater with temporary executables, a fake release server, and an
injected builder/manager. Keep the real running development tool out of tests.

| Case | Observable result |
|---|---|
| Go-built copy moved outside current `GOBIN`; another copy first on `PATH` | Only the resolved running destination is updated |
| Local VCS build reports the latest release label and is dirty | Default apply preserves it and explains provenance |
| Symlink into a package-owned directory | Ownership is recognized; direct replacement is refused |
| Supported Homebrew install, explicit upgrade | Exact installed formula is delegated to its owning brew; manager progress and exit status are handled |
| Scoop running-instance policy, helper handoff and safe status polling | Original process exits; one exact package updates; another live instance blocks; querying does not hold the installed image open |
| Scoop prints an error but exits zero; helper is replayed or interrupted | Failure/interruption stays visible; no false success, duplicate update or overwritten completed result |
| Manager-owned check, missing/wrong-prefix brew, ambiguous receipt | No upgrade or binary staging; accurate plan or actionable unsupported result |
| Manager succeeds without changing a pinned/current package; tap trails GitHub | Effective installed version is reported; no false latest-version claim or fallback |
| Manager moves its current link and removes the old keg; PATH contains another copy | Verify the new owning-manager target, not the old running path or PATH shadow |
| Source-only release; Go absent or too old | Check remains useful; apply gives toolchain guidance and retains the old file |
| Expected archive download/checksum fails | Failure is reported; no source fallback or old-file change |
| Staged binary has the wrong version/package/platform | Verification fails before publication |
| Two updaters, changed target, symlink retarget, cancellation, rename failure | No lost update; old target and unrelated files survive; owned staging is cleaned |
| JSON/check/read-only/offline documentation paths | No prompts or check writes; apply policy is enforced; help/version/skill need no network |

These are acceptance cases, not claims that a particular updater has passed
them. Add platform execution evidence for every replacement path shipped.

## Sources and scope

Reviewed 2026-09-23. The transaction and ownership guidance above is original
synthesis; the following primary sources establish the underlying behavior.

- [Homebrew upgrade and Cellar commands](https://docs.brew.sh/Manpage): a named
  upgrade targets that installed package and respects pins; `--cellar` with a
  formula reports its version-independent Cellar directory.

- Inspected Scoop at `b588a06e41d920d2123ec70aee682bae14935939` on
  2026-09-23: [update process guard and outer exit](https://github.com/ScoopInstaller/Scoop/blob/b588a06e41d920d2123ec70aee682bae14935939/libexec/scoop-update.ps1),
  [running-image lookup](https://github.com/ScoopInstaller/Scoop/blob/b588a06e41d920d2123ec70aee682bae14935939/lib/install.ps1), and
  [error output helper](https://github.com/ScoopInstaller/Scoop/blob/b588a06e41d920d2123ec70aee682bae14935939/lib/core.ps1).
  Scope: these manager boundaries, plus isolated Windows install/update probes;
  this is not an audit of every Scoop hook or future release.
- [Go version-qualified installation](https://go.dev/ref/mod#go-install) and
  [toolchain selection](https://go.dev/doc/toolchain): destination, module scope,
  and installed-toolchain controls.
- [os.Executable](https://pkg.go.dev/os#Executable) and
  [runtime/debug.BuildInfo](https://pkg.go.dev/runtime/debug#BuildInfo): running
  path limitations and available build metadata; neither is an installer record.
- Inspected [dev-cli upgrade](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/internal/cli/upgrade.go)
  and [source builder](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/internal/selfupdate/source.go):
  examples of staged source builds, version checks, native-toolchain policy and
  destination revalidation. Its directory-based installer guesses and managed
  `go install ...@latest` path are not a general guarantee that a moved running
  binary will be replaced; use the identity/ownership contract above.
