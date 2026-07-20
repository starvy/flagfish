package adminops

import (
	"context"
	"io"

	"github.com/starvy/flagfish/internal/platform/exporter"
)

// ExportUsersCSV streams the whole user roster as CSV. It is a read, but it lives here because this is
// the service that already holds the pool the admin surface writes through; the CSV shaping and its
// quoting belong to the exporter package. The rows go straight to w as they are scanned — an organiser
// pulling a thousands-row roster for eligibility never assembles it in memory first.
func (s *Service) ExportUsersCSV(ctx context.Context, w io.Writer) error {
	return exporter.WriteUsers(ctx, s.pool, w)
}

// ExportTeamsCSV streams the whole team roster as CSV, on the same terms as ExportUsersCSV.
func (s *Service) ExportTeamsCSV(ctx context.Context, w io.Writer) error {
	return exporter.WriteTeams(ctx, s.pool, w)
}
