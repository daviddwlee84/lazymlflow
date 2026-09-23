# Changelog

## Unreleased

## 0.3.0 — 2026-09-23

- Add Windows amd64/arm64 ZIP releases, PowerShell completion and verified Scoop installation/upgrade support. Upgrades exit into a private helper with visible progress and queryable final results; checks remain read-only and manager failures never trigger source fallback.
- Verify native Windows behavior in CI and refresh the canonical go-cli-tui development guidance.

## 0.2.1 — 2026-09-23

- Add `upgrade --check` and reviewed `upgrade --yes` for the lazymlflow
  executable's verified Homebrew formula. JSON stays clean, the installed
  version is verified after delegation, and no MLflow configuration, server,
  database or provider is loaded or changed.
