-- Content group: challenges, files, tags, flags, hints.
-- Order: challenges → files (files.challenge_id → challenges) → tags/flags/hints.

-- +goose Up

CREATE TABLE challenges (
    id              bigserial   PRIMARY KEY,
    name            text        NOT NULL,
    category        text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    attribution     text,
    connection_info text,

    -- No plugin system. Challenge types are in-tree interfaces, so the set is closed.
    type  text NOT NULL DEFAULT 'standard' CHECK (type IN ('standard','dynamic')),
    state text NOT NULL DEFAULT 'visible'  CHECK (state IN ('visible','hidden')),

    -- `value` is the current asking price, not what past solves are worth — that is solves.value
    -- (00003). Summing the live challenges.value on every standings query is what makes decay
    -- retroactively rewrite history; stamping each solve's value avoids it.
    value int NOT NULL,

    -- Dynamic scoring, single store: these columns live on `challenges`, not split across a
    -- separate dynamic-challenge table. Functions: `linear`, `logarithmic`.
    function text NOT NULL DEFAULT 'static' CHECK (function IN ('static','linear','logarithmic')),
    initial  int,
    minimum  int,
    decay    int,
    -- decay > 0 is deliberate: the score expression divides by decay squared, so reject decay=0
    -- at the boundary instead of silently coercing it to 1.
    CONSTRAINT challenges_dynamic_params CHECK (
        function = 'static'
        OR (initial IS NOT NULL AND minimum IS NOT NULL AND decay IS NOT NULL
            AND decay > 0 AND minimum >= 0 AND initial >= minimum)
    ),

    max_attempts int NOT NULL DEFAULT 0 CHECK (max_attempts >= 0),
    -- Declare the default and close the set, rather than leaving `logic` to backfill to '' and
    -- letting any unknown value be treated as 'any'.
    logic        text NOT NULL DEFAULT 'any' CHECK (logic IN ('any','all')),
    position     int  NOT NULL DEFAULT 0,
    next_id      bigint REFERENCES challenges(id) ON DELETE SET NULL,
    requirements jsonb NOT NULL DEFAULT '{}'::jsonb,  -- {prerequisites:[id], anonymize:bool|"preview"}

    -- Two orthogonal opt-ins: flag_mode is how flags are issued; flags.type is how they are
    -- compared. A regex flag cannot be pool-issued — that predicate spans two tables, so it is
    -- enforced in the service layer, not here.
    flag_mode         text NOT NULL DEFAULT 'static' CHECK (flag_mode IN ('static','unique')),
    first_blood       text NOT NULL DEFAULT 'none'   CHECK (first_blood IN ('none','announce','bonus')),
    first_blood_bonus int,
    CONSTRAINT challenges_fb_bonus CHECK ((first_blood = 'bonus') = (first_blood_bonus IS NOT NULL)),

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX challenges_board_idx ON challenges (state, category, position);  -- the visible board

-- ── files: one table, not three ─────────────────────────────────
CREATE TABLE files (
    id           bigserial   PRIMARY KEY,
    location     text        NOT NULL,
    sha256sum    bytea       NOT NULL,   -- raw sha256 bytes, not a hex string
    size_bytes   bigint      NOT NULL,
    challenge_id bigint      REFERENCES challenges(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),

    -- One table for every file, with the ownership invariant in a CHECK: UNIQUE(location) below
    -- must hold across all files, and challenge_instances.artifact_id needs a single FK target.
    -- `<= 1`, not `= 1`: an ownerless media-library file is legitimate. Later subtypes (pages,
    -- solutions) widen this to num_nonnulls(challenge_id, page_id, solution_id) <= 1.
    CONSTRAINT files_at_most_one_owner CHECK (num_nonnulls(challenge_id) <= 1)
);
-- Without a unique index, a check-then-insert on location lets concurrent uploads double-insert.
CREATE UNIQUE INDEX files_location_uniq ON files (location);
CREATE INDEX files_challenge_idx ON files (challenge_id) WHERE challenge_id IS NOT NULL;

CREATE TABLE tags (
    id           bigserial PRIMARY KEY,
    challenge_id bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    value        text   NOT NULL,
    -- Prevents duplicate tags per challenge.
    UNIQUE (challenge_id, value)
);
CREATE INDEX tags_challenge_idx ON tags (challenge_id);

CREATE TABLE flags (
    id           bigserial PRIMARY KEY,
    challenge_id bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    -- static + regex, a closed set; `type` is required and constrained, not left open.
    type         text NOT NULL CHECK (type IN ('static','regex')),
    content      text NOT NULL,
    -- A real boolean, not a magic "case_insensitive" string stored in a data column.
    case_insensitive boolean NOT NULL DEFAULT false
);
-- Loaded on every submit of a flag_mode='static' challenge. The hottest read in the product.
CREATE INDEX flags_challenge_idx ON flags (challenge_id);

CREATE TABLE hints (
    id           bigserial PRIMARY KEY,
    challenge_id bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    title        text,
    content      text   NOT NULL,
    cost         int    NOT NULL DEFAULT 0 CHECK (cost >= 0),
    requirements jsonb  NOT NULL DEFAULT '{}'::jsonb,   -- {prerequisites:[hint_id]}
    position     int    NOT NULL DEFAULT 0
);
CREATE INDEX hints_challenge_idx ON hints (challenge_id, position);

-- +goose Down
DROP TABLE hints;
DROP TABLE flags;
DROP TABLE tags;
DROP TABLE files;
DROP TABLE challenges;
