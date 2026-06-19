# Contributing

Thanks for taking a look at `token-burn`.

This project is intentionally small. Contributions should preserve the core
shape:

- provider live usage is the source of truth
- no local transcript/session/token-log inference by default
- no custom provider authentication management
- secrets must never be logged or exported
- SQLite stays the local history store
- OpenTelemetry is the external observability path

## Development

```sh
go test ./...
go build -o bin/token-burn ./cmd/token-burn
```

Prefer small, focused changes with tests.

Provider HTTP tests should use `httptest.NewServer`. SQLite tests should use
`t.TempDir`.

## Security-sensitive Changes

Be especially careful around:

- provider credential lookup
- OAuth/token parsing
- logs and diagnostic output
- raw provider JSON storage
- OpenTelemetry attributes

Account email addresses and raw provider tokens should not be exported by
default.

## Provider Notes

The live usage endpoints used here are not stable public APIs. When updating a
provider parser, prefer tolerant decoding and keep the normalized provider model
small.
