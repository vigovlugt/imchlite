// Package ai provides the machine learning models used to index and search
// the media library.
package ai

import (
	"context"
	_ "embed"
	"fmt"
	"image"
	"os"
	"path/filepath"

	ort "github.com/microsoft/onnxruntime/go/onnxruntime"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"

	"github.com/vigovlugt/imchlite/internal/cachedir"
)

// The SigLIP2 visual encoder is platform independent, so unlike the ffmpeg and
// onnxruntime binaries it is embedded without build tags.
//
//go:embed bin/visual_model.onnx
var visualModelBytes []byte

const (
	visualModelFilename = "visual_model.onnx"
	visualInputName     = "image"
	visualOutputName    = "embedding"

	// The SigLIP2 visual encoder expects a 224x224 RGB image.
	imageSize = 224
)

// ClipVisual produces embeddings for image and video frames.
type ClipVisual struct {
	Path    string
	session *ort.Session
}

// Setup extracts the embedded visual model to the user cache directory on
// first use and returns the directory containing it. The directory persists
// between runs.
func Setup() (string, error) {
	if len(visualModelBytes) == 0 {
		return "", fmt.Errorf("no embedded visual model")
	}

	return cachedir.Ensure("clip-visual", func(dir string) error {
		return os.WriteFile(filepath.Join(dir, visualModelFilename), visualModelBytes, 0o644)
	})
}

// NewClipVisual creates a new ClipVisual model using the model file in the
// directory created by Setup.
func NewClipVisual(dir string) (*ClipVisual, error) {
	path := filepath.Join(dir, visualModelFilename)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("visual model: %w", err)
	}

	session, err := ort.NewSession(path, nil)
	if err != nil {
		return nil, fmt.Errorf("load visual model: %w", err)
	}
	return &ClipVisual{Path: path, session: session}, nil
}

// Close releases the underlying inference session.
func (c *ClipVisual) Close() error {
	if c.session == nil {
		return nil
	}
	return c.session.Close()
}

// Embed returns the embedding for the webp thumbnail at path.
func (c *ClipVisual) Embed(ctx context.Context, path string) ([]float32, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, err := webp.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode thumbnail: %w", err)
	}

	input, err := ort.CreateTensor[float32]([]int64{1, 3, imageSize, imageSize}, preprocess(img))
	if err != nil {
		return nil, err
	}
	defer input.Close()

	outputs, err := c.session.Run(ctx, map[string]*ort.Tensor{visualInputName: input}, []string{visualOutputName})
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, t := range outputs {
			_ = t.Close()
		}
	}()

	data, err := ort.TensorData[float32](outputs[visualOutputName])
	if err != nil {
		return nil, err
	}
	return append([]float32(nil), data...), nil
}

// preprocess squashes img to 224x224 (without preserving aspect ratio) using
// bicubic interpolation and returns a normalized NCHW float32 tensor:
// (pixel/255 - 0.5) / 0.5.
func preprocess(img image.Image) []float32 {
	dst := image.NewNRGBA(image.Rect(0, 0, imageSize, imageSize))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)

	plane := imageSize * imageSize
	data := make([]float32, 3*plane)
	for y := range imageSize {
		for x := range imageSize {
			c := dst.NRGBAAt(x, y)
			i := y*imageSize + x
			data[i] = float32(c.R)/127.5 - 1
			data[plane+i] = float32(c.G)/127.5 - 1
			data[2*plane+i] = float32(c.B)/127.5 - 1
		}
	}
	return data
}
