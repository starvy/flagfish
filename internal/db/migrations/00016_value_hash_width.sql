-- Pin the width of a pool entry's flag hash.
--
-- value_hash is sha256(flag): exactly 32 bytes, always. The submit path probes the pool by the raw
-- 32-byte digest, so a short or truncated hash uploaded through the pool write path would sit in the
-- table and silently never match any submission — a challenge that looks armed and is not. Make the
-- width a constraint the database enforces on every write, not a Go check the next writer can forget.

-- +goose Up
ALTER TABLE challenge_instances
    ADD CONSTRAINT challenge_instances_value_hash_len CHECK (octet_length(value_hash) = 32);

-- +goose Down
ALTER TABLE challenge_instances
    DROP CONSTRAINT challenge_instances_value_hash_len;
