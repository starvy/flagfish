-- Annotations are the keyed sibling of `tags`. A tag is a free string a challenge either carries or
-- does not ("web", "crypto"); an annotation is a (key, value) pair a renderer looks up BY NAME. That
-- difference is the whole reason for a second table: a view that wants to place a challenge on a map
-- needs to ask "what is this challenge's country", and a flat set of strings cannot answer a
-- question, only membership.
--
-- The key namespace is deliberately open. Nothing in the schema knows what `country` means, so a new
-- portal view is a new well-known key agreed between an author and a renderer — not a migration, not
-- a column, and not a plugin.

-- +goose Up
CREATE TABLE challenge_annotations (
    id           bigserial PRIMARY KEY,
    challenge_id bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    -- A machine identifier, not prose: it is looked up by name in client code, so the shape is
    -- pinned here rather than trusted to whichever admin client writes the row next.
    key          text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_]{0,63}$'),
    value        text NOT NULL CHECK (length(value) BETWEEN 1 AND 256),

    -- The well-known keys earn their own constraints. The full ISO 3166-1 list lives in the domain
    -- layer — it changes when the world does, not when the schema does — but the SHAPE of a country
    -- code is a fact the database can hold, and holding it here means no path into this table can
    -- store a lowercase or three-letter "country" for a renderer to trip over.
    CONSTRAINT challenge_annotations_country_alpha2
        CHECK (key <> 'country' OR value ~ '^[A-Z]{2}$'),

    -- One value per key per challenge. This is the arbiter of the admin upsert's ON CONFLICT, so two
    -- concurrent sets of the same key settle on one row instead of racing a SELECT-then-INSERT.
    UNIQUE (challenge_id, key)
);

-- The board read loads every visible challenge's annotations in one pass, keyed on this.
CREATE INDEX challenge_annotations_challenge_idx ON challenge_annotations (challenge_id);

-- Admin-mutable content, like tags and hints, so it earns the same capture trigger: an annotation
-- decides where a challenge appears on the globe, and an edit that moves it must record who did it.
CREATE TRIGGER audit_challenge_annotations AFTER INSERT OR UPDATE OR DELETE ON challenge_annotations
    FOR EACH ROW EXECUTE FUNCTION audit_capture();

-- +goose Down
DROP TRIGGER audit_challenge_annotations ON challenge_annotations;
DROP TABLE challenge_annotations;
