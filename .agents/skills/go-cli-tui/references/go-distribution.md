# Staged Go CLI distribution

Read when making a Go CLI installable by other users. Match the distribution
work to the requested release: source installation can be enough for an early
version; a Homebrew formula is not a prerequisite for sharing a useful tool.

## First stage: source installation

Inspect `go.mod`, the executable's `package main`, the license, and supported
platforms. Publish an install command for the actual main-package import path.
A layout with `cmd/forgebox/main.go` needs `/cmd/forgebox`, not just the module
root. For example, dev-cli's package is:

```sh
go install github.com/daviddwlee84/dev-cli/cmd/dev@latest
```

Document the project's minimum Go version, supported OSes, fixed-version and
upgrade commands, and executable location. Go installs into `GOBIN` when set,
otherwise normally `$GOPATH/bin` or `$HOME/go/bin`; users must include that
directory in `PATH`. Explain how to inspect `go env GOBIN GOPATH` instead of
assuming every installation uses `~/go/bin`. Add shell completion instructions
if the application provides it.

`@latest` is a version query: it prefers releases, then prereleases, then the
default branch tip when no versions are available. It does not generally mean
the newest commit on main. Show a pinned published semver tag for reproducible
installs, and distinguish branch/commit installs as development builds.

A local build does not validate public installation. Once publication is
authorized, push the reviewed source and final semver tag, then install the
fixed tag and `@latest` from outside the checkout with a disposable `GOBIN`.
Check `--version`, help, and a safe offline command on those installed binaries.
Verify the repository/tag are publicly reachable before claiming the public
commands work. Versioned `go install` ignores the surrounding module; also
check that the released module has no unsupported local `replace` dependence.
Publish a new tag for a correction rather than moving a published version.

## Source packaging boundaries

Treat each download channel separately. A small binary release does not prove
that source or module downloads are small.

| Channel | Boundary to inspect |
| --- | --- |
| Binary archive | Explicit executable, license, completion and runtime-file allowlist. |
| Git source archive | `.gitattributes` `export-ignore` for selected non-build paths. |
| Go module ZIP | Go's module rules; nested `go.mod` boundaries exclude their subtrees. |
| Git clone | The selected Git objects/history; archive rules do not reduce it. |

Inventory tracked sizes and build inputs before excluding anything. Pure
conversation/plan directories are candidates; maintained docs, tests, licenses,
embedded operational skills, helper scripts and generated/runtime assets may be
necessary inputs. Do not exclude every Markdown file, agent directory or script
directory by name. Inspect `go:embed`, `go:generate` and release hooks explicitly.

For a requested source-slimming change, use narrow root-anchored archive rules.
For example, `/.specstory export-ignore` and `/.specstory/** export-ignore` leave
the tracked evidence available in Git while omitting it from exported source.
Apply equivalent rules only to the intended plan roots, not their whole parent
agent directories. These exclusions are not secret redaction or Git untracking.

Go module fetching deliberately disables `export-ignore` and `export-subst`.
When the user also wants smaller module downloads, an empty or comment-only
`go.mod` in an existing pure-evidence directory marks a nested-module boundary;
Go omits that subtree from the parent module ZIP. Explain the marker's purpose,
confirm that no build/embed/generate input crosses it, and leave the production
module's dependencies unchanged. Do not create unused agent directories just
to place markers in them.

Verify the actual exported source **and** module ZIP: inspect members, extract
into clean temporary directories, build the real main package, and exercise
offline version/help/completion and applicable embedded-resource output. Report
compressed bytes separately from uncompressed file totals. Test that a missing
required asset or an ineffective exclusion is detected.

`golang.org/x/mod/zip.CreateFromVCS` alone can give a false positive: its Git
archive path can honor `export-ignore`, unlike the Go fetcher. For an offline
module check, use a standalone temporary clone at the exact candidate SHA,
override **only its** `info/attributes` with
`* -export-subst -export-ignore`, then use a pinned official `x/mod/zip` version
compatible with the project's Go minimum. Keep this verifier dependency in a
test-only module. Check and unpack that ZIP before building. After publication,
also inspect `go mod download -json` output and test a fixed-tag install with an
isolated module cache and `GOBIN`.

Preserve existing archive names, path layout, checksums and embedded build
provenance. If introducing a dedicated source asset, update publisher allowlists
and downstream validation together; retain the old contract for older tags.
New rules affect new versions, not immutable published tags or cached module
ZIPs. Source-capable updaters and package managers remain separate consumers.

## Version reporting across build paths

A release workflow may inject a string using `-ldflags -X`, while ordinary
`go install ...@version` does not run that workflow. Resolve versions in this
order:

1. A meaningful explicitly injected release/build value.
2. `runtime/debug.ReadBuildInfo().Main.Version` when present and not `(devel)`.
3. A truthful development fallback such as `dev`.

Treat empty/default sentinel values as unset, preserve meaningful pseudo-versions
for commit installs, and keep plain version output independent of the network.
Test injected precedence, module-version recovery, and the development fallback.
A test binary often has no release module version: use a small pure resolver
for deterministic tests and verify an actual installed release afterward.
Keep version output offline and make the upgrade journey explicit. A published
CLI should normally expose `upgrade` and `upgrade --check` (or its established
equivalent); do not wait for a separate request to notice that installed users
have no update entry point. Read [self-update.md](self-update.md) during this
distribution stage. A narrow adapter for a supported package manager is enough;
unsupported install types can retain clear manual instructions. If this product
deliberately omits the command, document the reason and external update path.

Choose its strategy from the artifacts actually
published and ownership of the running executable. Build provenance alone does
not identify an installer, and a moved Go-built binary needs an update at its
current resolved path. The reference covers source-only releases, verified
assets, package managers, local-build preservation, and transactional replacement.
Verify both a fresh install and an existing installation's upgrade/check flow;
for a manager adapter, exercise its real command boundary with a fake manager
instead of upgrading the developer's own installation as a generic test.

## Changelog and release consistency

Maintain a user-facing `CHANGELOG.md` with an Unreleased section and dated
version sections. Record visible behavior, compatibility changes and fixes;
do not replace it with internal commit chronology. When backfilling an existing
release, inspect what its tag actually contained. Link upgrade instructions to
the changelog and choose the next version under the project's policy.

Finish tests and required CI on the exact source to release. Then create the
immutable tag and derive release notes from that version's changelog section.
The changelog version, Git tag, hosted release and installed `--version` must
agree. Source `go install` needs the tag and module metadata, not merely a changed
hard-coded constant or a local build with injected linker flags. Verify fixed-tag
and `@latest` installs outside the checkout; account for public proxy indexing
before treating a newly pushed tag's absence as a code failure.

## Later stage: packaged releases

When users need installation without a Go toolchain, add the requested OS/arch
archives, checksums, release CI, and install/upgrade/uninstall verification.
Choose the package manager for the supported audience: for example, a personal
Homebrew tap can follow macOS/Linux release assets. Do not couple an early
source release to upstream Homebrew acceptance, Windows packaging, or automatic
updates unless they are part of the task.

When adding Windows packages, validate ZIP members and PE architecture, run the
native executable and completion, and exercise the selected manager's upgrade
flow. Cross-compilation alone does not establish runtime support. Preserve each
published tag's archive contract when adding platforms or completion files;
introduce the new inventory at a fresh version instead of replacing old assets.
Keep installed owners stable when adding a suite-wide installation switch, and
separate install-only apply from explicit updates of already-installed tools.

Keep source installs and packaged releases on the same public version contract.
If an operational skill is embedded, it travels with each binary; end users need
no separate `npx skills` update for `--skill` output. A separately installed skill
copy needs an explicit refresh path if that feature is offered. Updating the
project's development skills remains a contributor workflow.
For a requested distribution pipeline, the optional
[CLI release skill](https://github.com/daviddwlee84/agent-skills/tree/main/skills/local/cli-release-distribution)
covers broader release-channel work.

## Sources

Reviewed 2026-09-20.

- [Go install and module restrictions](https://go.dev/ref/mod#go-install), [version queries](https://go.dev/ref/mod#version-queries), and [publishing modules](https://go.dev/doc/modules/publishing): source-install and immutable-version contracts.
- [runtime/debug.ReadBuildInfo](https://pkg.go.dev/runtime/debug#ReadBuildInfo): metadata embedded in the executable.
- [dev-cli version resolver](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/internal/cli/root.go) and [installation documentation](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/README.md): inspected example of linker precedence, module-version fallback, and `/cmd/dev` installation. Its additional distribution/update features are not requirements for a new tool.

Packaging guidance checked 2026-09-22:

- [Git archive attributes](https://git-scm.com/docs/git-archive#ATTRIBUTES): export exclusions and attribute sources.
- [Go module ZIP rules](https://go.dev/ref/mod#zip-files) and [Go's Git fetcher](https://go.dev/src/cmd/go/internal/modfetch/codehost/git.go): nested-module exclusions and disabled export attributes.
- [Official module ZIP tools](https://pkg.go.dev/golang.org/x/mod/zip): creation, checking and extraction; inspect the pinned implementation when reproducing Go fetch behavior.
