-- A restore wipes the instance in one transaction with TRUNCATE ... CASCADE, and progress for that
-- restore is written to `tasks` on a second connection while that transaction is still open — the
-- only way a poller sees movement before the single transaction commits.
--
-- The FK tasks.created_by -> users dragged `tasks` into the restore's TRUNCATE CASCADE (users is
-- wiped, so every table referencing it is too). That took an ACCESS EXCLUSIVE lock on `tasks` for
-- the whole restore, which the second-connection progress write then blocked on forever — and it
-- would have deleted the restore's own tracking row on commit. `tasks` is operational bookkeeping,
-- not game data a restore reconstructs, so it must stay outside that transaction's locked set.
--
-- created_by stays as a plain nullable bigint: still the id of the admin who started the operation,
-- just no longer a referential dependency that entangles the task log with the restore.

-- +goose Up
ALTER TABLE tasks DROP CONSTRAINT tasks_created_by_fkey;

-- +goose Down
ALTER TABLE tasks
    ADD CONSTRAINT tasks_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;
