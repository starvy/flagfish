-- Unique flags.
--
-- A pool entry is not a flag, it is a bundle: a flag, an optional artifact, and arbitrary
-- per-account variables that the challenge description (a Go text/template) renders against.
-- So the pool table is `challenge_instances` with `vars jsonb`, and `flag_issues` references
-- an instance rather than a pool.
--
-- This is the deferred per-instance-provider seam with pre-generated bundles instead of runtime
-- orchestration: when per-team instancing arrives it swaps the source of an instance and leaves
-- the schema, the rendering and every query untouched.

-- +goose Up

CREATE TABLE challenge_instances (
    id           bigserial PRIMARY KEY,
    challenge_id bigint NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    value_hash   bytea  NOT NULL,               -- sha256(flag). The plaintext is never stored.
    artifact_id  bigint REFERENCES files(id) ON DELETE SET NULL,  -- the per-account binary/VM/PDF
    vars         jsonb  NOT NULL DEFAULT '{}'::jsonb,             -- {"host":"x.ctf","port":31001}
                                                                  -- never put the flag in here:
                                                                  -- InstanceView has no Flag field,
                                                                  -- by construction.
    generation   int    NOT NULL DEFAULT 1,     -- bump on re-upload; old issues stay attributable
    UNIQUE (challenge_id, value_hash, generation)
);
-- The submit path for flag_mode='unique': sha256(provided) → one indexed probe. O(1), not
-- O(N_accounts), and no per-byte timing channel — stronger than a constant-time compare, not a
-- compromise. Served by the unique's (challenge_id, value_hash) prefix; this index exists for the
-- pool-utilisation gauge (issued/total per challenge), the one number that must be right before
-- the event — pool exhaustion mid-CTF is a hard failure by design.
CREATE INDEX challenge_instances_challenge_idx ON challenge_instances (challenge_id);

CREATE TABLE flag_issues (
    challenge_id bigint      NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    -- users.id XOR teams.id per instance.user_mode. No FK: the target table is instance-dependent,
    -- and a conditional FK is not expressible in Postgres.
    account_id   bigint      NOT NULL,
    instance_id  bigint      NOT NULL REFERENCES challenge_instances(id) ON DELETE RESTRICT,
    assigned_at  timestamptz NOT NULL DEFAULT now(),

    -- Assignment happens lazily on first access — the only place in the product where reading a
    -- challenge mutates state. Both constraints are load-bearing:
    PRIMARY KEY (challenge_id, account_id),   -- an account cannot be issued two instances, even
                                              -- under concurrent first-views
    UNIQUE (instance_id)                      -- an instance cannot be issued to two accounts.
                                              -- this is the constraint that makes the anti-cheat
                                              -- property true.
);

-- +goose Down
DROP TABLE flag_issues;
DROP TABLE challenge_instances;
