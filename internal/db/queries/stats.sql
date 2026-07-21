-- Aggregations for the admin dashboard. Admin-gated: solve distributions are exactly what the
-- scoreboard freeze exists to hide, so this surface never leaks onto a public route. Reads only.

-- name: StatsTotals :one
-- The headline counters, one round trip. Scalar sub-selects rather than joins so an empty table
-- reads as zero, not as a missing row.
SELECT
    (SELECT count(*) FROM solves)::bigint      AS total_solves,
    (SELECT count(*) FROM submissions)::bigint AS total_submissions,
    (SELECT count(*) FROM awards)::bigint       AS total_awards,
    (SELECT count(DISTINCT challenge_id) FROM solves)::bigint AS solved_challenges;

-- name: StatsSubmissionsByType :many
-- The attempt log split by status. Every type present is one row; a type nobody hit is simply
-- absent, and the caller fills the zero.
SELECT type, count(*)::bigint AS count
  FROM submissions
 GROUP BY type
 ORDER BY count DESC, type ASC;

-- name: StatsChallengeSolves :many
-- One row per challenge with its solve count, most-solved first. LEFT JOIN so a challenge nobody has
-- solved appears with zero rather than vanishing — the least-solved tail is the half an organiser
-- actually acts on. The count is the per-challenge distribution the dashboard renders.
SELECT c.id AS challenge_id, c.name, c.category, c.value,
       count(s.id)::bigint AS solve_count
  FROM challenges c
  LEFT JOIN solves s ON s.challenge_id = c.id
 GROUP BY c.id, c.name, c.category, c.value
 ORDER BY solve_count DESC, c.id ASC;

-- name: StatsSolvesOverTime :many
-- Solves bucketed onto a time grid for the dashboard's timeline. The bucket width is a caller-chosen
-- date_trunc field ('hour', 'day', …), passed as a bound parameter — the handler restricts it to a
-- known set, so it is never interpolated SQL. Empty buckets do not appear; a sparse grid is the
-- caller's to fill.
SELECT date_trunc(sqlc.arg(bucket)::text, date)::timestamptz AS bucket,
       count(*)::bigint AS count
  FROM solves
 GROUP BY bucket
 ORDER BY bucket ASC;
