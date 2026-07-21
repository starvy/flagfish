-- One account's cumulative score over time, replayed from the same append-only ledger the
-- scoreboard sums. Powers the graph on a public profile, so it is freeze-safe by the same mechanism:
-- the caller passes the freeze horizon as `cutoff` and every event at or after it is excluded. The
-- query takes no is_admin freeze flag — `cutoff` IS the horizon, so the clamp cannot be forgotten by
-- handing in the wrong boolean.

-- name: GetScoreHistory :many
-- `admin` here only widens visibility to a hidden or banned subject; it never lifts the freeze. A
-- non-admin viewing a hidden account gets an empty timeline, exactly as that account is absent from
-- the board — and a non-existent account reads the same empty, leaking nothing about which is which.
--
-- The account column is resolved from `instance`, immutable since setup, so the timeline can never be
-- keyed on the wrong account model. `value <> 0` mirrors the scoreboard: a zero-point event does not
-- move the line. The running total is a window SUM over (date, value); microsecond ties settle on
-- value, and the final cumulative is order-independent regardless.
WITH mode AS (
    SELECT user_mode FROM instance
),
subject AS (
    SELECT COALESCE(u.hidden, t.hidden) AS hidden,
           COALESCE(u.banned, t.banned) AS banned
      FROM mode m
      LEFT JOIN users u ON m.user_mode = 'users' AND u.id = sqlc.arg(account_id)::bigint
      LEFT JOIN teams t ON m.user_mode = 'teams' AND t.id = sqlc.arg(account_id)::bigint
),
events AS (
    SELECT s.date AS date, s.value AS value
      FROM solves s CROSS JOIN mode m CROSS JOIN subject sub
     WHERE s.value <> 0
       AND (sqlc.arg(admin)::boolean OR (sub.hidden = false AND sub.banned = false))
       AND (CASE WHEN m.user_mode = 'teams' THEN s.team_id ELSE s.user_id END) = sqlc.arg(account_id)::bigint
       AND (sqlc.narg(cutoff)::timestamptz IS NULL OR s.date < sqlc.narg(cutoff)::timestamptz)
    UNION ALL
    SELECT a.date, a.value
      FROM awards a CROSS JOIN mode m CROSS JOIN subject sub
     WHERE a.value <> 0
       AND (sqlc.arg(admin)::boolean OR (sub.hidden = false AND sub.banned = false))
       AND (CASE WHEN m.user_mode = 'teams' THEN a.team_id ELSE a.user_id END) = sqlc.arg(account_id)::bigint
       AND (sqlc.narg(cutoff)::timestamptz IS NULL OR a.date < sqlc.narg(cutoff)::timestamptz)
)
SELECT date::timestamptz AS date,
       value::bigint AS delta,
       (sum(value) OVER (ORDER BY date, value))::bigint AS cumulative
  FROM events
 ORDER BY date, value;
