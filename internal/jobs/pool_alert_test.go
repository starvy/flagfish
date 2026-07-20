package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/riverqueue/river"
)

type stubNotifier struct {
	calls   int
	title   string
	content string
	err     error
}

func (s *stubNotifier) PublishSystem(_ context.Context, title, content string) error {
	s.calls++
	s.title, s.content = title, content
	return s.err
}

func TestPoolExhaustedAlertWorker_PublishesOnce(t *testing.T) {
	sn := &stubNotifier{}
	w := &PoolExhaustedAlertWorker{Notifier: sn}

	err := w.Work(context.Background(), &river.Job[PoolExhaustedAlert]{
		Args: PoolExhaustedAlert{ChallengeID: 7, ChallengeName: "pwn/heap"},
	})
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	if sn.calls != 1 {
		t.Fatalf("PublishSystem calls = %d, want 1", sn.calls)
	}
	if !strings.Contains(sn.content, "pwn/heap") || !strings.Contains(sn.content, "7") {
		t.Errorf("alert content does not name the challenge: %q", sn.content)
	}
}

// A failed publish is a real error the retry must see, never swallowed: an alert nobody was told
// about is worse than a loud retry.
func TestPoolExhaustedAlertWorker_SurfacesPublishError(t *testing.T) {
	sn := &stubNotifier{err: errors.New("boom")}
	w := &PoolExhaustedAlertWorker{Notifier: sn}
	if err := w.Work(context.Background(), &river.Job[PoolExhaustedAlert]{Args: PoolExhaustedAlert{ChallengeID: 1}}); err == nil {
		t.Fatal("Work must return the publish error so River retries it")
	}
}

// The alert is deduplicated by args, so a stampede against one challenge collapses to one job.
func TestPoolExhaustedAlert_DedupByArgs(t *testing.T) {
	opts := PoolExhaustedAlert{}.InsertOpts()
	if !opts.UniqueOpts.ByArgs {
		t.Error("PoolExhaustedAlert must be unique ByArgs, or every late registrant raises its own alert")
	}
	if opts.UniqueOpts.ByPeriod <= 0 {
		t.Error("PoolExhaustedAlert needs a dedup window so a pool re-emptied much later can alert again")
	}
}
