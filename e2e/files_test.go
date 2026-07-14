//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"net/http"
	"testing"
)

// TestFileUploadDownload uploads a challenge file as admin (to object storage) and downloads
// it back through the public endpoint, asserting the bytes round-trip. It skips when object
// storage is not configured (the upload endpoint says so with a 503).
func TestFileUploadDownload(t *testing.T) {
	ctx := context.Background()
	chal := adminCreateChallenge(t, "with-file-"+suffix(), "forensics", 100)

	content := []byte("hello from the e2e suite\n")
	up := uploadFile(t, chal, "brief.txt", content)
	if up.code == http.StatusServiceUnavailable {
		t.Skip("object storage not configured (upload returned 503) — skipping file round-trip")
	}
	up.require(t, http.StatusCreated)

	var file struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		SizeBytes int64  `json:"size_bytes"`
		SHA256    string `json:"sha256"`
	}
	up.decode(t, &file)
	if file.SizeBytes != int64(len(content)) {
		t.Errorf("size_bytes = %d, want %d", file.SizeBytes, len(content))
	}
	if file.SHA256 == "" {
		t.Error("upload did not return a content hash")
	}

	// The challenge detail lists the file.
	u := mustRegister(t)
	detail, err := u.api.ChallengeDetailWithResponse(ctx, chal)
	if err != nil || detail.JSON200 == nil {
		t.Fatalf("detail: %v (status %d)", err, detail.StatusCode())
	}
	if detail.JSON200.Files == nil || len(*detail.JSON200.Files) == 0 {
		t.Error("challenge detail should list the uploaded file")
	}

	// Downloading returns the exact bytes.
	dl, err := u.api.DownloadFileWithResponse(ctx, file.ID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if dl.StatusCode() != http.StatusOK {
		t.Fatalf("download status = %d, want 200", dl.StatusCode())
	}
	if !bytes.Equal(dl.Body, content) {
		t.Errorf("downloaded %q, want %q", dl.Body, content)
	}
}
