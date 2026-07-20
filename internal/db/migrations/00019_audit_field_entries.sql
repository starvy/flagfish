-- Custom registration fields became answerable once the register/-me answer paths and the admin
-- field CRUD landed. `fields` (the definitions) is already audited; `field_entries` (the answers)
-- was not. An answer is admin-visible content that gates login — a required field with no entry
-- blocks the profile-complete wall — so a change to who answered what earns the same capture
-- trigger the rest of the admin-mutable tables carry.
--
-- Registration and the self-serve /me edit do not stamp an actor, so those rows record a NULL
-- actor (system) — the honest reading of "the user set this themselves". The importer runs under
-- session_replication_role = replica, so a bulk field-entry restore still emits zero audit rows.

-- +goose Up
CREATE TRIGGER audit_field_entries AFTER INSERT OR UPDATE OR DELETE ON field_entries FOR EACH ROW EXECUTE FUNCTION audit_capture();

-- +goose Down
DROP TRIGGER audit_field_entries ON field_entries;
