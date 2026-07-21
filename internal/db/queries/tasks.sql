-- Async admin operations (backup, restore, import) tracked in the tasks table. River owns the
-- execution; this table owns the state a poller reads. The progress writes below run on a
-- connection SEPARATE from the restore transaction on purpose: a restore is one transaction so its
-- own progress UPDATEs would be invisible until it commits.

-- name: EnqueueTask :one
-- Inserts a queued task. The tasks_one_in_flight partial unique index raises 23505 when a task of
-- the same kind is already queued or running — that unique violation IS the single-in-flight
-- refusal, surfaced to the caller as 409, never a second job queued to collide.
INSERT INTO tasks (kind, state, created_by)
VALUES (@kind, 'queued', sqlc.narg(created_by))
RETURNING id, kind, state, progress, detail, error, created_by, created_at, updated_at;

-- name: StartTask :exec
-- Claims a queued task for a worker. Kept narrow to state='queued' so a redelivered job cannot
-- reset a task that already advanced.
UPDATE tasks
   SET state = 'running', progress = @progress, detail = sqlc.narg(detail), updated_at = now()
 WHERE id = @id AND state = 'queued';

-- name: SetTaskProgress :exec
-- Advances a running task. Written on its own pooled connection, so a caller polling GetTask sees
-- movement while a single-transaction restore is still open and uncommitted.
UPDATE tasks
   SET progress = @progress, detail = sqlc.narg(detail), updated_at = now()
 WHERE id = @id AND state = 'running';

-- name: FinishTask :exec
UPDATE tasks
   SET state = 'succeeded', progress = 100, detail = sqlc.narg(detail), updated_at = now()
 WHERE id = @id;

-- name: FailTask :exec
-- A failure is loud and terminal: the error text is stamped for the operator and the row leaves the
-- in-flight set, freeing the kind for another attempt.
UPDATE tasks
   SET state = 'failed', error = sqlc.narg(error), updated_at = now()
 WHERE id = @id;

-- name: GetTask :one
SELECT id, kind, state, progress, detail, error, created_by, created_at, updated_at
FROM tasks
WHERE id = @id;
