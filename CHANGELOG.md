# Changelog

## Unreleased

## 0.2.1 — 2026-09-23

- Add `upgrade --check` and reviewed `upgrade --yes` for the lazymlflow
  executable's verified Homebrew formula. JSON stays clean, the installed
  version is verified after delegation, and no MLflow configuration, server,
  database or provider is loaded or changed.
