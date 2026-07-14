// Package files is the challenge-artifact service: admin upload and delete on the write side, and the
// authorized read a download needs. The bytes live in object storage (content-addressed, deduped);
// this package owns the files rows that link a stored object to a challenge and its name. Every
// mutation runs in one transaction with the acting admin stamped, so the files row and its audit row
// commit together.
package files

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/db"
	"github.com/starvy/flagfish/internal/storage"
)

var (
	ErrChallengeNotFound = errors.New("files: challenge not found")
	ErrNotFound          = errors.New("files: file not found")
	// ErrObjectMissing means the row exists but its backing object does not — a corruption, surfaced
	// loudly rather than as a truncated download.
	ErrObjectMissing = errors.New("files: stored object is missing")
)

var Module = fx.Module("files", fx.Provide(New))

type Service struct {
	pool  *pgxpool.Pool
	store storage.Store
	q     *db.Queries
	log   *slog.Logger
}

func New(pool *pgxpool.Pool, store storage.Store, log *slog.Logger) *Service {
	return &Service{pool: pool, store: store, q: db.New(pool), log: log}
}

// NewFile is one upload: the original name, the content address, and the exact byte length. Content
// is the reader the bytes are streamed from; it has already been hashed to produce SHA.
type NewFile struct {
	Name    string
	SHA     string // lowercase hex sha256 of Content
	Size    int64
	Content io.Reader
}

// FileMeta is what a download resolves before it is allowed to stream: the name and address of the
// object, plus whether its challenge is hidden so the handler can 404 it for non-admins.
type FileMeta struct {
	ID          int64
	Name        string
	SHA         string
	Size        int64
	Hidden      bool
	ChallengeID int64
}

func (s *Service) tx(ctx context.Context, actor audit.Actor, fn func(q *db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("files: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := audit.Stamp(ctx, tx, actor); err != nil {
		return fmt.Errorf("files: %w", err)
	}
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("files: commit: %w", err)
	}
	return nil
}

// Upload streams the content to object storage, then records the files row. The object is stored
// before the row so a committed row always has its bytes; a crash between the two orphans an object,
// which is harmless because it is content-addressed and reused on the next identical upload.
//
//nolint:gocritic // hugeParam: NewFile is a value so a caller cannot mutate it mid-call.
func (s *Service) Upload(ctx context.Context, actor audit.Actor, challengeID int64, in NewFile) (db.File, error) {
	shaBytes, decErr := hex.DecodeString(in.SHA)
	if decErr != nil {
		return db.File{}, fmt.Errorf("files: bad content address: %w", decErr)
	}

	// Validate the challenge before uploading, so a wrong id does not leave an object behind.
	if _, err := s.q.AdminGetChallenge(ctx, challengeID); errors.Is(err, pgx.ErrNoRows) {
		return db.File{}, fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
	} else if err != nil {
		return db.File{}, fmt.Errorf("files: read challenge %d: %w", challengeID, err)
	}

	if err := s.store.Put(ctx, in.SHA, in.Size, in.Content); err != nil {
		return db.File{}, fmt.Errorf("files: store put: %w", err)
	}

	location := in.SHA + "/" + in.Name
	var out db.File
	err := s.tx(ctx, actor, func(q *db.Queries) error {
		row, insErr := q.AdminInsertFile(ctx, db.AdminInsertFileParams{
			Location: location, Sha256sum: shaBytes, SizeBytes: in.Size,
			ChallengeID: &challengeID, Name: in.Name,
		})
		if errors.Is(insErr, pgx.ErrNoRows) {
			// The content and name already exist: dedupe to the existing row.
			row, insErr = q.GetFileByLocation(ctx, location)
		}
		if insErr != nil {
			var pgErr *pgconn.PgError
			if errors.As(insErr, &pgErr) && pgErr.Code == "23503" {
				return fmt.Errorf("%w: id=%d", ErrChallengeNotFound, challengeID)
			}
			return fmt.Errorf("files: insert %s: %w", location, insErr)
		}
		out = row
		return nil
	})
	return out, err
}

// Delete removes the files row and, if no other row still points at the same object, the object. The
// row is the source of truth, so an object-delete failure is logged rather than failing the request:
// an orphaned blob is storage waste, not a correctness problem.
func (s *Service) Delete(ctx context.Context, actor audit.Actor, fileID int64) error {
	var sha []byte
	var remaining int64
	err := s.tx(ctx, actor, func(q *db.Queries) error {
		var delErr error
		sha, delErr = q.AdminDeleteFile(ctx, fileID)
		if errors.Is(delErr, pgx.ErrNoRows) {
			return fmt.Errorf("%w: id=%d", ErrNotFound, fileID)
		} else if delErr != nil {
			return fmt.Errorf("files: delete %d: %w", fileID, delErr)
		}
		var cntErr error
		remaining, cntErr = q.CountFilesBySha(ctx, sha)
		if cntErr != nil {
			return fmt.Errorf("files: count refs %d: %w", fileID, cntErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if remaining == 0 {
		if delErr := s.store.Delete(ctx, hex.EncodeToString(sha)); delErr != nil {
			s.log.WarnContext(ctx, "orphaned object left in storage after row delete",
				"file_id", fileID, "error", delErr)
		}
	}
	return nil
}

// Meta resolves the metadata a download needs and enforces nothing itself: the caller decides whether
// this principal may see a hidden challenge's file.
func (s *Service) Meta(ctx context.Context, fileID int64) (FileMeta, error) {
	row, err := s.q.GetChallengeFileForDownload(ctx, fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return FileMeta{}, fmt.Errorf("%w: id=%d", ErrNotFound, fileID)
	}
	if err != nil {
		return FileMeta{}, fmt.Errorf("files: meta %d: %w", fileID, err)
	}
	return FileMeta{
		ID: row.ID, Name: row.Name, SHA: hex.EncodeToString(row.Sha256sum),
		Size: row.SizeBytes, Hidden: row.State == "hidden", ChallengeID: row.ChallengeID,
	}, nil
}

// Content opens the object for streaming. It confirms the object is present first, so a corrupt
// row (no backing object) fails before any response header is written.
func (s *Service) Content(ctx context.Context, sha string) (io.ReadCloser, error) {
	exists, err := s.store.Stat(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("files: stat object: %w", err)
	}
	if !exists {
		return nil, ErrObjectMissing
	}
	rc, err := s.store.Get(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("files: open object: %w", err)
	}
	return rc, nil
}
