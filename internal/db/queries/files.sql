-- Challenge file attachments. The blob lives in object storage, content-addressed by sha256; these
-- rows are the link from a challenge to a stored object plus its human-facing name.

-- name: AdminInsertFile :one
-- location is derived from the content address and is UNIQUE, so a concurrent upload of the same
-- content under the same name collapses to one row instead of double-inserting. DO NOTHING (not a
-- no-op UPDATE) so a deduped upload emits no spurious audit row; the caller reads the existing row.
INSERT INTO files (location, sha256sum, size_bytes, challenge_id, name)
VALUES (@location, @sha256sum, @size_bytes, @challenge_id, @name)
ON CONFLICT (location) DO NOTHING
RETURNING *;

-- name: GetFileByLocation :one
SELECT * FROM files WHERE location = @location;

-- name: AdminDeleteFile :one
DELETE FROM files WHERE id = @id RETURNING sha256sum;

-- name: CountFilesBySha :one
-- Whether any other row still points at the same stored object, so a delete knows if the object is
-- now unreferenced and safe to remove.
SELECT count(*) FROM files WHERE sha256sum = @sha256sum;

-- name: GetChallengeFileForDownload :one
-- The metadata a download needs, joined to its challenge so the handler can enforce that a hidden
-- challenge's file is invisible to non-admins. A file with no owning challenge returns no row.
SELECT f.id, f.name, f.sha256sum, f.size_bytes, c.state, c.id AS challenge_id
FROM files f
JOIN challenges c ON c.id = f.challenge_id
WHERE f.id = @id;
