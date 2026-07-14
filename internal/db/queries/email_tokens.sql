-- Email verification and password reset tokens.

-- name: CreateEmailToken :one
INSERT INTO email_tokens (token_hash, purpose, user_id, expires_at)
VALUES (@token_hash, @purpose, @user_id, @expires_at)
RETURNING id;

-- name: ConsumeEmailToken :one
-- Single-use is enforced by this one statement: two concurrent consumers race on the row
-- lock, and the loser sees consumed_at already set and gets zero rows. A SELECT-then-UPDATE
-- would let both pass the check. Expiry lives in the WHERE clause so an expired token is
-- indistinguishable from a bad one, on the database's clock, not the app server's.
UPDATE email_tokens
   SET consumed_at = now()
 WHERE token_hash = @token_hash
   AND purpose = @purpose
   AND consumed_at IS NULL
   AND expires_at > now()
RETURNING user_id;

-- name: MarkUserVerified :exec
UPDATE users SET verified = true WHERE id = @user_id;

-- name: DeleteExpiredEmailTokens :exec
DELETE FROM email_tokens WHERE expires_at <= now();
