// Package hfmodel downloads model files from the Hugging Face hub on first
// run. Files are pinned by SHA-256 so a corrupted or tampered download is
// detected before the model is used. Downloads go to a staging file next to
// the target and are renamed into place, so an interrupted download never
// leaves a half-written file under its final name.
package hfmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// httpClient has no overall timeout: model files are gigabytes and a slow
// download should not be aborted mid-transfer. The connect phase is still
// bounded by the OS, and context cancellation aborts the transfer.
var httpClient = &http.Client{}

// Download fetches url into dir/name unless the file already exists. The
// download is verified against the pinned SHA-256, written to a temporary
// file and renamed into place, and progress is logged every 10 MiB.
func Download(ctx context.Context, dir, name, url, wantSHA256 string) (string, error) {
	dest := filepath.Join(dir, name)

	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", dest, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create model dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "."+name+"-staging-")
	if err != nil {
		return "", fmt.Errorf("create staging file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	hasher := sha256.New()
	const mib = 1 << 20
	var downloaded int64
	for chunk := 0; ; chunk++ {
		n, err := io.CopyN(io.MultiWriter(tmp, hasher), resp.Body, 10*mib)
		downloaded += n
		if chunk > 0 {
			log.Printf("downloading %s: %.0f MiB", name, float64(downloaded)/mib)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("download %s: %w", url, err)
		}
	}

	if got := hex.EncodeToString(hasher.Sum(nil)); got != wantSHA256 {
		return "", fmt.Errorf("download %s: sha256 mismatch (got %s, want %s), corrupted download?", url, got, wantSHA256)
	}

	if err := tmp.Chmod(0o644); err != nil {
		return "", fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", fmt.Errorf("move %s into place: %w", name, err)
	}
	log.Printf("downloaded %s (%.0f MiB)", name, float64(downloaded)/mib)
	return dest, nil
}
