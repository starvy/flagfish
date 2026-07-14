-- Team enrollment: join is by name, so a name must identify exactly one team, and membership
-- moves must be arbitrated by the database rather than by a check in Go.

-- +goose Up

-- Enrollment looks a team up by name and creation checks "name taken" — without an index both
-- are lost races and the lookup is ambiguous the moment they lose. The index is the arbiter.
CREATE UNIQUE INDEX teams_name_uniq ON teams (name);

-- A user leaves a team (team_id -> NULL) or joins from teamless (NULL -> team_id); a direct
-- switch is never legal, because solves stamp team_id at solve time and a silent switch would
-- let one user feed two teams. Enforced here so no future write path can do it by accident.
-- +goose StatementBegin
CREATE FUNCTION users_team_change_is_join_or_leave() RETURNS trigger AS $$
BEGIN
    IF OLD.team_id IS NOT NULL AND NEW.team_id IS NOT NULL
       AND NEW.team_id <> OLD.team_id THEN
        RAISE EXCEPTION 'user % is already on a team', OLD.id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER users_team_switch_guard
    BEFORE UPDATE OF team_id ON users
    FOR EACH ROW EXECUTE FUNCTION users_team_change_is_join_or_leave();

-- The num_users check used to run on UPDATE OF team_id too, where the row being updated is
-- already counted — so at a full instance every team join failed the *user* cap. How many
-- users exist is an INSERT question; team_size is the only cap a join answers to.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_account_caps() RETURNS trigger AS $$
DECLARE cap int; n int;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('registration'));
    IF TG_TABLE_NAME = 'users' THEN
        IF TG_OP = 'INSERT' THEN
            SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'num_users';
            IF cap IS NOT NULL AND cap > 0 THEN
                SELECT count(*) INTO n FROM users WHERE banned = false AND hidden = false;
                IF n >= cap THEN RAISE EXCEPTION 'num_users cap (%) reached', cap
                    USING ERRCODE = 'check_violation', CONSTRAINT = TG_NAME; END IF;
            END IF;
        END IF;
        IF NEW.team_id IS NOT NULL THEN
            SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'team_size';
            IF cap IS NOT NULL AND cap > 0 THEN
                SELECT count(*) INTO n FROM users WHERE team_id = NEW.team_id;
                IF n >= cap THEN RAISE EXCEPTION 'team_size cap (%) reached', cap
                    USING ERRCODE = 'check_violation', CONSTRAINT = TG_NAME; END IF;
            END IF;
        END IF;
    ELSE
        SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'num_teams';
        IF cap IS NOT NULL AND cap > 0 THEN
            SELECT count(*) INTO n FROM teams WHERE banned = false AND hidden = false;
            IF n >= cap THEN RAISE EXCEPTION 'num_teams cap (%) reached', cap
                USING ERRCODE = 'check_violation', CONSTRAINT = TG_NAME; END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enforce_account_caps() RETURNS trigger AS $$
DECLARE cap int; n int;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('registration'));
    IF TG_TABLE_NAME = 'users' THEN
        SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'num_users';
        IF cap IS NOT NULL AND cap > 0 THEN
            SELECT count(*) INTO n FROM users WHERE banned = false AND hidden = false;
            IF n >= cap THEN RAISE EXCEPTION 'num_users cap (%) reached', cap
                USING ERRCODE = 'check_violation', CONSTRAINT = TG_NAME; END IF;
        END IF;
        IF NEW.team_id IS NOT NULL THEN
            SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'team_size';
            IF cap IS NOT NULL AND cap > 0 THEN
                SELECT count(*) INTO n FROM users WHERE team_id = NEW.team_id;
                IF n >= cap THEN RAISE EXCEPTION 'team_size cap (%) reached', cap
                    USING ERRCODE = 'check_violation', CONSTRAINT = TG_NAME; END IF;
            END IF;
        END IF;
    ELSE
        SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'num_teams';
        IF cap IS NOT NULL AND cap > 0 THEN
            SELECT count(*) INTO n FROM teams WHERE banned = false AND hidden = false;
            IF n >= cap THEN RAISE EXCEPTION 'num_teams cap (%) reached', cap
                USING ERRCODE = 'check_violation', CONSTRAINT = TG_NAME; END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd
DROP TRIGGER users_team_switch_guard ON users;
DROP FUNCTION users_team_change_is_join_or_leave();
DROP INDEX teams_name_uniq;
