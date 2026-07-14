-- Auth group: sessions and rate-limit counters.
--
-- Both are tables because there is no Redis and neither needs one: a session is a KV blob with a
-- TTL, and a rate-limit counter is a row you increment atomically.

-- +goose Up

-- ── sessions ────────────────────────────────────────────────────────────────────
CREATE TABLE sessions (
    -- sha256(session id), never the id itself. Same rule as api_tokens: a database dump must not be
    -- a bag of live credentials, so the server holds only the digest and the cookie holds the secret.
    id_hash    bytea       PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- sha256 of the user's password hash AT ISSUE. Every request compares it against the current
    -- one, so changing a password invalidates every other session for free — no revocation list, no
    -- fan-out delete, and no window in which a stolen cookie outlives the password it was minted
    -- against.
    pw_fingerprint bytea    NOT NULL,

    -- The CSRF nonce for this session. Cookie auth only; token auth is CSRF-exempt because a bearer
    -- token is not something a browser attaches by itself.
    csrf_token text        NOT NULL,

    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    last_seen  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_idx   ON sessions (user_id);     -- "log me out everywhere"
CREATE INDEX sessions_expiry_idx ON sessions (expires_at);  -- the reaper

-- ── rate limits ─────────────────────────────────────────────────────────────────
-- The counter is a row and the increment is INSERT … ON CONFLICT DO UPDATE SET n = n + 1 RETURNING
-- n, which is atomic on Postgres by construction — a limiter that loses increments is a limiter
-- that silently stops limiting. The window is part of the key, so a new window is a new row rather
-- than a reset someone has to remember to perform.
CREATE TABLE rate_limits (
    bucket       text        NOT NULL,   -- "login:ip:1.2.3.4", "submit:acct:42", …
    window_start timestamptz NOT NULL,
    n            int         NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, window_start)
);
CREATE INDEX rate_limits_window_idx ON rate_limits (window_start);  -- the reaper

-- +goose Down
DROP TABLE rate_limits;
DROP TABLE sessions;
