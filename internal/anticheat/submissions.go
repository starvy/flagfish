package anticheat

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/starvy/flagfish/internal/db"
)

// SubmissionFilter narrows the live feed. Every field is optional and they combine with AND; a nil
// field is "any". Nothing here widens visibility — the whole feed is already behind the admin wall.
type SubmissionFilter struct {
	Type        *string
	ChallengeID *int64
	UserID      *int64
	TeamID      *int64
}

// SubmissionCursor is the keyset position: the (date, id) of the last row a page returned. The next
// page resumes strictly before it, so a page boundary is stable even as new attempts land ahead of it.
type SubmissionCursor struct {
	Date time.Time
	ID   int64
}

// SubmissionRow is one attempt as the review feed shows it. IP, the account names and the attribution
// are nullable: an attempt outlives display context, and attributed_account_id is only set for a
// matched unique flag. AttributedAccountID is read straight from the stamped column, never re-derived.
type SubmissionRow struct {
	ID                  int64
	Date                time.Time
	Type                string
	Provided            string
	IP                  *netip.Addr
	ChallengeID         int64
	ChallengeName       string
	UserID              int64
	UserName            *string
	TeamID              *int64
	TeamName            *string
	AttributedAccountID *int64
}

// SubmissionsPage is one keyset page plus the cursor to fetch the next. Next is nil at the end of the
// feed — a short page is the last page, so there is nothing further to resume from.
type SubmissionsPage struct {
	Rows []SubmissionRow
	Next *SubmissionCursor
}

// Submissions returns one keyset page of the attempt log, newest first, filtered by filter. after is
// the cursor from the previous page, or nil for the first. limit is the page size, bounded by the
// caller.
func (s *Service) Submissions(ctx context.Context, filter SubmissionFilter, after *SubmissionCursor, limit int) (SubmissionsPage, error) {
	var (
		beforeDate pgtype.Timestamptz
		beforeID   *int64
	)
	if after != nil {
		beforeDate = pgtype.Timestamptz{Time: after.Date, Valid: true}
		id := after.ID
		beforeID = &id
	}

	rows, err := s.q.ListSubmissions(ctx, db.ListSubmissionsParams{
		BeforeDate:  beforeDate,
		BeforeID:    beforeID,
		Type:        filter.Type,
		ChallengeID: filter.ChallengeID,
		UserID:      filter.UserID,
		TeamID:      filter.TeamID,
		Lim:         int32(limit), //nolint:gosec // limit is bounded by the handler
	})
	if err != nil {
		return SubmissionsPage{}, fmt.Errorf("anticheat: list submissions: %w", err)
	}

	page := SubmissionsPage{Rows: make([]SubmissionRow, len(rows))}
	for i := range rows {
		r := &rows[i]
		page.Rows[i] = SubmissionRow{
			ID: r.ID, Date: r.Date.Time, Type: r.Type, Provided: r.Provided, IP: r.Ip,
			ChallengeID: r.ChallengeID, ChallengeName: r.ChallengeName,
			UserID: r.UserID, UserName: r.UserName,
			TeamID: r.TeamID, TeamName: r.TeamName,
			AttributedAccountID: r.AttributedAccountID,
		}
	}
	// A full page might have more behind it; a short one is the tail. Only then is there a cursor.
	if len(rows) == limit && len(rows) > 0 {
		last := rows[len(rows)-1]
		page.Next = &SubmissionCursor{Date: last.Date.Time, ID: last.ID}
	}
	return page, nil
}
