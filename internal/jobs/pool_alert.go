package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
)

// poolAlertDedupWindow is how long one challenge's exhaustion alert stays deduplicated. Exhaustion
// is a burst: 300 late registrants open the same dead challenge in the same minute, and one alert is
// the whole point — not 300. The window is long enough to collapse that burst into a single notice,
// short enough that a pool refilled and re-emptied hours later alerts again.
const poolAlertDedupWindow = time.Hour

// AdminNotifier publishes an operator-facing notification with no acting admin. notify.Service
// satisfies it. Defined here as a narrow seam so the jobs package does not depend on the notify
// package's types.
type AdminNotifier interface {
	PublishSystem(ctx context.Context, title, content string) error
}

// PoolExhaustedAlert is enqueued off the 503 path when a unique-flag challenge runs out of
// instances. It is deduplicated per challenge (see InsertOpts) so a stampede of late registrants
// against one dead challenge raises exactly one alert.
//
// It is enqueued with a plain (non-transactional) insert: the issue transaction that discovered the
// exhaustion has already rolled back — there is nothing to bind the enqueue to, and a plain insert
// is the correct tool.
type PoolExhaustedAlert struct {
	ChallengeID   int64  `json:"challenge_id"`
	ChallengeName string `json:"challenge_name"`
}

// Kind is the River job kind. Stable across renames of the Go type.
func (PoolExhaustedAlert) Kind() string { return "pool_exhausted_alert" }

// InsertOpts deduplicates by args within the window: the whole point of the alert is that it fires
// once per challenge, not once per player who hit the empty pool.
func (PoolExhaustedAlert) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: river.QueueDefault,
		UniqueOpts: river.UniqueOpts{
			ByArgs:   true,
			ByPeriod: poolAlertDedupWindow,
		},
	}
}

// PoolExhaustedAlertWorker turns a queued alert into an operator notification. The notification is
// system-authored (no admin actor) because no human triggered it.
type PoolExhaustedAlertWorker struct {
	river.WorkerDefaults[PoolExhaustedAlert]
	Notifier AdminNotifier
	Log      *slog.Logger
}

func (w *PoolExhaustedAlertWorker) Work(ctx context.Context, job *river.Job[PoolExhaustedAlert]) error {
	title := "Challenge instances exhausted"
	content := fmt.Sprintf(
		"The unique-flag pool for %q (challenge %d) is empty: players without an instance cannot open it. Upload more instances.",
		job.Args.ChallengeName, job.Args.ChallengeID,
	)

	if w.Log != nil {
		w.Log.WarnContext(ctx, "unique-flag pool exhausted",
			"challenge_id", job.Args.ChallengeID, "challenge", job.Args.ChallengeName)
	}
	if err := w.Notifier.PublishSystem(ctx, title, content); err != nil {
		return fmt.Errorf("jobs: pool-exhausted alert for challenge %d: %w", job.Args.ChallengeID, err)
	}
	return nil
}
