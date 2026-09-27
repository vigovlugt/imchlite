package ai

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/daulet/tokenizers"
	ort "github.com/microsoft/onnxruntime/go/onnxruntime"

	"github.com/vigovlugt/imchlite/internal/cachedir"
	"github.com/vigovlugt/imchlite/internal/hfmodel"
)

const (
	textualInputName  = "text"
	textualOutputName = "embedding"

	// The SigLIP2 textual encoder expects a fixed sequence of contextLength
	// tokens, terminated by <eos> (id 1) and padded with <pad> (id 0). The
	// tokenizer from the Hugging Face repository does not pad, so padding and
	// truncation are applied in code below.
	contextLength = 64

	// The <pad> token id.
	padTokenID = 0
)

// ClipTextual produces embeddings for text queries.
type ClipTextual struct {
	Path      string
	session   *ort.Session
	tokenizer *tokenizers.Tokenizer
}

// SetupTextual downloads the textual model and tokenizer from the Hugging
// Face hub to the user cache directory on first use and returns the directory
// containing them. The directory persists between runs.
func SetupTextual(ctx context.Context) (string, error) {
	return cachedir.Ensure("clip-textual", func(dir string) error {
		if _, err := hfmodel.Download(ctx, dir, textualModelFilename, modelRepo+"/textual/model.onnx", textualModelSHA256); err != nil {
			return err
		}
		_, err := hfmodel.Download(ctx, dir, tokenizerFilename, modelRepo+"/textual/tokenizer.json", tokenizerSHA256)
		return err
	})
}

// NewClipTextual creates a new ClipTextual model using the model files in the
// directory created by SetupTextual.
func NewClipTextual(dir string) (*ClipTextual, error) {
	path := filepath.Join(dir, textualModelFilename)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("textual model: %w", err)
	}

	// The tokenizer from the Hugging Face repository has no baked-in
	// padding or truncation, so truncation to the model's context length is
	// set here; padding is applied after encoding.
	data, err := os.ReadFile(filepath.Join(dir, tokenizerFilename))
	if err != nil {
		return nil, fmt.Errorf("read tokenizer: %w", err)
	}
	tokenizer, err := tokenizers.FromBytesWithTruncation(data, contextLength, tokenizers.TruncationDirectionRight)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}

	session, err := ort.NewSession(path, nil)
	if err != nil {
		tokenizer.Close()
		return nil, fmt.Errorf("load textual model: %w", err)
	}
	return &ClipTextual{Path: path, session: session, tokenizer: tokenizer}, nil
}

// Close releases the underlying inference session and tokenizer.
func (c *ClipTextual) Close() error {
	if c.tokenizer != nil {
		_ = c.tokenizer.Close()
		c.tokenizer = nil
	}
	if c.session == nil {
		return nil
	}
	return c.session.Close()
}

// Embed returns the embedding for a text query.
func (c *ClipTextual) Embed(ctx context.Context, text string) ([]float32, error) {
	ids, _, err := c.tokenizer.EncodeErr(text, true)
	if err != nil {
		return nil, fmt.Errorf("tokenize: %w", err)
	}
	if len(ids) > contextLength {
		return nil, fmt.Errorf("tokenize: got %d tokens, want at most %d", len(ids), contextLength)
	}

	input, err := ort.CreateTensor[int32]([]int64{1, contextLength}, toInt32(pad(ids, contextLength, padTokenID)))
	if err != nil {
		return nil, err
	}
	defer input.Close()

	outputs, err := c.session.Run(ctx, map[string]*ort.Tensor{textualInputName: input}, []string{textualOutputName})
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, t := range outputs {
			_ = t.Close()
		}
	}()

	data, err := ort.TensorData[float32](outputs[textualOutputName])
	if err != nil {
		return nil, err
	}
	return append([]float32(nil), data...), nil
}

func toInt32(ids []uint32) []int32 {
	out := make([]int32, len(ids))
	for i, id := range ids {
		out[i] = int32(id)
	}
	return out
}

// pad right-pads ids with padID to length n. The ids already end with <eos>
// (added by the tokenizer's template), so padding after them matches the
// fixed-length encoding the model was trained with.
func pad(ids []uint32, n int, padID uint32) []uint32 {
	if len(ids) >= n {
		return ids
	}
	out := make([]uint32, n)
	copy(out, ids)
	for i := len(ids); i < n; i++ {
		out[i] = padID
	}
	return out
}
