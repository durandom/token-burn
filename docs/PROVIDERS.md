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

An explicit configured `auth_file` takes precedence and is auto-detected as
native Codex or Pi format. Without one, native Codex credentials are preferred:

- `$CODEX_HOME/auth.json`
- `~/.codex/auth.json`

If none is usable, the provider reads Pi's `openai-codex` OAuth entry from
`${PI_CODING_AGENT_DIR:-~/.pi/agent}/auth.json`.
Near-expiry and HTTP 401/403 credentials are refreshed with Pi's OpenAI OAuth
contract and persisted under the shared Pi lock while preserving unrelated and
unknown fields.

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

Probed in order:

1. `CLAUDE_CODE_OAUTH_TOKEN`
2. configured credentials file
3. `~/.claude/.credentials.json`
4. macOS Keychain entry `Claude Code-credentials`

The stored credential is a JSON container whose Claude login lives under
`claudeAiOauth`:

```json
{
  "claudeAiOauth": {
    "accessToken": "sk-ant-oat...",
    "refreshToken": "sk-ant-ort...",
    "expiresAt": 1787165038813,
    "subscriptionType": "max"
  },
  "mcpOAuth": {
    "<server>|<hash>": { "accessToken": "...", "refreshToken": "..." }
  }
}
```

The `accessToken` field is read by exact key from `claudeAiOauth` only. This is
load-bearing: Claude Code stores every OAuth-authenticated MCP server login in
the same container under `mcpOAuth`, each with its own `accessToken`. Searching
the container recursively for "some key named accessToken" returns an unrelated
MCP token, which the usage endpoint rejects with HTTP 401 — and because Go
randomizes map iteration, it does so intermittently rather than consistently.

### Token Refresh

```text
POST https://platform.claude.com/v1/oauth/token
grant_type=refresh_token&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&refresh_token=<token>
```

The endpoint and client ID were confirmed against the live service: an unknown
`client_id` answers `400 invalid_request_error` with "Client with id ... not
found", while this one gets past client validation. Both are undocumented and
may change.

A `400` is only treated as a dead login when the body says `invalid_grant` or
names the refresh token. Any other `400` - an unrecognised `client_id`, a
malformed request - is token-burn's problem, not the user's, and is reported as
a transient failure so nobody is sent to re-authenticate for a bug on this side.
The endpoint also rate-limits: a `429` is surfaced as `rate_limited` so the
daemon backs off.

The access token lives roughly twelve hours. Claude Code renews it while it is
running, so token-burn only needs to refresh when Claude Code has been idle
longer than that. A refresh is attempted five minutes ahead of `expiresAt`, and
once more if a poll is rejected with 401.

Rotations are written back to the source they came from. This is mandatory, not
an optimisation: Anthropic rotates the refresh token on every exchange and
invalidates the previous one, so a refresh that is not persisted would leave
Claude Code holding a dead refresh token. Two constraints follow:

- The write must preserve every sibling key, `mcpOAuth` included. Re-serialising
  only the fields token-burn models would sign the user out of every MCP server.
- The source is re-read and compared immediately before the write, and the write
  is abandoned if Claude Code changed it in the meantime. Neither backend offers
  a compare-and-swap, so this narrows the race rather than closing it. Losing it
  costs one redundant refresh, not a broken login.

On macOS the secret is passed to `security add-generic-password -U` through the
`-w` argument, not stdin: a stdin-prompted password is truncated at 128 bytes,
which would replace the multi-kilobyte container with a fragment. Every write is
read back and verified, and a failed write restores the previous content.

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
- `extra_usage` is additional/overage usage, not a normal reset quota bucket.
  It is only mapped when Claude reports it as enabled or returns concrete
  extra usage accounting fields.
- Claude Code statusline/hook payloads have had inconsistent availability for
  rate limit data, so the direct OAuth usage endpoint is preferred.

## GitHub Copilot

### Endpoints

```text
gh api /copilot_internal/user
gh api /users/<login>/settings/billing/ai_credit/usage?year=<yyyy>&month=<m>
```

The provider prefers `gh api`, keeping normal authentication delegated to
GitHub CLI. If the user request fails, it reads Pi's `github-copilot` OAuth
entry and calls the same GitHub REST endpoints directly.

### Credential Sources

- logged-in GitHub CLI session from `gh auth login` (preferred)
- configured Pi `auth_file`
- `${PI_CODING_AGENT_DIR:-~/.pi/agent}/auth.json`

Pi stores both a short-lived Copilot inference proxy token in `access` and the
GitHub OAuth token in `refresh`. Quota polling intentionally uses `refresh` for
GitHub REST; it does not use or refresh the inference proxy token. Pi credentials
with `enterpriseUrl` are never sent to public `api.github.com`; GitHub Enterprise
accounts require the preferred `gh` path so host routing remains delegated to
GitHub CLI.

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
- The provider also preserves locally returned subscription/quota metadata such
  as `copilot_plan`, `access_type_sku`, and per-window `entitlement`,
  `remaining`, `percent_remaining`, and `unlimited` flags. These are stored as
  raw diagnostic metadata when raw storage/output is enabled; they are not
  guessed from pricing pages.
- The `ai_credits` window tracks included credit consumption from
  `grossQuantity`. `netQuantity` and `netAmount` represent additional billable
  usage after included credits or discounts.
- Chat and completions may be reported as unlimited. Those windows are kept at
  `0%` used when no finite entitlement is available.
- `has_quota: false` on a quota snapshot is GitHub's own "blocked, no quota
  left" signal. It is surfaced as a 100%-used, `limit_reached` window rather
  than dropped, and it caps the `ai_credits` window at 100% used even when the
  hardcoded allowance table would otherwise compute lower usage.
- Billing usage failures are recorded as provider raw metadata, but do not block
  live quota windows from `/copilot_internal/user`.
- Pi per-request and session token counters are not read. They describe local
  inference calls, not provider-reported account subscription quota.

## xAI/Grok Subscription (Experimental)

### Endpoints

```text
GET https://cli-chat-proxy.grok.com/v1/user
GET https://cli-chat-proxy.grok.com/v1/billing?format=credits
```

These are undocumented Grok CLI proxy endpoints, not supported xAI public APIs.
The provider requests identity first, uses the returned `userId` only as a
transient header for billing, and never stores it.

### Headers

```text
Authorization: Bearer <pi_xai_oauth_access_token>
X-XAI-Token-Auth: xai-grok-cli
x-grok-client-version: <reviewed_semver>
x-grok-client-mode: headless
x-userid: <transient_user_id> # billing request only
```

Redirects are rejected and response bodies are bounded. Contract changes may
break this provider without notice.

### Credential Source and Refresh

- configured `auth_file`
- `${PI_CODING_AGENT_DIR}/auth.json`
- `~/.pi/agent/auth.json`

Only Pi's `xai` credential with `type = "oauth"` is accepted. Run `/login xai`
in Pi and choose **Use a subscription**. API-key credentials and `XAI_API_KEY`
are intentionally rejected because xAI API billing is separate from Grok/X
subscription usage.

Access tokens are refreshed proactively at Pi's stored expiry and once after an
HTTP 401/403. Refresh uses Pi's xAI OAuth client and existing refresh token:

```text
POST https://auth.x.ai/oauth2/token
Content-Type: application/x-www-form-urlencoded

grant_type=refresh_token
client_id=b1a00492-073a-47ea-816f-4c329264a828
refresh_token=<pi_refresh_token>
```

The refreshed credential replaces only the `xai` fields in Pi's `auth.json`.
Existing auth symlinks are canonicalized before locking. Writes preserve
unrelated providers and unknown fields, use mode `0600`, and share Pi's
`auth.json.lock` proper-lockfile convention. Lock acquisition is bounded;
heartbeats use the owned directory descriptor, and detected ownership loss
cancels refresh before persistence. A refresh
token omitted by xAI is preserved, matching Pi.

### Response Shape

Relevant current fields:

```json
{
  "subscriptionTier": "supergrok",
  "onDemandEnabled": true,
  "config": {
    "creditUsagePercent": 25,
    "currentPeriod": {
      "type": "USAGE_PERIOD_TYPE_WEEKLY",
      "start": "2026-07-29T13:00:00Z",
      "end": "2026-08-05T13:00:00Z"
    },
    "isUnifiedBillingUser": true,
    "onDemandCap": { "val": 1000 },
    "onDemandUsed": { "val": 250 },
    "prepaidBalance": { "val": 500 }
  }
}
```

An explicitly weekly current period maps to `weekly`; an explicitly monthly
period maps to `monthly`. Modern responses with an absent or unknown period type
use neutral `quota`. Only verified legacy responses with `used`, `monthlyLimit`,
and `billingPeriodEnd` default to `monthly`. Currency wrappers are integer cents
and are kept only as whitelisted diagnostic metadata.

## Subscription Metadata

`token-burn` only records plan labels that are present in provider-owned live
responses or vendor-owned local state:

- Codex currently exposes `plan_type` through `wham/usage`.
- GitHub Copilot exposes `copilot_plan`, `access_type_sku`, and quota
  entitlements through `/copilot_internal/user`.
- Claude Code's OAuth usage endpoint currently exposes usage buckets and reset
  times, but no verified plan label.
- Google Antigravity exposes a verified subscription label (`paidTier.name`,
  e.g. `Google AI Pro`) through `loadCodeAssist`.
- xAI/Grok exposes `subscriptionTier` when present in its experimental billing
  response.

## Google Antigravity

### Endpoint

```text
POST https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist
POST https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist
POST https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary
POST https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary
POST https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels
POST https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels
```

`token-burn` first calls `loadCodeAssist` to resolve the account's Cloud
project ID (`cloudaicompanionProject`) and plan (`paidTier.name`), then calls
`retrieveUserQuotaSummary` with that project ID for the real per-tier
five-hour and weekly windows. `fetchAvailableModels` is kept only as a
fallback for accounts/builds where the quota-summary endpoint isn't
available — see Notes below for why it isn't trusted as the primary source.

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

Relevant fields from `loadCodeAssist`:

```json
{
  "cloudaicompanionProject": "round-yew-rtd9f",
  "currentTier": { "id": "free-tier" },
  "paidTier": { "id": "g1-pro-tier", "name": "Google AI Pro" }
}
```

Relevant fields from `retrieveUserQuotaSummary` (request body
`{"project": "<cloudaicompanionProject>"}`):

```json
{
  "groups": [
    {
      "displayName": "Gemini Models",
      "description": "Models within this group: Gemini Flash, Gemini Pro",
      "buckets": [
        {
          "bucketId": "gemini-weekly",
          "window": "weekly",
          "resetTime": "2026-08-29T13:49:38Z",
          "description": "You have hit your 5-hour limit, so the weekly limit does not currently apply.",
          "remainingFraction": 0.5140309
        },
        {
          "bucketId": "gemini-5h",
          "window": "5h",
          "resetTime": "2026-08-23T12:06:27Z",
          "description": "You have hit your 5-hour limit, it will refresh in 2 hours, 22 minutes.",
          "remainingFraction": 0
        }
      ]
    },
    {
      "displayName": "Claude and GPT models",
      "description": "Models within this group: Claude Opus, Claude Sonnet, GPT-OSS",
      "buckets": [
        { "bucketId": "3p-weekly", "window": "weekly", "resetTime": "2026-08-30T09:44:05Z", "remainingFraction": 1 },
        { "bucketId": "3p-5h", "window": "5h", "resetTime": "2026-08-23T14:44:05Z", "remainingFraction": 1 }
      ]
    }
  ]
}
```

Relevant fields from the legacy `fetchAvailableModels` fallback:

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
    }
  }
}
```

### Notes

- Antigravity quota is fraction-based. `used_percent = 100 -
  remainingFraction * 100`.
- `token-burn` maps each `retrieveUserQuotaSummary` group to a pool
  (`gemini` or `claude_and_gpt` — matched by `displayName`) and each bucket to
  a window: the `5h` bucket keeps the bare pool name (`gemini`,
  `claude_and_gpt`) for continuity with prior history, and the `weekly`
  bucket gets a `_weekly` suffix (`gemini_weekly`, `claude_and_gpt_weekly`).
  Each bucket's human-readable `description` is stored as
  `<window>_status` raw diagnostic metadata.
- **Why not `fetchAvailableModels` alone**: it only reports a coarse
  per-model fraction with no weekly data, and once a pool is actually
  blocked it tends to stop reporting real fractions for that pool's models
  (`remainingFraction: null`) rather than reporting them as exhausted —
  confirmed against a real "Individual quota reached" block, where every
  model in the blocked pool went to `null` except a few untouched/preview
  models that reported a flat, misleadingly full `1.0` with a `resetTime`
  that kept sliding to "now + 5h" on every poll instead of counting down to
  a fixed point. `retrieveUserQuotaSummary` reported the same block
  correctly: `gemini-5h` at `remainingFraction: 0` with a real, fixed
  `resetTime` and the exact human-readable reason. Two independent
  reference implementations ([OpenUsage](https://github.com/robinebers/openusage/blob/main/docs/providers/antigravity.md),
  [CodexBar](https://github.com/steipete/CodexBar/blob/main/docs/antigravity.md))
  document this exact "all-100%, sliding reset" pattern as a known,
  named failure mode of the legacy endpoint.
- Internal, hidden, and known legacy Gemini 2.5/placeholder model rows are
  ignored in the `fetchAvailableModels` fallback path.
- The local Antigravity/`agy` language-server exposes the same
  `RetrieveUserQuotaSummary` payload over a local HTTPS port while the app or
  CLI process is running. `token-burn` intentionally stays process-free and
  calls the remote Cloud Code copy of the same endpoint instead, so no local
  process discovery or port scanning is required.
