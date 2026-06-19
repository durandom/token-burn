# Security

`token-burn` reads local provider authentication artifacts created by vendor
tools such as Codex and Claude Code. It does not implement its own OAuth flow
or store provider credentials.

## Reporting

Please report security issues privately through GitHub security advisories if
available, or contact the maintainer directly before opening a public issue.

## Secrets Policy

The project should never log or export:

- access tokens
- refresh tokens
- ID tokens
- authorization headers
- cookies
- raw session material

Raw provider JSON is not stored by default. If diagnostic raw JSON storage is
enabled later, obvious token and cookie fields must be redacted first.

OpenTelemetry attributes should avoid account email addresses and other
high-cardinality identifiers by default.
