# Research Links

These links were gathered before implementation planning. Several endpoints are
reverse-engineered or undocumented and may change.

## OpenUsage Reference

OpenUsage was used as a local reference for provider behavior and TUI
libraries. `token-burn` does not import OpenUsage internals.

## Codex

- CodexBar Codex docs:
  https://github.com/steipete/CodexBar/blob/main/docs/codex.md
- Codex Widget:
  https://github.com/fberbert/codex-widget
- OpenAI Codex app-server README, `account/rateLimits/read`:
  https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md
- OpenUsage Codex provider docs from another fork:
  https://github.com/robinebers/openusage/blob/main/docs/providers/codex.md
- codex-lb issue explaining `/wham/usage` vs Settings UI divergence:
  https://github.com/Soju06/codex-lb/issues/678

## Claude Code

- Claude Code statusline gist with OAuth usage API notes:
  https://gist.github.com/jtbr/4f99671d1cee06b44106456958caba8b
- Claude Usage CLI gist:
  https://gist.github.com/omachala/5ea5af4bfa0b194a1d48d6f2eedd6274
- ClaudeUsageWidget:
  https://github.com/dependentsign/ClaudeUsageWidget
- Claude Code issue requesting programmatic usage API:
  https://github.com/anthropics/claude-code/issues/38380
- Claude Code issue about statusline rate limit fields:
  https://github.com/anthropics/claude-code/issues/45133
- Claude Code issue requesting per-model weekly rate limits:
  https://github.com/anthropics/claude-code/issues/52661

## GitHub Copilot

- GitHub AI Credits billing for Copilot individuals:
  https://docs.github.com/copilot/concepts/billing/usage-based-billing-for-individuals
- GitHub REST billing usage endpoints:
  https://docs.github.com/en/rest/billing/usage
- Local `gh api /copilot_internal/user` probing showed Copilot quota snapshots
  for chat, completions, and premium interactions. This endpoint is internal and
  undocumented.

## Google Antigravity

- CodexBar Antigravity provider notes:
  https://github.com/steipete/CodexBar/blob/main/docs/antigravity.md
- OpenUsage Antigravity provider notes and plugin strategy:
  https://github.com/robinebers/openusage/blob/main/docs/providers/antigravity.md
- Antigravity Usage CLI:
  https://github.com/skainguyen1412/antigravity-usage
- Antigravity Usage Checker:
  https://github.com/tungcorn/antigravity-usage-checker

## Combined Tools

- i3 coding agent usage tracker:
  https://github.com/felixbrock/i3-coding-agent-usage-tracker
- Codex and Kimi limits checker gist:
  https://gist.github.com/ayagmar/6f2338af41c696ba74a6f130b4f569b0

## Libraries

- OpenTelemetry Go exporters:
  https://opentelemetry.io/docs/languages/go/exporters/
- OpenTelemetry OTLP metrics exporter spec:
  https://opentelemetry.io/docs/specs/otel/metrics/sdk_exporters/otlp/
- Go OTLP metric exporter package:
  https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlpmetric
- `modernc.org/sqlite`:
  https://pkg.go.dev/modernc.org/sqlite
- `github.com/kardianos/service`:
  https://github.com/kardianos/service
- Rust `rusqlite`, if Rust is reconsidered:
  https://docs.rs/rusqlite/
- Rust `opentelemetry-otlp`, if Rust is reconsidered:
  https://docs.rs/opentelemetry-otlp/latest/opentelemetry_otlp/
