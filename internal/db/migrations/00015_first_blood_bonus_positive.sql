-- A zero bonus writes an award the standings discard, and a negative one penalizes the first
-- solver; both are authoring mistakes the database can refuse outright. NULL (no bonus mode)
-- passes the CHECK, so the pairing rule in challenges_fb_bonus is untouched.

-- +goose Up
ALTER TABLE challenges
    ADD CONSTRAINT challenges_fb_bonus_positive CHECK (first_blood_bonus > 0);

-- +goose Down
ALTER TABLE challenges
    DROP CONSTRAINT challenges_fb_bonus_positive;
