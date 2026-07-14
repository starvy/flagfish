package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/starvy/flagfish/internal/domain/policy"
	"github.com/starvy/flagfish/internal/files"
)

type downloadFileInput struct {
	ID int64 `path:"id"`
}

func (s *Server) registerFiles() {
	// A file download is gated exactly as the challenge's detail view: same visibility, verification,
	// team and time gates. The per-challenge hidden state is the L2 check inside the handler.
	Register(s.Public, policy.ClassChallengeDetail, huma.Operation{
		OperationID: "download-file", Method: http.MethodGet, Path: "/files/{id}",
		Summary: "Download a challenge file", Tags: []string{"challenges"},
	}, s.downloadFile)
}

func (s *Server) downloadFile(ctx context.Context, in *downloadFileInput) (*huma.StreamResponse, error) {
	meta, err := s.opts.Files.Meta(ctx, in.ID)
	if errors.Is(err, files.ErrNotFound) {
		return nil, huma.Error404NotFound("file not found")
	}
	if err != nil {
		s.opts.Log.ErrorContext(ctx, "file meta failed", "error", err)
		return nil, huma.Error500InternalServerError("could not load file")
	}

	// A hidden challenge's file does not exist for anyone but an admin — a 404, not a 403, so its
	// existence is not disclosed.
	if meta.Hidden && !AuthOf(ctx).Principal.IsAdmin {
		return nil, huma.Error404NotFound("file not found")
	}

	rc, err := s.opts.Files.Content(ctx, meta.SHA)
	if err != nil {
		return nil, s.fileError(ctx, err, "download file")
	}

	//nolint:contextcheck // the stream body gets a huma.Context and threads it (hctx.Context()); contextcheck only sees a context.Context param.
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		defer func() { _ = rc.Close() }()
		hctx.SetHeader("Content-Type", contentTypeFor(meta.Name))
		hctx.SetHeader("Content-Length", strconv.FormatInt(meta.Size, 10))
		hctx.SetHeader("Content-Disposition", contentDisposition(meta.Name))
		hctx.SetHeader("X-Content-Type-Options", "nosniff")
		if _, err := io.Copy(hctx.BodyWriter(), rc); err != nil {
			// Headers and a 200 are already on the wire; the transfer broke mid-stream. Log it and
			// stop — there is no second status to send.
			s.opts.Log.WarnContext(hctx.Context(), "file stream interrupted", "file_id", in.ID, "error", err)
		}
	}}, nil
}

// contentTypeFor picks a MIME type from the name's extension, defaulting to octet-stream so an
// unknown type downloads rather than rendering.
func contentTypeFor(name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// contentDisposition builds an attachment header that survives non-ASCII names: a quoted ASCII
// fallback plus the RFC 5987 UTF-8 form.
func contentDisposition(name string) string {
	ascii := make([]rune, 0, len(name))
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			r = '_'
		}
		if r > 0x7f {
			r = '_'
		}
		ascii = append(ascii, r)
	}
	return `attachment; filename="` + string(ascii) + `"; filename*=UTF-8''` + url.PathEscape(name)
}
