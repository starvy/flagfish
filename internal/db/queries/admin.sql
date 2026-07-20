-- Admin mutations. Every statement here runs inside a transaction that has stamped the acting
-- admin into app.actor_id, so the capture triggers write the audit row in the same transaction
-- as the change itself.

-- ── challenges ──────────────────────────────────────────────────────────────────

-- name: AdminCreateChallenge :one
INSERT INTO challenges (
    name, category, description, attribution, connection_info,
    type, state, value, function, initial, minimum, decay,
    max_attempts, logic, position, first_blood, first_blood_bonus
) VALUES (
    @name, @category, @description, sqlc.narg(attribution), sqlc.narg(connection_info),
    @type, @state, @value, @function, sqlc.narg(initial), sqlc.narg(minimum), sqlc.narg(decay),
    @max_attempts, @logic, @position, @first_blood, sqlc.narg(first_blood_bonus)
)
RETURNING *;

-- name: AdminUpdateChallenge :one
-- Partial update with a third state for the nullable columns: an absent field keeps its value, a
-- clear flag nulls it, and a supplied value sets it. The three are exclusive per column — the
-- transport turns an omitted key into neither, an explicit null into the clear, and a value into
-- the narg. The dynamic-params CHECK still arbitrates the result, so clearing initial on a decayed
-- challenge is refused, not silently stored.
UPDATE challenges SET
    name            = COALESCE(sqlc.narg(name), name),
    category        = COALESCE(sqlc.narg(category), category),
    description     = COALESCE(sqlc.narg(description), description),
    attribution     = CASE WHEN @clear_attribution::bool THEN NULL
                           ELSE COALESCE(sqlc.narg(attribution), attribution) END,
    connection_info = CASE WHEN @clear_connection_info::bool THEN NULL
                           ELSE COALESCE(sqlc.narg(connection_info), connection_info) END,
    type            = COALESCE(sqlc.narg(type), type),
    value           = COALESCE(sqlc.narg(value), value),
    function        = COALESCE(sqlc.narg(function), function),
    initial         = CASE WHEN @clear_initial::bool THEN NULL
                           ELSE COALESCE(sqlc.narg(initial), initial) END,
    minimum         = CASE WHEN @clear_minimum::bool THEN NULL
                           ELSE COALESCE(sqlc.narg(minimum), minimum) END,
    decay           = CASE WHEN @clear_decay::bool THEN NULL
                           ELSE COALESCE(sqlc.narg(decay), decay) END,
    max_attempts    = COALESCE(sqlc.narg(max_attempts), max_attempts),
    logic           = COALESCE(sqlc.narg(logic), logic),
    position        = COALESCE(sqlc.narg(position), position),
    first_blood     = COALESCE(sqlc.narg(first_blood), first_blood),
    first_blood_bonus = CASE WHEN @clear_first_blood_bonus::bool THEN NULL
                             ELSE COALESCE(sqlc.narg(first_blood_bonus), first_blood_bonus) END,
    updated_at      = now()
WHERE id = @challenge_id
RETURNING *;

-- name: AdminReorderChallenges :execrows
-- Bulk position assignment in one statement: the two arrays are zipped by ordinality, so every
-- challenge moves or none does. A row count below the input length means an id did not exist.
UPDATE challenges c SET position = v.position, updated_at = now()
FROM (
    SELECT unnest(@ids::bigint[]) AS id,
           unnest(@positions::int[]) AS position
) v
WHERE c.id = v.id;

-- name: AdminSetChallengeState :one
UPDATE challenges SET state = @state, updated_at = now()
WHERE id = @challenge_id
RETURNING *;

-- name: AdminGetChallenge :one
SELECT * FROM challenges WHERE id = @challenge_id;

-- name: AdminDeleteChallenge :execrows
-- Flags, hints, tags and file links cascade. Ledger rows (solves, submissions, awards, hint
-- unlocks, issued flags) RESTRICT: a challenge with recorded history cannot be deleted, and the
-- caller maps that violation to a conflict.
DELETE FROM challenges WHERE id = @challenge_id;

-- ── flags ───────────────────────────────────────────────────────────────────────

-- name: AdminInsertFlag :one
INSERT INTO flags (challenge_id, type, content, case_insensitive)
VALUES (@challenge_id, @type, @content, @case_insensitive)
RETURNING *;

-- name: AdminGetFlag :one
SELECT * FROM flags WHERE id = @flag_id AND challenge_id = @challenge_id;

-- name: AdminUpdateFlag :one
-- Scoped by challenge_id in the WHERE, not checked in Go: a flag id from another challenge's URL
-- affects zero rows, so there is no window and no forgotten guard.
UPDATE flags SET
    type             = COALESCE(sqlc.narg(type), type),
    content          = COALESCE(sqlc.narg(content), content),
    case_insensitive = COALESCE(sqlc.narg(case_insensitive), case_insensitive)
WHERE id = @flag_id AND challenge_id = @challenge_id
RETURNING *;

-- name: AdminDeleteFlag :execrows
DELETE FROM flags WHERE id = @flag_id AND challenge_id = @challenge_id;

-- ── hints ───────────────────────────────────────────────────────────────────────

-- name: AdminInsertHint :one
INSERT INTO hints (challenge_id, title, content, cost, position)
VALUES (@challenge_id, sqlc.narg(title), @content, @cost, @position)
RETURNING *;

-- name: AdminUpdateHint :one
UPDATE hints SET
    title    = COALESCE(sqlc.narg(title), title),
    content  = COALESCE(sqlc.narg(content), content),
    cost     = COALESCE(sqlc.narg(cost), cost),
    position = COALESCE(sqlc.narg(position), position)
WHERE id = @hint_id AND challenge_id = @challenge_id
RETURNING *;

-- name: AdminDeleteHint :execrows
DELETE FROM hints WHERE id = @hint_id AND challenge_id = @challenge_id;

-- ── users ───────────────────────────────────────────────────────────────────────

-- name: AdminListUsers :many
-- COUNT(*) OVER () carries the total in the same round trip, so the pagination header never
-- disagrees with the page it describes.
SELECT id, name, email, role, verified, banned, hidden, team_id, created_at,
       COUNT(*) OVER () AS total
  FROM users
 ORDER BY id
 LIMIT @lim::int OFFSET @off::int;

-- name: AdminSetUserBanned :one
UPDATE users SET banned = @banned WHERE id = @user_id
RETURNING id, name, banned;

-- name: AdminSetUserRole :one
UPDATE users SET role = @role WHERE id = @user_id
RETURNING id, name, role;

-- name: AdminCountOtherAdmins :one
SELECT count(*) FROM users WHERE role = 'admin' AND banned = false AND id <> @user_id;

-- ── audit trail ───────────────────────────────────────────────────────────────────

-- name: AdminListAudit :many
-- Read-only feed over the capture triggers' output, newest first. Each filter is optional and
-- narrows independently; an unset one drops out of the WHERE rather than matching a sentinel. The
-- (at, id) tiebreak keeps the order total when many rows share a timestamp, so pages never overlap.
-- COUNT(*) OVER () rides along so the page and its total agree in one round trip.
SELECT id, actor_id, action, target_table, target_id, before, after, at, ip,
       COUNT(*) OVER () AS total
  FROM audit_log
 WHERE (sqlc.narg(actor_id)::bigint IS NULL OR actor_id = sqlc.narg(actor_id))
   AND (sqlc.narg(action)::text IS NULL OR action = sqlc.narg(action))
   AND (sqlc.narg(target_table)::text IS NULL OR target_table = sqlc.narg(target_table))
   AND (sqlc.narg(target_id)::bigint IS NULL OR target_id = sqlc.narg(target_id))
 ORDER BY at DESC, id DESC
 LIMIT @lim::int OFFSET @off::int;

-- ── tags ──────────────────────────────────────────────────────────────────────────
--
-- A tag is a (challenge_id, value) row; the same value on many challenges is one tag with several
-- uses. These statements treat a tag by its value, which is the unit an operator manages.

-- name: AdminListTags :many
SELECT value, count(*) AS uses
  FROM tags
 GROUP BY value
 ORDER BY value;

-- name: AdminCountTagUses :one
SELECT count(*) FROM tags WHERE value = @value;

-- name: AdminMergeTagCollisions :execrows
-- Drop the source rows on challenges that already carry the destination, so the rename that follows
-- cannot trip UNIQUE(challenge_id, value). These deletions are real merges and are audited as such.
DELETE FROM tags t
 WHERE t.value = @from_value
   AND EXISTS (SELECT 1 FROM tags o WHERE o.challenge_id = t.challenge_id AND o.value = @to_value);

-- name: AdminRenameTag :execrows
UPDATE tags SET value = @to_value WHERE value = @from_value;

-- name: AdminDeleteTag :execrows
DELETE FROM tags WHERE value = @value;
