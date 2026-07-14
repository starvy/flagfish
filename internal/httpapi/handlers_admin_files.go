package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/files"
	"github.com/starvy/flagfish/internal/storage"
)

// fileError maps file-service errors onto the wire. Database and storage error text never leaves this
// function: anything unrecognised is logged in full and returned as a bare 500.
func (s *Server) fileError(ctx context.Context, err error, action string) error {
	switch {
	case errors.Is(err, files.ErrChallengeNotFound):
		return huma.Error404NotFound("challenge not found")
	case errors.Is(err, files.ErrNotFound):
		return huma.Error404NotFound("file not found")
	case errors.Is(err, storage.ErrNotConfigured):
		return huma.Error503ServiceUnavailable("file storage is not configured")
	default:
		s.opts.Log.ErrorContext(ctx, action+" failed", "error", err)
		return huma.Error500InternalServerError("could not " + action)
	}
}

type adminFileBody struct {
	ID          int64  `json:"id"`
	ChallengeID *int64 `json:"challenge_id"`
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
}

type adminFileOutput struct {
	Body adminFileBody
}

func adminFile(f db.File) *adminFileOutput {
	return &adminFileOutput{Body: adminFileBody{
		ID: f.ID, ChallengeID: f.ChallengeID, Name: f.Name,
		SHA256: hex.EncodeToString(f.Sha256sum), SizeBytes: f.SizeBytes,
	}}
}

type adminUploadFileInput struct {
	ID      int64 `path:"id"`
	RawBody multipart.Form
}

type adminDeleteFileInput struct {
	FileID int64 `path:"fileID"`
}

func (s *Server) registerAdminFiles() {
	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-upload-file", Method: http.MethodPost, Path: "/challenges/{id}/files",
		DefaultStatus: http.StatusCreated,
		Summary:       "Attach a file to a challenge", Tags: []string{"admin/files"},
	}, s.adminUploadFile)

	Register(s.Admin, policy.ClassAdmin, huma.Operation{
		OperationID: "admin-delete-file", Method: http.MethodDelete, Path: "/files/{fileID}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete a challenge file", Tags: []string{"admin/files"},
	}, s.adminDeleteFile)
}

func (s *Server) adminUploadFile(ctx context.Context, in *adminUploadFileInput) (*adminFileOutput, error) {
	headers := in.RawBody.File["file"]
	if len(headers) == 0 {
		return nil, huma.Error422UnprocessableEntity(`expected a multipart file field named "file"`)
	}
	fh := headers[0]
	name := sanitizeFilename(fh.Filename)
	if name == "" {
		return nil, huma.Error422UnprocessableEntity("the uploaded file has no usable name")
	}

	f, openErr := fh.Open()
	if openErr != nil {
		s.opts.Log.ErrorContext(ctx, "open uploaded file failed", "error", openErr)
		return nil, huma.Error500InternalServerError("could not read the uploaded file")
	}
	defer func() { _ = f.Close() }()

	// Hash the content to its address, then rewind so the same reader streams to storage. The
	// multipart file is seekable — small parts stay in memory, larger ones spill to a temp file.
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		s.opts.Log.ErrorContext(ctx, "hashing uploaded file failed", "error", err)
		return nil, huma.Error500InternalServerError("could not read the uploaded file")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		s.opts.Log.ErrorContext(ctx, "rewinding uploaded file failed", "error", err)
		return nil, huma.Error500InternalServerError("could not read the uploaded file")
	}
	sha := hex.EncodeToString(h.Sum(nil))

	rec, err := s.opts.Files.Upload(ctx, s.adminActor(ctx), in.ID, files.NewFile{
		Name: name, SHA: sha, Size: fh.Size, Content: f,
	})
	if err != nil {
		return nil, s.fileError(ctx, err, "upload file")
	}
	return adminFile(rec), nil
}

func (s *Server) adminDeleteFile(ctx context.Context, in *adminDeleteFileInput) (*adminDeleteOutput, error) {
	if err := s.opts.Files.Delete(ctx, s.adminActor(ctx), in.FileID); err != nil {
		return nil, s.fileError(ctx, err, "delete file")
	}
	return &adminDeleteOutput{}, nil
}

// sanitizeFilename reduces a client-supplied filename to a safe base name: no directories, no
// traversal. An empty result means the name was unusable and the caller rejects the upload.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	switch name {
	case "", ".", "..", "/":
		return ""
	}
	return name
}
