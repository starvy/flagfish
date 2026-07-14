package adminops

import (
	"context"
	"fmt"

	"github.com/starvy/flagfish/internal/db"
)

// AuditFilter narrows the audit feed. Every field is optional and independent; a nil one does not
// constrain the result.
type AuditFilter struct {
	ActorID     *int64
	Action      *string
	TargetTable *string
	TargetID    *int64
}

// AuditPage is one page of the audit feed plus the total the pagination is computed from.
type AuditPage struct {
	Entries []db.AdminListAuditRow
	Total   int64
}

// ListAudit reads the audit trail newest-first. It is read-only — the trigger-written rows are the
// facts, this only serves them.
func (s *Service) ListAudit(ctx context.Context, filter AuditFilter, page, perPage int) (AuditPage, error) {
	rows, err := s.q.AdminListAudit(ctx, db.AdminListAuditParams{
		ActorID:     filter.ActorID,
		Action:      filter.Action,
		TargetTable: filter.TargetTable,
		TargetID:    filter.TargetID,
		Lim:         int32(perPage),              //nolint:gosec // the handler caps per_page
		Off:         int32((page - 1) * perPage), //nolint:gosec // page is bounded by the handler; a large offset just returns an empty page
	})
	if err != nil {
		return AuditPage{}, fmt.Errorf("adminops: list audit: %w", err)
	}
	p := AuditPage{Entries: rows}
	if len(rows) > 0 {
		p.Total = rows[0].Total
	}
	return p, nil
}
