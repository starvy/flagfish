-- A team's join secret was optional, and a NULL hash meant "admits an empty password". Team names
-- are printed on the scoreboard, so such a team was protected by nothing at all: a rival could walk
-- onto the roster and read the team's solves, hints and internal state. Every team must hold a
-- secret, and this is the only place that can make that true for rows that already exist — and for
-- any future row inserted by a path that forgets to set one.
--
-- The invariant is backed by a constraint, not a promise in the application: NOT NULL forces a
-- value, and the DEFAULT makes an omitted one a LOCKED hash rather than an error. A locked hash is
-- a well-formed Argon2id string over bytes that were never derived from any password, so it does
-- the full argon2 work and can never verify — the team is closed until a captain (or an admin, for
-- a captainless team) sets a real secret through the rotate route. Fail-closed: a team nobody
-- remembered to give a secret admits nobody, never everybody.

-- +goose Up

-- +goose StatementBegin
-- 16 random bytes of salt and 32 of digest, base64 with the padding stripped: the raw-std encoding
-- the Go hasher mints and parses. The bytes need no cryptographic quality — there is no plaintext
-- that produces them, so there is nothing to guess. random() is reseeded per row, so two locked
-- teams do not share a hash.
CREATE FUNCTION locked_join_secret() RETURNS text
    LANGUAGE sql VOLATILE AS $$
    SELECT '$argon2id$v=19$m=65536,t=3,p=4$'
        || rtrim(translate(encode(decode(md5(random()::text || clock_timestamp()::text), 'hex'), 'base64'), E'\n', ''), '=')
        || '$'
        || rtrim(translate(encode(decode(
               md5(random()::text || clock_timestamp()::text || 'a')
               || md5(random()::text || clock_timestamp()::text || 'b'),
               'hex'), 'base64'), E'\n', ''), '=');
$$;
-- +goose StatementEnd

UPDATE teams SET password_hash = locked_join_secret() WHERE password_hash IS NULL;

ALTER TABLE teams
    ALTER COLUMN password_hash SET DEFAULT locked_join_secret(),
    ALTER COLUMN password_hash SET NOT NULL;

-- +goose Down
ALTER TABLE teams
    ALTER COLUMN password_hash DROP NOT NULL,
    ALTER COLUMN password_hash DROP DEFAULT;

DROP FUNCTION locked_join_secret();
