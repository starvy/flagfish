-- The anti-cheat detectors. Reads only. These never mutate, never penalise, and never tell the
-- player anything: auto-penalisation was considered and rejected (no appeal path, and a false
-- positive becomes an instant ban during a live event). They feed an admin review queue; a human
-- decides, out of band.

-- name: FindFlagSharing :many
-- A predicate on `submissions` alone — no join to flag_issues, no join to
-- challenge_instances. That is the entire payoff of stamping `attributed_account_id` inside the
-- submit transaction: the evidence survives the instance being rotated, regenerated or
-- deleted, and it survives swapping FlagIssuer to the HMAC implementation later. A join-based audit
-- trail is only as durable as the rows it joins to; a stamped one is a fact.
--
-- Served by submissions_sharing_idx, which is partial — only correct, attributed submissions are
-- ever scanned, a tiny fraction of the table.
--
-- The account model comes from `instance`. In teams mode the unit is the team, so
-- intra-team sharing is invisible by design: teammates legitimately share artifacts and flags — that
-- is what a team is. The detector fires only across accounts, which is the thing worth catching.
SELECT sub.id, sub.date, sub.challenge_id, sub.user_id, sub.team_id, sub.ip,
       sub.attributed_account_id
  FROM submissions sub
  CROSS JOIN instance i
 WHERE sub.type = 'correct'
   AND sub.attributed_account_id IS NOT NULL
   AND sub.attributed_account_id IS DISTINCT FROM
       (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)
 ORDER BY sub.date DESC
 LIMIT sqlc.arg(lim)::int;

-- name: FindUnissuedSolves :many
-- The provable detector, not a statistical signal.
--
-- A solve on a flag_mode='unique' challenge by an account that was never issued an instance. There is
-- no honest way to hold a valid flag for a challenge whose artifact you never fetched: assignment is
-- lazy, so a flag_issues row exists for every account that so much as opened the challenge. No such
-- row means the flag came from somewhere else. Full stop.
--
-- Anti-join over solves_challenge_firstblood_idx × the flag_issues PK. No new index.
SELECT s.id AS solve_id, s.challenge_id, s.user_id, s.team_id, s.date, s.value
  FROM solves s
  JOIN challenges c ON c.id = s.challenge_id
  CROSS JOIN instance i
 WHERE c.flag_mode = 'unique'
   AND NOT EXISTS (
       SELECT 1
         FROM flag_issues fi
        WHERE fi.challenge_id = s.challenge_id
          AND fi.account_id = (CASE WHEN i.user_mode = 'teams' THEN s.team_id ELSE s.user_id END)
   )
 ORDER BY s.date DESC
 LIMIT sqlc.arg(lim)::int;

-- name: CountFlagSharingPairs :one
-- The distinct (issued-to, submitted-by) pair count behind FindFlagSharingPairs, for pagination.
SELECT count(*) FROM (
    SELECT sub.attributed_account_id,
           (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)
      FROM submissions sub
      CROSS JOIN instance i
     WHERE sub.type = 'correct'
       AND sub.attributed_account_id IS NOT NULL
       AND sub.attributed_account_id IS DISTINCT FROM
           (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)
     GROUP BY 1, 2
) pairs;

-- name: FindFlagSharingPairs :many
-- Flag sharing, folded to one row per offender pair: account `submitter` submitted correct flags that
-- were issued to account `issued_to`. Same predicate as FindFlagSharing — submissions alone, so it
-- survives instance rotation — but grouped, counted, and with the challenge span, so a pair that
-- shared twenty flags is one row of evidence, not twenty. Served by submissions_sharing_idx.
SELECT sub.attributed_account_id::bigint AS issued_to,
       (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)::bigint AS submitter,
       count(*)                                          AS submission_count,
       count(DISTINCT sub.challenge_id)                  AS challenge_count,
       array_agg(DISTINCT sub.challenge_id)::bigint[]    AS challenge_ids,
       min(sub.date)::timestamptz                        AS first_seen,
       max(sub.date)::timestamptz                        AS last_seen
  FROM submissions sub
  CROSS JOIN instance i
 WHERE sub.type = 'correct'
   AND sub.attributed_account_id IS NOT NULL
   AND sub.attributed_account_id IS DISTINCT FROM
       (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)
 GROUP BY sub.attributed_account_id,
          (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)
 ORDER BY submission_count DESC, last_seen DESC
 LIMIT sqlc.arg(lim)::int OFFSET sqlc.arg(off)::int;

-- name: CountIPOverlaps :one
-- The count of IP clusters meeting the threshold, for pagination.
SELECT count(*) FROM (
    SELECT sub.ip
      FROM submissions sub
      CROSS JOIN instance i
     WHERE sub.ip IS NOT NULL
       AND (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END) IS NOT NULL
     GROUP BY sub.ip
    HAVING count(DISTINCT (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END))
           >= sqlc.arg(min_accounts)::int
) clusters;

-- name: FindIPOverlaps :many
-- Addresses used by several distinct accounts. This is a SIGNAL, not a verdict: shared NAT, a campus,
-- a corporate egress or a carrier-grade NAT pool all put unrelated players behind one address, so a
-- cluster is evidence for a human to weigh, never grounds to act on alone. @min_accounts tunes how
-- many distinct accounts on one address is worth surfacing. In teams mode the account is the team, so
-- teammates behind one router do not trip it. Served by submissions_ip_idx.
SELECT sub.ip,
       count(DISTINCT (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END))
                                                         AS account_count,
       array_agg(DISTINCT (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END))::bigint[]
                                                         AS account_ids,
       min(sub.date)::timestamptz                        AS first_seen,
       max(sub.date)::timestamptz                        AS last_seen
  FROM submissions sub
  CROSS JOIN instance i
 WHERE sub.ip IS NOT NULL
   AND (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END) IS NOT NULL
 GROUP BY sub.ip
HAVING count(DISTINCT (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END))
       >= sqlc.arg(min_accounts)::int
 ORDER BY account_count DESC, sub.ip
 LIMIT sqlc.arg(lim)::int OFFSET sqlc.arg(off)::int;

-- name: FindFlagSharingForAccount :many
-- One account's sharing evidence, both directions: flags issued to it that another account submitted
-- (direction='issued_to') and flags it submitted that were issued to another (direction='submitted'),
-- grouped by counterparty. Bounded to one account, so it is not paginated.
SELECT direction, counterparty,
       count(*)                                   AS submission_count,
       array_agg(DISTINCT challenge_id)::bigint[] AS challenge_ids,
       min(date)::timestamptz                     AS first_seen,
       max(date)::timestamptz                     AS last_seen
  FROM (
    SELECT
        (CASE WHEN sub.attributed_account_id = sqlc.arg(account_id)::bigint
              THEN 'issued_to' ELSE 'submitted' END) AS direction,
        (CASE WHEN sub.attributed_account_id = sqlc.arg(account_id)::bigint
              THEN (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)
              ELSE sub.attributed_account_id END)::bigint AS counterparty,
        sub.challenge_id, sub.date
      FROM submissions sub
      CROSS JOIN instance i
     WHERE sub.type = 'correct'
       AND sub.attributed_account_id IS NOT NULL
       AND sub.attributed_account_id IS DISTINCT FROM
           (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)
       AND (sub.attributed_account_id = sqlc.arg(account_id)::bigint
            OR (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END) = sqlc.arg(account_id)::bigint)
  ) e
 GROUP BY direction, counterparty
 ORDER BY submission_count DESC;

-- name: FindIPOverlapForAccount :many
-- The other accounts that shared an address with @account_id, one row per (address, other account).
-- Same signal caveat as FindIPOverlaps: shared egress is common and this only points a human at a
-- coincidence worth checking.
WITH mine AS (
    SELECT DISTINCT sub.ip
      FROM submissions sub
      CROSS JOIN instance i
     WHERE sub.ip IS NOT NULL
       AND (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END) = sqlc.arg(account_id)::bigint
)
SELECT sub.ip,
       (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)::bigint AS other_account_id,
       count(*)                   AS submission_count,
       min(sub.date)::timestamptz AS first_seen,
       max(sub.date)::timestamptz AS last_seen
  FROM submissions sub
  CROSS JOIN instance i
  JOIN mine ON mine.ip = sub.ip
 WHERE (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END)
           IS DISTINCT FROM sqlc.arg(account_id)::bigint
   AND (CASE WHEN i.user_mode = 'teams' THEN sub.team_id ELSE sub.user_id END) IS NOT NULL
 GROUP BY sub.ip, other_account_id
 ORDER BY sub.ip, submission_count DESC
 LIMIT sqlc.arg(lim)::int;
