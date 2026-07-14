-- Tags became admin-mutable once the tag-admin endpoints landed (rename/merge/delete). They are an
-- admin-mutable content table like challenges and hints, so they earn the same capture trigger —
-- without it a merge or delete would rewrite the taxonomy with no record of who did it.

-- +goose Up
CREATE TRIGGER audit_tags AFTER INSERT OR UPDATE OR DELETE ON tags FOR EACH ROW EXECUTE FUNCTION audit_capture();

-- +goose Down
DROP TRIGGER audit_tags ON tags;
