package scoring

import (
	"sort"
	"time"
)

// An Event is one scoring event: a solve or an award. Both legs of the standings
// UNION ALL reduce to this shape.
//
// Value is the stamped value — solves.value, not challenges.value. A
// snapshot is a fact; a join to a mutable row is an opinion. Awards carry their
// own value already and may be negative.
type Event struct {
	AccountID int64     // user_id or team_id, per user_mode. Resolved before we get here.
	Value     int       // solves.value | awards.value
	At        time.Time // solves.date | awards.date
}

// An Entry is one account's aggregated position: the row the board is built from.
type Entry struct {
	AccountID int64
	Score     int
	// LastEvent is MAX(date) over the account's non-zero scoring events — the
	// account's most-recent scoring event, not its first. An award earned after
	// a solve moves this key later and can demote the account in a tie.
	LastEvent time.Time
}

// A Standing is an Entry with its place on the board. Place is 1-based and
// strictly increasing: tied accounts get distinct places, resolved by Less.
type Standing struct {
	Entry
	Place int
}

// Counts reports whether an event contributes to score and tiebreak at all.
//
// Zero-value solves and awards are excluded from both the score and the
// tiebreak. The consequence is deliberate: an account whose only events are
// zero-valued produces no Entry and is absent from the board — not shown at 0.
// The INNER JOIN in the standings query enforces the same rule.
func (e Event) Counts() bool { return e.Value != 0 }

// Aggregate folds scoring events into one Entry per account.
//
// Callers pass only the events their SQL predicates already admitted (not frozen,
// not from a hidden/banned account, in-bracket). Aggregate applies exactly one
// further rule — the zero-value exclusion — because it is the one rule that
// decides whether an account appears on the board at all.
//
// The result is unordered; call Rank.
func Aggregate(events []Event) []Entry {
	byAccount := make(map[int64]*Entry, len(events))
	for _, ev := range events {
		if !ev.Counts() {
			continue
		}
		e, ok := byAccount[ev.AccountID]
		if !ok {
			e = &Entry{AccountID: ev.AccountID}
			byAccount[ev.AccountID] = e
		}
		e.Score += ev.Value
		if ev.At.After(e.LastEvent) {
			e.LastEvent = ev.At
		}
	}

	out := make([]Entry, 0, len(byAccount))
	for _, e := range byAccount {
		out = append(out, *e)
	}
	return out
}

// Less is the standings ordering rule, and the only one:
//
//	ORDER BY score DESC, last_event ASC, account_id ASC
//
// Clause 1 is the score. Clause 2 is the tiebreak that carries meaning: of two
// accounts on the same score, the one that got there and stopped first wins.
//
// Clause 3 only ever fires on an exact-timestamp tie at an identical score. It
// exists so the board is stable, not so it is fair — determinism insurance, and
// account_id buys it for nothing.
func Less(a, b Entry) bool {
	if a.Score != b.Score {
		return a.Score > b.Score // score DESC
	}
	if !a.LastEvent.Equal(b.LastEvent) {
		return a.LastEvent.Before(b.LastEvent) // last_event ASC
	}
	return a.AccountID < b.AccountID // account_id ASC — total, therefore deterministic
}

// Rank sorts entries in place into board order.
func Rank(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool { return Less(entries[i], entries[j]) })
}

// Board is the whole read path in one call: aggregate, order, place.
//
// Because Less is a total order (account_id is unique), the board is a pure
// function of the event set: any interleaving of the same events produces the
// same board, byte for byte. That is the property the concurrency suite pins.
func Board(events []Event) []Standing {
	entries := Aggregate(events)
	Rank(entries)

	out := make([]Standing, len(entries))
	for i, e := range entries {
		out[i] = Standing{Entry: e, Place: i + 1}
	}
	return out
}
