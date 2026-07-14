-- Bulk restore path for the archive importer (internal/platform/importer).
--
-- Every insert here is a :copyfrom: the loader streams whole translated tables through the binary
-- COPY protocol inside one transaction, with explicit ids so the archive's identity is preserved.
-- The transaction runs under `session_replication_role = replica`, so foreign keys and audit
-- triggers are suppressed during the COPY and the tables can be loaded in any order; unique and
-- check constraints stay live, which is what turns a conflicting archive into an atomic failure
-- rather than a silently partial import. Dangling references are caught explicitly afterwards by
-- the Count*WithoutParent probes below.

-- name: ImportBrackets :copyfrom
INSERT INTO brackets (id, name, description, applies_to)
VALUES ($1, $2, $3, $4);

-- name: ImportTeams :copyfrom
INSERT INTO teams (
    id, name, email, password_hash, secret, website, affiliation, country,
    bracket_id, captain_id, hidden, banned, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: ImportUsers :copyfrom
INSERT INTO users (
    id, name, email, password_hash, role, secret, website, affiliation, country,
    language, bracket_id, team_id, hidden, banned, verified, must_change_password, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17);

-- name: ImportChallenges :copyfrom
INSERT INTO challenges (
    id, name, category, description, attribution, connection_info, type, state, value,
    function, initial, minimum, decay, max_attempts, logic, position, next_id, requirements,
    flag_mode, first_blood, first_blood_bonus, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
    $19, $20, $21, $22, $23
);

-- name: ImportFiles :copyfrom
INSERT INTO files (id, location, sha256sum, size_bytes, challenge_id, created_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ImportFlags :copyfrom
INSERT INTO flags (id, challenge_id, type, content, case_insensitive)
VALUES ($1, $2, $3, $4, $5);

-- name: ImportTags :copyfrom
INSERT INTO tags (id, challenge_id, value)
VALUES ($1, $2, $3);

-- name: ImportHints :copyfrom
INSERT INTO hints (id, challenge_id, title, content, cost, requirements, position)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ImportSubmissions :copyfrom
INSERT INTO submissions (
    id, challenge_id, user_id, team_id, type, provided, ip, date, attributed_account_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ImportSolves :copyfrom
INSERT INTO solves (id, submission_id, challenge_id, user_id, team_id, value, date)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ImportAwards :copyfrom
INSERT INTO awards (
    id, user_id, team_id, type, challenge_id, name, description, value, category, icon, date
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: ImportConfig :copyfrom
INSERT INTO config (key, value)
VALUES ($1, $2);

-- Referential-integrity probes. session_replication_role=replica ignores foreign keys during the
-- COPY rather than deferring them, so re-enabling it validates nothing — a dangling reference has to
-- be counted explicitly, still inside the transaction, so the whole restore rolls back on the first
-- one found.

-- name: CountFlagsWithoutChallenge :one
SELECT count(*) FROM flags f
    LEFT JOIN challenges c ON c.id = f.challenge_id
    WHERE c.id IS NULL;

-- name: CountTagsWithoutChallenge :one
SELECT count(*) FROM tags t
    LEFT JOIN challenges c ON c.id = t.challenge_id
    WHERE c.id IS NULL;

-- name: CountHintsWithoutChallenge :one
SELECT count(*) FROM hints h
    LEFT JOIN challenges c ON c.id = h.challenge_id
    WHERE c.id IS NULL;

-- name: CountFilesWithoutChallenge :one
SELECT count(*) FROM files f
    WHERE f.challenge_id IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM challenges c WHERE c.id = f.challenge_id);

-- name: CountUsersWithoutTeam :one
SELECT count(*) FROM users u
    WHERE u.team_id IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM teams t WHERE t.id = u.team_id);

-- name: CountTeamsWithoutCaptain :one
SELECT count(*) FROM teams t
    WHERE t.captain_id IS NOT NULL
      AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = t.captain_id);

-- name: CountSubmissionsWithoutParent :one
SELECT count(*) FROM submissions s
    WHERE NOT EXISTS (SELECT 1 FROM challenges c WHERE c.id = s.challenge_id)
       OR NOT EXISTS (SELECT 1 FROM users u WHERE u.id = s.user_id);

-- name: CountSolvesWithoutParent :one
SELECT count(*) FROM solves s
    WHERE NOT EXISTS (SELECT 1 FROM challenges c WHERE c.id = s.challenge_id)
       OR NOT EXISTS (SELECT 1 FROM users u WHERE u.id = s.user_id)
       OR (s.submission_id IS NOT NULL
           AND NOT EXISTS (SELECT 1 FROM submissions sub WHERE sub.id = s.submission_id));

-- name: CountAwardsWithoutParent :one
SELECT count(*) FROM awards a
    WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = a.user_id)
       OR (a.challenge_id IS NOT NULL
           AND NOT EXISTS (SELECT 1 FROM challenges c WHERE c.id = a.challenge_id));
