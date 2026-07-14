-- Read models for the challenge board and challenge detail. All account-scoped predicates resolve the
-- mode from the instance singleton, the same way the hot path and scoreboard do, so "solved by me" and
-- the solve count can never be computed against the wrong column.

-- name: ListChallenges :many
-- The visible board. solve_count and solved are scoped to visible accounts, matching the decay and
-- first-blood exclusion, so a hidden admin test-solve does not inflate a challenge's count.
-- cutoff is the freeze horizon (strict <, NULL = live): a frozen viewer's solve_count must not
-- tick up on a post-freeze solve, or the count leaks what the frozen board hides.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT
    c.id, c.name, c.category, c.value, c.function, c.state, c.requirements,
    (SELECT count(*)
       FROM solves s
       CROSS JOIN mode m
       LEFT JOIN users su ON su.id = s.user_id
       LEFT JOIN teams st ON st.id = s.team_id
      WHERE s.challenge_id = c.id
        AND (sqlc.narg(cutoff)::timestamptz IS NULL OR s.date < sqlc.narg(cutoff)::timestamptz)
        AND CASE WHEN m.user_mode = 'teams'
                 THEN st.hidden = false AND st.banned = false
                 ELSE su.hidden = false AND su.banned = false
            END
    )::bigint AS solve_count,
    EXISTS (
        SELECT 1 FROM solves s
        CROSS JOIN mode m
        WHERE s.challenge_id = c.id
          AND CASE WHEN m.user_mode = 'teams'
                   THEN s.team_id = sqlc.narg(team_id)::bigint
                   ELSE s.user_id = sqlc.arg(user_id)::bigint
              END
    ) AS solved,
    -- Every prerequisite challenge in requirements.prerequisites has a solve by this account. A
    -- challenge with no prerequisites yields zero elements to check, so it is met by default. The
    -- caller decides whether an unmet challenge is hidden or shown locked, from the anonymize flag.
    NOT EXISTS (
        SELECT 1
          FROM jsonb_array_elements_text(c.requirements -> 'prerequisites') AS req(pid)
         WHERE NOT EXISTS (
            SELECT 1 FROM solves ps
            CROSS JOIN mode pm
            WHERE ps.challenge_id = req.pid::bigint
              AND CASE WHEN pm.user_mode = 'teams'
                       THEN ps.team_id = sqlc.narg(team_id)::bigint
                       ELSE ps.user_id = sqlc.arg(user_id)::bigint
                  END
         )
    ) AS prereqs_met
  FROM challenges c
 WHERE c.state = 'visible'
 ORDER BY c.category, c.position, c.id;

-- name: GetChallengeForView :one
-- A single visible challenge with the same solve_count / solved projection (and freeze cutoff)
-- as the board.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT
    c.id, c.name, c.category, c.description, c.attribution, c.connection_info,
    c.type, c.value, c.function, c.max_attempts, c.state, c.requirements, c.flag_mode,
    (SELECT count(*)
       FROM solves s
       CROSS JOIN mode m
       LEFT JOIN users su ON su.id = s.user_id
       LEFT JOIN teams st ON st.id = s.team_id
      WHERE s.challenge_id = c.id
        AND (sqlc.narg(cutoff)::timestamptz IS NULL OR s.date < sqlc.narg(cutoff)::timestamptz)
        AND CASE WHEN m.user_mode = 'teams'
                 THEN st.hidden = false AND st.banned = false
                 ELSE su.hidden = false AND su.banned = false
            END
    )::bigint AS solve_count,
    EXISTS (
        SELECT 1 FROM solves s
        CROSS JOIN mode m
        WHERE s.challenge_id = c.id
          AND CASE WHEN m.user_mode = 'teams'
                   THEN s.team_id = sqlc.narg(team_id)::bigint
                   ELSE s.user_id = sqlc.arg(user_id)::bigint
              END
    ) AS solved,
    -- Same prerequisite projection as the board: unmet means the caller withholds solvable
    -- content (or 404s the whole row when the anonymize flag hides it).
    NOT EXISTS (
        SELECT 1
          FROM jsonb_array_elements_text(c.requirements -> 'prerequisites') AS req(pid)
         WHERE NOT EXISTS (
            SELECT 1 FROM solves ps
            CROSS JOIN mode pm
            WHERE ps.challenge_id = req.pid::bigint
              AND CASE WHEN pm.user_mode = 'teams'
                       THEN ps.team_id = sqlc.narg(team_id)::bigint
                       ELSE ps.user_id = sqlc.arg(user_id)::bigint
                  END
         )
    ) AS prereqs_met
  FROM challenges c
 WHERE c.id = @challenge_id AND c.state = 'visible';

-- name: ListChallengeTags :many
SELECT value FROM tags WHERE challenge_id = @challenge_id ORDER BY value;

-- name: ListChallengeFiles :many
-- The board shows the name and size; the download link is built from the id. location is the storage
-- key and never leaves the server.
SELECT id, name, size_bytes FROM files WHERE challenge_id = @challenge_id ORDER BY id;

-- name: ListChallengeHints :many
-- Hint content is a purchase, never listed: the row carries the price and whether this account already
-- paid it, and the content itself is delivered only by UnlockHint.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT
    h.id, h.title, h.cost, h.position,
    EXISTS (
        SELECT 1 FROM hint_unlocks hu
        CROSS JOIN mode m
        WHERE hu.hint_id = h.id
          AND CASE WHEN m.user_mode = 'teams'
                   THEN hu.team_id = sqlc.narg(team_id)::bigint
                   ELSE hu.user_id = sqlc.arg(user_id)::bigint
              END
    ) AS unlocked,
    -- Locked: a prerequisite hint has not been unlocked by this account, so UnlockHint will reject
    -- the purchase. A hint with no prerequisites has zero elements to check and is never locked.
    EXISTS (
        SELECT 1
          FROM jsonb_array_elements_text(h.requirements -> 'prerequisites') AS req(pid)
         WHERE NOT EXISTS (
            SELECT 1 FROM hint_unlocks phu
            CROSS JOIN mode pm
            WHERE phu.hint_id = req.pid::bigint
              AND CASE WHEN pm.user_mode = 'teams'
                       THEN phu.team_id = sqlc.narg(team_id)::bigint
                       ELSE phu.user_id = sqlc.arg(user_id)::bigint
                  END
         )
    ) AS locked
  FROM hints h
 WHERE h.challenge_id = @challenge_id
 ORDER BY h.position, h.id;

-- name: ListChallengeSolves :many
-- Who solved a challenge, oldest first, with cutoff (strict <, NULL = live) as the freeze horizon.
-- The challenge is the driving table and EVERY solve/account predicate lives in an ON clause, so the
-- two empty cases stay distinguishable in one round trip: zero rows means no visible challenge (the
-- caller 404s), while `shown = false` marks a row the challenge kept but the projection must hide —
-- no visible solve at all, or a hidden/banned solver filtered out. The caller drops the unshown rows.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT
    COALESCE(u.name, t.name, '')     AS name,
    COALESCE(s.value, 0)::int        AS value,
    s.date,
    (COALESCE(u.id, t.id) IS NOT NULL)::boolean AS shown
  FROM challenges c
  CROSS JOIN mode m
  LEFT JOIN solves s
         ON s.challenge_id = c.id
        AND (sqlc.narg(cutoff)::timestamptz IS NULL OR s.date < sqlc.narg(cutoff)::timestamptz)
  LEFT JOIN users u
         ON m.user_mode = 'users' AND u.id = s.user_id AND u.hidden = false AND u.banned = false
  LEFT JOIN teams t
         ON m.user_mode = 'teams' AND t.id = s.team_id AND t.hidden = false AND t.banned = false
 WHERE c.id = @challenge_id AND c.state = 'visible'
 ORDER BY s.date ASC, s.id ASC;
