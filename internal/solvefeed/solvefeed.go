// Package solvefeed is the live solve pulse: a broadcaster holding one dedicated Postgres connection
// LISTENing for solves, and the publish side that signals one from inside the solve transaction.
//
// It is deliberately NOT the notifications bus, and the difference is the delivery contract rather
// than the plumbing. A notification is a persisted row with a replay log: a client that reconnects
// is owed what it missed. A solve pulse is ephemeral — it exists to make something flash on a map at
// the moment it happens, and a pulse replayed thirty seconds later is not late news, it is wrong. So
// there is no table, no watermark and no catch-up here, and a subscriber that was not connected
// missed nothing it should be told about.
//
// The payload carries no account, team or score. A viewer learns that a challenge was solved, never
// by whom — which is what lets the feed be open to every player without becoming a live scoreboard
// that outruns the frozen one.
package solvefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// channel is the LISTEN/NOTIFY channel.
const channel = "solves"

// An Event is one solve, exactly as it goes on the wire.
//
// Every field is stamped by the transaction that produced it rather than re-read by the listener.
// FirstBlood especially: it is decided under the challenge lock, from a count that is only exact
// inside that transaction, and there is no column to read it back from afterwards. A listener that
// recomputed it would be publishing an opinion about a fact that was already established.
//
// FirstBlood follows the challenge's own first_blood setting, so it is false on a challenge
// configured 'none' even for the solve that happened to be first — the same condition that decides
// whether the announcement webhook fires. It reports a first blood the product recognises, not an
// arithmetic fact about ordering.
type Event struct {
	SolveID     int64     `json:"solve_id"`
	ChallengeID int64     `json:"challenge_id"`
	FirstBlood  bool      `json:"first_blood"`
	SolvedAt    time.Time `json:"solved_at"`
}

// Publish signals a solve to every replica.
//
// It MUST be called on the transaction that inserted the solve. Postgres holds a NOTIFY until
// commit, so a rolled-back solve wakes nobody and a delivered pulse always refers to a solve that
// really happened — the same property the first-blood enqueue relies on, for the same reason.
//
// The whole event travels in the payload rather than an id the listener reloads. NOTIFY's 8 kB
// budget is not the reason; the reason is that first_blood is not a column, so there is nothing to
// reload it from.
func Publish(ctx context.Context, tx pgx.Tx, e Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("solvefeed: marshal event: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, channel, string(payload)); err != nil {
		return fmt.Errorf("solvefeed: signal solve %d: %w", e.SolveID, err)
	}
	return nil
}
