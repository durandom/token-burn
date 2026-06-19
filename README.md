# token-burn

Live AI coding subscription quota monitor for Codex/OpenAI, Claude Code,
GitHub Copilot, and Google Antigravity.

`token-burn` is a small local daemon, CLI, and terminal dashboard for watching
real provider-reported quota usage, reset times, burn rate, and forecasted
exhaustion for AI coding tools.

It answers the practical question:

> How hot is my subscription quota right now, and will I hit the limit before it
> resets?

The important design choice is that `token-burn` asks provider-owned live usage
endpoints. It does **not** infer quota usage from local transcripts, token logs,
session files, or pricing estimates. Local logs only describe the machine you
are on. Provider usage captures the whole account, including work done from
other machines.

## Current Providers

- Codex / ChatGPT subscription usage via
  `https://chatgpt.com/backend-api/wham/usage`
- Claude Code subscription usage via
  `https://api.anthropic.com/api/oauth/usage`
- GitHub Copilot quota and AI Credits usage via the logged-in GitHub CLI:
  `gh api /copilot_internal/user` and GitHub billing usage endpoints
- Google Antigravity model quota usage via existing Antigravity OAuth state and
  Google Cloud Code usage endpoints

These endpoints and credential files are not stable public APIs. Expect sharp
edges and occasional breakage.

## What It Does

- Monitors live Codex/OpenAI, Claude Code, GitHub Copilot, and Google
  Antigravity subscription quota usage.
- Polls provider usage APIs on a gentle interval, defaulting to 60 seconds.
- Stores every observed quota window in local SQLite for history.
- Shows current quota state in a fast terminal UI dashboard.
- Forecasts burn rate, exhaustion time, and projected percent at reset.
- Exports current usage and forecast gauges through OpenTelemetry OTLP.
- Ships an OpenObserve dashboard template for long-term observability.
- Runs as a user service on macOS via LaunchAgent.
- Reuses existing provider authentication from the vendor tools.

## Use Cases

- See whether a Codex, Claude Code, GitHub Copilot, or Google Antigravity
  subscription will hit a usage limit before its reset time.
- Track quota usage from all machines on the same provider account, not just the
  current workstation.
- Export AI coding subscription usage metrics into OpenTelemetry, OpenObserve,
  Prometheus-compatible backends, or other observability systems.
- Keep a local SQLite time-series history of provider-reported quota windows.

## Philosophy

`token-burn` is deliberately boring infrastructure for a weirdly modern problem.

- The provider is the source of truth.
- The local database is history, not authority.
- Authentication belongs to Codex, Claude Code, GitHub CLI, Google Antigravity,
  and the OS credential store.
- OpenTelemetry is the integration path for serious dashboards and retention.
- The default experience should work on a normal logged-in workstation.
- The TUI should be glanceable: the bar is the analog clock, text is the
  digital readout.

## Install

Prebuilt binaries are published on GitHub Releases for:

- macOS amd64 / arm64
- Linux amd64 / arm64

Install the latest release into `~/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/durandom/token-burn/main/scripts/install.sh | sh
```

Install a specific release:

```sh
curl -fsSL https://raw.githubusercontent.com/durandom/token-burn/main/scripts/install.sh | TOKEN_BURN_VERSION=v0.1.0 sh
```

Then run:

```sh
token-burn version
token-burn once --json
token-burn tui
```

Upgrade later:

```sh
token-burn upgrade
```

If `~/.local/bin` is not on your `PATH`, either add it or set
`TOKEN_BURN_INSTALL_DIR`.

```sh
TOKEN_BURN_INSTALL_DIR=/usr/local/bin sh scripts/install.sh
```

## Install From Source

Requirements:

- Go 1.26+
- A logged-in Codex, Claude Code, GitHub CLI, and/or Google Antigravity
  installation
- macOS for `install` service management today

```sh
git clone https://github.com/durandom/token-burn.git
cd token-burn
go build -o bin/token-burn ./cmd/token-burn
```

Run one live fetch:

```sh
./bin/token-burn once --json
```

Install the background daemon on macOS:

```sh
./bin/token-burn install --binary "$PWD/bin/token-burn"
```

Open the dashboard:

```sh
./bin/token-burn tui
```

## TUI

The TUI is intentionally compact.

```text
token-burn  last poll 10:17:37
q quit  r refresh  auto-refresh 60s

claude/claude-default
  five hour        [███▒▒───────────────────]  14.0%
                   resets in 1h 32m · 3.2%/h · reset ~19% · reset first

copilot/copilot-default
  ai credits       [█████████───────────────]  37.2%
                   resets in 11d 13h · 0.0%/h · reset ~37%
```

Bar legend:

- `█` current usage
- `▒` forecasted additional usage by reset
- `─` likely unused capacity

`reset ~N%` is the projected usage at reset. It can exceed `100%` when the
current burn rate would overshoot the quota before reset; the bar itself remains
capped at full.

The TUI reads SQLite only. Provider polling belongs to the daemon, so refreshing
the dashboard does not create extra provider requests.

## Commands

```text
token-burn once --json
token-burn daemon
token-burn status
token-burn history --provider codex --window five_hour --since 24h
token-burn forecast --provider claude --window five_hour
token-burn forecast --provider copilot --window ai_credits
token-burn forecast --provider antigravity --window claude_and_gpt
token-burn tui
token-burn upgrade
token-burn install
token-burn service-status --json
token-burn uninstall
token-burn otel-test
```

## Configuration

There is no `init` command. `token-burn` creates a small XDG-driven config file
the first time it loads configuration.

```text
Config:   ${XDG_CONFIG_HOME:-~/.config}/token-burn/config.toml
Database: ${XDG_STATE_HOME:-~/.local/state}/token-burn/token-burn.db
Logs:     ${XDG_STATE_HOME:-~/.local/state}/token-burn/token-burn.log
```

Default config:

```toml
poll_interval = "60s"
http_timeout = "15s"
database_path = "/home/alice/.local/state/token-burn/token-burn.db"

[otel]
enabled = false
endpoint = "http://localhost:4318"
protocol = "http/protobuf"
export_interval = "60s"

[[accounts]]
provider = "codex"
id = "codex-default"

[[accounts]]
provider = "claude"
id = "claude-default"

[[accounts]]
provider = "copilot"
id = "copilot-default"

[[accounts]]
provider = "antigravity"
id = "antigravity-default"
```

## Authentication

`token-burn` does not run OAuth flows or store provider credentials.

Codex credentials are read from:

- configured `auth_file`
- `$CODEX_HOME/auth.json`
- `~/.codex/auth.json`

Claude credentials are read from:

- `CLAUDE_CODE_OAUTH_TOKEN`
- configured `credentials_file`
- `~/.claude/.credentials.json`
- macOS Keychain entry used by Claude Code

GitHub Copilot credentials are not read directly. The Copilot provider shells
out to the logged-in GitHub CLI and uses `gh api`, so `gh auth login` is the
only setup path.

Google Antigravity credentials are read from existing Antigravity state stores
and, on macOS, the `agy` Keychain item. `token-burn` does not run a Google OAuth
login flow or write back to Antigravity credential stores. If the stored access
token is expired and OAuth client credentials are supplied via environment, it
uses the existing vendor refresh token to mint a short-lived access token and
caches only that access token under token-burn's own XDG cache.

Secrets are treated as secrets. Authorization headers and obvious token/cookie
fields are redacted from diagnostics.

## OpenTelemetry

Enable OTLP metrics in config:

```toml
[otel]
enabled = true
endpoint = "http://localhost:4318"
protocol = "http/protobuf"
export_interval = "60s"
```

See [docs/OTEL.md](docs/OTEL.md) for metric names and a local collector sketch.
An OpenObserve dashboard template lives in
[contrib/openobserve/token-burn.dashboard.json](contrib/openobserve/token-burn.dashboard.json);
import notes are in [docs/DASHBOARDS.md](docs/DASHBOARDS.md).

## Development

```sh
go test ./...
go build -o bin/token-burn ./cmd/token-burn
```

Publish a release by pushing a semver-ish tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

The release workflow builds archives, publishes checksums, and attaches them to
the GitHub Release.

The code is intentionally split into small internal packages:

```text
cmd/token-burn/          CLI entrypoint
internal/provider/       provider interface and shared models
internal/provider/codex/ live Codex usage client
internal/provider/claude live Claude usage client
internal/provider/copilot live GitHub Copilot usage client
internal/provider/antigravity live Google Antigravity usage client
internal/store/          SQLite schema and queries
internal/forecast/       burn-rate and reset projection logic
internal/otel/           OTLP metric exporter
internal/daemon/         poll loop and backoff
internal/service/        user service install/status
internal/tui/            Bubble Tea dashboard
```

## Status

This is early software. It is useful on the author's machine, covered by tests,
and intentionally small, but it depends on provider behavior that may change.

Roadmap:

- Linux systemd user service install
- Windows viability check
- retention cleanup
- Homebrew formula/tap
- more provider shapes as they appear

More detail lives in [docs/ROADMAP.md](docs/ROADMAP.md).

## License

MIT. See [LICENSE](LICENSE).
