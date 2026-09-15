# PLAN.md — Claude-Keychain-Fallback + AGY-Refresh-Credentials

Stand: 2026-09-15. Umsetzung noch nicht begonnen. Zwei getrennte
Arbeitspakete, unabhängig voneinander umsetzbar.

Hintergrund (Diagnose vom 2026-09-15):

- **Claude**: token-burn liest nur die *erste* Credential-Quelle
  (`env → credentials_file → ~/.claude/.credentials.json → Keychain`).
  Eine abgelaufene `~/.claude/.credentials.json` (Refresh-Token bereits
  verbraucht → HTTP 400 `invalid_grant`) blockiert dadurch dauerhaft den
  gesunden, von Claude Code aktiv rotierten Keychain-Eintrag.
- **Antigravity**: Der eigene OAuth-Refresh ist vollständig implementiert
  (`Provider.refreshToken`, Grant gegen `https://oauth2.googleapis.com/token`,
  Ergebnis landet im token-burn-Cache `antigravity-auth.json`), scheitert
  aber im Daemon an fehlenden `TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID` /
  `TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_SECRET`. Die LaunchAgent-plist setzt
  nur `PATH`/`HOME`/`XDG_*`.

---

## Arbeitspaket A — AGY: Client-Credentials per Umgebungsvariablen in den Daemon

Ziel: Der eingebaute Refresh überbrückt die Lücken, in denen kein
Vendor-Tool (`agy`/IDE) den Token frischt. Gewählter Weg: Credentials als
Umgebungsvariablen in die LaunchAgent-plist (kein Runtime-Discovery, kein
`agy`-Aufruf durch den Daemon).

### A1 — Richtiges Client-Paar ermitteln

Die `agy`-Binary (`~/.local/bin/agy`, v1.2.3) bettet mindestens zwei
Google-Client-IDs und `GOCSPX-…`-Secrets ein. Es muss das Paar gefunden
werden, das zum Antigravity-Login gehört:

- Kandidaten extrahieren:
  `strings ~/.local/bin/agy | grep -E '[0-9]+-[a-z0-9]+\.apps\.googleusercontent\.com'`
  bzw. `grep GOCSPX-`.
- Zuordnung testen: einmaliger `refresh_token`-Grant gegen
  `https://oauth2.googleapis.com/token` mit dem vorhandenen Refresh-Token
  aus `~/.gemini/antigravity-cli/antigravity-oauth-token`. Google rotiert
  den Refresh-Token bei diesem Grant nicht — der Test ist gefahrlos;
  falsches Paar → `400 invalid_client`, richtiges Paar → neues
  Access-Token, Refresh-Token bleibt gültig.

### A2 — Config-Mechanik: `[service] env` in config.toml

Neuer optionaler Config-Abschnitt:

```toml
[service.env]
TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_ID = "…"
TOKEN_BURN_ANTIGRAVITY_OAUTH_CLIENT_SECRET = "…"
```

- `internal/config`: `ServiceConfig` mit `Env map[string]string`
  parsen; Default leer; Validierung: Keys müssen `^[A-Z_][A-Z0-9_]*$`
  folgen (keine heimlichen Werte-Injektionen über PID-Mappings o. Ä.).
- `internal/cli` (`installSpec`): Werte in `service.Spec` übernehmen.

### A3 — Service-Plumbing: zusätzliche EnvironmentVariables in die Units

- `internal/service`: `Spec` um `ExtraEnv map[string]string` erweitern.
- `LaunchAgentPlist`: zusätzliche Einträge im
  `EnvironmentVariables`-Dict (nach den vorhandenen PATH/HOME/XDG-*),
  deterministisch sortiert (Teststabilität), XML-escaping wie gehabt.
- Die plist wird bereits mit `0600` geschrieben — Secrets dort sind
  akzeptabel (user-only). Das bleibt so, kein weiterer Schutz nötig,
  aber dokumentieren.
- `service_systemd.go`: parity — `Environment=KEY=value`-Zeilen in die
  User-Unit schreiben (Linux ist zweitrangig, aber nicht vergessen).
- Daemon-seitig ist nichts zu ändern: der Daemon erbt das Environment,
  der Provider liest es über `p.env()` zum Fetch-Zeitpunkt.

### A4 — Sicherheit / Hygiene

- Extrahierte Werte niemals ins Repo committen (auch nicht in
  Test-Fixtures, Logs oder Docs).
- `docs/PROVIDERS.md` (Antigravity-Abschnitt) ergänzen: Woher die Werte
  kommen (aus der `agy`-Binary extrahieren, Befehl angeben — ohne
  konkrete Werte), wie sie konfiguriert werden, und dass der Daemon
  einen Neustart (`token-burn install` + Restart) braucht, um sie zu
  sehen.

### A5 — Tests

- `config`: TOML mit `[service.env]` parsen, Default-Verhalten ohne
  Abschnitt, ungültige Keys abgelehnt.
- `service`: plist enthält sortierte Extra-Env-Einträge; ohne Abschnitt
  unverändertes Dict; systemd-Unit-Parity.
- `cli`: `installSpec` reicht Config-Werte an `Spec` weiter.

### A6 — Verifikation

1. `go test ./...`
2. Werte in `~/.config/token-burn/config.toml` eintragen,
   `token-burn install`, Daemon-Neustart.
3. Lücken-Test: eine Stunde (+ Skew) keine `agy`-Nutzung; danach in
   `poll_runs` prüfen, dass `antigravity` weiter `success` liefert
   (früher: `auth_expired` ab Token-Expiry + ~15 Min). Im token-burn
   Cache (`antigravity-auth.json`, Quelle `token_burn_cache`) landet
   der Refresh-Ergebnis-Token.

---

## Arbeitspaket B — Claude: Candidate-Loop über alle Credential-Quellen

Ziel: Keine einzelne tote Quelle darf den Provider blockieren. Gleiche
Architektur wie Antigravity (`tokenCandidates` + Fallback-Schleife).

### B1 — `resolveCredential` → Kandidatensammlung

- Kandidaten sammeln statt first-match-only:
  1. `CLAUDE_CODE_OAUTH_TOKEN` (env; explizit, kein Refresh möglich —
     bleibt Short-Circuit wie heute),
  2. konfigurierte `credentials_file`,
  3. `~/.claude/.credentials.json`,
  4. macOS Keychain (`Claude Code-credentials`).
- Sortierung nach Sammlung: env zuerst; alle anderen nach `expiresAt`
  **absteigend** (frischeste zuerst). Damit schlägt der von Claude Code
  rotierte Keychain-Eintrag die abgelaufene Datei automatisch, ohne
  dass Quellen hart umsortiert werden.
- Lesefehler einer Quelle (Keychain-Deny, kaputtes JSON) sind kein
  Fatal-Fehler mehr: Quelle überspringen, nächste probieren; erst wenn
  *gar nichts* lesbar ist → `ErrAuthMissing`.

### B2 — Auswertungsschleife im Fetch

Pro Kandidat, in Sortierfolge:

1. Access-Token gültig (`!needsRefresh`) → direkt `mapUsageResponse`
   versuchen.
2. Sonst Refresh (`requestRefresh`); bei Erfolg Rotation quellentreu
   zurückschreiben (`persistCredential` — Datei → Datei, Keychain →
   Keychain; Sibling-Keys wie `mcpOAuth` bleiben erhalten, Mechanik
   existiert bereits) und Usage abrufen.
3. Bei `invalid_grant` / HTTP 400 / 401: Kandidat als tot markieren,
   **nächsten** Kandidaten probieren. Max. ein Versuch pro Kandidat pro
   Poll — keine Retry-Schleifen.
4. Direkter Usage-Call mit 401 → gleiche Behandlung (Refresh, dann
   nächster Kandidat).
5. Alle Kandidaten tot/leer → `ErrAuthExpired` mit Hinweis, welche
   Quellen geprüft wurden (ohne Token-Werte).

### B3 — Bekanntes Restrisiko dokumentieren

Der Keychain-Refresh-Token ist Single-Use und wird von laufenden
Claude-Code-Sessions ebenfalls rotiert. token-burn und Claude Code
konkurrieren im Gleichzeitigkeitsfall um denselben Token; der jeweils
andere liest vor dem nächsten Refresh den neuen Stand, aber ein
gelegentlicher 400 beim exakten Gleichzeitigkeitsfall bleibt möglich
(dann nächster Poll wieder OK). Das gehört in `docs/PROVIDERS.md`.

### B4 — Tests (table-driven, `httptest.NewServer`, vorhandene Muster)

- Tote Datei + frischer Keychain → Keychain gewinnt, Datei bleibt
  unangetastet.
- Frische Datei → Keychain wird nicht angerührt (Kein-Write-Check).
- Refresh mit 400 → fällt zum nächsten Kandidaten durch und ist dort
  erfolgreich.
- Alle Quellen tot → `ErrAuthExpired`, alle Quellen in der Meldung.
- env-Token → Short-Circuit, keine weiteren Quellen gelesen.
- Rotation landet in der richtigen Quelle (Datei-Mtime bzw.
  Keychain-Write-Stub), Sibling-Keys erhalten.
- Keychain-Parsetfehler → Fallback zur nächsten Quelle, kein Fatal.

### B5 — Docs

- `docs/PROVIDERS.md`: neue Such-/Auswertungsreihenfolge, Verhalten bei
  toter Quelle, Restrisiko Single-Use-Race.
- `README.md`: ein Satz im Claude-Abschnitt.

---

## Rollout (beide Pakete)

1. `go test ./...`, `gofmt`, `go vet`.
2. Lokal installieren: `bin/token-burn install`, Daemon-Neustart.
3. Live-Check `token-burn once --verbose`: claude + antigravity grün.
4. Beobachtung über mindestens eine Token-Lücke (AGY: >1 h ohne
   Vendor-Nutzung; Claude: nächster Claude-Code-Rotationszeitpunkt).
5. Commit(s) getrennt je Arbeitspaket; Version-Bump/Release separat,
   wenn beide verifiziert sind.

## Explizit nicht Teil dieses Plans

- Runtime-Discovery der Credentials aus `Antigravity.app`/`agy`-Binary
  (CodexBar-Stil) — nur manuelle Extraktion + Config.
- Daemon-seitiges Anstoßen von `agy -p /usage`.
- Änderungen an xai/codex/copilot/zai-Providern.
