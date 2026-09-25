package migrations

func init() {
	// API tokens (#31): scoped, expiring bearer tokens of users, and the
	// instance-wide token settings on the security policy.
	//
	// Invariants enforced by the schema:
	//   - only a verifier (SHA-256 of the 256-bit secret) is stored, unique;
	//   - a token belongs to one user for its whole life and dies with the
	//     account (ON DELETE CASCADE); its owner, verifier, creation time,
	//     expiry and scope never change (triggers), only its name, the
	//     last-use record and the revocation;
	//   - a revocation is final (it cannot be cleared or changed) and always
	//     has a reason;
	//   - scopes are allow grants of one capability at one scope, one row
	//     per (token, capability, scope): no duplicates.
	Migrations.MustRegister(
		Tx(Exec(
			`CREATE TABLE api_tokens (
				id             TEXT PRIMARY KEY,
				user_id        TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
				name           TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 64),
				verifier       TEXT NOT NULL UNIQUE CHECK (length(verifier) = 64),
				created_at     TEXT NOT NULL,
				expires_at     TEXT,
				last_used_at   TEXT,
				last_used_ip   TEXT NOT NULL DEFAULT '',
				revoked_at     TEXT,
				revoked_by     TEXT NOT NULL DEFAULT '',
				revoked_reason TEXT CHECK (revoked_reason IN ('user', 'owner', 'user_disabled', 'credential_reset', 'restore')),
				CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
			) STRICT`,
			`CREATE INDEX api_tokens_user ON api_tokens (user_id)`,
			`CREATE TRIGGER api_tokens_identity_fixed BEFORE UPDATE OF id, user_id, verifier, created_at, expires_at ON api_tokens
			BEGIN SELECT RAISE(ABORT, 'the owner, secret, creation time and expiry of an API token never change'); END`,
			`CREATE TRIGGER api_tokens_revocation_final BEFORE UPDATE OF revoked_at, revoked_by, revoked_reason ON api_tokens
			WHEN OLD.revoked_at IS NOT NULL
			BEGIN SELECT RAISE(ABORT, 'an API token revocation is final'); END`,
			`CREATE TABLE api_token_scopes (
				token_id       TEXT    NOT NULL REFERENCES api_tokens (id) ON DELETE CASCADE,
				capability     TEXT    NOT NULL CHECK (length(capability) BETWEEN 3 AND 128),
				scope_kind     TEXT    NOT NULL CHECK (scope_kind IN ('instance', 'environment', 'resource')),
				environment_id TEXT    NOT NULL DEFAULT '',
				resource_type  TEXT    NOT NULL DEFAULT '',
				resource_id    TEXT    NOT NULL DEFAULT '',
				position       INTEGER NOT NULL,
				PRIMARY KEY (token_id, capability, scope_kind, environment_id, resource_type, resource_id)
			) STRICT`,
			`CREATE TRIGGER api_token_scopes_fixed BEFORE UPDATE ON api_token_scopes
			BEGIN SELECT RAISE(ABORT, 'the scope of an API token never changes'); END`,
			`ALTER TABLE security_settings ADD COLUMN api_tokens_enabled INTEGER NOT NULL DEFAULT 1 CHECK (api_tokens_enabled IN (0, 1))`,
			`ALTER TABLE security_settings ADD COLUMN api_token_max_days INTEGER NOT NULL DEFAULT 90 CHECK (api_token_max_days BETWEEN 1 AND 3650)`,
			`ALTER TABLE security_settings ADD COLUMN api_tokens_non_expiring INTEGER NOT NULL DEFAULT 0 CHECK (api_tokens_non_expiring IN (0, 1))`,
		)),
		Tx(Exec(
			`ALTER TABLE security_settings DROP COLUMN api_tokens_non_expiring`,
			`ALTER TABLE security_settings DROP COLUMN api_token_max_days`,
			`ALTER TABLE security_settings DROP COLUMN api_tokens_enabled`,
			`DROP TRIGGER api_token_scopes_fixed`,
			`DROP TABLE api_token_scopes`,
			`DROP TRIGGER api_tokens_revocation_final`,
			`DROP TRIGGER api_tokens_identity_fixed`,
			`DROP TABLE api_tokens`,
		)),
	)
}
