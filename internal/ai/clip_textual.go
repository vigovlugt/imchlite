package ai

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/daulet/tokenizers"
	ort "github.com/microsoft/onnxruntime/go/onnxruntime"

	"github.com/vigovlugt/imchlite/internal/cachedir"
)

// The SigLIP2 textual encoder is platform independent, so unlike the ffmpeg
// and onnxruntime binaries it is embedded without build tags.
//
//go:embed bin/text_model.onnx
var textualModelBytes []byte

//go:embed bin/tokenizer.json
var tokenizerBytes []byte

const (
	textualModelFilename = "text_model.onnx"
	tokenizerFilename    = "tokenizer.json"
	textualInputName     = "text"
	textualOutputName    = "embedding"

	// The SigLIP2 textual encoder expects a fixed sequence of 64 tokens,
	// padded with the <pad> token (id 0) and terminated by <eos> (id 1);
	// the padding and truncation are baked into the embedded tokenizer.json.
	contextLength = 64
)

// ClipTextual produces embeddings for text queries.
type ClipTextual struct {
	Path      string
	session   *ort.Session
	tokenizer *tokenizers.Tokenizer
}

// SetupTextual extracts the embedded textual model and tokenizer to the user
// cache directory on first use and returns the directory containing them.
// The directory persists between runs.
func SetupTextual() (string, error) {
	if len(textualModelBytes) == 0 {
		return "", fmt.Errorf("no embedded textual model")
	}

	return cachedir.Ensure("clip-textual", func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, textualModelFilename), textualModelBytes, 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, tokenizerFilename), tokenizerBytes, 0o644)
	})
}

// NewClipTextual creates a new ClipTextual model using the model files in the
// directory created by SetupTextual.
func NewClipTextual(dir string) (*ClipTextual, error) {
	path := filepath.Join(dir, textualModelFilename)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("textual model: %w", err)
	}

	tokenizerPath := filepath.Join(dir, tokenizerFilename)
	tokenizer, err := tokenizers.FromFile(tokenizerPath)
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
	if len(ids) != contextLength {
		return nil, fmt.Errorf("tokenize: got %d tokens, want %d", len(ids), contextLength)
	}

	input, err := ort.CreateTensor[int32]([]int64{1, contextLength}, toInt32(ids))
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
