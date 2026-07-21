package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/anticheat"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// submissionTypes is the closed set the attempt log stores. Kept here so an unknown ?type is a 422 at
// the edge rather than a filter that silently matches nothing.
var submissionTypes = map[string]bool{
	"correct": true, "incorrect": true, "partial": true, "discard": true, "ratelimited": true,
}

type adminSubmissionsInput struct {
	// Cursor is the opaque keyset position from a previous page's next_cursor. Absent means the first
	// page. An offset is deliberately not offered — it drifts as new attempts land during a live event.
	Cursor      string `query:"cursor"`
	Limit       int    `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Type        string `query:"type" doc:"correct|incorrect|partial|discard|ratelimited"`
	ChallengeID int64  `query:"challenge_id" minimum:"1"`
	UserID      int64  `query:"user_id" minimum:"1"`
	TeamID      int64  `query:"team_id" minimum:"1"`
}

// submissionBody is one attempt. ip, the names and the attribution are nullable and omitted when
// absent; attributed_account_id is the value stamped at submit time, passed through untouched.
type submissionBody struct {
	ID                  int64     `json:"id"`
	Date                time.Time `json:"date"`
	Type                string    `json:"type"`
	Provided            string    `json:"provided"`
	IP                  *string   `json:"ip,omitempty"`
	ChallengeID         int64     `json:"challenge_id"`
	ChallengeName       string    `json:"challenge_name"`
	UserID              int64     `json:"user_id"`
	UserName            *string   `json:"user_name,omitempty"`
	TeamID              *int64    `json:"team_id,omitempty"`
	TeamName            *string   `json:"team_name,omitempty"`
	AttributedAccountID *int64    `json:"attributed_account_id,omitempty"`
}

type adminSubmissionsOutput struct {
	Body struct {
		Submissions []submissionBody `json:"submissions"`
		// NextCursor is empty on the last page. Feed it back as ?cursor to fetch the next.
		NextCursor string `json:"next_cursor,omitempty"`
		Limit      int    `json:"limit"`
	}
}

func (s *Server) registerAdminSubmissions() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-submissions", Method: http.MethodGet, Path: "/submissions",
		Summary: "Live submissions log, keyset-paginated newest-first (filter by type/challenge/user/team)",
		Tags:    []string{"admin/submissions"},
	}, s.adminSubmissions)
}

func (s *Server) adminSubmissions(ctx context.Context, in *adminSubmissionsInput) (*adminSubmissionsOutput, error) {
	filter := anticheat.SubmissionFilter{}
	if in.Type != "" {
		if !submissionTypes[in.Type] {
			return nil, huma.Error422UnprocessableEntity("unknown submission type: " + in.Type)
		}
		t := in.Type
		filter.Type = &t
	}
	if in.ChallengeID > 0 {
		filter.ChallengeID = &in.ChallengeID
	}
	if in.UserID > 0 {
		filter.UserID = &in.UserID
	}
	if in.TeamID > 0 {
		filter.TeamID = &in.TeamID
	}

	var after *anticheat.SubmissionCursor
	if in.Cursor != "" {
		cur, err := decodeSubmissionCursor(in.Cursor)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid cursor")
		}
		after = &cur
	}

	page, err := s.opts.Anticheat.Submissions(ctx, filter, after, in.Limit)
	if err != nil {
		return nil, s.anticheatError(ctx, err, "list submissions")
	}

	out := &adminSubmissionsOutput{}
	out.Body.Limit = in.Limit
	out.Body.Submissions = make([]submissionBody, len(page.Rows))
	for i := range page.Rows {
		r := &page.Rows[i]
		b := submissionBody{
			ID: r.ID, Date: r.Date, Type: r.Type, Provided: r.Provided,
			ChallengeID: r.ChallengeID, ChallengeName: r.ChallengeName,
			UserID: r.UserID, UserName: r.UserName,
			TeamID: r.TeamID, TeamName: r.TeamName,
			AttributedAccountID: r.AttributedAccountID,
		}
		if r.IP != nil {
			ip := r.IP.String()
			b.IP = &ip
		}
		out.Body.Submissions[i] = b
	}
	if page.Next != nil {
		out.Body.NextCursor = encodeSubmissionCursor(*page.Next)
	}
	return out, nil
}

func encodeSubmissionCursor(c anticheat.SubmissionCursor) string {
	return encodeKeysetCursor(c.Date, c.ID)
}

func decodeSubmissionCursor(s string) (anticheat.SubmissionCursor, error) {
	date, id, err := decodeKeysetCursor(s)
	if err != nil {
		return anticheat.SubmissionCursor{}, err
	}
	return anticheat.SubmissionCursor{Date: date, ID: id}, nil
}
