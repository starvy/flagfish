-- +goose Up
-- The original upload filename, kept for the download's Content-Disposition and for the board's
-- file list. The stored object is content-addressed by sha256, so its location carries no name;
-- this column is where the human-facing name lives.
ALTER TABLE files ADD COLUMN name text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE files DROP COLUMN name;
