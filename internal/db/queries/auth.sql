-- Authentication: sessions, API tokens, and the one query that resolves a caller to a Principal.

-- name: LoadPrincipal :one
-- THE authentication query. Sessions and API tokens both converge here before any
-- authorization runs, so a token cannot route around a wall a cookie hits.
--
-- Deliberately absent: `hidden`. A hidden account authenticates and plays normally —
-- hiddenness is a scoreboard predicate, never an auth decision.
--
-- `profile_complete` is the required-fields gate. NOT EXISTS over the missing entries,
-- so the answer is a boolean and not a count the caller has to interpret.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT
    u.id                AS user_id,
    u.team_id,
    u.role = 'admin'    AS is_admin,
    u.verified,
    u.banned,
    u.must_change_password,
    COALESCE(t.banned, false) AS team_banned,
    COALESCE((SELECT m.user_mode = 'teams' FROM mode m) AND u.team_id IS NULL, false)::boolean AS teamless,
    NOT EXISTS (
        SELECT 1 FROM fields f
         WHERE f.applies_to = 'user' AND f.required
           AND NOT EXISTS (
               SELECT 1 FROM field_entries fe
                WHERE fe.field_id = f.id AND fe.user_id = u.id
                  AND fe.value IS NOT NULL
                  AND fe.value <> 'null'::jsonb AND fe.value <> '""'::jsonb
           )
    ) AS profile_complete,
    NOT EXISTS (
        SELECT 1 FROM fields f
         WHERE f.applies_to = 'team' AND f.required AND u.team_id IS NOT NULL
           AND NOT EXISTS (
               SELECT 1 FROM field_entries fe
                WHERE fe.field_id = f.id AND fe.team_id = u.team_id
                  AND fe.value IS NOT NULL
                  AND fe.value <> 'null'::jsonb AND fe.value <> '""'::jsonb
           )
    ) AS team_profile_complete
  FROM users u
  LEFT JOIN teams t ON t.id = u.team_id
 WHERE u.id = @user_id;

-- ── sessions ────────────────────────────────────────────────────────────────────

-- name: CreateSession :one
INSERT INTO sessions (id_hash, user_id, pw_fingerprint, csrf_token, expires_at)
VALUES (@id_hash, @user_id, @pw_fingerprint, @csrf_token, @expires_at)
RETURNING id_hash, user_id, csrf_token, expires_at;

-- name: GetSession :one
-- Expiry is a WHERE clause, not a Go comparison: an expired session must be indistinguishable from
-- a missing one, and it must be so at the only place that can be tricked into disagreeing — the
-- database. `now()` is the transaction's clock, not the app server's, so a skewed pod cannot extend
-- a session.
--
-- pw_fingerprint comes back so the caller can compare it against the user's current password hash.
-- A mismatch means the password changed after this session was minted, and the session is dead.
SELECT s.id_hash, s.user_id, s.pw_fingerprint, s.csrf_token, s.expires_at,
       u.password_hash
  FROM sessions s
  JOIN users u ON u.id = s.user_id
 WHERE s.id_hash = @id_hash
   AND s.expires_at > now();

-- name: TouchSession :exec
UPDATE sessions SET last_seen = now() WHERE id_hash = @id_hash;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id_hash = @id_hash;

-- name: DeleteUserSessions :exec
-- "Log out everywhere". Also what a ban should call — a banned user's live cookie is still a live
-- cookie, and the ban wall stops them on the next request, but there is no reason to leave the door
-- shut and unlocked.
DELETE FROM sessions WHERE user_id = @user_id;

-- name: DeleteTeamSessions :exec
-- A team ban's session sweep. The ban wall stops every member on their next request even
-- without this — the wall reads team_banned per request — but a live cookie on a banned
-- team is still a door left unlocked.
DELETE FROM sessions
 WHERE user_id IN (SELECT id FROM users WHERE team_id = @team_id);

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= now();

-- ── api tokens ──────────────────────────────────────────────────────────────────

-- name: CreateAPIToken :one
INSERT INTO api_tokens (user_id, token_hash, description, expires_at)
VALUES (@user_id, @token_hash, @description, @expires_at)
RETURNING id, user_id, description, created_at, expires_at;

-- name: GetAPIToken :one
-- Looked up by sha256(token); the plaintext is shown once, at creation, and never stored —
-- a table of plaintext secrets turns every dump or export into a bearer-credential leak.
-- Expiry is enforced here, in the WHERE clause, for the same reason as sessions.
SELECT id, user_id, description, created_at, expires_at
  FROM api_tokens
 WHERE token_hash = @token_hash
   AND expires_at > now();

-- name: ListAPITokens :many
SELECT id, user_id, description, created_at, expires_at
  FROM api_tokens WHERE user_id = @user_id ORDER BY created_at DESC;

-- name: DeleteAPIToken :execrows
-- Scoped by user_id as well as id: ownership is enforced in the WHERE clause rather than by a
-- SELECT-then-check in Go, so there is no window and no forgotten guard. Zero rows affected means
-- "not yours, or not there", and the caller cannot tell the difference — which is correct.
DELETE FROM api_tokens WHERE id = @id AND user_id = @user_id;

-- name: DeleteExpiredAPITokens :exec
DELETE FROM api_tokens WHERE expires_at <= now();

-- ── credentials ─────────────────────────────────────────────────────────────────

-- name: GetUserByEmail :one
-- Case-insensitive, matching users_email_uniq: lookup and the uniqueness constraint must
-- agree on the fold, or an address can be registered twice and reset neither time.
SELECT id, name, email, password_hash, role, verified, banned, must_change_password, team_id
  FROM users WHERE lower(email) = lower(@email);

-- name: GetUserByID :one
SELECT id, name, email, password_hash, role, verified, banned, must_change_password, team_id,
       website, affiliation, country, language
  FROM users WHERE id = @user_id;

-- name: UpdateOwnProfile :one
-- The self-serve profile write: the player-owned fields and nothing else. Identity and
-- moderation state are not in the SET list, so this statement cannot be talked into touching them.
UPDATE users SET
    website     = CASE WHEN @clear_website::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(website), website) END,
    affiliation = CASE WHEN @clear_affiliation::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(affiliation), affiliation) END,
    country     = CASE WHEN @clear_country::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(country), country) END,
    language    = CASE WHEN @clear_language::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(language), language) END
WHERE id = @user_id
RETURNING id, name, email, role, verified, banned, team_id, website, affiliation, country, language;

-- name: UpdatePasswordHash :exec
-- The rehash-on-login path only: a bcrypt hash from an import, silently upgraded to Argon2id while
-- we hold the plaintext. The password itself has not changed, so must_change_password stays put —
-- a forced user logging in must not discharge the order by the act of logging in.
UPDATE users SET password_hash = @password_hash WHERE id = @user_id;

-- name: UpdatePasswordAndClearForcedChange :exec
-- A real password change: the user chose a new password, so a pending forced change is satisfied.
-- One statement, so the flag can never clear without the hash that justifies it.
UPDATE users SET password_hash = @password_hash, must_change_password = false
 WHERE id = @user_id;

-- name: CreateUser :one
-- Registration. The unique email and the num_users/team_size caps are enforced by the index and the
-- caps trigger, so a duplicate raises 23505 and an over-cap insert raises a check_violation — both
-- surface as errors here rather than as a lost check-then-insert race.
INSERT INTO users (name, email, password_hash, verified)
VALUES (@name, @email, @password_hash, @verified)
RETURNING id, verified;

-- name: CreateAdmin :one
-- The first-admin bootstrap. role='admin' cannot be granted through the admin API without an admin
-- already existing, so the very first one is made here instead. verified is stamped true because the
-- account has no inbox flow behind a console command. The caller runs this with the caps trigger
-- disabled for the transaction, so a full instance cannot lock its own first admin out.
INSERT INTO users (name, email, password_hash, role, verified)
VALUES (@name, @email, @password_hash, 'admin', true)
RETURNING id;

-- name: PromoteToAdmin :execrows
-- Elevate an existing user to a verified admin, by email (case-insensitive, matching
-- users_email_uniq). role is not one of the columns the caps trigger watches, so this needs no
-- exemption. execrows so the caller can distinguish "no such user" from a real promotion.
UPDATE users SET role = 'admin', verified = true
WHERE lower(email) = lower(@email);

-- ── rate limits ─────────────────────────────────────────────────────────────────

-- name: BumpRateLimit :one
-- Atomic on Postgres alone. The row is the counter, so there is no increment to lose: two
-- concurrent bumps serialize on the primary key and both see a correct value.
INSERT INTO rate_limits (bucket, window_start, n)
VALUES (@bucket, @window_start, 1)
ON CONFLICT (bucket, window_start) DO UPDATE
   SET n = rate_limits.n + 1
RETURNING n;

-- name: DeleteOldRateLimits :exec
DELETE FROM rate_limits WHERE window_start < @before;
