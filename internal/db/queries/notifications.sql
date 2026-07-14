-- Notifications are broadcast, not addressed: every row goes to every client, so there is no
-- targeting column to filter on. The interesting property lives in the service, where the INSERT
-- and the pg_notify that wakes the listeners share one transaction — a row that persists is a row
-- that was announced, and a rolled-back write never wakes the fleet.

-- name: InsertNotification :one
INSERT INTO notifications (title, content)
VALUES (@title, @content)
RETURNING id, title, content, date;

-- name: GetNotification :one
-- The listen pump carries only an id in the 8 kB NOTIFY payload and reloads the row here, so the
-- content it fans out is the committed row rather than a copy that could disagree with it.
SELECT id, title, content, date
FROM notifications
WHERE id = @id;

-- name: RecentNotifications :many
-- Replay on connect: newest first, capped, so a client that just opened the stream is caught up
-- without paging. The caller emits them oldest-first.
SELECT id, title, content, date
FROM notifications
ORDER BY date DESC, id DESC
LIMIT @lim::int;

-- name: ListNotifications :many
-- COUNT(*) OVER () returns the total in the same round trip rather than a second query. Ordered by
-- a total key (date then id) so pages are stable when two rows share a timestamp.
SELECT id, title, content, date, COUNT(*) OVER () AS total
FROM notifications
ORDER BY date DESC, id DESC
LIMIT @lim::int OFFSET @off::int;
