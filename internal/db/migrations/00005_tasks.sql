-- Platform group: notifications, tasks.
-- River's river_* tables are created by River's own migrator — never expose another library's
-- schema as our API, so we define none of them.

-- +goose Up

CREATE TABLE notifications (
    id      bigserial   PRIMARY KEY,
    title   text        NOT NULL,
    content text        NOT NULL,
    date    timestamptz NOT NULL DEFAULT now()
);
-- No user_id/team_id targeting columns: every notification is broadcast to every client. Do not
-- re-add them as "targeting".
CREATE INDEX notifications_date_idx ON notifications (date DESC);

-- `tasks` owns the state; River owns the execution. Import status lives in this table, not in cache
-- keys — a cache eviction must not lose the status of a database restore.
CREATE TABLE tasks (
    id         bigserial   PRIMARY KEY,
    kind       text        NOT NULL CHECK (kind IN ('import','export')),
    state      text        NOT NULL CHECK (state IN ('queued','running','succeeded','failed','cancelled')),
    progress   int         NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    detail     text,                     -- "restoring submissions"
    error      text,
    created_by bigint      REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- The single-in-flight guard lives in the database, not in River's `MaxWorkers: 1` — which is
-- per-client, so three replicas would otherwise give you three concurrent database restores.
CREATE UNIQUE INDEX tasks_one_in_flight ON tasks (kind) WHERE state IN ('queued','running');
-- Implementation trap: progress must be written on a second connection. The restore is one
-- transaction (for real rollback), so progress UPDATEs inside it are invisible until it commits.

-- +goose Down
DROP TABLE tasks;
DROP TABLE notifications;
