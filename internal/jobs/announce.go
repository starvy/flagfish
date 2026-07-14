package jobs

import (
	"time"

	"github.com/riverqueue/river"
)

// AnnounceFirstBlood is enqueued inside the submit transaction, so a rolled-back solve
// un-enqueues its own announcement.
//
// The fields are a snapshot of the moment of the solve: the worker must not re-derive
// the challenge name or solver from mutable rows at send time.
type AnnounceFirstBlood struct {
	ChallengeID   int64  `json:"challenge_id"`
	ChallengeName string `json:"challenge_name"`
	UserID        int64  `json:"user_id"`
	TeamID        *int64 `json:"team_id,omitempty"`
	SolveID       int64  `json:"solve_id"`
	// SolvedAt is the authoritative event time: it decides both freeze suppression
	// (an event at or after the freeze must not be announced) and the staleness TTL.
	// Stamped from the solve row, not read from the clock at send time.
	SolvedAt time.Time `json:"solved_at"`
}

// Kind is the River job kind. It is stable across renames of the Go type; changing it
// orphans queued jobs.
func (AnnounceFirstBlood) Kind() string { return "announce_first_blood" }

// InsertOpts queues the announcement. The worker checks the scoreboard freeze at send
// time, not here — announcements leak exactly what a freeze hides.
func (AnnounceFirstBlood) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: river.QueueDefault}
}
