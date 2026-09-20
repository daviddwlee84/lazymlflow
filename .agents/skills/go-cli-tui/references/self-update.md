# Go CLI self-update

Read when implementing an explicit `upgrade`/`update` command. A source release
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
| Package manager owns the resolved executable | Report or invoke the owning manager's upgrade command under the command's normal execution policy |
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
| Source-only release; Go absent or too old | Check remains useful; apply gives toolchain guidance and retains the old file |
| Expected archive download/checksum fails | Failure is reported; no source fallback or old-file change |
| Staged binary has the wrong version/package/platform | Verification fails before publication |
| Two updaters, changed target, symlink retarget, cancellation, rename failure | No lost update; old target and unrelated files survive; owned staging is cleaned |
| JSON/check/read-only/offline documentation paths | No prompts or check writes; apply policy is enforced; help/version/skill need no network |

These are acceptance cases, not claims that a particular updater has passed
them. Add platform execution evidence for every replacement path shipped.

## Sources and scope

Reviewed 2026-09-20. The transaction and ownership guidance above is original
synthesis; the following primary sources establish the underlying behavior.

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
