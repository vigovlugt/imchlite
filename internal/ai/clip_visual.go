// Package ai provides the machine learning models used to index and search
// the media library.
package ai

import (
	"context"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	ort "github.com/microsoft/onnxruntime/go/onnxruntime"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"

	"github.com/vigovlugt/imchlite/internal/cachedir"
	"github.com/vigovlugt/imchlite/internal/clients/hfmodel"
	"github.com/vigovlugt/imchlite/internal/clients/onnxruntime"
)

// The SigLIP2 models are downloaded from the immich-app Hugging Face
// repository on first use; the hashes pin the exact files so an updated or
// corrupted download is rejected.
const (
	modelRepo = "https://huggingface.co/immich-app/ViT-B-16-SigLIP2__webli/resolve/main"

	visualModelFilename = "visual_model.onnx"
	visualModelSHA256   = "fee10c729875dd203d94f396ac3d664e301d39c977ef6d92e7f467a2a716f0b0"

	textualModelFilename = "text_model.onnx"
	textualModelSHA256   = "fc9991b415d7d0fac7ed540e875fd08a647c8a1abba99f12542531b004c4b68f"

	tokenizerFilename  = "tokenizer.json"
	tokenizerSHA256    = "220c63d496e0c14e63eb656c91e0215e926202e4c74b1f089e09f1920d779b04"
	tokenizerMaxLength = 64
)

const (
	visualInputName  = "image"
	visualOutputName = "embedding"

	// The SigLIP2 visual encoder expects a 224x224 RGB image.
	imageSize = 224
)

// ClipVisual produces embeddings for image and video frames.
type ClipVisual struct {
	Path string

	// session is set by the load goroutine before ready is closed; read it
	// only after receiving from ready.
	session *onnxruntime.Session
	// ready is closed once the model load finished; if loadErr is non-nil
	// the load failed and Embed returns it.
	ready   chan struct{}
	loadErr error
}

// Setup downloads the visual model from the Hugging Face hub to the user
// cache directory on first use and returns the directory containing it. The
// directory persists between runs.
func Setup(ctx context.Context) (string, error) {
	return cachedir.Ensure("clip-visual", func(dir string) error {
		_, err := hfmodel.Download(ctx, dir, visualModelFilename, modelRepo+"/visual/model.onnx", visualModelSHA256)
		return err
	})
}

// NewClipVisual creates a new ClipVisual model using the model file in the
// directory created by Setup. The inference session is created in the
// background once rt is initialized; Embed and Close wait for it to finish.
func NewClipVisual(dir string, rt *onnxruntime.Runtime) (*ClipVisual, error) {
	path := filepath.Join(dir, visualModelFilename)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("visual model: %w", err)
	}

	c := &ClipVisual{Path: path, ready: make(chan struct{})}
	go func() {
		defer close(c.ready)

		if err := rt.Wait(); err != nil {
			c.loadErr = fmt.Errorf("load visual model: %w", err)
			return
		}

		start := time.Now()
		session, err := onnxruntime.NewSession(path)
		if err != nil {
			c.loadErr = fmt.Errorf("load visual model: %w", err)
			return
		}
		c.session = session
		log.Printf("debug: clip visual model loaded in %s", time.Since(start))
	}()
	return c, nil
}

// WaitLoad blocks until the background model load finished, or ctx is done.
// It returns the load error, if any.
func (c *ClipVisual) WaitLoad(ctx context.Context) error {
	select {
	case <-c.ready:
		return c.loadErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close releases the underlying inference session. It waits for a
// background model load to finish first.
func (c *ClipVisual) Close() error {
	<-c.ready
	if c.session == nil {
		return nil
	}
	return c.session.Close()
}

// EmbedTimings holds the wall-clock duration in milliseconds of each stage
// of a single embedding.
type EmbedTimings struct {
	DecodeMs    int64
	TransformMs int64
	// InferenceWaitMs is the time spent waiting for other inference to
	// finish, as sessions run one at a time; InferenceMs excludes it.
	InferenceWaitMs int64
	InferenceMs     int64
}

// Embed returns the embedding for the webp thumbnail read from r, along
// with the per-stage timings. It blocks until the background model load
// finished.
func (c *ClipVisual) Embed(ctx context.Context, r io.Reader) ([]float32, EmbedTimings, error) {
	var timings EmbedTimings

	if err := c.WaitLoad(ctx); err != nil {
		return nil, timings, err
	}

	decodeStart := time.Now()
	img, err := webp.Decode(r)
	if err != nil {
		return nil, timings, fmt.Errorf("decode thumbnail: %w", err)
	}
	timings.DecodeMs = time.Since(decodeStart).Milliseconds()

	transformStart := time.Now()
	input, err := ort.CreateTensor[float32]([]int64{1, 3, imageSize, imageSize}, preprocess(img))
	if err != nil {
		return nil, timings, err
	}
	defer input.Close()
	timings.TransformMs = time.Since(transformStart).Milliseconds()

	inferenceStart := time.Now()
	outputs, wait, err := c.session.Run(ctx, map[string]*ort.Tensor{visualInputName: input}, []string{visualOutputName})
	if err != nil {
		return nil, timings, err
	}
	defer func() {
		for _, t := range outputs {
			_ = t.Close()
		}
	}()
	timings.InferenceWaitMs = wait.Milliseconds()
	timings.InferenceMs = (time.Since(inferenceStart) - wait).Milliseconds()

	data, err := ort.TensorData[float32](outputs[visualOutputName])
	if err != nil {
		return nil, timings, err
	}
	return append([]float32(nil), data...), timings, nil
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
