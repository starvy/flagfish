-- Team enrollment and the team profile read models.

-- name: CreateTeam :one
-- The unique name index and the num_teams caps trigger arbitrate: a duplicate raises 23505 and an
-- over-cap insert raises a check_violation on the INSERT itself, never in a prior SELECT.
INSERT INTO teams (name, password_hash, captain_id)
VALUES (@name, @password_hash, @captain_id)
RETURNING id, name, created_at;

-- name: EnrollUser :execrows
-- "Already on a team" is the WHERE clause, not a prior read: zero rows means the user was enrolled
-- elsewhere by the time this ran. The caps trigger enforces team_size on the same statement.
UPDATE users SET team_id = @team_id
 WHERE id = @user_id AND team_id IS NULL;

-- name: GetTeamForJoin :one
SELECT id, password_hash, banned FROM teams WHERE name = @name;

-- name: UpdateTeamPasswordHash :exec
-- Rehash-on-join: an imported bcrypt join password is upgraded while the plaintext is in hand.
UPDATE teams SET password_hash = @password_hash WHERE id = @team_id;

-- name: SetJoinSecretByCaptain :execrows
-- Captaincy is the WHERE clause, so a demoted captain's in-flight rotate affects zero rows rather
-- than racing past a check. Zero rows also covers a caller who is on no team at all.
UPDATE teams SET password_hash = @password_hash WHERE captain_id = @captain_id;

-- name: AdoptCaptainlessTeam :exec
-- A sole member adopts a captainless team (the captain's user row was deleted, FK SET NULL).
UPDATE teams t SET captain_id = @user_id
 WHERE t.id = @team_id AND t.captain_id IS NULL
   AND (SELECT count(*) FROM users u WHERE u.team_id = t.id) = 1;

-- name: LeaveTeam :execrows
-- Departure is forbidden once the team has scored: solves stamp team_id, so a roster that can
-- shrink after scoring would misattribute the board. The condition lives in the statement so two
-- racing writes (a leave and a solve) serialize in the database, not in Go.
UPDATE users u SET team_id = NULL
 WHERE u.id = @user_id AND u.team_id = @team_id
   AND NOT EXISTS (SELECT 1 FROM solves s WHERE s.team_id = u.team_id);

-- name: ReassignCaptainAfterLeave :exec
-- min(id) is deterministic and NULL empties the seat when the last member walks out.
UPDATE teams t
   SET captain_id = (SELECT min(u.id) FROM users u WHERE u.team_id = t.id)
 WHERE t.id = @team_id AND t.captain_id = @user_id;

-- name: GetTeamPublicProfile :one
-- Hidden and banned teams 404 publicly, matching the board and the solve lists. The score sums
-- the stamped solves.team_id ledger — the same legs the scoreboard reads, and it takes the same
-- freeze horizon: cutoff is strict `<`, NULL = live. Both legs carry it, because a public team
-- page that sums live is the frozen board read one team at a time.
SELECT t.id, t.name, t.website, t.affiliation, t.country, t.created_at,
       (COALESCE((SELECT sum(s.value) FROM solves s
                   WHERE s.team_id = t.id
                     AND (sqlc.narg(cutoff)::timestamptz IS NULL
                          OR s.date < sqlc.narg(cutoff)::timestamptz)), 0)
      + COALESCE((SELECT sum(a.value) FROM awards a
                   WHERE a.team_id = t.id
                     AND (sqlc.narg(cutoff)::timestamptz IS NULL
                          OR a.date < sqlc.narg(cutoff)::timestamptz)), 0))::bigint AS score
  FROM teams t
 WHERE t.id = @team_id AND t.hidden = false AND t.banned = false;

-- name: UpdateTeamByCaptain :one
-- Captaincy is the WHERE clause, not a prior read: zero rows means the caller is not the captain
-- (or has no team) by the time this runs, so there is no window and no forgotten guard.
UPDATE teams t SET
    email       = CASE WHEN @clear_email::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(email), t.email) END,
    website     = CASE WHEN @clear_website::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(website), t.website) END,
    affiliation = CASE WHEN @clear_affiliation::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(affiliation), t.affiliation) END,
    country     = CASE WHEN @clear_country::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(country), t.country) END
WHERE t.id = (SELECT u.team_id FROM users u WHERE u.id = @user_id)
  AND t.captain_id = @user_id
RETURNING t.id;

-- name: GetOwnTeam :one
-- No cutoff: an account always sees its own live score, freeze or not. Withholding it would tell
-- the team nothing an attacker wants and everything they already know.
-- t.email rides along because this is the team's own view — the public profile never selects it.
SELECT t.id, t.name, t.email, t.website, t.affiliation, t.country, t.created_at, t.captain_id,
       (COALESCE((SELECT sum(s.value) FROM solves s WHERE s.team_id = t.id), 0)
      + COALESCE((SELECT sum(a.value) FROM awards a WHERE a.team_id = t.id), 0))::bigint AS score
  FROM teams t
  JOIN users u ON u.team_id = t.id
 WHERE u.id = @user_id;

-- name: KickMember :execrows
-- Removal is the captain's alone, over a current teammate who is not the captain, and only while
-- the team has never scored. All three live in the WHERE, so a demoted captain, a stale target, or
-- a team already on the board changes zero rows — there is no check to race past. The scored guard
-- mirrors leave: solves stamp team_id, so a roster that can shrink after scoring would leave the
-- board attributing points to a lineup nobody can reconstruct.
UPDATE users m SET team_id = NULL
 WHERE m.id = @member_id
   AND m.id <> @captain_id
   AND EXISTS (SELECT 1 FROM teams t
                WHERE t.id = m.team_id AND t.captain_id = @captain_id)
   AND NOT EXISTS (SELECT 1 FROM solves s WHERE s.team_id = m.team_id);

-- name: TransferCaptaincy :execrows
-- The seat moves only from the current captain to a current teammate; both facts are the WHERE, so
-- a demoted captain or a non-member target moves zero rows. Transfer carries no scored guard — it
-- changes who holds the seat, never the roster, so it cannot misattribute a solve.
UPDATE teams t SET captain_id = @new_captain_id
 WHERE t.captain_id = @captain_id
   AND EXISTS (SELECT 1 FROM users m WHERE m.id = @new_captain_id AND m.team_id = t.id);

-- name: DisbandTeam :execrows
-- Disband is a plain delete guarded only by captaincy. The RESTRICT foreign keys from the ledger
-- (submissions, solves, awards, hint_unlocks) refuse the delete the instant the team has any
-- history, surfacing as a foreign_key_violation the API turns into a conflict — a scored team is
-- retired by hide/ban, never erased. Any remaining members fall to team_id NULL via the ON DELETE
-- SET NULL on users, so a zero-history team created by mistake vanishes cleanly.
DELETE FROM teams t
 WHERE t.id = (SELECT u.team_id FROM users u WHERE u.id = @captain_id)
   AND t.captain_id = @captain_id;

-- name: TeamCaptainScored :one
-- Read-side disambiguation for the roster mutations above: which of captaincy, membership, or the
-- scored guard turned a zero-row write away. Off the hot path — it runs only to shape an error.
SELECT t.captain_id,
       EXISTS (SELECT 1 FROM solves s WHERE s.team_id = t.id)::boolean AS scored
  FROM teams t WHERE t.id = @team_id;

-- name: ListTeamMembers :many
-- Per-member attribution reads the stamped solves.team_id, so points stay with the team that
-- scored them regardless of later roster churn. include_masked lifts the hidden/banned member
-- filter for the team's own view; cutoff is the freeze horizon (strict `<`, NULL = live) for the
-- public one, where an unclamped per-member breakdown says who scored what during the freeze.
SELECT u.id, u.name,
       COALESCE(u.id = t.captain_id, false)::boolean AS captain,
       (SELECT count(*) FROM solves s
         WHERE s.team_id = t.id AND s.user_id = u.id
           AND (sqlc.narg(cutoff)::timestamptz IS NULL
                OR s.date < sqlc.narg(cutoff)::timestamptz))::bigint AS solve_count,
       COALESCE((SELECT sum(s.value) FROM solves s
                  WHERE s.team_id = t.id AND s.user_id = u.id
                    AND (sqlc.narg(cutoff)::timestamptz IS NULL
                         OR s.date < sqlc.narg(cutoff)::timestamptz)), 0)::bigint AS points
  FROM users u
  JOIN teams t ON t.id = u.team_id
 WHERE u.team_id = @team_id
   AND (sqlc.arg(include_masked)::boolean OR (u.hidden = false AND u.banned = false))
 ORDER BY u.id
 -- Defence in depth: a roster is already bounded by the team_size cap at enrollment, so this trims
 -- nothing a real team can reach. It exists so the query is bounded by its own text, not by trust in
 -- a config value, on a route any player can hit.
 LIMIT 1000;
