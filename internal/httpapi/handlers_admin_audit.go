package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// adminAuditEntry is one audit_log row on the wire. before/after are the trigger's captured JSON,
// served verbatim; ip is rendered as text. actor_id is null for a system, migration, or console
// write — the audit trail records those too.
type adminAuditEntry struct {
	ID          int64           `json:"id"`
	ActorID     *int64          `json:"actor_id,omitempty"`
	Action      string          `json:"action"`
	TargetTable string          `json:"target_table"`
	TargetID    *int64          `json:"target_id,omitempty"`
	Before      json.RawMessage `json:"before,omitempty"`
	After       json.RawMessage `json:"after,omitempty"`
	At          time.Time       `json:"at"`
	IP          *string         `json:"ip,omitempty"`
}

// Huma does not support pointer query parameters, so the optional filters are value types whose zero
// means "unset": a valid actor or target id is always positive, and an empty action or table filters
// nothing.
type adminListAuditInput struct {
	Page        int    `query:"page" minimum:"1" maximum:"1000000" default:"1"`
	PerPage     int    `query:"per_page" minimum:"1" maximum:"100" default:"50"`
	Actor       int64  `query:"actor" minimum:"0"`
	Action      string `query:"action" enum:"INSERT,UPDATE,DELETE"`
	TargetTable string `query:"target_table"`
	TargetID    int64  `query:"target_id" minimum:"0"`
}

type adminListAuditOutput struct {
	Body struct {
		Entries []adminAuditEntry `json:"entries"`
		Total   int64             `json:"total"`
		Page    int               `json:"page"`
		PerPage int               `json:"per_page"`
	}
}

func (s *Server) registerAdminAudit() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-audit", Method: http.MethodGet, Path: "/audit",
		Summary: "Read the audit trail (paginated, newest first, filterable)", Tags: []string{"admin/audit"},
	}, s.adminListAudit)
}

func (s *Server) adminListAudit(ctx context.Context, in *adminListAuditInput) (*adminListAuditOutput, error) {
	var filter adminops.AuditFilter
	if in.Actor > 0 {
		filter.ActorID = &in.Actor
	}
	if in.Action != "" {
		filter.Action = &in.Action
	}
	if in.TargetTable != "" {
		filter.TargetTable = &in.TargetTable
	}
	if in.TargetID > 0 {
		filter.TargetID = &in.TargetID
	}
	page, err := s.opts.AdminOps.ListAudit(ctx, filter, in.Page, in.PerPage)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list audit")
	}

	out := &adminListAuditOutput{}
	out.Body.Total = page.Total
	out.Body.Page = in.Page
	out.Body.PerPage = in.PerPage
	out.Body.Entries = make([]adminAuditEntry, len(page.Entries))
	for i := range page.Entries {
		e := &page.Entries[i]
		var ip *string
		if e.Ip != nil {
			s := e.Ip.String()
			ip = &s
		}
		out.Body.Entries[i] = adminAuditEntry{
			ID: e.ID, ActorID: e.ActorID, Action: e.Action,
			TargetTable: e.TargetTable, TargetID: e.TargetID,
			Before: e.Before, After: e.After, At: e.At.Time, IP: ip,
		}
	}
	return out, nil
}
