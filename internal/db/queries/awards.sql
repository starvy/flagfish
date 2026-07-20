-- Manual awards: the admin write surface for out-of-band point adjustments — a cheating penalty,
-- a live-dispute correction, a make-good. A manual award is always type='standard'; the ledger's
-- other award types (hint_unlock, first_blood) are gameplay facts stamped under the challenge lock,
-- never an admin decision. The scoreboard already sums awards.value alongside solves.value, so a
-- single INSERT here moves standings and nothing on the read side changes.

-- name: GrantUserAward :one
-- Users mode: the scoring account is the user. team_id rides along from the user's own row (NULL in
-- users mode), exactly as the submit path writes both. Zero rows ⇒ no such user.
INSERT INTO awards (user_id, team_id, type, name, description, value)
SELECT u.id, u.team_id, 'standard', 'manual adjustment', @reason, @value
  FROM users u
 WHERE u.id = @user_id
RETURNING id, user_id, team_id, value, description, date;

-- name: GrantTeamAward :one
-- Teams mode: the scoring account is the team. awards.user_id is NOT NULL, so the adjustment is
-- recorded against the team's captain — its representative member, kept current by the enrolment
-- trigger. A team with no members (captain_id IS NULL) has no way to score and is refused as zero
-- rows rather than silently violating the NOT NULL.
INSERT INTO awards (user_id, team_id, type, name, description, value)
SELECT t.captain_id, t.id, 'standard', 'manual adjustment', @reason, @value
  FROM teams t
 WHERE t.id = @team_id AND t.captain_id IS NOT NULL
RETURNING id, user_id, team_id, value, description, date;

-- name: ListUserManualAwards :many
-- Only type='standard' rows: hint spends and first-blood bonuses are gameplay facts, not admin
-- adjustments, and must never appear in a revoke list.
SELECT id, value, description, date
  FROM awards
 WHERE user_id = @user_id AND type = 'standard'
 ORDER BY date DESC, id DESC;

-- name: ListTeamManualAwards :many
SELECT id, value, description, date
  FROM awards
 WHERE team_id = @team_id AND type = 'standard'
 ORDER BY date DESC, id DESC;

-- name: RevokeManualAward :one
-- A revoke is a real DELETE, never a mutation of a ledger row: scoreboard time-travel replays the
-- ledger, so a revoked adjustment must read as never-having-happened, not as a rewritten fact. The
-- WHERE type='standard' inside the delete is the guard — a hint_unlock or first_blood award is a
-- gameplay fact and cannot be deleted through this path whatever id is passed. The surrounding CTE
-- reads the row's type once so the caller can tell "no such award" from "refused: not manual",
-- without a check-then-delete window: the delete's own predicate, not the read, is what protects the
-- gameplay awards.
WITH target AS (
    SELECT a.id AS award_id, a.type AS award_type FROM awards a WHERE a.id = @id
),
del AS (
    DELETE FROM awards
     WHERE awards.id = (SELECT t.award_id FROM target t WHERE t.award_type = 'standard')
    RETURNING awards.id AS deleted_id
)
-- COALESCE to non-null sentinels so the single result row scans cleanly whatever happened: an empty
-- found_type means "no such award" (type is never empty), a zero deleted_id means "not deleted"
-- (ids start at 1). The caller reads the two together to pick the outcome.
SELECT COALESCE((SELECT t.award_type FROM target t), '')::text   AS found_type,
       COALESCE((SELECT d.deleted_id FROM del d), 0)::bigint      AS deleted_id;
