-- +goose Up
-- Stamp the trigger's name into the cap errors as the constraint name. Callers map
-- check_violation to "registration is full" by that name; a bare check_violation would make
-- any future CHECK on users read as the cap.
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
                USING ERRCODE = 'check_violation'; END IF;
        END IF;
        IF NEW.team_id IS NOT NULL THEN
            SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'team_size';
            IF cap IS NOT NULL AND cap > 0 THEN
                SELECT count(*) INTO n FROM users WHERE team_id = NEW.team_id;
                IF n >= cap THEN RAISE EXCEPTION 'team_size cap (%) reached', cap
                    USING ERRCODE = 'check_violation'; END IF;
            END IF;
        END IF;
    ELSE
        SELECT NULLIF(value,'')::int INTO cap FROM config WHERE key = 'num_teams';
        IF cap IS NOT NULL AND cap > 0 THEN
            SELECT count(*) INTO n FROM teams WHERE banned = false AND hidden = false;
            IF n >= cap THEN RAISE EXCEPTION 'num_teams cap (%) reached', cap
                USING ERRCODE = 'check_violation'; END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd
