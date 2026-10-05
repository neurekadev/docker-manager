# Database migrations

Binding conventions (split out of CLAUDE.md). Read this file when your change touches this area.

- New file `internal/db/migrations/<UTC YYYYMMDDHHMMSS>_<snake_name>.go`.
- In `init()`: `Migrations.MustRegister(Tx(up), Tx(down))` — call it directly
  (Bun derives the name from the caller's file name). `Exec(stmts...)` helps.
- Raw SQL, `STRICT` tables (`INTEGER`/`TEXT`/`REAL`/`BLOB`), timestamps as
  `TEXT` via Bun `time.Time` (UTC), IDs as `TEXT` UUIDv7 from `ids.New()`.
- Never edit a migration merged to `main`; add a new one. The one
  exception is a migration that aborts on the databases it would change
  (it never applied there): fix it in place (#286).
- Scope rows of API tokens never change (trigger `api_token_scopes_fixed`):
  a migration replaces them with INSERT and DELETE, never UPDATE.
