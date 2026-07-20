//go:build integration

package concurrency

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/domain/account"
)

// uploadPool drives the real admin write path (adminops.ReplacePool) to land a generation, exactly
// as the pool-upload endpoint does. Each re-upload bumps the generation. The actor id is arbitrary —
// audit_log.actor_id has no FK — so the concurrency fixture need not seed an admin user.
func (f *fixture) uploadPool(svc *adminops.Service, challengeID int64, flags []string) adminops.PoolResult {
	f.t.Helper()
	insts := make([]adminops.NewInstance, len(flags))
	for i, fl := range flags {
		insts[i] = adminops.NewInstance{ValueHash: sha256.Sum256([]byte(fl))}
	}
	res, err := svc.ReplacePool(context.Background(), audit.Actor{ID: 1}, challengeID, adminops.PoolUpload{Instances: insts})
	if err != nil {
		f.t.Fatalf("upload pool: %v", err)
	}
	return res
}

func poolFlags(challengeID int64, gen, n int) []string {
	out := make([]string, n)
	for i := range n {
		out[i] = fmt.Sprintf("flagfish{rot_%d_g%d_%d}", challengeID, gen, i)
	}
	return out
}

// A pool rotation mid-event must not corrupt issuance: uploading a fresh generation through the real
// admin write path while N accounts are being issued their instances must still leave exactly one
// instance per account, each distinct. Adding a generation only adds unissued rows; the advisory
// lock and UNIQUE(instance_id) keep the pick safe regardless of when the new rows appear.
func TestFlagPool_Rotation_UnderConcurrentIssuance_NoDoubleIssue(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)
	admin := adminops.New(f.pool)

	ch := f.seedChallenge(challengeSpec{Value: 500, FlagMode: "unique"})
	f.uploadPool(admin, ch, poolFlags(ch, 1, N)) // generation 1: exactly enough for everyone

	accounts := make([]int64, N)
	for i := range N {
		_, accounts[i] = f.seedUser(fmt.Sprintf("rot%03d", i))
	}

	// A rotator uploads generation 2 while the field is being issued from generation 1.
	var rot sync.WaitGroup
	rot.Add(1)
	go func() {
		defer rot.Done()
		f.uploadPool(admin, ch, poolFlags(ch, 2, N))
	}()

	var mu sync.Mutex
	seen := make(map[int64]int64) // instanceID -> accountID
	errs := race(N, func(i int) error {
		inst, err := f.svc.IssueInstance(ctx, ch, accounts[i])
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		if prev, dup := seen[inst.InstanceID]; dup {
			return fmt.Errorf("instance %d issued to BOTH account %d and account %d", inst.InstanceID, prev, accounts[i])
		}
		seen[inst.InstanceID] = accounts[i]
		return nil
	})
	rot.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if len(seen) != N {
		t.Errorf("distinct instances issued = %d, want %d — a rotation caused a double-issue", len(seen), N)
	}
	if n := f.count(`SELECT count(*) FROM flag_issues WHERE challenge_id = $1`, ch); n != N {
		t.Errorf("flag_issues rows = %d, want %d", n, N)
	}
	if n := f.count(
		`SELECT count(*) FROM (SELECT instance_id FROM flag_issues GROUP BY instance_id HAVING count(*) > 1) x`,
	); n != 0 {
		t.Errorf("instances issued more than once = %d, want 0", n)
	}
}

// After a rotation, new issues come from the newest generation while prior issues stay pinned to the
// instance they were assigned from — which is what keeps a submission attributed under a previous
// generation attributable after the author re-uploads the pool.
func TestFlagPool_Rotation_NewIssuesUseNewestGeneration_PriorIssuesPinned(t *testing.T) {
	f := setup(t, account.ModeUsers)
	ctx := testCtx(t)
	admin := adminops.New(f.pool)

	ch := f.seedChallenge(challengeSpec{Value: 500, FlagMode: "unique"})
	g1 := f.uploadPool(admin, ch, poolFlags(ch, 1, 2))
	if g1.Generation != 1 {
		t.Fatalf("first upload generation = %d, want 1", g1.Generation)
	}

	_, early := f.seedUser("early")
	instEarly, err := f.svc.IssueInstance(ctx, ch, early)
	if err != nil {
		t.Fatalf("issue early: %v", err)
	}
	if instEarly.Generation != 1 {
		t.Fatalf("early account issued from generation %d, want 1", instEarly.Generation)
	}

	// Rotate: a fresh generation 2.
	g2 := f.uploadPool(admin, ch, poolFlags(ch, 2, 3))
	if g2.Generation != 2 {
		t.Fatalf("second upload generation = %d, want 2", g2.Generation)
	}

	// New accounts draw from the newest generation, not the leftover generation-1 instance.
	for _, name := range []string{"late-a", "late-b", "late-c"} {
		_, acct := f.seedUser(name)
		inst, issErr := f.svc.IssueInstance(ctx, ch, acct)
		if issErr != nil {
			t.Fatalf("issue %s: %v", name, issErr)
		}
		if inst.Generation != 2 {
			t.Errorf("%s issued from generation %d, want 2 (newest)", name, inst.Generation)
		}
	}

	// The early account's issue is untouched by the rotation: still its original generation-1
	// instance, and re-issuing is idempotent onto that same row.
	reissue, err := f.svc.IssueInstance(ctx, ch, early)
	if err != nil {
		t.Fatalf("re-issue early: %v", err)
	}
	if reissue.InstanceID != instEarly.InstanceID || reissue.Generation != 1 {
		t.Errorf("early re-issue = instance %d gen %d, want instance %d gen 1 — a rotation moved a prior issue",
			reissue.InstanceID, reissue.Generation, instEarly.InstanceID)
	}
}
