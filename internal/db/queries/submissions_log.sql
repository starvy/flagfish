-- The live submissions feed an organiser watches during an event and mines for cheat review.
-- Read-only over the append-only attempt log; it never joins its way to a scoring decision.

-- name: ListSubmissions :many
-- Keyset pagination, newest first, on the (date, id) cursor — never OFFSET, which re-counts the
-- skipped rows on every page and drifts as new attempts land mid-scroll during a live event. A NULL
-- cursor is the first page; otherwise the strict (date, id) < (before_date, before_id) tuple resumes
-- exactly after the last row already seen. The display joins are LEFT so a challenge or account whose
-- name cannot be resolved never drops the attempt from a cheat-review feed.
--
-- attributed_account_id is passed through verbatim — it is a fact stamped inside the submit
-- transaction, never re-derived by joining the (mutable, rotatable) flag issue here.
--
-- Ordering matches submissions_admin_list_idx and submissions_admin_type_idx (both DESC on date),
-- so the unfiltered feed and the `type` facet each walk an index backwards rather than sort.
SELECT sub.id, sub.date, sub.type, sub.provided, sub.ip,
       sub.challenge_id, c.name AS challenge_name,
       sub.user_id, u.name AS user_name,
       sub.team_id, t.name AS team_name,
       sub.attributed_account_id
  FROM submissions sub
  JOIN      challenges c ON c.id = sub.challenge_id
  LEFT JOIN users      u ON u.id = sub.user_id
  LEFT JOIN teams      t ON t.id = sub.team_id
 WHERE (sqlc.narg(before_date)::timestamptz IS NULL
        OR sub.date < sqlc.narg(before_date)::timestamptz
        OR (sub.date = sqlc.narg(before_date)::timestamptz AND sub.id < sqlc.narg(before_id)::bigint))
   AND (sqlc.narg(type)::text          IS NULL OR sub.type         = sqlc.narg(type)::text)
   AND (sqlc.narg(challenge_id)::bigint IS NULL OR sub.challenge_id = sqlc.narg(challenge_id)::bigint)
   AND (sqlc.narg(user_id)::bigint      IS NULL OR sub.user_id      = sqlc.narg(user_id)::bigint)
   AND (sqlc.narg(team_id)::bigint      IS NULL OR sub.team_id      = sqlc.narg(team_id)::bigint)
 ORDER BY sub.date DESC, sub.id DESC
 LIMIT sqlc.arg(lim)::int;
