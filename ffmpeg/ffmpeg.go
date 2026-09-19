package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/vigovlugt/imchlite/cachedir"
)

type FFmpeg struct {
	Path string
}

// Setup returns the ffmpeg binary directory from the user cache directory,
// extracting the embedded copy on first use. The directory persists between
// runs, so Teardown is a no-op.
func Setup() (string, error) {
	if len(ffmpegBytes) == 0 {
		return "", fmt.Errorf("no embedded ffmpeg binary for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	return cachedir.Ensure("ffmpeg", func(dir string) error {
		return os.WriteFile(filepath.Join(dir, ffmpegFilename), ffmpegBytes, 0o755)
	})
}

// New returns an FFmpeg runner using the binary in the directory created by
// Setup.
func New(dir string) (*FFmpeg, error) {
	path := filepath.Join(dir, ffmpegFilename)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("ffmpeg binary: %w", err)
	}
	return &FFmpeg{Path: path}, nil
}

func (f *FFmpeg) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, f.Path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("ffmpeg %v: %w: %s", args, err, stderr.String())
	}

	return stdout.Bytes(), stderr.Bytes(), nil
}

func (f *FFmpeg) Thumbnail(ctx context.Context, source, dest string, size, quality int) error {
	_, _, err := f.Run(ctx,
		"-y",
		"-i", source,
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase", size, size),
		"-frames:v", "1",
		"-q:v", strconv.Itoa(quality),
		dest,
	)
	return err
}

// ThumbnailFromReader generates a thumbnail from an in-memory stream. The
// input format must be one ffmpeg can demux without seeking (jpeg, png, ...).
func (f *FFmpeg) ThumbnailFromReader(ctx context.Context, r io.Reader, dest string, size, quality int) error {
	cmd := exec.CommandContext(ctx, f.Path,
		"-y",
		"-i", "pipe:0",
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase", size, size),
		"-frames:v", "1",
		"-q:v", strconv.Itoa(quality),
		dest,
	)
	cmd.Stdin = r
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg thumbnail from stdin: %w: %s", err, stderr.String())
	}
	return nil
}
