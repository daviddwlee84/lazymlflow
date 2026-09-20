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
Update checks and self-update behavior are separate product features.

## Later stage: packaged releases

When users need installation without a Go toolchain, add the requested OS/arch
archives, checksums, release CI, and install/upgrade/uninstall verification.
Choose the package manager for the supported audience: for example, a personal
Homebrew tap can follow macOS/Linux release assets. Do not couple an early
source release to upstream Homebrew acceptance, Windows packaging, or automatic
updates unless they are part of the task.

Keep source installs and packaged releases on the same public version contract.
If an operational skill is embedded, it travels with each binary; a separately
installed skill copy needs an explicit refresh path if that feature is offered.
For a requested distribution pipeline, the optional
[CLI release skill](https://github.com/daviddwlee84/agent-skills/tree/main/skills/local/cli-release-distribution)
covers broader release-channel work.

## Sources

Reviewed 2026-09-20.

- [Go install and module restrictions](https://go.dev/ref/mod#go-install), [version queries](https://go.dev/ref/mod#version-queries), and [publishing modules](https://go.dev/doc/modules/publishing): source-install and immutable-version contracts.
- [runtime/debug.ReadBuildInfo](https://pkg.go.dev/runtime/debug#ReadBuildInfo): metadata embedded in the executable.
- [dev-cli version resolver](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/internal/cli/root.go) and [installation documentation](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/README.md): inspected example of linker precedence, module-version fallback, and `/cmd/dev` installation. Its additional distribution/update features are not requirements for a new tool.
