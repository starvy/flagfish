-- Email tokens: single-use, expiring secrets for email verification and password reset.
-- Only sha256(token) is stored — a database dump must not be a bag of live reset links.

-- +goose Up
CREATE TABLE email_tokens (
    id          bigserial   PRIMARY KEY,
    token_hash  bytea       NOT NULL UNIQUE,   -- sha256(token). Never the plaintext.
    purpose     text        NOT NULL CHECK (purpose IN ('verify','reset')),
    user_id     bigint      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz                    -- set exactly once, by the consuming UPDATE
);
CREATE INDEX email_tokens_user_idx   ON email_tokens (user_id);
CREATE INDEX email_tokens_expiry_idx ON email_tokens (expires_at);   -- expiry sweep

-- +goose Down
DROP TABLE email_tokens;
