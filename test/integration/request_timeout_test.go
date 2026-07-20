//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/starvy/flagfish/internal/domain/account"
	"github.com/starvy/flagfish/internal/httpapi"
)

// The per-request deadline must bound a wedged JSON handler and must NOT reach the long-lived SSE
// stream. Both halves run against one server whose deadline is set deliberately short.
//
// The mount boundary is what this test defends: the JSON APIs are registered on a nested group that
// carries requestDeadline, while the SSE route is registered on the raw gated router that does not.
// Regress that — move the stream under the deadline — and the second half fails as the stream is cut.
func TestRequestDeadline_BoundsJSONButNotSSE(t *testing.T) {
	const deadline = 500 * time.Millisecond
	nf := newNotifyAPI(t, account.ModeUsers, func(o *httpapi.Options) { o.RequestTimeout = deadline })
	ctx := context.Background()

	// Seed the accounts in-process: Argon2id hashing must not run under the short HTTP deadline —
	// it would be cut, and it is not what this test is about.
	player, err := nf.acct.Register(ctx, "player", "player@example.com", "correct-horse-battery", true, "", nil)
	if err != nil {
		t.Fatalf("seed player: %v", err)
	}
	admin, err := nf.acct.Register(ctx, "root", "root@example.com", "correct-horse-battery", true, "", nil)
	if err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	nf.promoteAdmin("root@example.com")

	// --- the JSON surface IS bounded ------------------------------------------------------------
	// Hold the challenge row exactly as the correct submit path would, so the submit's lazy lock
	// acquisition blocks. Without the deadline the request hangs on the lock; with it, pgx observes
	// the cancelled request context and the handler returns promptly with a 500.
	chID := nf.seedChallenge("Locked", "misc", 100)
	nf.seedFlag(chID, "flag{correct}")

	holder, err := nf.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	var locked int64
	if err := holder.QueryRow(ctx,
		`SELECT id FROM challenges WHERE id=$1 FOR NO KEY UPDATE`, chID).Scan(&locked); err != nil {
		t.Fatalf("take the lock: %v", err)
	}

	start := time.Now()
	res, _ := nf.do(http.MethodPost, fmt.Sprintf("/api/v1/challenges/%d/attempt", chID),
		map[string]any{"flag": "flag{correct}"}, withCookie(player.ID), withCSRF(player.CSRFToken))
	elapsed := time.Since(start)

	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("submit under a held lock: status %d, want 500 (the request deadline cut it)", res.StatusCode)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("submit took %s: the request deadline did not fire on the JSON surface", elapsed)
	}
	if elapsed < deadline-150*time.Millisecond {
		t.Fatalf("submit returned in %s, well before the %s deadline — it cannot have blocked on the lock, "+
			"so this proves nothing about the deadline", elapsed, deadline)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release the lock: %v", err)
	}

	// --- the SSE stream is NOT bounded ----------------------------------------------------------
	// Open the stream, idle well past the deadline, then publish. If the deadline reached the stream
	// its request context would have been cancelled and the connection closed, so the event would
	// never arrive.
	s := nf.openStream(player.ID)
	defer s.close()

	time.Sleep(3 * deadline)

	id := nf.publish(admin.ID, admin.CSRFToken, "still here", "the stream outlived the request deadline")
	ev := s.waitFor(t, id, 3*time.Second)
	if ev.Title != "still here" {
		t.Fatalf("received %+v, want the notification published after the deadline elapsed", ev)
	}
}
