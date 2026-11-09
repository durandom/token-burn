# AGENTS.md - token-burn

Guidance for coding agents working in this repository.

## Project Intent

Build a small, reliable daemon and CLI for monitoring live subscription quota
usage for AI coding tools.

The primary use case is live quota visibility:

- percentage used
- reset time
- window duration
- plan/account metadata
- forecasted exhaustion time
- OpenTelemetry export

Do not infer subscription usage from local session files, transcripts, token
logs, or pricing estimates unless explicitly requested later. This project is
about provider live usage signals.

## Language Direction

The implementation is Go.

Rationale:

- Simple daemon/CLI story.
- Strong OpenTelemetry support.
- Easy SQLite integration.
- A CGO-free binary is possible with `modernc.org/sqlite`.

## Layout

```text
cmd/token-burn/               CLI entrypoint
internal/cli/                 command wiring
internal/config/              XDG config load and defaults
internal/provider/            provider interface and shared models
internal/provider/codex/      live Codex usage client
internal/provider/claude/     live Claude usage client
internal/provider/copilot/    Copilot quota via the logged-in GitHub CLI
internal/provider/antigravity/ Antigravity quota via existing OAuth state
internal/store/               SQLite schema, migrations, queries
internal/forecast/            burn-rate and exhaustion forecast logic
internal/otel/                OTLP metric exporter
internal/daemon/              poll loop, backoff, graceful shutdown
internal/service/             macOS LaunchAgent and Linux systemd user unit
internal/tui/                 read-only dashboard over SQLite
internal/upgrade/             self-upgrade from GitHub Releases
```

## Code Style

- Keep the provider interface small and shaped around live usage windows.
- Prefer table-driven tests with the standard `testing` package.
- HTTP provider tests should use `httptest.NewServer`.
- SQLite tests should use `t.TempDir`.
- Never log raw access tokens, refresh tokens, cookies, or authorization headers.
- Store raw provider JSON only after redacting obvious token/cookie fields.
- Use UTC timestamps in storage; format in local time only in CLI/TUI output.
- Keep the TUI a read-only view over SQLite; it must not poll providers.

## Security

- Treat OAuth files and keychain values as secrets.
- Redact secrets in logs and diagnostics.
- Do not export account email as an OTel attribute by default. Use account IDs or
  configured aliases.
- Poll gently. Default interval should be 60 seconds or slower.

## References

See `docs/RESEARCH_LINKS.md` for public references and endpoint notes.


<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:46cd31e7 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   bd dolt push
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->

<!-- BEGIN BEADS CODEX SETUP: generated by bd setup codex -->
## Beads Issue Tracker

Use Beads (`bd`) for durable task tracking in repositories that include it. Use the `beads` skill at `.agents/skills/beads/SKILL.md` (project install) or `~/.agents/skills/beads/SKILL.md` (global install) for Beads workflow guidance, then use the `bd` CLI for issue operations.

### Quick Reference

```bash
bd ready                # Find available work
bd show <id>            # View issue details
bd update <id> --claim  # Claim work
bd close <id>           # Complete work
bd prime                # Refresh Beads context
```

### Rules

- Use `bd` for all task tracking; do not create markdown TODO lists.
- Run `bd prime` when Beads context is missing or stale. Codex 0.129.0+ can load Beads context automatically through native hooks; use `/hooks` to inspect or toggle them.
- Keep persistent project memory in Beads via `bd remember`; do not create ad hoc memory files.

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.
<!-- END BEADS CODEX SETUP -->
