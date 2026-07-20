-- The ledger is memory: submissions, solves, awards and hint_unlocks are stamped facts, and
-- flag_issues is the anti-cheat record of who was issued which unique flag. A parent delete that
-- cascades into any of them rewrites history with no trace of what was lost, so every FK into
-- these tables now refuses the delete. The API surfaces each refusal as a conflict; hiding or
-- banning the parent remains the way to retire it.
--
-- This deliberately reverses 00010's carve-out ("submissions keep their CASCADE: a never-solved
-- challenge can be deleted along with its failed attempts"). Failed attempts are anticheat
-- evidence — they carry the submitter's IP and the attributed account — and a challenge delete
-- must not quietly destroy them.
--
-- RESTRICT, not NO ACTION, matching 00010: NO ACTION is checked at end of statement, so a ledger
-- row removed by another cascade path in the same statement passes silently; RESTRICT fires
-- immediately. solves.submission_id keeps its SET NULL — deleting an attempt must not retract
-- points — and the existing RESTRICTs are untouched.

-- +goose Up
ALTER TABLE submissions
    DROP CONSTRAINT submissions_challenge_id_fkey,
    ADD CONSTRAINT submissions_challenge_id_fkey FOREIGN KEY (challenge_id)
        REFERENCES challenges(id) ON DELETE RESTRICT,
    DROP CONSTRAINT submissions_user_id_fkey,
    ADD CONSTRAINT submissions_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE RESTRICT,
    DROP CONSTRAINT submissions_team_id_fkey,
    ADD CONSTRAINT submissions_team_id_fkey FOREIGN KEY (team_id)
        REFERENCES teams(id) ON DELETE RESTRICT;

ALTER TABLE solves
    DROP CONSTRAINT solves_user_id_fkey,
    ADD CONSTRAINT solves_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE RESTRICT,
    DROP CONSTRAINT solves_team_id_fkey,
    ADD CONSTRAINT solves_team_id_fkey FOREIGN KEY (team_id)
        REFERENCES teams(id) ON DELETE RESTRICT;

ALTER TABLE awards
    DROP CONSTRAINT awards_user_id_fkey,
    ADD CONSTRAINT awards_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE RESTRICT,
    DROP CONSTRAINT awards_team_id_fkey,
    ADD CONSTRAINT awards_team_id_fkey FOREIGN KEY (team_id)
        REFERENCES teams(id) ON DELETE RESTRICT,
    DROP CONSTRAINT awards_challenge_id_fkey,
    ADD CONSTRAINT awards_challenge_id_fkey FOREIGN KEY (challenge_id)
        REFERENCES challenges(id) ON DELETE RESTRICT;

ALTER TABLE hint_unlocks
    DROP CONSTRAINT hint_unlocks_hint_id_fkey,
    ADD CONSTRAINT hint_unlocks_hint_id_fkey FOREIGN KEY (hint_id)
        REFERENCES hints(id) ON DELETE RESTRICT,
    DROP CONSTRAINT hint_unlocks_user_id_fkey,
    ADD CONSTRAINT hint_unlocks_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE RESTRICT,
    DROP CONSTRAINT hint_unlocks_team_id_fkey,
    ADD CONSTRAINT hint_unlocks_team_id_fkey FOREIGN KEY (team_id)
        REFERENCES teams(id) ON DELETE RESTRICT;

-- Already transitively refused via challenge_instances → flag_issues.instance_id RESTRICT; the
-- flip makes the rule a declaration rather than an accident of the cascade path.
ALTER TABLE flag_issues
    DROP CONSTRAINT flag_issues_challenge_id_fkey,
    ADD CONSTRAINT flag_issues_challenge_id_fkey FOREIGN KEY (challenge_id)
        REFERENCES challenges(id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE submissions
    DROP CONSTRAINT submissions_challenge_id_fkey,
    ADD CONSTRAINT submissions_challenge_id_fkey FOREIGN KEY (challenge_id)
        REFERENCES challenges(id) ON DELETE CASCADE,
    DROP CONSTRAINT submissions_user_id_fkey,
    ADD CONSTRAINT submissions_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    DROP CONSTRAINT submissions_team_id_fkey,
    ADD CONSTRAINT submissions_team_id_fkey FOREIGN KEY (team_id)
        REFERENCES teams(id) ON DELETE CASCADE;

ALTER TABLE solves
    DROP CONSTRAINT solves_user_id_fkey,
    ADD CONSTRAINT solves_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    DROP CONSTRAINT solves_team_id_fkey,
    ADD CONSTRAINT solves_team_id_fkey FOREIGN KEY (team_id)
        REFERENCES teams(id) ON DELETE CASCADE;

ALTER TABLE awards
    DROP CONSTRAINT awards_user_id_fkey,
    ADD CONSTRAINT awards_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    DROP CONSTRAINT awards_team_id_fkey,
    ADD CONSTRAINT awards_team_id_fkey FOREIGN KEY (team_id)
        REFERENCES teams(id) ON DELETE CASCADE,
    DROP CONSTRAINT awards_challenge_id_fkey,
    ADD CONSTRAINT awards_challenge_id_fkey FOREIGN KEY (challenge_id)
        REFERENCES challenges(id) ON DELETE CASCADE;

ALTER TABLE hint_unlocks
    DROP CONSTRAINT hint_unlocks_hint_id_fkey,
    ADD CONSTRAINT hint_unlocks_hint_id_fkey FOREIGN KEY (hint_id)
        REFERENCES hints(id) ON DELETE CASCADE,
    DROP CONSTRAINT hint_unlocks_user_id_fkey,
    ADD CONSTRAINT hint_unlocks_user_id_fkey FOREIGN KEY (user_id)
        REFERENCES users(id) ON DELETE CASCADE,
    DROP CONSTRAINT hint_unlocks_team_id_fkey,
    ADD CONSTRAINT hint_unlocks_team_id_fkey FOREIGN KEY (team_id)
        REFERENCES teams(id) ON DELETE CASCADE;

ALTER TABLE flag_issues
    DROP CONSTRAINT flag_issues_challenge_id_fkey,
    ADD CONSTRAINT flag_issues_challenge_id_fkey FOREIGN KEY (challenge_id)
        REFERENCES challenges(id) ON DELETE CASCADE;
