// Package storage is the blob store for challenge artifacts. It is content-addressed: an object is
// named by the sha256 of its bytes, so identical uploads collapse to one object and an upload is
// idempotent. The Store interface is the extension seam — the production backend is S3-compatible
// object storage (s3.go), and a future backend implements the same four methods without any caller
// changing.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// ErrNotConfigured is returned by every method of the disabled store. An instance with no object
// storage configured still boots — it just cannot serve files, and it says so loudly rather than
// pretending an upload succeeded.
var ErrNotConfigured = errors.New("storage: object storage is not configured")

// Store is a content-addressed blob store. Every method keys on sha, the lowercase-hex sha256 of the
// content, so the backend owns its own key layout and callers never construct a path.
type Store interface {
	// Put stores content under its address. size is the exact byte length. It is idempotent:
	// content already present is not re-uploaded, which is where dedupe happens.
	Put(ctx context.Context, sha string, size int64, r io.Reader) error
	// Get opens the object for reading. The caller closes it.
	Get(ctx context.Context, sha string) (io.ReadCloser, error)
	// Stat reports whether the address is present.
	Stat(ctx context.Context, sha string) (bool, error)
	// Delete removes the object. Deleting an absent object is not an error.
	Delete(ctx context.Context, sha string) error
}

// objectKey is the backend-internal layout for a content address: two levels of sharding so no single
// prefix accumulates every object. This is the only place the layout is defined.
func objectKey(sha string) string {
	if len(sha) < 4 {
		return sha
	}
	return sha[:2] + "/" + sha[2:4] + "/" + sha
}

// NewStore builds the object store from its configuration. An empty configuration yields a disabled
// store that boots but refuses every operation; a partial one is refused loudly here rather than
// failing on the first upload of the event.
func NewStore(cfg S3Config, log *slog.Logger) (Store, error) {
	if !cfg.configured() {
		log.Warn("object storage is not configured: challenge file upload and download are disabled")
		return disabled{}, nil
	}
	s, err := NewS3Store(cfg)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	return s, nil
}

// disabled is the store an unconfigured instance gets. It never lies about success.
type disabled struct{}

func (disabled) Put(context.Context, string, int64, io.Reader) error { return ErrNotConfigured }
func (disabled) Get(context.Context, string) (io.ReadCloser, error)  { return nil, ErrNotConfigured }

func (disabled) Stat(context.Context, string) (bool, error) { return false, ErrNotConfigured }
func (disabled) Delete(context.Context, string) error       { return ErrNotConfigured }
