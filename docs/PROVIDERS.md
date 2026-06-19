# Provider Notes

## Authentication Policy

`token-burn` must not manage separate provider authentication.

Providers should read the same local credentials that the official coding tool
uses after the user has logged in normally. Missing or expired credentials should
produce clear errors that point back to the vendor login command, not a custom
token setup flow.

The implementation should stay cross-platform where the credential source is
cross-platform. Platform-specific credential lookup, such as macOS Keychain, must
be optional and isolated behind small source files.

## Usage Source Policy

Only provider-owned live usage APIs count as quota usage sources.

Do not infer subscription usage from transcripts, JSONL session files, local
token logs, pricing estimates, hooks, or statusline payloads. Those may be useful
for other tools, but they cannot reliably show account-level quota state across
multiple machines.

## Codex

### Endpoint

```text
GET https://chatgpt.com/backend-api/wham/usage
```

### Headers

```text
Authorization: Bearer <access_token>
ChatGPT-Account-Id: <account_id>   # optional but important for multi-account users
Accept: application/json
User-Agent: token-burn
```

### Credential Sources

- configured `auth_file`
- `$CODEX_HOME/auth.json`
- `~/.codex/auth.json`

### Response Shape

Relevant fields:

```json
{
  "plan_type": "plus",
  "rate_limit": {
    "primary_window": {
      "used_percent": 12,
      "limit_window_seconds": 18000,
      "reset_at": 1776111121
    },
    "secondary_window": {
      "used_percent": 2,
      "limit_window_seconds": 604800,
      "reset_at": 1776672455
    }
  },
  "code_review_rate_limit": null,
  "additional_rate_limits": [],
  "credits": {
    "has_credits": true,
    "unlimited": false,
    "balance": 50
  }
}
```

### Notes

- `18000` seconds means 5 hours.
- `604800` seconds means 7 days.
- The endpoint is not a public OpenAI Platform API and may change.
- Different OpenAI UI surfaces may disagree during reset propagation windows.

## Claude Code

### Endpoint

```text
GET https://api.anthropic.com/api/oauth/usage
```

### Headers

```text
Authorization: Bearer <oauth_access_token>
anthropic-beta: oauth-2025-04-20
Content-Type: application/json
User-Agent: token-burn
```

### Credential Sources

- configured credentials file
- `CLAUDE_CODE_OAUTH_TOKEN`
- `~/.claude/.credentials.json`
- macOS Keychain entry `Claude Code-credentials`

### Response Shape

Relevant fields:

```json
{
  "five_hour": {
    "utilization": 37.0,
    "resets_at": "2026-02-08T04:59:59.000000+00:00"
  },
  "seven_day": {
    "utilization": 26.0,
    "resets_at": "2026-02-12T14:59:59.771647+00:00"
  },
  "seven_day_opus": null,
  "seven_day_sonnet": {
    "utilization": 1.0,
    "resets_at": "2026-02-13T20:59:59.771655+00:00"
  },
  "extra_usage": {
    "is_enabled": false,
    "monthly_limit": null,
    "used_credits": null,
    "utilization": null
  }
}
```

### Notes

- `utilization` is already a percent.
- `resets_at` is a timestamp string.
- Claude Code statusline/hook payloads have had inconsistent availability for
  rate limit data, so the direct OAuth usage endpoint is preferred.

## GitHub Copilot

### Endpoints

```text
gh api /copilot_internal/user
gh api /users/<login>/settings/billing/ai_credit/usage?year=<yyyy>&month=<m>
```

The provider intentionally shells out to `gh api` instead of reading GitHub
tokens directly. This keeps authentication delegated to GitHub CLI and avoids
storing another token copy.

### Credential Sources

- logged-in GitHub CLI session from `gh auth login`

### Response Shape

Relevant fields from `/copilot_internal/user`:

```json
{
  "login": "octocat",
  "access_type_sku": "max_monthly_subscriber_quota",
  "copilot_plan": "individual_max",
  "quota_reset_date_utc": "2026-07-01T00:00:00.000Z",
  "token_based_billing": true,
  "quota_snapshots": {
    "premium_interactions": {
      "has_quota": true,
      "entitlement": 20000,
      "remaining": 15000,
      "percent_remaining": 75,
      "unlimited": false
    },
    "chat": {
      "has_quota": true,
      "unlimited": true
    }
  }
}
```

Relevant fields from GitHub AI Credits usage:

```json
{
  "usageItems": [
    {
      "product": "Copilot AI Credits",
      "sku": "AI Credit",
      "model": "GPT-5",
      "unitType": "ai-credits",
      "grossQuantity": 2000,
      "grossAmount": 20,
      "discountQuantity": 2000,
      "discountAmount": 20,
      "netQuantity": 0,
      "netAmount": 0
    }
  ]
}
```

### Notes

- `/copilot_internal/user` is not a stable public REST API and may change.
- GitHub's AI Credits usage endpoint is documented, but access still depends on
  the logged-in GitHub account and permissions.
- `token-burn` maps known individual plan allowances to a normalized monthly
  `ai_credits` window: Pro 1500, Pro+ 7000, Max 20000.
- The `ai_credits` window tracks included credit consumption from
  `grossQuantity`. `netQuantity` and `netAmount` represent additional billable
  usage after included credits or discounts.
- Chat and completions may be reported as unlimited. Those windows are kept at
  `0%` used when no finite entitlement is available.
- Billing usage failures are recorded as provider raw metadata, but do not block
  live quota windows from `/copilot_internal/user`.

## Google Antigravity

### Endpoint

```text
POST https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels
POST https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels
```

### Headers

```text
Authorization: Bearer <google_oauth_access_token>
Content-Type: application/json
Accept: application/json
User-Agent: antigravity
```

### Credential Sources

- Antigravity VS Code-compatible state database:
  `~/Library/Application Support/Antigravity IDE/User/globalStorage/state.vscdb`
- Antigravity app state database:
  `~/Library/Application Support/Antigravity/User/globalStorage/state.vscdb`
- macOS Keychain item used by `agy`: service `gemini`, account `antigravity`

`token-burn` does not start a Google OAuth login flow and does not write back to
Antigravity's state database or Keychain item. If the stored access token has
expired and OAuth client credentials are supplied via environment, it uses the
existing vendor refresh token to mint a short-lived access token through
Google's OAuth token endpoint, then stores only that access token in
token-burn's own XDG cache.

### State Database Token Shape

Antigravity stores OAuth state in `ItemTable` under key
`antigravityUnifiedStateSync.oauthToken`. The value is a double-wrapped base64
protobuf envelope with a sentinel string:

```text
oauthTokenInfoSentinelKey
```

The final `OAuthTokenInfo` protobuf includes an access token, refresh token, and
expiry timestamp. `token-burn` uses the refresh token only reactively when the
access token is expired or rejected.

### Token Refresh

```text
POST https://oauth2.googleapis.com/token
Content-Type: application/x-www-form-urlencoded

client_id=${TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID}
client_secret=${TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_SECRET}
refresh_token=<refresh_token>
grant_type=refresh_token
```

The refreshed access token is cached at:

```text
${XDG_CACHE_HOME:-~/.cache}/token-burn/antigravity-auth.json
```

### Response Shape

Relevant fields from `fetchAvailableModels`:

```json
{
  "models": {
    "gemini-3-pro": {
      "displayName": "Gemini 3 Pro",
      "model": "gemini-3-pro",
      "quotaInfo": {
        "remainingFraction": 0.8,
        "resetTime": "2026-06-26T10:00:00Z"
      }
    },
    "claude-sonnet": {
      "displayName": "Claude Sonnet 4.5",
      "model": "claude-sonnet",
      "quotaInfo": {
        "remainingFraction": 0.45,
        "resetTime": "2026-06-19T15:00:00Z"
      }
    }
  }
}
```

### Notes

- Antigravity quota is fraction-based. `used_percent = 100 -
  remainingFraction * 100`.
- `token-burn` normalizes returned model quotas into two pooled windows:
  `gemini` and `claude_and_gpt`.
- For each pool, the most constrained user-facing model drives the window.
- Internal, hidden, and known legacy Gemini 2.5/placeholder model rows are
  ignored.
- The local Antigravity/`agy` language-server quota summary can expose richer
  session-vs-weekly buckets, but this provider intentionally starts with the
  process-free OAuth-backed Cloud Code API.
