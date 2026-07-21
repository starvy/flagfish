//go:build integration

// The worker's context lifetime, against a real River client and a real Postgres — the only
// place the bug this guards against is visible, because it lives in River's fetch loop rather
// than in any code of ours.
package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"go.uber.org/fx"
)

// recordingLifecycle captures the hooks so the test can run OnStart with a context of its
// own choosing — fx's real one carries the boot budget, which is the whole point here.
type recordingLifecycle struct{ hooks []fx.Hook }

func (l *recordingLifecycle) Append(h fx.Hook) { l.hooks = append(l.hooks, h) }

type lifecycleProbe struct{}

func (lifecycleProbe) Kind() string { return "lifecycle_probe" }

type lifecycleProbeWorker struct {
	river.WorkerDefaults[lifecycleProbe]
	worked chan struct{}
}

func (w *lifecycleProbeWorker) Work(context.Context, *river.Job[lifecycleProbe]) error {
	close(w.worked)
	return nil
}

// River keeps the context passed to Start for the client's whole life, so a worker started on
// fx's OnStart context stops fetching the moment the boot budget expires — with no error at the
// enqueue site, which keeps answering "queued". Jobs inserted after that deadline must still run.
func TestWorkerLifecycle_OutlivesTheStartContext(t *testing.T) {
	p := pool(t)
	worked := make(chan struct{})
	workers := river.NewWorkers()
	river.AddWorker(workers, &lifecycleProbeWorker{worked: worked})

	c, err := river.NewClient(riverpgxv5.New(p), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
		Workers: workers,
	})
	if err != nil {
		t.Fatalf("worker client: %v", err)
	}

	var lc recordingLifecycle
	appendWorkerLifecycle(context.Background(), &lc, c, testLog())
	if len(lc.hooks) != 1 {
		t.Fatalf("expected exactly one lifecycle hook, got %d", len(lc.hooks))
	}
	hook := lc.hooks[0]

	startCtx, cancelStart := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStart()
	if err := hook.OnStart(startCtx); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := hook.OnStop(stopCtx); err != nil {
			t.Errorf("worker stop: %v", err)
		}
	})

	<-startCtx.Done()

	if _, err := c.Insert(context.Background(), lifecycleProbe{}, nil); err != nil {
		t.Fatalf("insert probe job: %v", err)
	}

	select {
	case <-worked:
	case <-time.After(30 * time.Second):
		t.Fatal("job was never worked: the worker died with the start context")
	}
}
