package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/domain/policy"
)

// The unique-flag pool write path. It is JSON, not multipart, and shaped for the future CLI: the
// client hashes each flag and uploads the digest as hex, so the server never sees a plaintext flag
// and cannot leak one it does not hold.

type adminPoolInstanceInput struct {
	// ValueHash is sha256(flag) as 64 hex chars. The pattern rejects the obvious mistakes; the
	// handler decodes to the 32 raw bytes and the value_hash CHECK is the final backstop.
	ValueHash  string          `json:"value_hash" pattern:"^[0-9a-fA-F]{64}$" doc:"sha256(flag) as lowercase hex"`
	ArtifactID *int64          `json:"artifact_id,omitempty"`
	Vars       json.RawMessage `json:"vars,omitempty" doc:"arbitrary per-account JSON for the description template; never the flag"`
}

type adminPoolUploadInput struct {
	ID   int64 `path:"id"`
	Body struct {
		Instances []adminPoolInstanceInput `json:"instances" minItems:"1" maxItems:"10000"`
	}
}

type adminPoolUploadOutput struct {
	Body struct {
		Generation int32    `json:"generation"`
		Inserted   int      `json:"inserted"`
		Idempotent bool     `json:"idempotent" doc:"true when the upload matched the newest generation and nothing was written"`
		Warnings   []string `json:"warnings,omitempty"`
	}
}

type adminInstanceBody struct {
	ID         int64           `json:"id"`
	ValueHash  string          `json:"value_hash"`
	ArtifactID *int64          `json:"artifact_id,omitempty"`
	Vars       json.RawMessage `json:"vars"`
	Generation int32           `json:"generation"`
	IssuedTo   *int64          `json:"issued_to,omitempty"`
	AssignedAt *time.Time      `json:"assigned_at,omitempty"`
}

type adminListInstancesInput struct {
	ID      int64 `path:"id"`
	Page    int   `query:"page" minimum:"1" maximum:"1000000" default:"1"`
	PerPage int   `query:"per_page" minimum:"1" maximum:"100" default:"50"`
}

type adminListInstancesOutput struct {
	Body struct {
		Instances []adminInstanceBody `json:"instances"`
		Total     int64               `json:"total"`
		Page      int                 `json:"page"`
		PerPage   int                 `json:"per_page"`
	}
}

type poolStatBody struct {
	ChallengeID int64  `json:"challenge_id"`
	Name        string `json:"name"`
	Total       int64  `json:"total"`
	Issued      int64  `json:"issued"`
	// Utilization is issued/total as a 0..1 fraction, computed once here so every client's gauge
	// agrees rather than each dividing for itself.
	Utilization float64 `json:"utilization"`
}

type adminPoolStatsOutput struct {
	Body struct {
		Pools []poolStatBody `json:"pools"`
	}
}

func (s *Server) registerAdminInstances() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-upload-instances", Method: http.MethodPut, Path: "/challenges/{id}/instances",
		Summary: "Upload a challenge's unique-flag instance pool (new generation)", Tags: []string{"admin/instances"},
	}, s.adminUploadInstances)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-list-instances", Method: http.MethodGet, Path: "/challenges/{id}/instances",
		Summary: "List a challenge's instance pool", Tags: []string{"admin/instances"},
	}, s.adminListInstances)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-pool-stats", Method: http.MethodGet, Path: "/pool/stats",
		Summary: "Pool utilisation for every unique-flag challenge", Tags: []string{"admin/instances"},
	}, s.adminPoolStats)
}

func (s *Server) adminUploadInstances(ctx context.Context, in *adminPoolUploadInput) (*adminPoolUploadOutput, error) {
	instances := make([]adminops.NewInstance, len(in.Body.Instances))
	for i, e := range in.Body.Instances {
		raw, err := hex.DecodeString(e.ValueHash)
		if err != nil || len(raw) != 32 {
			return nil, huma.Error422UnprocessableEntity("instance value_hash must be 64 hex characters (a sha256 digest)")
		}
		var h [32]byte
		copy(h[:], raw)
		instances[i] = adminops.NewInstance{ValueHash: h, ArtifactID: e.ArtifactID, Vars: e.Vars}
	}

	res, err := s.opts.AdminOps.ReplacePool(ctx, s.adminActor(ctx), in.ID, adminops.PoolUpload{Instances: instances})
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "upload instances")
	}
	out := &adminPoolUploadOutput{}
	out.Body.Generation = res.Generation
	out.Body.Inserted = res.Inserted
	out.Body.Idempotent = res.Idempotent
	out.Body.Warnings = res.Warnings
	return out, nil
}

func (s *Server) adminListInstances(ctx context.Context, in *adminListInstancesInput) (*adminListInstancesOutput, error) {
	page, err := s.opts.AdminOps.ListInstances(ctx, in.ID, in.Page, in.PerPage)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "list instances")
	}
	out := &adminListInstancesOutput{}
	out.Body.Total = page.Total
	out.Body.Page = in.Page
	out.Body.PerPage = in.PerPage
	out.Body.Instances = make([]adminInstanceBody, len(page.Instances))
	for i, r := range page.Instances {
		vars := r.Vars
		if len(vars) == 0 {
			vars = json.RawMessage("{}")
		}
		body := adminInstanceBody{
			ID: r.ID, ValueHash: hex.EncodeToString(r.ValueHash), ArtifactID: r.ArtifactID,
			Vars: vars, Generation: r.Generation, IssuedTo: r.IssuedTo,
		}
		if r.AssignedAt.Valid {
			t := r.AssignedAt.Time
			body.AssignedAt = &t
		}
		out.Body.Instances[i] = body
	}
	return out, nil
}

func (s *Server) adminPoolStats(ctx context.Context, _ *struct{}) (*adminPoolStatsOutput, error) {
	stats, err := s.opts.AdminOps.PoolStats(ctx)
	if err != nil {
		return nil, s.adminOpsError(ctx, err, "read pool stats")
	}
	out := &adminPoolStatsOutput{}
	out.Body.Pools = make([]poolStatBody, len(stats))
	for i, st := range stats {
		var util float64
		if st.Total > 0 {
			util = float64(st.Issued) / float64(st.Total)
		}
		out.Body.Pools[i] = poolStatBody{
			ChallengeID: st.ChallengeID, Name: st.Name,
			Total: st.Total, Issued: st.Issued, Utilization: util,
		}
	}
	return out, nil
}
