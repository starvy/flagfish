-- Public account profiles: a user's page, gated exactly like the scoreboard and the team page.

-- name: GetUserPublicProfile :one
-- A hidden or banned user 404s to the public and is visible to an admin — the `admin` flag is the
-- only thing that widens the row, mirroring the scoreboard. The score sums the stamped solves.user_id
-- and awards.user_id ledgers under the caller's freeze horizon (cutoff strict `<`, NULL = live), so a
-- public page served during a freeze hands over the frozen standing, one account at a time.
SELECT u.id, u.name, u.website, u.affiliation, u.country, u.created_at,
       u.bracket_id, b.name AS bracket_name,
       (COALESCE((SELECT sum(s.value) FROM solves s
                   WHERE s.user_id = u.id
                     AND (sqlc.narg(cutoff)::timestamptz IS NULL
                          OR s.date < sqlc.narg(cutoff)::timestamptz)), 0)
      + COALESCE((SELECT sum(a.value) FROM awards a
                   WHERE a.user_id = u.id
                     AND (sqlc.narg(cutoff)::timestamptz IS NULL
                          OR a.date < sqlc.narg(cutoff)::timestamptz)), 0))::bigint AS score
  FROM users u
  LEFT JOIN brackets b ON b.id = u.bracket_id
 WHERE u.id = @user_id
   AND (sqlc.arg(admin)::boolean OR (u.hidden = false AND u.banned = false));

-- name: ListUserSolves :many
-- The account's solved challenges, newest first, under the same freeze horizon as the score above.
-- Only visible challenges are listed: the total score sums the whole ledger (matching the board), but
-- naming a hidden challenge here would leak its existence, so the itemised history hides it just as
-- the per-challenge solve list does.
--
-- Bounded to the most recent page: this is embedded in a public profile document, not a paged feed,
-- and the headline score is summed separately over the whole ledger — so the cap trims only the tail
-- of the history view, never the total. Without it a heavy solver's page is a slow query and a
-- one-request scrape at a large event.
SELECT c.id AS challenge_id, c.name AS challenge_name, c.category,
       s.value::int AS value, s.date
  FROM solves s
  JOIN challenges c ON c.id = s.challenge_id
 WHERE s.user_id = @user_id
   AND c.state = 'visible'
   AND (sqlc.narg(cutoff)::timestamptz IS NULL OR s.date < sqlc.narg(cutoff)::timestamptz)
 ORDER BY s.date DESC, s.id DESC
 LIMIT 100;
