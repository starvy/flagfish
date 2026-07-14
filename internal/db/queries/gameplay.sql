-- The submit hot path.
--
-- The ordering that matters: the challenge lock is taken lazily, on the correct-flag path only.
-- ~99% of submissions are wrong answers; a lock at the top of the transaction would serialize every
-- wrong guess on the hottest challenge through one row — a queue where you meant to build a guard.

-- name: GetChallenge :one
-- Step 1 of submit: read the challenge with no lock. The flag compare happens against this.
-- A hidden challenge is not playable; the guard lives in this read so no caller can forget it.
SELECT * FROM challenges WHERE id = @challenge_id AND state = 'visible';

-- name: LockChallengeForSubmit :one
-- Taken only after the flag compared correct. Under this lock the prior-solve count is exact, so
-- first blood is decided inside the submitting transaction, and the decay recalc is exact for free.
-- Re-read `value` from this row, not from GetChallenge: another solver may have decayed the
-- challenge in between. Snapshot what the lock says, not what the optimistic read said.
--
-- FOR NO KEY UPDATE: every INSERT into `submissions` and `solves` takes FOR KEY SHARE on this row
-- through the FK, and plain FOR UPDATE conflicts with that — it would serialize every wrong guess
-- behind the lock, one layer down. FOR NO KEY UPDATE lets the wrong-answer inserts sail through yet
-- still conflicts with itself, so two correct paths serialize. We never UPDATE challenges.id, so
-- the weaker lock costs nothing. Verified empirically.
SELECT * FROM challenges WHERE id = @challenge_id AND state = 'visible' FOR NO KEY UPDATE;

-- name: InsertSubmission :one
-- The append-only attempt log. `attributed_account_id` is the account the matched flag was
-- issued to — NULL unless flag_mode='unique'. It is stamped here, never joined at query time, so
-- sharing detection survives the instance being rotated, regenerated or deleted.
INSERT INTO submissions (challenge_id, user_id, team_id, type, provided, ip, attributed_account_id)
VALUES (@challenge_id, @user_id, @team_id, @type, @provided, @ip, @attributed_account_id)
RETURNING id, date;

-- name: InsertSolve :one
-- The duplicate check is the constraint, never a SELECT. Zero rows returned (pgx.ErrNoRows) is
-- the `already_solved` signal; UNIQUE(challenge_id, user_id) / UNIQUE(challenge_id, team_id) is the
-- arbiter.
--
-- Not a data-modifying CTE. A CTE would insert the `submissions` row even when this insert
-- conflicts, orphaning a type='correct' submission with no solve. Two statements, one transaction.
--
-- @value is the per-solve snapshot: the asking price at the moment of the solve, read under the
-- challenge lock and never recomputed. It is what makes the scoreboard append-only.
INSERT INTO solves (submission_id, challenge_id, user_id, team_id, value)
VALUES (@submission_id, @challenge_id, @user_id, @team_id, @value)
ON CONFLICT DO NOTHING
RETURNING id, value, date;

-- name: CountSolvesExcluding :one
-- First-blood detection, run under the challenge lock so it is exact, not a guess.
--
-- Two columns, and first blood requires both: `prior_solves = 0 AND actor_eligible`.
-- "Hidden and banned accounts cannot take first blood" is two rules, not one:
--
--   prior_solves   — hidden/banned solvers are excluded from the count, so a hidden admin
--                    test-solving a challenge does not burn its first blood for the real solvers.
--   actor_eligible — the submitter is itself visible. The count alone silently gets this wrong:
--                    for a hidden solver the count is 0 *by construction* (it excluded itself),
--                    so `prior == 0` would hand the bonus to the hidden account.
--
-- The account model comes from `instance`, not from a caller-supplied flag: user_mode is
-- immutable and singleton, so reading it here costs one cached-page lookup and makes it impossible
-- for a caller to count the wrong column.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT
    (SELECT count(*)
       FROM solves s
       CROSS JOIN mode m
       LEFT JOIN users u ON u.id = s.user_id
       LEFT JOIN teams t ON t.id = s.team_id
      WHERE s.challenge_id = @challenge_id
        AND s.id <> @exclude_solve_id
        AND CASE WHEN m.user_mode = 'teams'
                 THEN t.hidden = false AND t.banned = false
                 ELSE u.hidden = false AND u.banned = false
            END
    )::bigint AS prior_solves,
    COALESCE(
        (SELECT CASE WHEN m.user_mode = 'teams'
                     THEN (SELECT t.hidden = false AND t.banned = false
                             FROM teams t WHERE t.id = sqlc.narg(team_id)::bigint)
                     ELSE (SELECT u.hidden = false AND u.banned = false
                             FROM users u WHERE u.id = sqlc.arg(user_id)::bigint)
                END
           FROM mode m),
        false
    )::boolean AS actor_eligible;

-- name: InsertAward :one
-- The first-blood bonus and the hint-unlock charge are both awards (`value` may be negative).
-- `awards_one_first_blood_per_challenge` (partial unique) prevents a double bonus even if the
-- challenge lock is ever bypassed — by an admin grant, an import, or a psql session.
INSERT INTO awards (user_id, team_id, type, challenge_id, name, description, value, category, icon)
VALUES (@user_id, @team_id, @type, @challenge_id, @name, @description, @value, @category, @icon)
RETURNING id, value, date;

-- name: RecalcChallengeValue :exec
-- One statement, never a read-modify-write — a read-modify-write is a last-writer-wins race, and
-- this is only safe because the caller already holds the challenge lock.
--
-- GREATEST(n - 1, 0): the first solver pays the max value. NULLIF(c.decay, 0): decay = 0 is already
-- rejected by challenges_dynamic_params, but if it ever leaked in, the value collapses to `minimum`
-- rather than dividing by zero. This updates only the current asking price shown on the board; it
-- does not touch a single past solve — that is the whole point.
--
-- Integer arithmetic, deliberately — do not "restore" the float8/CEIL version. The same curve runs
-- in Go (scoring.Curve.ValueAt), and float8 does not round identically: for (initial=3901,
-- minimum=205, decay=142) the true value at n = decay is 205, but float64 lands a hair high and
-- CEIL lifts it to 206, so displayed and stored price would diverge for the rest of the event.
-- Over the integers both engines do the same floor: init - floor(D*n^2/decay^2), D = initial - minimum.
--
-- The two branches clamp n differently, and the difference is not cosmetic:
--   linear      — n is not clamped; its floor is the outer GREATEST. Clamping at decay would stop
--                 the decrement early and strand the challenge above `minimum`. The product is
--                 bigint because decay * n overflows int4 at real-world sizes.
--   logarithmic — n is clamped at decay: past the knee the curve is flat, so the clamp changes no
--                 result, and it is what bounds the squared product.
UPDATE challenges c
   SET value = GREATEST(
           c.minimum::bigint,
           CASE c.function
               WHEN 'linear' THEN
                   c.initial::bigint - (c.decay::bigint * GREATEST(cnt.n - 1, 0)::bigint)
               ELSE
                   c.initial::bigint - (
                       ((c.initial - c.minimum)::bigint
                        * LEAST(GREATEST(cnt.n - 1, 0), c.decay)::bigint
                        * LEAST(GREATEST(cnt.n - 1, 0), c.decay)::bigint)
                       / (NULLIF(c.decay, 0)::bigint * NULLIF(c.decay, 0)::bigint)
                   )
           END
       )::int,
       updated_at = now()
  FROM (
      SELECT count(*) AS n
        FROM solves s
        CROSS JOIN instance i
        LEFT JOIN users u ON u.id = s.user_id
        LEFT JOIN teams t ON t.id = s.team_id
       WHERE s.challenge_id = @challenge_id
         AND CASE WHEN i.user_mode = 'teams'
                  THEN t.hidden = false AND t.banned = false
                  ELSE u.hidden = false AND u.banned = false
             END
  ) AS cnt
 WHERE c.id = @challenge_id
   AND c.function <> 'static';   -- calling this on a static challenge is a no-op, not a corruption

-- name: GetChallengeFlags :many
-- The flag_mode='static' compare path. Loaded on every submit of a static challenge — the
-- hottest read in the product — and served entirely by flags_challenge_idx.
--
-- Every flag is returned and the caller (domain/flags.MatchAny) evaluates ALL of them without
-- short-circuiting: stopping at the first match would leak, through response latency, which flag
-- matched. The whole point of crypto/subtle is lost if the loop above it branches on the result.
SELECT id, challenge_id, type, content, case_insensitive
  FROM flags
 WHERE challenge_id = @challenge_id
 ORDER BY id;

-- name: GetUserMode :one
-- The account model. Singleton and immutable (enforced by instance_user_mode_immutable), so this
-- is one cached-page lookup. `instance` is created at setup; NO ROWS means the instance was never
-- set up, which is a hard error and not a defaultable condition. Loud beats silent.
SELECT user_mode FROM instance;

-- name: CountSolvedPrerequisites :one
-- The prerequisite gate for the submit path. Counts how many of the given challenge ids this
-- account has solved; the caller compares that against the number required. It is a plain indexed
-- read with no lock, so a prerequisite-locked challenge is rejected before the challenge lock is
-- ever taken — the wrong-answer fast path is untouched. The account model comes from `instance`.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT count(DISTINCT s.challenge_id)::bigint
  FROM solves s
  CROSS JOIN mode m
 WHERE s.challenge_id = ANY(@prerequisites::bigint[])
   AND CASE WHEN m.user_mode = 'teams'
            THEN s.team_id = sqlc.narg(team_id)::bigint
            ELSE s.user_id = sqlc.arg(user_id)::bigint
       END;

-- name: LockAttemptCounter :exec
-- The max_attempts gate is a check-then-act: count wrong answers, then insert one. Without a lock
-- an account's own concurrent submissions all read the same stale count and every one slips past
-- the cap, so a scripted client brute-forces past max_attempts. This advisory lock serializes THIS
-- account's submissions to THIS challenge, making the count that follows exact. It is keyed per
-- (challenge, account): different players never contend, so the challenge is not globally serialized
-- and the wrong-answer hot path across players is untouched — this is not the challenge-row lock.
-- Taken only when the challenge has a cap. A hash collision would merely make two unrelated pairs
-- occasionally serialize, which is harmless. The xact lock auto-releases at commit or rollback.
SELECT pg_advisory_xact_lock(hashtextextended(concat_ws(':', @challenge_id::bigint, @account_id::bigint), 0));

-- name: CountIncorrectSubmissions :one
-- The max_attempts gate: wrong answers only, all-time, scoped to the account. A COUNT(*) over the
-- append-only log rather than a counter row — there is no increment to lose.
WITH mode AS (
    SELECT user_mode FROM instance
)
SELECT count(*)::bigint
  FROM submissions s
  CROSS JOIN mode m
 WHERE s.challenge_id = @challenge_id
   AND s.type = 'incorrect'
   AND CASE WHEN m.user_mode = 'teams'
            THEN s.team_id = sqlc.narg(team_id)::bigint
            ELSE s.user_id = sqlc.arg(user_id)::bigint
       END;
