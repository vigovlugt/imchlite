package ffmpeg

import (
	"context"
	"strings"
	"testing"
)

func TestExtractAndRun(t *testing.T) {
	f, err := Extract()
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	defer f.Close()

	out, _, err := f.Run(context.Background(), "-version")
	if err != nil {
		t.Fatalf("run ffmpeg: %v", err)
	}
	t.Logf("%s", firstLine(out))
}

func TestThumbnail(t *testing.T) {
	f, err := Extract()
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	defer f.Close()

	dir := t.TempDir()
	source := dir + "/in.png"
	dest := dir + "/out.webp"
	if _, _, err := f.Run(context.Background(),
		"-y", "-f", "lavfi", "-i", "testsrc=size=800x600", "-frames:v", "1", source,
	); err != nil {
		t.Fatalf("generate test image: %v", err)
	}

	if err := f.Thumbnail(context.Background(), source, dest, 250, 80); err != nil {
		t.Fatalf("thumbnail: %v", err)
	}

	_, stderr, err := f.Run(context.Background(), "-i", dest, "-f", "null", "-")
	if err != nil {
		t.Fatalf("probe thumbnail: %v", err)
	}
	if !strings.Contains(string(stderr), "250x188") {
		t.Errorf("expected 250x188 output, got: %s", stderr)
	}
}

func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i])
		}
	}
	return string(b)
}
