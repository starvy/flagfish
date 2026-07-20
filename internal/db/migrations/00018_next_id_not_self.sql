-- A challenge that suggests itself as the next one is a loop: solving it points the player back at
-- the same page. The suggestion pointer already SET NULLs when its target is deleted; a
-- self-reference is the one shape the FK cannot catch, so the database refuses it directly. NULL
-- (no suggestion) passes, so an unset next_id is untouched.

-- +goose Up
ALTER TABLE challenges
    ADD CONSTRAINT challenges_next_not_self CHECK (next_id IS NULL OR next_id <> id);

-- +goose Down
ALTER TABLE challenges
    DROP CONSTRAINT challenges_next_not_self;
