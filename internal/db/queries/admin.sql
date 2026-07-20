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

-- name: AdminSetChallengeRequirements :one
-- Whole-value replace: the column is one document, so a partial patch has no meaning here.
UPDATE challenges SET requirements = @requirements::jsonb, updated_at = now()
WHERE id = @challenge_id
RETURNING *;

-- name: AdminFilterChallengeIDs :many
-- Existence probe for prerequisite validation: the caller diffs the echo against its input to name
-- the ids that do not exist.
SELECT id FROM challenges WHERE id = ANY(@ids::bigint[]);

-- name: AdminListChallengeRequirements :many
-- The whole prerequisite graph, for the cycle warning on requirement writes. Boards are small; one
-- read beats a traversal query nothing else needs.
SELECT id, requirements FROM challenges;

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
INSERT INTO hints (challenge_id, title, content, cost, position, requirements)
VALUES (@challenge_id, sqlc.narg(title), @content, @cost, @position, @requirements::jsonb)
RETURNING *;

-- name: AdminUpdateHint :one
UPDATE hints SET
    title        = COALESCE(sqlc.narg(title), title),
    content      = COALESCE(sqlc.narg(content), content),
    cost         = COALESCE(sqlc.narg(cost), cost),
    position     = COALESCE(sqlc.narg(position), position),
    requirements = COALESCE(sqlc.narg(requirements)::jsonb, requirements)
WHERE id = @hint_id AND challenge_id = @challenge_id
RETURNING *;

-- name: AdminFilterHintIDs :many
-- Existence probe for hint-prerequisite validation, scoped to the challenge: a cross-challenge id
-- filters out here and is reported by the caller the same as one that does not exist at all.
SELECT id FROM hints WHERE id = ANY(@ids::bigint[]) AND challenge_id = @challenge_id;

-- name: AdminDeleteHint :execrows
DELETE FROM hints WHERE id = @hint_id AND challenge_id = @challenge_id;

-- ── users ───────────────────────────────────────────────────────────────────────

-- name: AdminListUsers :many
-- COUNT(*) OVER () carries the total in the same round trip, so the pagination header never
-- disagrees with the page it describes. The (q, field) search mirrors the team list; email is
-- searchable here and nowhere public.
SELECT id, name, email, role, verified, banned, hidden, team_id,
       website, affiliation, country, must_change_password, created_at,
       COUNT(*) OVER () AS total
  FROM users
 WHERE (sqlc.narg(q)::text IS NULL OR CASE sqlc.narg(field)::text
            WHEN 'email'       THEN email       ILIKE '%' || sqlc.narg(q) || '%'
            WHEN 'website'     THEN website     ILIKE '%' || sqlc.narg(q) || '%'
            WHEN 'affiliation' THEN affiliation ILIKE '%' || sqlc.narg(q) || '%'
            WHEN 'country'     THEN country     ILIKE '%' || sqlc.narg(q) || '%'
            ELSE name ILIKE '%' || sqlc.narg(q) || '%'
        END)
 ORDER BY id
 LIMIT @lim::int OFFSET @off::int;

-- name: AdminGetUser :one
SELECT id, name, email, role, verified, banned, hidden, team_id, bracket_id,
       website, affiliation, country, must_change_password, created_at
  FROM users
 WHERE id = @user_id;

-- name: AdminUpdateUser :one
-- Deliberately narrow SET: email, role, banned, hidden, team_id and must_change_password are not
-- reachable from this statement — each moves through its own route or not at all.
UPDATE users SET
    name        = COALESCE(sqlc.narg(name), name),
    website     = CASE WHEN @clear_website::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(website), website) END,
    affiliation = CASE WHEN @clear_affiliation::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(affiliation), affiliation) END,
    country     = CASE WHEN @clear_country::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(country), country) END
WHERE id = @user_id
RETURNING id, name, email, role, verified, banned, hidden, team_id, bracket_id,
          website, affiliation, country, must_change_password, created_at;

-- name: AdminSetUserHidden :one
UPDATE users SET hidden = @hidden WHERE id = @user_id
RETURNING id, name, hidden;

-- name: AdminForcePasswordChange :one
-- Set-only: the clear belongs to the password change itself, in the same statement as the new
-- hash, so the order can never discharge without the password that satisfies it.
UPDATE users SET must_change_password = true WHERE id = @user_id
RETURNING id, name, must_change_password;

-- name: AdminSetUserBanned :one
UPDATE users SET banned = @banned WHERE id = @user_id
RETURNING id, name, banned;

-- name: AdminSetUserRole :one
UPDATE users SET role = @role WHERE id = @user_id
RETURNING id, name, role;

-- name: AdminCountOtherAdmins :one
SELECT count(*) FROM users WHERE role = 'admin' AND banned = false AND id <> @user_id;

-- ── teams ───────────────────────────────────────────────────────────────────────

-- name: AdminListTeams :many
-- COUNT(*) OVER () carries the total in the same round trip, like AdminListUsers. The search is
-- one optional (q, field) pair; an unset q drops the filter entirely, and an unknown field falls
-- back to the name so the CASE can never silently match nothing.
SELECT t.id, t.name, t.email, t.website, t.affiliation, t.country,
       t.bracket_id, t.captain_id, t.hidden, t.banned, t.created_at,
       (SELECT count(*) FROM users u WHERE u.team_id = t.id)::bigint AS member_count,
       COUNT(*) OVER () AS total
  FROM teams t
 WHERE (sqlc.narg(q)::text IS NULL OR CASE sqlc.narg(field)::text
            WHEN 'email'       THEN t.email       ILIKE '%' || sqlc.narg(q) || '%'
            WHEN 'website'     THEN t.website     ILIKE '%' || sqlc.narg(q) || '%'
            WHEN 'affiliation' THEN t.affiliation ILIKE '%' || sqlc.narg(q) || '%'
            WHEN 'country'     THEN t.country     ILIKE '%' || sqlc.narg(q) || '%'
            ELSE t.name ILIKE '%' || sqlc.narg(q) || '%'
        END)
 ORDER BY t.id
 LIMIT @lim::int OFFSET @off::int;

-- name: AdminCreateTeam :one
-- Same arbiters as the self-serve create: teams_name_uniq and the num_teams caps trigger decide
-- on the INSERT itself, never in a prior check. captain_id stays NULL — an admin-provisioned team
-- is captainless until its first member joins and adopts it.
INSERT INTO teams (name, password_hash, email, website, affiliation, country)
VALUES (@name, sqlc.narg(password_hash), sqlc.narg(email), sqlc.narg(website),
        sqlc.narg(affiliation), sqlc.narg(country))
RETURNING id, name, email, website, affiliation, country, bracket_id, captain_id,
          hidden, banned, created_at;

-- name: AdminUpdateTeam :one
-- Deliberately narrow SET: banned, hidden, captain_id, password_hash and membership are not
-- reachable from this statement — they move through their own routes or not at all.
UPDATE teams SET
    name        = COALESCE(sqlc.narg(name), name),
    email       = CASE WHEN @clear_email::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(email), email) END,
    website     = CASE WHEN @clear_website::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(website), website) END,
    affiliation = CASE WHEN @clear_affiliation::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(affiliation), affiliation) END,
    country     = CASE WHEN @clear_country::bool THEN NULL
                       ELSE COALESCE(sqlc.narg(country), country) END
WHERE id = @team_id
RETURNING id, name, email, website, affiliation, country, bracket_id, captain_id,
          hidden, banned, created_at;

-- name: AdminSetTeamBanned :one
UPDATE teams SET banned = @banned WHERE id = @team_id
RETURNING id, name, banned;

-- name: AdminSetTeamHidden :one
UPDATE teams SET hidden = @hidden WHERE id = @team_id
RETURNING id, name, hidden;

-- name: AdminGetTeam :one
SELECT t.id, t.name, t.email, t.website, t.affiliation, t.country,
       t.bracket_id, t.captain_id, t.hidden, t.banned, t.created_at,
       (SELECT count(*) FROM users u WHERE u.team_id = t.id)::bigint AS member_count
  FROM teams t
 WHERE t.id = @team_id;

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

-- name: AdminAddTag :one
-- No pre-checks: UNIQUE(challenge_id, value) refuses the duplicate and the FK refuses a missing
-- challenge, each mapped by the caller. A pre-read would just be the same check with a race in it.
INSERT INTO tags (challenge_id, value)
VALUES (@challenge_id, @value)
RETURNING *;

-- name: AdminRemoveTag :execrows
DELETE FROM tags WHERE challenge_id = @challenge_id AND value = @value;

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
