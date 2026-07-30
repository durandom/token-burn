# token-burn

Live AI coding subscription quota monitor for Codex/OpenAI, Claude Code,
GitHub Copilot, Google Antigravity, and xAI/Grok.

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
- xAI/Grok subscription usage via Pi's xAI OAuth session and the experimental,
  undocumented Grok CLI billing endpoint

These endpoints and credential files are not stable public APIs. Expect sharp
edges and occasional breakage.

## What It Does

- Monitors live Codex/OpenAI, Claude Code, GitHub Copilot, Google Antigravity,
  and xAI/Grok subscription quota usage.
- Polls provider usage APIs on a gentle interval, defaulting to five minutes.
- Stores every observed quota window in local SQLite for history.
- Shows current quota state in a fast terminal UI dashboard.
- Forecasts burn rate, exhaustion time, and projected percent at reset.
- Exports current usage and forecast gauges through OpenTelemetry OTLP.
- Ships an OpenObserve dashboard template for long-term observability.
- Runs as a user service on macOS via LaunchAgent and on Linux via a systemd user unit.
- Reuses existing provider authentication from the vendor tools.

## Use Cases

- See whether a Codex, Claude Code, GitHub Copilot, Google Antigravity, or
  xAI/Grok subscription will hit a usage limit before its reset time.
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
  Pi, and the OS credential store.
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

Known issue: upgrading **from v0.1.14 or older** can fail with
`invalid cross-device link` and remove the installed binary when `/tmp` and
the install directory are on different filesystems (for example tmpfs `/tmp`
and a separate `/home`). The replace logic is fixed in v0.1.15+, but the old
binary performs the upgrade, so the failure can still happen once during that
transition. Recovery is a reinstall:

```sh
curl -fsSL https://raw.githubusercontent.com/durandom/token-burn/main/scripts/install.sh | sh
```

If `~/.local/bin` is not on your `PATH`, either add it or set
`TOKEN_BURN_INSTALL_DIR`.

```sh
TOKEN_BURN_INSTALL_DIR=/usr/local/bin sh scripts/install.sh
```

## Install From Source

Requirements:

- Go 1.26+
- A logged-in Codex, Claude Code, GitHub CLI, Google Antigravity, and/or Pi
  provider subscription session
- macOS (LaunchAgent) or Linux (systemd user unit) for `install` service management

```sh
git clone https://github.com/durandom/token-burn.git
cd token-burn
go build -o bin/token-burn ./cmd/token-burn
```

Run one live fetch:

```sh
./bin/token-burn once --json
```

Install the background daemon (macOS LaunchAgent or Linux systemd user unit):

```sh
./bin/token-burn install --binary "$PWD/bin/token-burn"
```

On Linux this writes `~/.config/systemd/user/token-burn.service`, runs
`systemctl --user enable --now token-burn.service`, and attempts
`loginctl enable-linger` so the daemon keeps polling after logout. Enabling
linger may require polkit authorization; if it fails the daemon still runs
while you are logged in, and you can enable it manually:

```sh
loginctl enable-linger "$USER"
```

Inspect or remove it with:

```sh
systemctl --user status token-burn.service
journalctl --user -u token-burn.service -f
./bin/token-burn uninstall
```

Open the dashboard:

```sh
./bin/token-burn tui
```

## TUI

The TUI is intentionally compact. Account panels use the available terminal
width, bars scale with it, and quota details collapse onto one line when the
normal layout would exceed the terminal height. The five-provider default fits
a typical `100x30` terminal; smaller viewports use an ultra-compact row or show
the minimum required height. Override automatic selection when needed:

```sh
token-burn tui --layout auto     # responsive default
token-burn tui --layout normal   # two lines per quota window
token-burn tui --layout compact  # one line with adaptive bars
token-burn tui --layout ultra    # one line without bars
```

```text
token-burn  last poll 14:03:55
q quit  r refresh  daemon poll 5m

antigravity/antigravity-default
  claude and gpt   [────────────────────────]   0.0%
                   resets in 4h 59m · 0.0%/h · reset ~0%
  gemini           [────────────────────────]   0.2%
                   resets in 2h 51m · 0.0%/h · reset ~0%

claude/claude-default
  five hour        [█████▒▒▒▒▒▒─────────────]  20.0%
                   resets in 2h 46m · 9.3%/h · reset ~46% · reset first
  seven day        [██▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒]  10.0%
                   resets in 6d 12h · 1.8%/h · reset ~285% · 100% in 2d 3h

codex/codex-default  pro
  five hour        [█▒▒▒▒▒──────────────────]   3.0%
                   resets in 4h 24m · 5.2%/h · reset ~26% · reset first
  seven day        [███▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒]  12.0%
                   resets in 5d 9h · 0.7%/h · reset ~100% · 100% in 5d 9h

copilot/copilot-default  individual max
  ai credits       [█████████───────────────]  37.2%
                   resets in 11d 11h · 0.0%/h · reset ~37%
```

Bar legend:

- `█` current usage
- `▒` forecasted additional usage by reset
- `─` likely unused capacity

`reset ~N%` is the projected usage at reset. It can exceed `100%` when the
current burn rate would overshoot the quota before reset; the bar itself remains
capped at full.

Provider/account headers include a plan label only when the live provider
response exposes one. `token-burn` does not infer subscription names from local
logs or pricing tables.

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
poll_interval = "5m"
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

[[accounts]]
provider = "xai"
id = "xai-default"
```

## Authentication

`token-burn` does not run login flows or maintain a separate credential store.

Codex credentials are read from native Codex auth first:

- configured `auth_file`
- `$CODEX_HOME/auth.json`
- `~/.codex/auth.json`

If none is usable, `token-burn` falls back to Pi's `openai-codex` OAuth entry in
`${PI_CODING_AGENT_DIR:-~/.pi/agent}/auth.json`. Rotated Pi credentials are
persisted under Pi-compatible locking.

Claude credentials are read from:

- `CLAUDE_CODE_OAUTH_TOKEN`
- configured `credentials_file`
- `~/.claude/.credentials.json`
- macOS Keychain entry used by Claude Code

GitHub Copilot prefers the logged-in GitHub CLI and uses `gh api`. If that
request fails, it falls back to Pi's `github-copilot` OAuth entry. The GitHub
OAuth token is used only for provider-owned quota and billing endpoints; the
short-lived Copilot inference proxy token is not used for quota polling.

Google Antigravity credentials are read from existing Antigravity state stores
and, on macOS, the `agy` Keychain item. `token-burn` does not run a Google OAuth
login flow or write back to Antigravity credential stores. If the stored access
token is expired and OAuth client credentials are supplied via environment, it
uses the existing vendor refresh token to mint a short-lived access token and
caches only that access token under token-burn's own XDG cache.

xAI/Grok requires Pi subscription OAuth from `/login xai` (choose **Use a
subscription**). Credentials are read from configured `auth_file`, then
`${PI_CODING_AGENT_DIR:-~/.pi/agent}/auth.json`. `XAI_API_KEY` is not accepted:
xAI API billing is separate from Grok subscription quota. When needed,
`token-burn` refreshes Pi's OAuth entry and updates `auth.json` under Pi's shared
lock while preserving all other entries.

Secrets are treated as secrets. Authorization headers and obvious token/cookie
fields are redacted from diagnostics. Pi's per-request/session token counters
are not subscription quota and are never read; only provider-owned live usage
endpoints feed `token-burn`.

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
internal/provider/xai/  experimental live Grok subscription usage client
internal/piauth/         shared read/refresh-safe Pi auth.json access
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

- Windows viability check
- retention cleanup
- Homebrew formula/tap
- more provider shapes as they appear

More detail lives in [docs/ROADMAP.md](docs/ROADMAP.md).

## License

MIT. See [LICENSE](LICENSE).
