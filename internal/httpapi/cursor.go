package httpapi

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Keyset cursors, shared by every paginated list on both surfaces.
//
// A cursor is opaque over the wire: the client round-trips it without parsing, so its shape stays
// ours to change. It carries the (date, id) of the last row a page read — the exact keyset the
// query resumes from. Never an offset: an offset re-walks the skipped rows and drifts as new rows
// land mid-scroll, which during a live event is every page.

// errBadCursor is the single failure mode a malformed token surfaces: the exact parse fault is
// never shown to the caller (it becomes one 422), so there is nothing to gain from wrapping each
// step.
var errBadCursor = errors.New("malformed cursor")

func encodeKeysetCursor(date time.Time, id int64) string {
	raw := strconv.FormatInt(date.UnixNano(), 10) + ":" + strconv.FormatInt(id, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeKeysetCursor(s string) (time.Time, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, 0, errBadCursor
	}
	nanos, id, ok := strings.Cut(string(b), ":")
	if !ok {
		return time.Time{}, 0, errBadCursor
	}
	n, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return time.Time{}, 0, errBadCursor
	}
	i, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return time.Time{}, 0, errBadCursor
	}
	return time.Unix(0, n).UTC(), i, nil
}
