package httpapi

import (
	"context"
	"io"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/platform/exporter"
)

// CSV exports live under ClassExport, the admin-only route class. They are organiser artifacts —
// final standings for prizes, the account roster for eligibility — so they are the ADMIN board:
// hidden and banned rows are present and flagged, exactly as the admin scoreboard sees them.
func (s *Server) registerAdminExport() {
	// The standings export needs the board read service; users and teams need the adminops pool. This
	// func is only called with adminops wired, so guard the standings route on the board alone.
	if s.opts.Board != nil {
		Register(s.Admin, policy.ClassExport, huma.Operation{
			OperationID: "admin-export-standings", Method: http.MethodGet, Path: "/export/standings.csv",
			Summary: "Export the final standings as CSV", Tags: []string{"admin/export"},
		}, s.adminExportStandings)
	}

	Register(s.Admin, policy.ClassExport, huma.Operation{
		OperationID: "admin-export-users", Method: http.MethodGet, Path: "/export/users.csv",
		Summary: "Export the user roster as CSV", Tags: []string{"admin/export"},
	}, s.adminExportUsers)

	Register(s.Admin, policy.ClassExport, huma.Operation{
		OperationID: "admin-export-teams", Method: http.MethodGet, Path: "/export/teams.csv",
		Summary: "Export the team roster as CSV", Tags: []string{"admin/export"},
	}, s.adminExportTeams)
}

func (s *Server) adminExportStandings(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
	// admin=true, no bracket, no limit: the whole final board including hidden and banned. Fetched here
	// rather than in the stream body so a read failure is a real 500, not a truncated 200.
	entries, err := s.opts.Board.Top(ctx, true, nil, 0)
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "standings export read failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load the standings")
	}
	rows := make([]exporter.StandingRow, len(entries))
	for i, e := range entries {
		bracket := ""
		if e.BracketName != nil {
			bracket = *e.BracketName
		}
		rows[i] = exporter.StandingRow{
			Rank: i + 1, AccountID: e.AccountID, Name: e.Name, Bracket: bracket,
			Score: e.Score, LastEvent: e.LastEvent, Hidden: e.Hidden, Banned: e.Banned,
		}
	}
	return s.csvStream("standings.csv", func(_ context.Context, w io.Writer) error {
		return exporter.WriteStandings(w, rows)
	}), nil
}

func (s *Server) adminExportUsers(_ context.Context, _ *struct{}) (*huma.StreamResponse, error) {
	return s.csvStream("users.csv", s.opts.AdminOps.ExportUsersCSV), nil
}

func (s *Server) adminExportTeams(_ context.Context, _ *struct{}) (*huma.StreamResponse, error) {
	return s.csvStream("teams.csv", s.opts.AdminOps.ExportTeamsCSV), nil
}

// csvStream builds the streaming response shared by the three exports: the CSV headers, then the
// caller's writer draining rows into the body. Like the file download, the response status and headers
// are committed the moment the body func runs, so an error surfacing mid-stream can only be logged —
// the fallible reads the handler can catch (the standings query) are run before this is called.
func (s *Server) csvStream(filename string, write func(context.Context, io.Writer) error) *huma.StreamResponse {
	//nolint:contextcheck // the stream body gets a huma.Context and threads it (hctx.Context()); contextcheck only sees a context.Context param.
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		hctx.SetHeader("Content-Type", "text/csv; charset=utf-8")
		hctx.SetHeader("Content-Disposition", contentDisposition(filename))
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		if err := write(hctx.Context(), hctx.BodyWriter()); err != nil {
			// Headers and a 200 are already on the wire; the export broke mid-stream. Log and stop —
			// there is no second status to send.
			s.opts.Log.WarnContext(hctx.Context(), "csv export stream failed", "file", filename, "error", err)
		}
	}}
}
