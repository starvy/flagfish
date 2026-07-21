-- Admin-side roster repair: an organizer acting on someone else's team. The captain's own
-- kick/leave path lives in teams.sql; these are the organizer's equivalents, and they differ in
-- exactly one way — see AdminDetachMember.

-- name: AdminListTeamMembers :many
-- The admin roster view. email is admin-only data, which is why it is selected here and never on
-- the public team page.
SELECT u.id, u.name, u.email, u.banned, u.hidden,
       COALESCE(u.id = t.captain_id, false)::boolean AS captain
  FROM users u
  JOIN teams t ON t.id = u.team_id
 WHERE u.team_id = @team_id
 ORDER BY u.id;

-- name: AdminDetachMember :execrows
-- Membership is the WHERE, not a prior read: zero rows means the user was not on that team by the
-- time this ran, and the caller turns that into a named refusal instead of a silent success.
--
-- Unlike the captain's kick this carries no "team has never scored" guard, and that is deliberate.
-- Every ledger row stamps team_id when it is written, so points already earned stay credited to the
-- team that earned them no matter who leaves afterwards. Fixing a roster mid-event is the entire
-- reason this statement exists; the audit trigger records who did it.
UPDATE users u SET team_id = NULL
 WHERE u.id = @user_id AND u.team_id = @team_id;

-- name: AdminTeamExists :one
-- Read-side disambiguation only: it shapes "no such team" versus "user is not on that team" after a
-- zero-row write. The foreign key, not this, is what actually refuses a bad target.
SELECT EXISTS (SELECT 1 FROM teams t WHERE t.id = @team_id)::boolean AS present;

-- name: AdminTeamIsBanned :one
-- Whether a move's destination would wall its arrival out of the API.
SELECT t.banned FROM teams t WHERE t.id = @team_id;
