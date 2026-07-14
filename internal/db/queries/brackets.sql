-- Brackets: named divisions that partition the scoreboard. A bracket is a filter over the one
-- global ranking, never a separate scoring pool, so the standings query joins it as a WHERE
-- predicate (scoreboard.sql). Membership is a single nullable FK on the account, so an account
-- belongs to at most one bracket by construction, and deleting a bracket only nulls its members.

-- name: ListBrackets :many
-- All brackets, optionally scoped to one account kind. The public list passes the instance mode so
-- a client only offers filters that can match; the admin list passes nothing and sees every row.
SELECT * FROM brackets
 WHERE (sqlc.narg(applies_to)::text IS NULL OR applies_to = sqlc.narg(applies_to)::text)
 ORDER BY id;

-- name: GetBracket :one
SELECT * FROM brackets WHERE id = @bracket_id;

-- name: AdminCreateBracket :one
INSERT INTO brackets (name, description, applies_to)
VALUES (@name, sqlc.narg(description), @applies_to)
RETURNING *;

-- name: AdminUpdateBracket :one
-- Partial update: an absent field keeps its value. applies_to is immutable — flipping it would
-- silently strand every current member, whose account kind no longer matches.
UPDATE brackets SET
    name        = COALESCE(sqlc.narg(name), name),
    description = COALESCE(sqlc.narg(description), description)
WHERE id = @bracket_id
RETURNING *;

-- name: AdminDeleteBracket :execrows
-- Members are not blocked: bracket_id is ON DELETE SET NULL, so a delete simply unassigns them.
DELETE FROM brackets WHERE id = @bracket_id;

-- name: AdminAssignUserBracket :one
-- bracket_id NULL clears the assignment. Touching bracket_id does not trip the registration-cap
-- trigger, which fires only on team_id.
UPDATE users SET bracket_id = sqlc.narg(bracket_id) WHERE id = @user_id
RETURNING id, name, bracket_id;

-- name: AdminAssignTeamBracket :one
UPDATE teams SET bracket_id = sqlc.narg(bracket_id) WHERE id = @team_id
RETURNING id, name, bracket_id;
