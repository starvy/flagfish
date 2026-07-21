//go:build integration

package security

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// A challenge's flags are readable by an admin and by no one else. The admin editor cannot author a
// challenge without reading back the flags already on it, so the admin detail returns them in full —
// but the same content must never reach a player-facing route, where a flag is only ever compared,
// never returned. This pins both halves at once: remove the admin gate on the read and the second
// assertion still holds, remove the redaction on the public detail and the first proves it leaked.
func TestAdminChallengeDetailReturnsFlagsButPublicDetailDoesNot(t *testing.T) {
	f := setup(t)

	f.user("author", pw, asAdmin)
	sess, err := f.acct.Login(context.Background(), "author@ctf.test", pw)
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	admin := []func(*http.Request){withCookie(sess.ID), withCSRF(sess.CSRFToken)}

	const secret = "flag{s_admin_only_secret}"

	created := f.do(http.MethodPost, "/api/v1/admin/challenges",
		append([]func(*http.Request){withBody("application/json",
			[]byte(`{"name":"reversing","category":"rev","value":100}`))}, admin...)...)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create challenge: %d (%s)", created.StatusCode, created.Body)
	}
	var ch struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(created.Body), &ch); err != nil {
		t.Fatalf("decode created challenge: %v", err)
	}

	addFlag := f.do(http.MethodPost, "/api/v1/admin/challenges/"+strconv.FormatInt(ch.ID, 10)+"/flags",
		append([]func(*http.Request){withBody("application/json",
			[]byte(`{"type":"static","content":"`+secret+`"}`))}, admin...)...)
	if addFlag.StatusCode != http.StatusCreated {
		t.Fatalf("add flag: %d (%s)", addFlag.StatusCode, addFlag.Body)
	}

	// The admin detail reads the flag back — the authoring surface depends on it.
	adminDetail := f.do(http.MethodGet, "/api/v1/admin/challenges/"+strconv.FormatInt(ch.ID, 10),
		withCookie(sess.ID))
	if adminDetail.StatusCode != http.StatusOK {
		t.Fatalf("admin detail: %d (%s)", adminDetail.StatusCode, adminDetail.Body)
	}
	if !strings.Contains(adminDetail.Body, secret) {
		t.Fatalf("admin detail did not return the flag content — the editor cannot author without it:\n%s", adminDetail.Body)
	}

	// The player-facing detail is a 200 for this verified admin, and it must not carry the flag.
	publicDetail := f.do(http.MethodGet, "/api/v1/challenges/"+strconv.FormatInt(ch.ID, 10),
		withCookie(sess.ID))
	if publicDetail.StatusCode != http.StatusOK {
		t.Fatalf("public detail: %d (%s)", publicDetail.StatusCode, publicDetail.Body)
	}
	if strings.Contains(publicDetail.Body, secret) {
		t.Fatalf("the public challenge detail leaked a flag — it must only ever be compared, never returned:\n%s", publicDetail.Body)
	}
}
