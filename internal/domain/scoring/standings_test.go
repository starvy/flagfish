package scoring_test

import (
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/scoring"
)

func at(minute int) time.Time {
	return time.Date(2026, 7, 14, 12, minute, 0, 0, time.UTC)
}

func TestBoardOrdersByScoreThenLastEventThenAccountID(t *testing.T) {
	events := []scoring.Event{
		{AccountID: 1, Value: 100, At: at(10)},
		{AccountID: 1, Value: 100, At: at(50)}, // acct 1: 200, last event 12:50
		{AccountID: 2, Value: 200, At: at(20)}, // acct 2: 200, last event 12:20 — got there and stopped first
		{AccountID: 3, Value: 300, At: at(59)}, // acct 3: 300 — score always wins
	}

	board := scoring.Board(events)

	want := []int64{3, 2, 1}
	for i, id := range want {
		if board[i].AccountID != id || board[i].Place != i+1 {
			t.Fatalf("place %d: got account %d (place %d), want account %d", i+1, board[i].AccountID, board[i].Place, id)
		}
	}
}

// Clause 3, account_id ASC, fires only on an exact tie of score and timestamp,
// and it exists to make the board stable.
func TestTiebreakClause3IsAccountID(t *testing.T) {
	same := at(30)
	board := scoring.Board([]scoring.Event{
		{AccountID: 77, Value: 100, At: same},
		{AccountID: 12, Value: 100, At: same},
		{AccountID: 45, Value: 100, At: same},
	})

	want := []int64{12, 45, 77}
	for i, id := range want {
		if board[i].AccountID != id {
			t.Fatalf("place %d: got %d, want %d (account_id ASC)", i+1, board[i].AccountID, id)
		}
	}
}

// The tiebreak key is the account's last scoring event, so an award earned
// after a solve can demote you in a tie.
func TestLateAwardDemotesInATie(t *testing.T) {
	board := scoring.Board([]scoring.Event{
		{AccountID: 1, Value: 200, At: at(10)}, // leader, done at 12:10
		{AccountID: 2, Value: 100, At: at(5)},  // catches up...
		{AccountID: 2, Value: 100, At: at(40)}, // ...but only at 12:40
	})

	if board[0].AccountID != 1 {
		t.Fatalf("the account whose last scoring event is EARLIER must win the tie; got %d first", board[0].AccountID)
	}
}

// An account whose only solves are zero-valued is absent from the board — not
// shown at 0. This asserts absence, which is the whole point of the INNER JOIN.
func TestZeroOnlySolverAbsentFromBoard(t *testing.T) {
	board := scoring.Board([]scoring.Event{
		{AccountID: 1, Value: 100, At: at(10)},
		{AccountID: 2, Value: 0, At: at(5)}, // zero-value solve
		{AccountID: 2, Value: 0, At: at(6)}, // and another
	})

	if len(board) != 1 {
		t.Fatalf("board has %d rows, want 1 — the zero-only solver must be absent, not present at 0", len(board))
	}
	if board[0].AccountID != 1 {
		t.Fatalf("wrong account survived: %d", board[0].AccountID)
	}
}

// ...and a zero-value event must not move the tiebreak key of an account that is
// on the board.
func TestZeroValueEventDoesNotMoveTheTiebreak(t *testing.T) {
	withZero := scoring.Board([]scoring.Event{
		{AccountID: 1, Value: 100, At: at(10)},
		{AccountID: 1, Value: 0, At: at(59)}, // late, worthless — must not count
		{AccountID: 2, Value: 100, At: at(20)},
	})
	if withZero[0].AccountID != 1 {
		t.Fatal("a zero-value event moved MAX(date) and demoted the account")
	}
	if !withZero[0].LastEvent.Equal(at(10)) {
		t.Fatalf("LastEvent = %v, want 12:10 — the zero-value event leaked into the tiebreak", withZero[0].LastEvent)
	}
}

func TestNegativeAwardsSubtract(t *testing.T) {
	board := scoring.Board([]scoring.Event{
		{AccountID: 1, Value: 100, At: at(10)},
		{AccountID: 1, Value: -50, At: at(20)}, // a penalty
	})
	if board[0].Score != 50 {
		t.Fatalf("score = %d, want 50", board[0].Score)
	}
}

// ---------------------------------------------------------------------------
// Properties
// ---------------------------------------------------------------------------

// The headline invariant: the board is a pure function of the event set. Any
// interleaving — any arrival order, any concurrent submit — yields the same board.
// This is what "standings are deterministic" means, and it is the claim the
// concurrency suite pins against a real database.
func TestPropertyBoardIsIndependentOfEventOrder(t *testing.T) {
	r := rand.New(rand.NewSource(42))

	for range 300 {
		n := r.Intn(60) + 1
		events := make([]scoring.Event, n)
		for j := range events {
			events[j] = scoring.Event{
				AccountID: int64(r.Intn(8) + 1),
				Value:     r.Intn(11) * 50, // deliberately includes 0
				At:        at(r.Intn(60)),  // deliberately collides timestamps
			}
		}

		want := scoring.Board(events)

		for range 5 {
			shuffled := append([]scoring.Event(nil), events...)
			r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })

			if got := scoring.Board(shuffled); !reflect.DeepEqual(got, want) {
				t.Fatalf("board depends on event order:\n got %+v\nwant %+v", got, want)
			}
		}
	}
}

// score == sum of the account's non-zero event values, always.
func TestPropertyScoreIsTheSumOfCountingEvents(t *testing.T) {
	r := rand.New(rand.NewSource(1234))

	for range 300 {
		events := make([]scoring.Event, r.Intn(50)+1)
		sums := map[int64]int{}
		for j := range events {
			ev := scoring.Event{
				AccountID: int64(r.Intn(5) + 1),
				Value:     r.Intn(21)*10 - 100, // negatives and zeroes included
				At:        at(r.Intn(60)),
			}
			events[j] = ev
			if ev.Counts() {
				sums[ev.AccountID] += ev.Value
			}
		}

		for _, s := range scoring.Board(events) {
			if s.Score != sums[s.AccountID] {
				t.Fatalf("account %d: score %d, want %d", s.AccountID, s.Score, sums[s.AccountID])
			}
			delete(sums, s.AccountID)
		}
		// Everything left over must be an account with no counting events at all:
		// those are absent from the board by construction, and there is no other
		// reason for an account to be missing.
		for id, sum := range sums {
			if sum != 0 {
				t.Fatalf("account %d has score %d but is missing from the board", id, sum)
			}
		}
	}
}

// Less must be a strict total order, or the board is not deterministic and
// sort.SliceStable is hiding it from us.
func TestPropertyLessIsAStrictTotalOrder(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	entries := make([]scoring.Entry, 200)
	for i := range entries {
		entries[i] = scoring.Entry{
			AccountID: int64(r.Intn(20)),
			Score:     r.Intn(5) * 100,
			LastEvent: at(r.Intn(3)),
		}
	}

	for _, a := range entries {
		if scoring.Less(a, a) {
			t.Fatalf("Less is not irreflexive at %+v", a)
		}
		for _, b := range entries {
			if a == b {
				continue
			}
			ab, ba := scoring.Less(a, b), scoring.Less(b, a)
			if ab && ba {
				t.Fatalf("Less is not asymmetric: %+v <-> %+v", a, b)
			}
			if a.AccountID != b.AccountID && !ab && !ba {
				t.Fatalf("Less is not total: %+v and %+v are incomparable", a, b)
			}
		}
	}
}
