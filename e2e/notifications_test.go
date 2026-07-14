//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/starvy/flagfish/e2e/client"
)

// TestNotificationsPublishListStream publishes an admin notification and confirms it appears
// both in the paginated list and on the SSE stream (via the connect-time replay), and that a
// non-admin cannot publish.
func TestNotificationsPublishListStream(t *testing.T) {
	ctx := context.Background()
	u := mustRegister(t)

	title := "notice-" + suffix()
	content := "the CTF is now live"

	// A normal user cannot publish.
	walled := u.adminReq(t, http.MethodPost, "/notifications", map[string]any{"title": title, "content": content})
	if walled.code != 403 {
		t.Fatalf("non-admin publish = %d, want 403; body=%s", walled.code, walled.body)
	}

	// The admin publishes it.
	admin.adminReq(t, http.MethodPost, "/notifications", map[string]any{
		"title": title, "content": content,
	}).require(t, http.StatusCreated)

	// It shows up in the paginated list.
	list, err := u.api.ListNotificationsWithResponse(ctx, &client.ListNotificationsParams{})
	if err != nil || list.JSON200 == nil {
		t.Fatalf("list notifications: %v (status %d)", err, list.StatusCode())
	}
	if !hasNotification(list.JSON200, title) {
		t.Errorf("published notification %q missing from the list", title)
	}

	// It is delivered over the SSE stream (the connect-time replay covers it).
	ev := u.sseNotification(t, title, 20*time.Second)
	if ev.Content != content {
		t.Errorf("streamed content = %q, want %q", ev.Content, content)
	}
}

func hasNotification(body *client.ListNotificationsOutputBody, title string) bool {
	if body.Notifications == nil {
		return false
	}
	for _, n := range *body.Notifications {
		if n.Title == title {
			return true
		}
	}
	return false
}
