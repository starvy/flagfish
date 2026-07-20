-- Pages: the small CMS behind rules / FAQ / sponsors. A page is addressed by a URL slug (route),
-- carries markdown the client renders, and has two visibility flags: draft keeps it unpublished,
-- auth_required keeps a published page to participants only. Both are enforced in the policy layer
-- against the row, not here — the table only guarantees the slug is unique.
--
-- Markdown is stored verbatim and rendered client-side. The server never turns it into HTML, so it
-- is never in the business of sanitising author output; format is a closed set so a future renderer
-- is an explicit migration, not a surprise.
--
-- Text-only in v1: a page owns no files, so files.challenge_id / files_at_most_one_owner is left
-- untouched. When pages grow attachments, add files.page_id and widen that CHECK to
-- num_nonnulls(challenge_id, page_id) <= 1 per the note in 00002.

-- +goose Up
CREATE TABLE pages (
    id            bigserial   PRIMARY KEY,
    route         text        NOT NULL,
    title         text        NOT NULL,
    content       text        NOT NULL DEFAULT '',
    format        text        NOT NULL DEFAULT 'markdown' CHECK (format IN ('markdown')),
    -- A new page is a draft by default: publishing is the deliberate act, so a half-written page
    -- cannot leak onto the public site by being forgotten.
    draft         boolean     NOT NULL DEFAULT true,
    auth_required boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    -- The slug is the URL. Without a unique index a check-then-insert on route lets two concurrent
    -- creates land the same slug; the constraint is the check, and its violation is the 409.
    CONSTRAINT pages_route_key UNIQUE (route)
);

-- Pages are admin-mutable content, like challenges and hints, so they earn the same capture trigger:
-- an edit that rewrites the rules mid-event must record who did it and what it said before.
CREATE TRIGGER audit_pages AFTER INSERT OR UPDATE OR DELETE ON pages FOR EACH ROW EXECUTE FUNCTION audit_capture();

-- +goose Down
DROP TRIGGER audit_pages ON pages;
DROP TABLE pages;
