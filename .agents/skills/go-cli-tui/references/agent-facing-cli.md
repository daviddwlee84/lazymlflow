# Agent-facing CLI and embedded knowledge

Read when the product should support agents as well as people. Reuse the CLI's
domain operations and validation; an agent-specific HTTP wrapper or parallel
command implementation creates another contract to maintain. An embedded skill
is useful when operating the tool requires knowledge beyond flag syntax. It is
not a requirement for every CLI.

## Ship knowledge with the binary

Maintain one operational `SKILL.md` and focused references in the application's
source tree, and embed them with `go:embed`. A static `--skill` or `skill print`
entry can expose that version's instructions; named topics can disclose longer
references on demand. Use the installed binary's `--help` as the authority for
exact command syntax rather than maintaining a second hand-written inventory.

Keep the entry short: identify the target, inspect state, perform the authorized
operation, and verify the result. References should explain domain distinctions,
operation scope, credentials, and recovery that help an agent choose correctly.
Application-specific knowledge belongs here; this development skill should not
become an operating manual for every product built with it.

Treat printing documentation as a static path like help/version. Parse flags
normally, then bypass config loading, discovery, network probes, and directory
creation. It should work offline with missing or malformed user settings. Define
how documentation and machine-output flags interact; do not silently print
Markdown into a JSON contract. A boolean `--skill=false` must retain its normal
false meaning. Unknown options and topic names still report usage errors.

Embedding couples the guide to a build, but does not prove its accuracy. Test
frontmatter, embedded reference availability, equivalent print entry points,
and a few meaningful guide examples against a fake backend. Assert that static
paths never initialize runtime services. Skill installation/synchronization is
a separate feature; do not add a management subsystem solely to print a guide.

## Make automation predictable

- Preserve existing success JSON/NDJSON. Choose and document one machine error
  envelope on stderr with stable codes and a safe message; optional operation
  and HTTP status fields can support recovery. Keep logs, ANSI, progress, and
  secrets out of data output. Map errors at the outer CLI boundary so parsing,
  config, authentication, and runtime failures follow the same contract.
- Machine-output intent suppresses all prompting even under a PTY. This covers
  wizards, confirmation, SSH password/host-key prompts, and child-process
  authentication handoffs. Return a distinguishable authentication-required
  error instead of hanging; ordinary interactive execution may retain a
  deliberate terminal handoff.
- For indefinite streams, provide bounds when agents need finite collection,
  for example `--duration` and `--limit`. Define when duration starts and whether
  limit counts filtered or raw events. A quiet stream must still end on time.
  Reaching a requested bound succeeds; connection failure, output failure, and
  user cancellation keep their own outcomes. Retain existing unbounded defaults
  unless the requested product contract changes them.
- Document the real scope of `--read-only`, preview, and local registration
  commands. A restriction on remote control does not necessarily forbid local
  preferences; active probes may still cause remote work. State the actual
  boundary rather than suggesting an absolute guarantee.
- Preserve target identity across read, write, and verification. Obtain names
  and IDs from data, quote them, and keep credentials in documented references
  such as environment variables or files. Do not guess a default target after
  an explicitly selected target fails.
- A transport failure after submission can leave a mutation's outcome unknown.
  Report that state separately from a known rejection. Read back the same target
  before retrying; cancellation does not prove rollback. Documentation informs
  the agent's choice but grants no additional authority to mutate resources.

Select relevant tests: JSON errors with clean stdout, PTY plus machine output
without authentication handoff, missing/broken config on static paths, a quiet
bounded stream, filtered counts, cancellation, output errors, and an uncertain
write followed by read-back. Test observable contracts rather than exact prose.

## Sources and limits

Reviewed 2026-09-20. The automation contract above is design guidance, not a
claim that every cited CLI already implements every behavior.

- [dev-cli embedded skill](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/internal/skill/skill.go) and [entry points](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/internal/cli/root.go): one embedded operational tree and binary-owned `--skill`, inspired there by Herdr.
- [dev-cli operational entry](https://github.com/daviddwlee84/dev-cli/blob/689836cdea61d46ee86cdfa4a0df42ac324e6561/internal/skill/dev-cli/SKILL.md): workflow boundaries, command-help authority, and references loaded by task.
- [Go embed](https://pkg.go.dev/embed): compile-time file inclusion; it does not validate the documentation's semantics.
