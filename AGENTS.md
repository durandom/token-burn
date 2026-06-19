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

Prefer Go for the first implementation.

Rationale:

- Simple daemon/CLI story.
- Strong OpenTelemetry support.
- Easy SQLite integration.
- Existing OpenUsage source code is Go and can be used as reference.
- A CGO-free binary is possible with `modernc.org/sqlite`.

## Expected Shape

```text
cmd/token-burn/          CLI entrypoint
internal/provider/       provider interface and shared models
internal/provider/codex/ live Codex usage client
internal/provider/claude live Claude usage client
internal/store/          SQLite schema, migrations, queries
internal/forecast/       burn-rate and exhaustion forecast logic
internal/otel/           OTLP metric exporter
internal/daemon/         poll loop, backoff, graceful shutdown
internal/service/        macOS LaunchAgent install/uninstall/status
```

## Code Style

- Keep the provider interface small and shaped around live usage windows.
- Prefer table-driven tests with the standard `testing` package.
- HTTP provider tests should use `httptest.NewServer`.
- SQLite tests should use `t.TempDir`.
- Never log raw access tokens, refresh tokens, cookies, or authorization headers.
- Store raw provider JSON only after redacting obvious token/cookie fields.
- Use UTC timestamps in storage; format in local time only in CLI output.
- Avoid UI/TUI complexity. CLI text and JSON are enough.

## Security

- Treat OAuth files and keychain values as secrets.
- Redact secrets in logs and diagnostics.
- Do not export account email as an OTel attribute by default. Use account IDs or
  configured aliases.
- Poll gently. Default interval should be 60 seconds or slower.

## References

See `docs/RESEARCH_LINKS.md` for public references and endpoint notes.

