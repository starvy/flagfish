-- Deleting a challenge must not silently destroy its solves. A solve is a stamped scoreboard fact:
-- the standings, the time-travel view and the gameplay audit trail are all sums over it, and a
-- CASCADE here would let one admin DELETE rewrite every one of them with no trace of what was lost.
-- RESTRICT puts the rule in the database — the API turns the violation into a 409 telling the admin
-- the challenge has solves. Submissions keep their CASCADE: a never-solved challenge can be deleted
-- along with its failed attempts.

-- +goose Up
ALTER TABLE solves
    DROP CONSTRAINT solves_challenge_id_fkey,
    ADD CONSTRAINT solves_challenge_id_fkey FOREIGN KEY (challenge_id)
        REFERENCES challenges(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE solves
    DROP CONSTRAINT solves_challenge_id_fkey,
    ADD CONSTRAINT solves_challenge_id_fkey FOREIGN KEY (challenge_id)
        REFERENCES challenges(id) ON DELETE CASCADE;
