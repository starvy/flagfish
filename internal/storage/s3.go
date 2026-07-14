package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config is the object-storage connection. It works against any S3-compatible endpoint — AWS S3,
// MinIO, Cloudflare R2 — and the credentials arrive from the process environment, never from a file
// or the config table.
type S3Config struct {
	Endpoint  string // host:port, no scheme (TLS is decided by UseSSL)
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	PathStyle bool // MinIO and most self-hosted gateways need path-style addressing
}

// configured reports whether the operator asked for object storage at all. Any one of the four
// core fields being set is taken as intent to configure it, so a half-filled config is caught by
// validate rather than silently treated as "disabled".
func (c S3Config) configured() bool {
	return c.Endpoint != "" || c.Bucket != "" || c.AccessKey != "" || c.SecretKey != ""
}

// validate refuses a half-configured bucket at construction, the same loud-fail rule the mailer
// applies: a missing credential is a boot error, not a surprise on the first upload.
func (c S3Config) validate() error {
	var missing []string
	if c.Endpoint == "" {
		missing = append(missing, "endpoint")
	}
	if c.Bucket == "" {
		missing = append(missing, "bucket")
	}
	if c.AccessKey == "" {
		missing = append(missing, "access key")
	}
	if c.SecretKey == "" {
		missing = append(missing, "secret key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("object storage is partially configured; missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

type s3Store struct {
	client *minio.Client
	bucket string
}

// NewS3Store validates the config and builds the client. It does not reach the network: reachability
// surfaces on the first real operation, the same way the database pool does.
func NewS3Store(cfg S3Config) (Store, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	}
	if cfg.PathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	client, err := minio.New(cfg.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("object storage client for %q: %w", cfg.Endpoint, err)
	}
	return &s3Store{client: client, bucket: cfg.Bucket}, nil
}

func (s *s3Store) Put(ctx context.Context, sha string, size int64, r io.Reader) error {
	key := objectKey(sha)
	// Content-addressed: if the object is already there, its bytes are already these bytes. Skip
	// the upload — this is the dedupe.
	if _, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{}); err == nil {
		return nil
	} else if !isNotFound(err) {
		return fmt.Errorf("storage: stat %s: %w", key, err)
	}
	if _, err := s.client.PutObject(ctx, s.bucket, key, r, size,
		minio.PutObjectOptions{ContentType: "application/octet-stream"}); err != nil {
		return fmt.Errorf("storage: put %s: %w", key, err)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, sha string) (io.ReadCloser, error) {
	key := objectKey(sha)
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("storage: get %s: %w", key, err)
	}
	return obj, nil
}

func (s *s3Store) Stat(ctx context.Context, sha string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.bucket, objectKey(sha), minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("storage: stat %s: %w", objectKey(sha), err)
}

func (s *s3Store) Delete(ctx context.Context, sha string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, objectKey(sha), minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("storage: delete %s: %w", objectKey(sha), err)
	}
	return nil
}

// isNotFound distinguishes "the object is not there" from a transport or auth failure, so a missing
// object is a clean false and a broken bucket is still a loud error.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.StatusCode == http.StatusNotFound || resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket"
	}
	return false
}

// clientFor builds a bare client from a config, for the bootstrap and audit helpers below.
func clientFor(cfg S3Config) (*minio.Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	}
	if cfg.PathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}
	client, err := minio.New(cfg.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("storage: client: %w", err)
	}
	return client, nil
}

// ObjectKeys lists the stored keys for a content address — zero or one for a given sha. Used to audit
// the bucket (an orphan sweep, and the dedupe assertion in tests); the serving path never lists.
func ObjectKeys(ctx context.Context, cfg S3Config, sha string) ([]string, error) {
	client, err := clientFor(cfg)
	if err != nil {
		return nil, err
	}
	var keys []string
	for obj := range client.ListObjects(ctx, cfg.Bucket, minio.ListObjectsOptions{Prefix: objectKey(sha), Recursive: true}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("storage: list %q: %w", objectKey(sha), obj.Err)
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}

// EnsureBucket creates the bucket if it does not exist. It is idempotent and used by tests and by an
// operator bootstrapping a fresh instance; the serving path never calls it.
func EnsureBucket(ctx context.Context, cfg S3Config) error {
	client, err := clientFor(cfg)
	if err != nil {
		return err
	}
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return fmt.Errorf("storage: bucket exists %q: %w", cfg.Bucket, err)
	}
	if exists {
		return nil
	}
	if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
		// A concurrent creator winning the race is success, not failure.
		if exists, existErr := client.BucketExists(ctx, cfg.Bucket); existErr == nil && exists {
			return nil
		}
		return fmt.Errorf("storage: make bucket %q: %w", cfg.Bucket, err)
	}
	return nil
}
