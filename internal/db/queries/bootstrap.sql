-- The console bootstrap: the one path from an empty database to a playable instance.
--
-- Two rows make an instance live — the instance singleton (which the scoring SQL keys on) and
-- config.setup (which the route policy gates every request on, including /login). They are written
-- in the same transaction as the first admin, so a live instance always has an admin and an
-- instance with an admin is always live.

-- name: EnsureInstance :one
-- The singleton, created once and never rewritten: user_mode is fixed at setup, and the immutability
-- trigger says so. ON CONFLICT makes a re-run idempotent and hands back the mode actually in force —
-- the no-op SET exists only so RETURNING fires on the conflict path, and it re-asserts the same
-- user_mode so the trigger stays satisfied.
INSERT INTO instance (id, user_mode, version)
VALUES (true, @user_mode, @version)
ON CONFLICT (id) DO UPDATE
   SET user_mode = instance.user_mode
RETURNING user_mode;

-- name: MarkSetupComplete :exec
-- Flips the instance live. Until this row says "true" the policy gate denies every route with
-- setup-incomplete, so a fresh install has no front door. Idempotent: re-running the bootstrap on a
-- live instance rewrites the same value.
INSERT INTO config (key, value)
VALUES ('setup', 'true')
ON CONFLICT (key) DO UPDATE
   SET value = 'true', updated_at = now();
