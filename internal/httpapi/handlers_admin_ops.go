package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/opsjob"
	"github.com/starvy/flagfish/internal/platform/exporter"
)

// The async ops surface: an admin triggers a backup, restore, or foreign import, gets a task id back
// immediately, and polls it for progress. The heavy work runs in a River worker over the tasks
// queue; these handlers only enqueue and report. All live under ClassAdmin, so a non-admin caller is
// refused by the policy gate before any handler runs.
func (s *Server) registerAdminOps() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-backup", Method: http.MethodPost, Path: "/backup",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Start a backup (async, over the tasks queue)", Tags: []string{"admin/ops"},
	}, s.adminBackup)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-restore", Method: http.MethodPost, Path: "/restore",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Restore a --backup archive (async)", Tags: []string{"admin/ops"},
	}, s.adminRestore)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-import", Method: http.MethodPost, Path: "/import",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Import a foreign (CTFd) archive (async, one-way)", Tags: []string{"admin/ops"},
	}, s.adminImport)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-task", Method: http.MethodGet, Path: "/tasks/{id}",
		Summary: "Report an async operation's progress", Tags: []string{"admin/ops"},
	}, s.adminTask)

	// The download is a raw stream, not a JSON body, so it returns a huma.StreamResponse like the
	// file download does.
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-task-download", Method: http.MethodGet, Path: "/tasks/{id}/download",
		Summary: "Download a finished backup's archive", Tags: []string{"admin/ops"},
	}, s.adminTaskDownload)
}

type taskBody struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	Progress  int32     `json:"progress"`
	Detail    string    `json:"detail,omitempty"`
	Error     string    `json:"error,omitempty"`
	Download  string    `json:"download,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type taskOutput struct {
	Body taskBody
}

func taskOut(t *opsjob.Task) *taskOutput {
	b := taskBody{
		ID: t.ID, Kind: t.Kind, State: t.State, Progress: t.Progress,
		Detail: t.Detail, Error: t.Error, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
	if t.Downloadable() {
		b.Download = fmt.Sprintf("/api/v1/admin/tasks/%d/download", t.ID)
	}
	return &taskOutput{Body: b}
}

type adminBackupInput struct {
	Body struct {
		// Profile chooses fidelity: "backup" (the default) is the restorable disaster-recovery
		// archive; "safe" is field-masked and shareable but not restorable.
		Profile string `json:"profile,omitempty" enum:"backup,safe" doc:"backup (restorable, default) or safe (shareable, masked)"`
	}
}

func (s *Server) adminBackup(ctx context.Context, in *adminBackupInput) (*taskOutput, error) {
	profile := exporter.ProfileBackup
	if in.Body.Profile != "" {
		p, err := exporter.ParseProfile(in.Body.Profile)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity(err.Error())
		}
		profile = p
	}
	task, err := s.opts.Ops.EnqueueBackup(ctx, s.adminActor(ctx).ID, profile)
	if err != nil {
		return nil, s.opsError(ctx, err, "start backup")
	}
	return taskOut(&task), nil
}

type adminRestoreInput struct {
	RawBody multipart.Form
}

func (s *Server) adminRestore(ctx context.Context, in *adminRestoreInput) (*taskOutput, error) {
	f, size, err := archiveUpload(in.RawBody)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	task, err := s.opts.Ops.EnqueueRestore(ctx, s.adminActor(ctx).ID, f, size)
	if err != nil {
		return nil, s.opsError(ctx, err, "start restore")
	}
	return taskOut(&task), nil
}

type adminImportInput struct {
	RawBody multipart.Form
}

func (s *Server) adminImport(ctx context.Context, in *adminImportInput) (*taskOutput, error) {
	f, size, err := archiveUpload(in.RawBody)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	// The force/assume knobs mirror the CLI flags. They are optional text fields on the same form.
	assume := formValue(in.RawBody, "assume_revision")
	forceType := formValue(in.RawBody, "force_unknown_challenge_type")

	task, err := s.opts.Ops.EnqueueImport(ctx, s.adminActor(ctx).ID, f, size, assume, forceType)
	if err != nil {
		return nil, s.opsError(ctx, err, "start import")
	}
	return taskOut(&task), nil
}

type adminTaskInput struct {
	ID int64 `path:"id"`
}

func (s *Server) adminTask(ctx context.Context, in *adminTaskInput) (*taskOutput, error) {
	task, err := s.opts.Ops.Get(ctx, in.ID)
	if err != nil {
		return nil, s.opsError(ctx, err, "read task")
	}
	return taskOut(&task), nil
}

func (s *Server) adminTaskDownload(ctx context.Context, in *adminTaskInput) (*huma.StreamResponse, error) {
	rc, filename, err := s.opts.Ops.OpenBackup(ctx, in.ID)
	if err != nil {
		return nil, s.opsError(ctx, err, "download backup")
	}

	//nolint:contextcheck // the stream body gets a huma.Context and threads it (hctx.Context()); contextcheck only sees a context.Context param.
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer func() { _ = rc.Close() }()
		hctx.SetHeader("Content-Type", "application/zip")
		hctx.SetHeader("Content-Disposition", contentDisposition(filename))
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		if _, err := io.Copy(hctx.BodyWriter(), rc); err != nil {
			// The 200 and headers are already on the wire; the transfer broke mid-stream. Log and stop.
			s.opts.Log.WarnContext(hctx.Context(), "backup stream interrupted", "task", in.ID, "error", err)
		}
	}}, nil
}

// archiveUpload pulls the single "archive" file part out of a multipart form and opens it. The
// multipart file is seekable — small parts stay in memory, larger ones spill to a temp file — so the
// service can stream it straight to the object store, and its declared size is authoritative.
func archiveUpload(form multipart.Form) (multipart.File, int64, error) {
	headers := form.File["archive"]
	if len(headers) == 0 {
		return nil, 0, huma.Error422UnprocessableEntity(`expected a multipart file field named "archive"`)
	}
	fh := headers[0]
	f, err := fh.Open()
	if err != nil {
		return nil, 0, huma.Error500InternalServerError("could not read the uploaded archive")
	}
	return f, fh.Size, nil
}

func formValue(form multipart.Form, key string) string {
	if vs := form.Value[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// opsError maps the ops-service sentinels onto the wire. The single-in-flight refusal is the load-
// bearing one: a second concurrent operation of the same kind is a 409, not a queue-behind. Database
// and storage error text never leaves this function.
func (s *Server) opsError(ctx context.Context, err error, action string) error {
	switch {
	case errors.Is(err, opsjob.ErrInFlight):
		return huma.Error409Conflict("an operation of this kind is already in progress")
	case errors.Is(err, opsjob.ErrTaskNotFound):
		return huma.Error404NotFound("task not found")
	case errors.Is(err, opsjob.ErrBackupNotReady):
		return huma.Error409Conflict("backup is not ready to download")
	case errors.Is(err, opsjob.ErrStorageUnavailable):
		return huma.Error503ServiceUnavailable("object storage is not configured")
	default:
		s.opts.Log.ErrorContext(ctx, action+" failed", "error", err)
		return huma.Error500InternalServerError("could not " + action)
	}
}
