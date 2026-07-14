-- Platform-level writes whose only interesting property is that they are race-free:
-- a unique constraint plus ON CONFLICT, never a check-then-insert.

-- name: InsertFileOnce :one
-- ON CONFLICT DO UPDATE, not DO NOTHING: a re-upload of a byte-identical object must hand
-- back the existing id, not ErrNoRows. `location` is content-addressed upstream, so the
-- SET is a no-op write that exists only to make RETURNING fire on the conflict path.
INSERT INTO files (location, sha256sum, size_bytes, challenge_id)
VALUES (@location, @sha256sum, @size_bytes, @challenge_id)
ON CONFLICT (location) DO UPDATE
   SET sha256sum = EXCLUDED.sha256sum
RETURNING id, location, sha256sum, size_bytes, challenge_id, created_at;

-- name: UpsertTracking :one
-- Runs on every authenticated request. One idempotent statement that cannot raise:
-- there is no error path left to mishandle.
INSERT INTO tracking (user_id, ip)
VALUES (@user_id, @ip)
ON CONFLICT (user_id, ip) DO UPDATE
   SET date = now()
RETURNING id, user_id, ip, date;
