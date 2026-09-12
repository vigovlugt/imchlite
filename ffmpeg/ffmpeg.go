package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

type FFmpeg struct {
	dir  string
	Path string
}

func Extract() (*FFmpeg, error) {
	dir, err := os.MkdirTemp("", "imchlite-ffmpeg-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	f := &FFmpeg{dir: dir}
	if len(ffmpegBytes) == 0 {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("no embedded ffmpeg binary for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	path := filepath.Join(dir, "ffmpeg"+ffmpegFilename)
	if err := os.WriteFile(path, ffmpegBytes, 0o755); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("extract ffmpeg: %w", err)
	}
	f.Path = path

	return f, nil
}

func (f *FFmpeg) Close() error {
	return os.RemoveAll(f.dir)
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
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", size, size),
		"-frames:v", "1",
		"-q:v", strconv.Itoa(quality),
		dest,
	)
	return err
}
