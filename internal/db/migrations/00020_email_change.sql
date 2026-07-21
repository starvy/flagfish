-- Self-service email change, proven before it takes effect. The address a player wants sits in
-- pending_email until a token delivered to THAT address is confirmed; only then does it become the
-- live email. The live email is never rewritten on the strength of a token sent to the old one, so a
-- change is a re-verification, not an edit.

-- +goose Up
ALTER TABLE users ADD COLUMN pending_email text;

-- Two accounts cannot queue the same new address, and the fold matches users_email_uniq so a pending
-- address and a live one are compared the one way an identity is compared — case-insensitively.
CREATE UNIQUE INDEX users_pending_email_uniq ON users (lower(pending_email));

-- The change token carries its own purpose so that ONLY a token delivered to the new address can
-- flip the live email. A leftover registration 'verify' token, sent to the OLD address, must not be
-- able to complete a change the account holder never proved control of the new inbox for.
ALTER TABLE email_tokens DROP CONSTRAINT email_tokens_purpose_check;
ALTER TABLE email_tokens ADD CONSTRAINT email_tokens_purpose_check
    CHECK (purpose IN ('verify', 'reset', 'email_change'));

-- +goose Down
ALTER TABLE email_tokens DROP CONSTRAINT email_tokens_purpose_check;
ALTER TABLE email_tokens ADD CONSTRAINT email_tokens_purpose_check
    CHECK (purpose IN ('verify', 'reset'));
DROP INDEX users_pending_email_uniq;
ALTER TABLE users DROP COLUMN pending_email;
