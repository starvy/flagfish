-- Custom registration fields: admin-defined profile questions (affiliation, eligibility, a consent
-- checkbox) collected at sign-up. `fields` holds the definitions; `field_entries` holds one answer
-- per (field, account), enforced by the one-owner CHECK and the per-field UNIQUE. A `required` field
-- with no entry is what the login `profile_complete` gate reads as an incomplete profile, so the
-- answer writes below are what keep that gate satisfiable.

-- ── admin CRUD over the definitions ──────────────────────────────────────────────

-- name: AdminCreateField :one
INSERT INTO fields (name, applies_to, field_type, description, required, public, editable, position)
VALUES (@name, @applies_to, @field_type, sqlc.narg(description), @required, @public, @editable, @position)
RETURNING *;

-- name: AdminListFields :many
-- Every field, in the order a form would render them: user fields then team fields, each by their
-- authored position, id as the stable tie-break.
SELECT * FROM fields ORDER BY applies_to, position, id;

-- name: AdminGetField :one
SELECT * FROM fields WHERE id = @field_id;

-- name: AdminUpdateField :one
-- Partial update: an absent argument keeps its value. field_type and applies_to are immutable —
-- flipping either would reinterpret every stored answer (a text answer read as a bool, a user
-- answer counted against a team gate), so they are create-only, exactly as a bracket's applies_to is.
UPDATE fields SET
    name        = COALESCE(sqlc.narg(name), name),
    description = CASE WHEN @clear_description::bool THEN NULL
                      ELSE COALESCE(sqlc.narg(description), description) END,
    required    = COALESCE(sqlc.narg(required), required),
    public      = COALESCE(sqlc.narg(public), public),
    editable    = COALESCE(sqlc.narg(editable), editable),
    position    = COALESCE(sqlc.narg(position), position)
WHERE id = @field_id
RETURNING *;

-- name: AdminDeleteField :execrows
-- Answers are cascaded, not blocked: field_entries.field_id is ON DELETE CASCADE. An answer is user
-- data, not a gameplay ledger row — removing a retired question takes its answers with it, and the
-- audit trigger on field_entries records each removed answer. execrows so the caller can tell "no
-- such field" from a real delete.
DELETE FROM fields WHERE id = @field_id;

-- name: CountFieldEntries :one
-- How many answers a field carries, for the admin delete confirmation ("this removes N answers").
SELECT count(*) FROM field_entries WHERE field_id = @field_id;

-- ── user answers: registration + /me ─────────────────────────────────────────────

-- name: ListUserFieldDefs :many
-- The user-applicable field definitions, for the register-time validation and the /me editor. Read
-- inside the registration transaction so required-field enforcement sees the fields as of the write.
SELECT id, name, field_type, description, required, public, editable, position
  FROM fields WHERE applies_to = 'user' ORDER BY position, id;

-- name: ListUserFieldsWithAnswers :many
-- The /me view: every user field with this caller's current answer (NULL where unanswered), so the
-- settings form can render both the fields still owed and the ones already filled in.
SELECT f.id, f.name, f.field_type, f.description, f.required, f.public, f.editable, f.position,
       fe.value
  FROM fields f
  LEFT JOIN field_entries fe ON fe.field_id = f.id AND fe.user_id = @user_id::bigint
 WHERE f.applies_to = 'user'
 ORDER BY f.position, f.id;

-- name: ListPublicUserFieldAnswers :many
-- The public projection of a user's answers: only fields flagged public, only those actually
-- answered. This is what a public profile view is allowed to show — a non-public answer never
-- leaves this query.
SELECT f.id, f.name, f.field_type, fe.value
  FROM fields f
  JOIN field_entries fe ON fe.field_id = f.id AND fe.user_id = @user_id::bigint
 WHERE f.applies_to = 'user' AND f.public
   AND fe.value IS NOT NULL AND fe.value <> 'null'::jsonb AND fe.value <> '""'::jsonb
 ORDER BY f.position, f.id;

-- name: UpsertUserFieldEntry :exec
-- One answer per (field, user), so a re-answer is an UPSERT on the UNIQUE(field_id, user_id), never
-- a second row the profile-complete gate would have to pick a winner from.
INSERT INTO field_entries (field_id, user_id, value)
VALUES (@field_id, @user_id, @value)
ON CONFLICT (field_id, user_id) DO UPDATE SET value = EXCLUDED.value;

-- name: DeleteUserFieldEntry :exec
-- Clearing an optional, editable answer. A DELETE, not a value=null write: "no answer" has one
-- representation (no row), which the gate and the reads all agree on.
DELETE FROM field_entries WHERE field_id = @field_id AND user_id = @user_id;
