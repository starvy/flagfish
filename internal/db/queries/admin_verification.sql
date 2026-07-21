-- Admin-side verification repair: the escape hatch for an event whose mail never arrives.
-- Every statement here runs inside a transaction that has stamped the acting admin into
-- app.actor_id, so the capture triggers write the audit row in the same transaction as the
-- change itself. Flipping who counts as a verified player is exactly the kind of act that
-- has to be attributable afterwards.

-- name: AdminSetUserVerified :one
UPDATE users SET verified = @verified WHERE id = @user_id
RETURNING id, name, verified;

-- name: AdminVerifyAllUsers :execrows
-- The whole-event repair, for a mailer that accepted everything and delivered nothing.
-- Narrowed to the rows that actually change: the audit trail then names exactly the accounts
-- this unblocked, and running it twice is a no-op rather than a second sweep of noise.
UPDATE users SET verified = true WHERE verified = false;
