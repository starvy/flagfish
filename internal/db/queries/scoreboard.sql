-- Standings and scoreboard time-travel. The heaviest read in the product.
-- Four things here are load-bearing:
--
--   1. `SUM(solves.value)`, not `SUM(challenges.value)`: the score is what each solve was
--      worth at the time. Summing the live value lets decay retroactively rewrite history.
--   2. `value <> 0` on both legs: a zero-point solve or award must not move the tiebreak's
--      timestamp.
--   3. INNER JOIN to the account table: an account with no non-zero scoring event is absent
--      from the board, not shown at rank N with 0 points.
--   4. The tiebreak is `score DESC, last_event ASC, account_id ASC` — the last clause is
--      determinism insurance for an exact-microsecond tie at an identical score.
--
-- `cutoff` is the freeze horizon, strict `<`: a solve exactly at the freeze timestamp is
-- hidden. NULL = no freeze / admin view. `admin` includes hidden and banned accounts.
-- `lim` = 0 means no limit.

-- name: GetUserStandings :many
WITH scoring AS (
    SELECT s.user_id AS account_id, s.value AS value, s.date AS date
      FROM solves s
     WHERE s.value <> 0
       AND (sqlc.narg(cutoff)::timestamptz IS NULL OR s.date < sqlc.narg(cutoff)::timestamptz)
    UNION ALL
    SELECT a.user_id, a.value, a.date
      FROM awards a
     WHERE a.value <> 0
       AND (sqlc.narg(cutoff)::timestamptz IS NULL OR a.date < sqlc.narg(cutoff)::timestamptz)
),
sumscores AS (
    SELECT account_id, SUM(value)::bigint AS score, MAX(date)::timestamptz AS last_event
      FROM scoring
     GROUP BY account_id
)
SELECT u.id AS account_id, u.name, u.bracket_id, b.name AS bracket_name,
       u.hidden, u.banned, ss.score, ss.last_event
  FROM users u
  JOIN sumscores ss ON ss.account_id = u.id
  LEFT JOIN brackets b ON b.id = u.bracket_id
 WHERE (sqlc.arg(admin)::boolean OR (u.hidden = false AND u.banned = false))
   AND (sqlc.narg(bracket_id)::bigint IS NULL OR u.bracket_id = sqlc.narg(bracket_id)::bigint)
 ORDER BY ss.score DESC, ss.last_event ASC, u.id ASC
 LIMIT NULLIF(sqlc.arg(lim)::int, 0);

-- name: GetTeamStandings :many
WITH scoring AS (
    SELECT s.team_id AS account_id, s.value AS value, s.date AS date
      FROM solves s
     WHERE s.value <> 0
       AND s.team_id IS NOT NULL
       AND (sqlc.narg(cutoff)::timestamptz IS NULL OR s.date < sqlc.narg(cutoff)::timestamptz)
    UNION ALL
    SELECT a.team_id, a.value, a.date
      FROM awards a
     WHERE a.value <> 0
       AND a.team_id IS NOT NULL
       AND (sqlc.narg(cutoff)::timestamptz IS NULL OR a.date < sqlc.narg(cutoff)::timestamptz)
),
sumscores AS (
    SELECT account_id, SUM(value)::bigint AS score, MAX(date)::timestamptz AS last_event
      FROM scoring
     GROUP BY account_id
)
SELECT t.id AS account_id, t.name, t.bracket_id, b.name AS bracket_name,
       t.hidden, t.banned, ss.score, ss.last_event
  FROM teams t
  JOIN sumscores ss ON ss.account_id = t.id
  LEFT JOIN brackets b ON b.id = t.bracket_id
 WHERE (sqlc.arg(admin)::boolean OR (t.hidden = false AND t.banned = false))
   AND (sqlc.narg(bracket_id)::bigint IS NULL OR t.bracket_id = sqlc.narg(bracket_id)::bigint)
 ORDER BY ss.score DESC, ss.last_event ASC, t.id ASC
 LIMIT NULLIF(sqlc.arg(lim)::int, 0);

-- name: GetStandingsAsOf :many
-- Scoreboard time-travel: "what did the board look like at 14:00?". Possible only because
-- stamped solve values make the scoreboard append-only.
--
-- The caller must clamp `as_of` to the freeze horizon for non-admins — otherwise
-- `?as_of=<now>` during a freeze hands the live board to anyone who asks. This query
-- deliberately takes no admin freeze flag, so the clamp cannot be forgotten by passing
-- the wrong boolean: `as_of` is the horizon.
--
-- The account column is resolved from `instance`, a singleton and immutable, so the
-- time-travel board can never be computed against the wrong account model.
WITH mode AS (
    SELECT user_mode FROM instance
),
scoring AS (
    SELECT (CASE WHEN m.user_mode = 'teams' THEN s.team_id ELSE s.user_id END)::bigint AS account_id,
           s.value AS value, s.date AS date
      FROM solves s CROSS JOIN mode m
     WHERE s.value <> 0 AND s.date < sqlc.arg(as_of)::timestamptz
    UNION ALL
    SELECT (CASE WHEN m.user_mode = 'teams' THEN a.team_id ELSE a.user_id END)::bigint,
           a.value, a.date
      FROM awards a CROSS JOIN mode m
     WHERE a.value <> 0 AND a.date < sqlc.arg(as_of)::timestamptz
),
sumscores AS (
    SELECT account_id, SUM(value)::bigint AS score, MAX(date)::timestamptz AS last_event
      FROM scoring
     WHERE account_id IS NOT NULL
     GROUP BY account_id
)
SELECT ss.account_id,
       COALESCE(u.name, t.name)     AS name,
       COALESCE(u.bracket_id, t.bracket_id) AS bracket_id,
       COALESCE(u.hidden, t.hidden) AS hidden,
       COALESCE(u.banned, t.banned) AS banned,
       ss.score,
       ss.last_event
  FROM sumscores ss
  CROSS JOIN mode m
  LEFT JOIN users u ON m.user_mode = 'users' AND u.id = ss.account_id
  LEFT JOIN teams t ON m.user_mode = 'teams' AND t.id = ss.account_id
 WHERE COALESCE(u.id, t.id) IS NOT NULL   -- inner-join semantics: no scoring event, no row
   AND (sqlc.arg(admin)::boolean OR COALESCE(u.hidden, t.hidden) = false)
   AND (sqlc.arg(admin)::boolean OR COALESCE(u.banned, t.banned) = false)
   AND (sqlc.narg(bracket_id)::bigint IS NULL
        OR COALESCE(u.bracket_id, t.bracket_id) = sqlc.narg(bracket_id)::bigint)
 ORDER BY ss.score DESC, ss.last_event ASC, ss.account_id ASC
 LIMIT NULLIF(sqlc.arg(lim)::int, 0);
