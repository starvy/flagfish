-- Admin reads for the challenge authoring console. These are plain reads — no audit transaction —
-- and they deliberately ignore the visible/hidden state and the anonymize gate that the player
-- catalog applies: an operator authors every challenge, hidden ones included, and reads back the
-- flags a player is never shown.

-- name: AdminListChallenges :many
-- The operator's board: every challenge in board order regardless of state, each row carrying the
-- counts the console shows (solves, flags, hints) so the list renders them without a per-row read.
SELECT
    c.id, c.name, c.category, c.value, c.function, c.state,
    (SELECT count(*) FROM solves s WHERE s.challenge_id = c.id)::bigint AS solve_count,
    (SELECT count(*) FROM flags  f WHERE f.challenge_id = c.id)::bigint AS flag_count,
    (SELECT count(*) FROM hints  h WHERE h.challenge_id = c.id)::bigint AS hint_count
  FROM challenges c
 ORDER BY c.category, c.position, c.id;

-- name: AdminListChallengeFlags :many
-- Every flag on a challenge, plaintext content included. This is the one read that returns a flag's
-- content, and it is reachable only behind the admin gate — the player detail redacts flags entirely.
SELECT * FROM flags WHERE challenge_id = @challenge_id ORDER BY id;

-- name: AdminListChallengeHints :many
-- Every hint on a challenge in unlock order, body included — unlike the player list, which withholds
-- the content until it is bought.
SELECT * FROM hints WHERE challenge_id = @challenge_id ORDER BY position, id;
