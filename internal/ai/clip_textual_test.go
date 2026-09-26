package ai

import (
	"context"
	"math"
	"testing"

	"github.com/vigovlugt/imchlite/internal/onnxruntime"
)

func TestClipTextualEmbed(t *testing.T) {
	if err := onnxruntime.Setup(); err != nil {
		t.Skipf("onnxruntime: %v", err)
	}

	dir, err := SetupTextual()
	if err != nil {
		t.Fatal(err)
	}

	clip, err := NewClipTextual(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer clip.Close()

	city, err := clip.Embed(context.Background(), "city")
	if err != nil {
		t.Fatal(err)
	}
	if len(city) != 768 {
		t.Fatalf("embedding size: got %d, want 768", len(city))
	}

	mountain, err := clip.Embed(context.Background(), "mountain")
	if err != nil {
		t.Fatal(err)
	}

	// Two different queries must produce different, normalized embeddings.
	if equal(city, mountain) {
		t.Fatal("different queries produced identical embeddings")
	}
	if n := norm(city); math.Abs(n-1) > 0.01 {
		t.Fatalf("embedding norm: got %f, want ~1", n)
	}
}

func equal(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}
