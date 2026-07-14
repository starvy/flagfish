//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/starvy/flagfish/internal/domain/account"
)

func scoreboardAt(asOf time.Time) string {
	v := url.Values{}
	v.Set("as_of", asOf.UTC().Format(time.RFC3339Nano))
	return "/api/v1/scoreboard?" + v.Encode()
}

func withFreeze(freeze time.Time) [2]string {
	return [2]string{"freeze", strconv.FormatInt(freeze.Unix(), 10)}
}

// as_of pointed before the freeze is a plain history query: it returns the board exactly as it
// stood at that instant, so a solve that landed after as_of is absent even though it predates the
// freeze.
func TestScoreboardAsOfBeforeFreezeShowsHistory(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, withFreeze(freeze))
	seedTimelineAnchored(f, freeze)

	// as_of sits between Early's solve (freeze-2h) and Mid's (freeze-30m).
	board := f.decodeStandings(f.get(t, scoreboardAt(freeze.Add(-time.Hour))))
	if !board.has("Early") {
		t.Fatalf("as_of before the freeze must show the pre-as_of solver: %+v", board.Standings)
	}
	if board.has("Mid") {
		t.Fatalf("Mid solved after as_of and must be absent from the historical board: %+v", board.Standings)
	}
	if board.has("Late") {
		t.Fatalf("post-freeze solver leaked into a historical board: %+v", board.Standings)
	}
}

// A non-admin cannot travel past the freeze horizon. ?as_of=<now> during a freeze must be clamped
// to the freeze, so the post-freeze solver stays hidden. Remove the clamp in the handler and Late
// appears — this test is the guard on that clamp.
func TestScoreboardAsOfAfterFreezeClampedForNonAdmin(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, withFreeze(freeze))
	seedTimelineAnchored(f, freeze)

	// Ask for the live instant. The clamp must pull it back to the freeze.
	board := f.decodeStandings(f.get(t, scoreboardAt(time.Now().Add(time.Minute))))
	if !board.has("Early") || !board.has("Mid") {
		t.Fatalf("clamped board must still show every pre-freeze solver: %+v", board.Standings)
	}
	if board.has("Late") {
		t.Fatalf("as_of=now was NOT clamped to the freeze: the post-freeze solver leaked: %+v", board.Standings)
	}
}

// ?preview=true is an admin's key to the live board during a freeze. A player holding the same
// parameter still sees the frozen board — preview is never a bypass for a non-exempt viewer.
func TestScoreboardPreviewLiveForAdminOnly(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, withFreeze(freeze))
	seedTimelineAnchored(f, freeze)

	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct horse battery")
	player, _ := f.register("Ada", "ada@ctf.test", "correct horse battery")

	adminBoard := f.decodeStandings(mustGet(t, f, "/api/v1/scoreboard?preview=true", withCookie(admin)))
	if !adminBoard.has("Late") {
		t.Fatalf("admin ?preview must see the live board incl. the post-freeze solver: %+v", adminBoard.Standings)
	}

	playerBoard := f.decodeStandings(mustGet(t, f, "/api/v1/scoreboard?preview=true", withCookie(player)))
	if playerBoard.has("Late") {
		t.Fatalf("a player's ?preview must be ignored — the post-freeze solver leaked: %+v", playerBoard.Standings)
	}
}

// An admin's ?preview clears the clamp too: with the exemption they may point as_of past the freeze.
func TestScoreboardAsOfAfterFreezeHonouredForAdminPreview(t *testing.T) {
	freeze := time.Now().Add(-time.Hour)
	f := newAPI(t, account.ModeUsers, withFreeze(freeze))
	seedTimelineAnchored(f, freeze)

	admin := f.registerAdmin("Boss", "boss@ctf.test", "correct horse battery")
	path := scoreboardAt(time.Now().Add(time.Minute)) + "&preview=true"
	board := f.decodeStandings(mustGet(t, f, path, withCookie(admin)))
	if !board.has("Late") {
		t.Fatalf("admin ?preview&as_of=now must honour the requested instant: %+v", board.Standings)
	}
}

// A malformed as_of is rejected at the edge — a 422, never a silent fall-through to now.
func TestScoreboardAsOfMalformed422(t *testing.T) {
	f := newAPI(t, account.ModeUsers)
	res, body := f.do(http.MethodGet, "/api/v1/scoreboard?as_of=not-a-timestamp", nil)
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("malformed as_of must be 422, got %d: %s", res.StatusCode, body)
	}
}

// --- local helpers --------------------------------------------------------

// seedTimelineAnchored plants the Early/Mid/Late history around a given freeze.
func seedTimelineAnchored(f *apiFix, freeze time.Time) {
	f.t.Helper()
	ch := f.seedChallenge("Reversing", "rev", 100)
	early := f.seedNamedUser("Early", "early@ctf.test")
	mid := f.seedNamedUser("Mid", "mid@ctf.test")
	late := f.seedNamedUser("Late", "late@ctf.test")
	f.seedDatedSolve(ch, early, 100, freeze.Add(-2*time.Hour))
	f.seedDatedSolve(ch, mid, 300, freeze.Add(-30*time.Minute))
	f.seedDatedSolve(ch, late, 500, time.Now())
}

func mustGet(t *testing.T, f *apiFix, path string, mut ...func(*http.Request)) []byte {
	t.Helper()
	res, body := f.do(http.MethodGet, path, nil, mut...)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, res.StatusCode, body)
	}
	return body
}
