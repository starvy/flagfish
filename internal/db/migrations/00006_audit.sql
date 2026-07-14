-- Audit trail.
--
-- Neither layer alone is sufficient: HTTP middleware knows who but never sees the row's prior state;
-- a Postgres trigger knows what changed but not that it was alice. So middleware stamps the actor
-- into a transaction-local setting (`SET LOCAL app.actor_id` / `app.ip`), and one generic trigger
-- function does the capture.
--
-- Triggers rather than a Go-side interceptor, for completeness by construction: the audit lives at
-- the table, not the handler. A new admin endpoint cannot forget to audit itself. Neither can a
-- migration, a console session, or a psql fix at 3am.
--
-- Non-goal (chosen, not overlooked): tamper-evidence. No hash chain, no revoked DELETE grant.

-- +goose Up

CREATE TABLE audit_log (
    id           bigserial   PRIMARY KEY,
    actor_id     bigint,                     -- NULL = system/migration/console. No FK: the row must
                                             -- survive the actor's deletion (that is the point).
    action       text        NOT NULL CHECK (action IN ('INSERT','UPDATE','DELETE')),
    target_table text        NOT NULL,
    target_id    bigint,
    before       jsonb,
    after        jsonb,
    at           timestamptz NOT NULL DEFAULT now(),
    ip           inet
);
CREATE INDEX audit_log_target_idx ON audit_log (target_table, target_id, at DESC);  -- "history of this challenge"
CREATE INDEX audit_log_actor_idx  ON audit_log (actor_id, at DESC);                 -- "what did alice do"
CREATE INDEX audit_log_at_idx     ON audit_log (at DESC);                           -- the global feed

-- Masking: `to_jsonb(NEW)` on users/teams would put `password_hash` and `secret` into an
-- admin-readable table. They are stripped here, not at the read site — a redaction you have to
-- remember is a redaction you will forget. The `-` operator on a jsonb object is a no-op when the
-- key is absent, so this stays generic across every audited table.
--
-- OLD/NEW are read via to_jsonb() and never field-wise: `NEW.id` is unassigned in a DELETE trigger
-- and `OLD.id` in an INSERT trigger, so COALESCE(NEW.id, OLD.id) cannot be written literally in
-- PL/pgSQL. Extracting the id from the jsonb keeps one code path for all three ops.
-- +goose StatementBegin
CREATE FUNCTION audit_capture() RETURNS trigger AS $$
DECLARE
    j_before jsonb;
    j_after  jsonb;
BEGIN
    IF TG_OP IN ('UPDATE','DELETE') THEN
        j_before := to_jsonb(OLD) - 'password_hash' - 'secret';
    END IF;
    IF TG_OP IN ('INSERT','UPDATE') THEN
        j_after := to_jsonb(NEW) - 'password_hash' - 'secret';
    END IF;

    INSERT INTO audit_log (actor_id, action, target_table, target_id, before, after, ip)
    VALUES (
        nullif(current_setting('app.actor_id', true), '')::bigint,
        TG_OP,
        TG_TABLE_NAME,
        COALESCE((j_after->>'id')::bigint, (j_before->>'id')::bigint),
        j_before,
        j_after,
        nullif(current_setting('app.ip', true), '')::inet
    );
    RETURN NULL;   -- AFTER trigger
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Rule 1: admin-mutable tables only.
-- Rule 2: never `submissions` or `solves`. They are already immutable gameplay facts; a trigger
--         there would double the write volume on the hottest path in the product for zero
--         information. This is the single easiest way to wreck the hot path.
--         (`awards` is audited: it is admin-mutable, and the only gameplay writes to it are the rare
--         first-blood bonus and the hint unlock — not the per-submission path.)
-- Rule 3: the importer runs under `SET session_replication_role = replica`, which disables triggers —
--         so a million-row restore emits zero audit rows. A convenient accident; do not "fix" it.
CREATE TRIGGER audit_challenges          AFTER INSERT OR UPDATE OR DELETE ON challenges          FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_flags               AFTER INSERT OR UPDATE OR DELETE ON flags               FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_challenge_instances AFTER INSERT OR UPDATE OR DELETE ON challenge_instances FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_hints               AFTER INSERT OR UPDATE OR DELETE ON hints               FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_users               AFTER INSERT OR UPDATE OR DELETE ON users               FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_teams               AFTER INSERT OR UPDATE OR DELETE ON teams               FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_config              AFTER INSERT OR UPDATE OR DELETE ON config              FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_awards              AFTER INSERT OR UPDATE OR DELETE ON awards              FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_notifications       AFTER INSERT OR UPDATE OR DELETE ON notifications       FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_files               AFTER INSERT OR UPDATE OR DELETE ON files               FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_brackets            AFTER INSERT OR UPDATE OR DELETE ON brackets            FOR EACH ROW EXECUTE FUNCTION audit_capture();
CREATE TRIGGER audit_fields              AFTER INSERT OR UPDATE OR DELETE ON fields              FOR EACH ROW EXECUTE FUNCTION audit_capture();

-- +goose Down
DROP TRIGGER audit_fields              ON fields;
DROP TRIGGER audit_brackets            ON brackets;
DROP TRIGGER audit_files               ON files;
DROP TRIGGER audit_notifications       ON notifications;
DROP TRIGGER audit_awards              ON awards;
DROP TRIGGER audit_config              ON config;
DROP TRIGGER audit_teams               ON teams;
DROP TRIGGER audit_users               ON users;
DROP TRIGGER audit_hints               ON hints;
DROP TRIGGER audit_challenge_instances ON challenge_instances;
DROP TRIGGER audit_flags               ON flags;
DROP TRIGGER audit_challenges          ON challenges;
DROP FUNCTION audit_capture();
DROP TABLE audit_log;
