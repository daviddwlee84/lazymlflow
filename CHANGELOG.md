# Changelog

## Unreleased

- Add persistent cross-experiment run bookmarks in a Pinned sidebar view, `*` pin/unpin in Runs, and offline CLI listing/removal.
- Show complete run and experiment names with `i`, wrapped scrolling, and `Y` to copy the full name.
- Add `/` live filtering to contextual help, including shortcut keys and descriptions, with Enter to keep the filter and Esc to clear it before closing.

## 0.3.1 — 2026-09-25

- Add cross-experiment Running, Recent, Unread and Alerts views, persistent local read and acknowledgement state, cached experiment counts, and exact-key metric-update subscriptions. Completed selected runs stay visible while being read in Running.
- Share session overlays within each Activity view, preserve missing metric selections, and add quick overlay toggling, persistent metric pins and ordering.
- Add raw sample markers, EMA smoothing, independent 0–1 curve scales, and raw extrema with logged-step, sample and time distances. Chart transforms preserve original metric history and never assume that steps are epochs.
- Preview bounded text and JSON artifacts in the dashboard or CLI, with size-aware confirmation, optional bat/pager viewing of the same prefix, and cleanup of private temporary files.
- Cancel owned Windows pager process trees before releasing their preview files and terminal session.
- Expand race, real-terminal and MLflow compatibility checks, including retained Activity selection and bounded artifact reads.

## 0.3.0 — 2026-09-23

- Add Windows amd64/arm64 ZIP releases, PowerShell completion and verified Scoop installation/upgrade support. Upgrades exit into a private helper with visible progress and queryable final results; checks remain read-only and manager failures never trigger source fallback.
- Verify native Windows behavior in CI and refresh the canonical go-cli-tui development guidance.

## 0.2.1 — 2026-09-23

- Add `upgrade --check` and reviewed `upgrade --yes` for the lazymlflow
  executable's verified Homebrew formula. JSON stays clean, the installed
  version is verified after delegation, and no MLflow configuration, server,
  database or provider is loaded or changed.
