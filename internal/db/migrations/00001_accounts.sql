-- Accounts group: instance, config, brackets, teams, users, fields, tokens, tracking.
-- Emission order is FK-dependency order.
-- Conventions: timestamptz everywhere, text never varchar(n), bigserial ids.

-- +goose Up

-- ── instance ────────────────────────────────────────────────────────────────────
-- user_mode is fixed at setup: flipping it would silently re-point every scoring query at a
-- different column. It lives in its own singleton table, not in config, so the immutability is
-- enforceable in the DATABASE rather than in the admin UI.
CREATE TABLE instance (
    id        boolean     PRIMARY KEY DEFAULT true CHECK (id),   -- singleton
    user_mode text        NOT NULL CHECK (user_mode IN ('users','teams')),
    setup_at  timestamptz NOT NULL DEFAULT now(),
    version   text        NOT NULL
);

-- +goose StatementBegin
CREATE FUNCTION instance_user_mode_is_immutable() RETURNS trigger AS $$
BEGIN
    IF NEW.user_mode IS DISTINCT FROM OLD.user_mode THEN
        RAISE EXCEPTION 'user_mode is fixed at setup and cannot be changed'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER instance_user_mode_immutable
    BEFORE UPDATE ON instance
    FOR EACH ROW EXECUTE FUNCTION instance_user_mode_is_immutable();

-- ── config ──────────────────────────────────────────────────────────────────────
-- EAV storage; the typed accessor lives in Go and serves reads from an atomic.Pointer snapshot.
CREATE TABLE config (
    id         bigserial   PRIMARY KEY,
    key        text        NOT NULL,
    value      text,
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- Config is read by key, so a duplicate row would make the read nondeterministic.
CREATE UNIQUE INDEX config_key_uniq ON config (key);

-- ── brackets ────────────────────────────────────────────────────────────────────
CREATE TABLE brackets (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL,
    description text,
    applies_to  text NOT NULL CHECK (applies_to IN ('users','teams'))
);

-- ── teams ───────────────────────────────────────────────────────────────────────
CREATE TABLE teams (
    id            bigserial   PRIMARY KEY,
    name          text        NOT NULL,   -- display name; deliberately NOT unique
    email         text,
    password_hash text,                   -- Argon2id; bcrypt verified + rehashed on login
    secret        text,
    website       text,
    affiliation   text,
    country       text,
    bracket_id    bigint      REFERENCES brackets(id) ON DELETE SET NULL,
    captain_id    bigint,                 -- FK added after `users` exists (circular)
    hidden        boolean     NOT NULL DEFAULT false,
    banned        boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);
-- Email is an identity, and identities are compared case-insensitively: a case-sensitive UNIQUE
-- lets `A@x` and `a@x` coexist while a lowercasing lookup then finds only one of them.
CREATE UNIQUE INDEX teams_email_uniq ON teams (lower(email));
CREATE INDEX teams_bracket_idx ON teams (bracket_id) WHERE bracket_id IS NOT NULL;

-- ── users ───────────────────────────────────────────────────────────────────────
CREATE TABLE users (
    id            bigserial   PRIMARY KEY,
    name          text        NOT NULL,   -- display name; deliberately NOT unique
    email         text        NOT NULL,
    password_hash text,                   -- NULL for OAuth-only accounts (deferred)
    -- Admin is a flag, not a subtype: it adds no columns of its own.
    role          text        NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
    secret        text,
    website       text,
    affiliation   text,
    country       text,
    language      text,
    bracket_id    bigint      REFERENCES brackets(id) ON DELETE SET NULL,
    -- ON DELETE SET NULL, not a hand-written nulling pass in the delete handler: any other route
    -- that removes a team would otherwise orphan its members.
    team_id       bigint      REFERENCES teams(id) ON DELETE SET NULL,
    hidden        boolean     NOT NULL DEFAULT false,
    banned        boolean     NOT NULL DEFAULT false,
    verified      boolean     NOT NULL DEFAULT false,
    must_change_password boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_uniq   ON users (lower(email));
CREATE INDEX        users_team_idx     ON users (team_id)    WHERE team_id    IS NOT NULL;
CREATE INDEX        users_bracket_idx  ON users (bracket_id) WHERE bracket_id IS NOT NULL;

ALTER TABLE teams
    ADD CONSTRAINT teams_captain_fk FOREIGN KEY (captain_id)
        REFERENCES users(id) ON DELETE SET NULL;

-- The num_users / num_teams / team_size caps are config values, so they cannot be a CHECK — the rule
-- is inherently count-then-insert, and N concurrent registrations would all read the same pre-insert
-- count and blow past the cap. A transaction-scoped advisory lock serializes the count with the
-- insert, which is what makes the cap exact.
-- +goose StatementBegin
CREATE FUNCTION enforce_account_caps() RETURNS trigger AS $$
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

-- NB: the importer runs under `session_replication_role = replica`, which disables these triggers.
-- That is intended: an import replays an existing instance, it does not register into a live one.
CREATE TRIGGER users_caps BEFORE INSERT OR UPDATE OF team_id ON users
    FOR EACH ROW EXECUTE FUNCTION enforce_account_caps();
CREATE TRIGGER teams_caps BEFORE INSERT ON teams
    FOR EACH ROW EXECUTE FUNCTION enforce_account_caps();

-- ── custom registration fields ──────────────────────────────────────────────────
CREATE TABLE fields (
    id          bigserial PRIMARY KEY,
    name        text NOT NULL,
    applies_to  text NOT NULL CHECK (applies_to IN ('user','team')),
    field_type  text NOT NULL CHECK (field_type IN ('text','boolean')),
    description text,
    required    boolean NOT NULL DEFAULT false,
    public      boolean NOT NULL DEFAULT false,
    editable    boolean NOT NULL DEFAULT false,
    position    int     NOT NULL DEFAULT 0
);

-- UNIQUE (field_id, owner): one answer per field per account, or a read has to pick a winner.
-- `value` is jsonb rather than text so a boolean field stays a boolean instead of round-tripping
-- through a string.
CREATE TABLE field_entries (
    id       bigserial PRIMARY KEY,
    field_id bigint NOT NULL REFERENCES fields(id) ON DELETE CASCADE,
    user_id  bigint REFERENCES users(id) ON DELETE CASCADE,
    team_id  bigint REFERENCES teams(id) ON DELETE CASCADE,
    value    jsonb,
    CONSTRAINT field_entries_one_owner CHECK (num_nonnulls(user_id, team_id) = 1),
    UNIQUE (field_id, user_id),
    UNIQUE (field_id, team_id)
);
CREATE INDEX field_entries_user_idx ON field_entries (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX field_entries_team_idx ON field_entries (team_id) WHERE team_id IS NOT NULL;

-- ── api tokens ──────────────────────────────────────────────────────────────────
-- Only sha256(token) is stored, so a database dump — or an export of one — is not a bag of live
-- bearer credentials. The plaintext is shown once, at creation, and never again.
CREATE TABLE api_tokens (
    id          bigserial   PRIMARY KEY,
    user_id     bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  bytea       NOT NULL UNIQUE,   -- sha256(token). Never the plaintext.
    description text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL
);
CREATE INDEX api_tokens_user_idx   ON api_tokens (user_id);
CREATE INDEX api_tokens_expiry_idx ON api_tokens (expires_at);   -- expiry sweep

-- ── tracking ────────────────────────────────────────────────────────────────────
CREATE TABLE tracking (
    id      bigserial   PRIMARY KEY,
    user_id bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    ip      inet        NOT NULL,
    date    timestamptz NOT NULL DEFAULT now(),
    -- UNIQUE (user_id, ip) is what lets the write be a single
    -- `ON CONFLICT … DO UPDATE SET date = now()`. Without it, two concurrent requests from the same
    -- IP race on a check-then-insert, and the loser's transaction dies mid-request.
    UNIQUE (user_id, ip)
);
CREATE INDEX tracking_ip_idx          ON tracking (ip);              -- shared-IP detection
CREATE INDEX tracking_user_recent_idx ON tracking (user_id, date DESC);

-- +goose Down
DROP TABLE tracking;
DROP TABLE api_tokens;
DROP TABLE field_entries;
DROP TABLE fields;
DROP TRIGGER teams_caps ON teams;
DROP TRIGGER users_caps ON users;
DROP FUNCTION enforce_account_caps();
ALTER TABLE teams DROP CONSTRAINT teams_captain_fk;
DROP TABLE users;
DROP TABLE teams;
DROP TABLE brackets;
DROP TABLE config;
DROP TRIGGER instance_user_mode_immutable ON instance;
DROP FUNCTION instance_user_mode_is_immutable();
DROP TABLE instance;
