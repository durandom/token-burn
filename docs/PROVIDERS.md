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
