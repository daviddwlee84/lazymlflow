# Releasing lazymlflow

Stable tags use `vMAJOR.MINOR.PATCH` and are immutable. Push the release commit to
`main`, wait for CI, then tag that exact commit. Do not move an existing tag.

The release workflow reruns this repository's CI at the selected tag, verifies
that the tag is an ancestor of `origin/main`, and builds with GoReleaser 2.18.2.
It publishes only after all four archives and their SHA-256 manifest have been
uploaded to a draft and downloaded again for verification.

## Artifact contract

- Targets: Darwin/Linux, amd64/arm64, with `CGO_ENABLED=0`.
- Archives: `lazymlflow_<version-without-v>_<os>_<arch>.tar.gz`.
- Each archive contains the flat `lazymlflow` executable, `LICENSE`, and
  `completions/lazymlflow.bash` / `completions/lazymlflow.zsh`.
- `checksums.txt` names exactly those four archives.
- The linker injects the Git tag into `main.version`.
- Homebrew publication is managed centrally; this workflow does not write to a tap.

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
