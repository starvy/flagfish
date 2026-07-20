-- CMS pages: the rules/FAQ/sponsors content. The public reads are keyed by the URL slug; the admin
-- CRUD is keyed by id. Route uniqueness is the table's (pages_route_key), so a duplicate slug is a
-- constraint violation the service maps to a 409, never a check-then-insert.

-- name: GetPageByRoute :one
-- The public read. Returns the row whatever its draft/auth_required flags — the policy gate decides
-- visibility against them, so the query must not pre-filter drafts or the gate could never be tested.
SELECT * FROM pages WHERE route = @route;

-- name: ListPublishedPages :many
-- The public list behind the nav: published pages only, drafts never appear. auth_required rides
-- along so the client can mark a link that will ask an anonymous visitor to log in.
SELECT id, route, title, auth_required
  FROM pages
 WHERE draft = false
 ORDER BY title;

-- name: AdminListPages :many
-- The admin index: every page, draft or not. Content is omitted — the list does not need the body,
-- and a rules page is large.
SELECT id, route, title, format, draft, auth_required, created_at, updated_at
  FROM pages
 ORDER BY title;

-- name: AdminGetPage :one
SELECT * FROM pages WHERE id = @id;

-- name: AdminCreatePage :one
INSERT INTO pages (route, title, content, format, draft, auth_required)
VALUES (@route, @title, @content, @format, @draft, @auth_required)
RETURNING *;

-- name: AdminUpdatePage :one
-- Partial update: an absent field keeps its value. Changing route to one already taken trips
-- pages_route_key, which the service turns into a 409.
UPDATE pages SET
    route         = COALESCE(sqlc.narg(route), route),
    title         = COALESCE(sqlc.narg(title), title),
    content       = COALESCE(sqlc.narg(content), content),
    format        = COALESCE(sqlc.narg(format), format),
    draft         = COALESCE(sqlc.narg(draft), draft),
    auth_required = COALESCE(sqlc.narg(auth_required), auth_required),
    updated_at    = now()
WHERE id = @id
RETURNING *;

-- name: AdminDeletePage :execrows
DELETE FROM pages WHERE id = @id;
