# Releasing lazymlflow

Stable tags use `vMAJOR.MINOR.PATCH` and are immutable. Push the release commit to
`main`, wait for CI, then tag that exact commit. Do not move an existing tag.

The release workflow reruns this repository's CI at the selected tag, verifies
that the tag is an ancestor of `origin/main`, and builds with GoReleaser 2.18.2.
It publishes only after the version-specific archive inventory and SHA-256
manifest have been uploaded to a draft and downloaded again for verification.

## Artifact contract

- Starting at v0.3.0: Darwin/Linux/Windows, amd64/arm64, with `CGO_ENABLED=0`.
- Darwin/Linux archives: `lazymlflow_<version-without-v>_<os>_<arch>.tar.gz`.
- Windows archives: `lazymlflow_<version-without-v>_windows_<arch>.zip`.
- Each archive contains the flat `lazymlflow` executable (`lazymlflow.exe` on Windows),
  `LICENSE`, and Bash, Zsh and PowerShell files under `completions/`.
- `lazymlflow_<version-without-v>_source.tar.gz` is filtered by `.gitattributes`.
- New releases have six binary archives, one source archive and `checksums.txt`:
  eight assets; the checksum manifest names all seven archives.
- Historical tags keep the version-bound inventory in `scripts/release.py`.
  Do not add Windows files to an existing release or replace immutable assets.
- Linker metadata carries the exact stable tag; verification checks product,
  version, architecture, PE/ELF/Mach-O headers and safe archive membership.
- Homebrew and Scoop publication is owned by their central repositories. The
  product release workflow does not write package manifests directly.
- Required Windows CI runs native Go tests and real isolated Scoop check,
  process-exit handoff, update, no-op, running-instance and checksum-failure cases.

## Verify without publishing

```sh
python3 -m unittest discover -s scripts -p "test_*release*.py"
goreleaser check
goreleaser release --snapshot --clean
python3 scripts/release.py check --project lazymlflow --binary lazymlflow --smoke
```

Snapshots are local build artifacts, not stable releases. Native smoke checks
verify startup on the build host; header checks verify all target architectures.

## Retry

Rerun the failed GitHub Actions run, or dispatch `Release` with the existing tag.
Matching assets are retained; missing assets are uploaded only while the release
is still a draft. A differing existing asset or an incomplete already-public
release stops the job without replacement. Never delete or overwrite assets as
an automatic recovery step. Resolve the discrepancy explicitly or publish a new
version.

Builds use a fixed GoReleaser version, trimmed paths, and commit timestamps.
Changing the Go toolchain or release inputs may produce different bytes; the
retry guard intentionally refuses to overwrite them.

## Source and Go module downloads

Conversation records and agent plans stay in Git history for development, but
are not build inputs. `.gitattributes` excludes `.specstory` and the Claude,
Codex, Cursor, and OpenCode `plans` directories from source archives. Evidence
roots already present also carry a documented nested `go.mod` marker: the Go
module ZIP omits nested modules, because `go install` ignores `export-ignore`.
No product dependency or runtime resource is removed. A full Git clone still
contains tracked history; use a shallow clone when that is the intended workflow.

`python3 scripts/check-distribution.py --baseline <previous-commit>` verifies
actual Git archives and independently generated official `golang.org/x/mod`
v0.38.0 ZIPs. It disables export attributes only inside a throwaway clone, checks
required embedded assets, extracts each distribution, then builds and exercises
version, help, completion, and product-specific read-only commands with isolated
HOME/XDG directories. The product's root `go.mod`/`go.sum` and original Git state
are unchanged. CI tests also prove a missing boundary marker or embedded build
resource causes verification to fail.

Run the packaging regression cases with:

```sh
python3 -m unittest discover -s scripts -p test_distribution.py
python3 scripts/check-distribution.py
```

After publication, verify the real public Go proxy and both fixed-version and
`@latest` installs in disposable runner state:

```sh
gh workflow run public-module.yml --ref main -f version=vMAJOR.MINOR.PATCH
```

This manual workflow checks out the immutable tag and loads its verifier from
the workflow revision. It does not alter releases, tags, or installed user tools.
