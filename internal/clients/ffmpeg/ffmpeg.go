package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/vigovlugt/imchlite/internal/cachedir"
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
	filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,format=rgb24", size, size)
	_, _, err := f.Run(ctx,
		"-y",
		"-i", source,
		// Complex filtergraph for tiled HEIF/HEIC images (Apple Photos).
		"-filter_complex", filter+"[out]",
		"-map", "[out]",
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
		"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,format=rgb24", size, size),
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

// maxWebpDimension is the largest width or height a webp image can have.
const maxWebpDimension = 16383

// Preview converts an image to webp at its native resolution, only scaling
// it down when it exceeds webp's dimension limit, and returns the encoded
// bytes.
func (f *FFmpeg) Preview(ctx context.Context, source string, quality int) ([]byte, error) {
	filter := fmt.Sprintf("scale='min(iw,%[1]d)':'min(ih,%[1]d)':force_original_aspect_ratio=decrease,format=rgb24", maxWebpDimension)
	stdout, _, err := f.Run(ctx,
		"-i", source,
		// Complex filtergraph for tiled HEIF/HEIC images (Apple Photos).
		"-filter_complex", filter+"[out]",
		"-map", "[out]",
		"-frames:v", "1",
		"-q:v", strconv.Itoa(quality),
		"-f", "webp",
		"pipe:1",
	)
	return stdout, err
}

// Decode decodes the first frame of source, shrunk so its shortest side is
// at most size, into an RGB image. ffmpeg writes it back as an uncompressed
// PPM: a short "P6 <width> <height> 255" text header followed by raw RGB
// bytes.
func (f *FFmpeg) Decode(ctx context.Context, source string, size int) (*image.NRGBA, error) {
	filter := fmt.Sprintf("scale='if(lt(iw,ih),min(iw,%[1]d),-2)':'if(lt(iw,ih),-2,min(ih,%[1]d))',format=rgb24", size)
	cmd := exec.CommandContext(ctx, f.Path,
		"-loglevel", "error",
		"-i", source,
		// Complex filtergraph for tiled HEIF/HEIC images (Apple Photos).
		"-filter_complex", filter+"[out]",
		"-map", "[out]",
		"-frames:v", "1",
		"-f", "image2pipe",
		"-c:v", "ppm",
		"pipe:1",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode %s: %w: %s", source, err, stderr.String())
	}

	br := bufio.NewReader(&stdout)
	var w, h, maxValue int
	if _, err := fmt.Fscanf(br, "P6\n%d %d\n%d\n", &w, &h, &maxValue); err != nil {
		return nil, fmt.Errorf("ffmpeg decode %s: read ppm header: %w", source, err)
	}
	if maxValue != 255 || w <= 0 || h <= 0 {
		return nil, fmt.Errorf("ffmpeg decode %s: unexpected ppm %dx%d with max value %d", source, w, h, maxValue)
	}
	rgb := make([]byte, w*h*3)
	if _, err := io.ReadFull(br, rgb); err != nil {
		return nil, fmt.Errorf("ffmpeg decode %s: read ppm pixels: %w", source, err)
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range w * h {
		copy(img.Pix[i*4:], rgb[i*3:i*3+3])
		img.Pix[i*4+3] = 255
	}
	return img, nil
}
