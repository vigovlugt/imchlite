// Package utils holds small helpers shared across packages.
package utils

import (
	"encoding/binary"
	"math"
)

// EncodeEmbedding packs a float32 vector as a little-endian byte blob, the
// native vec1 format stored in the asset_clip_embeddings table.
func EncodeEmbedding(embedding []float32) []byte {
	blob := make([]byte, 4*len(embedding))
	for i, v := range embedding {
		binary.LittleEndian.PutUint32(blob[4*i:], math.Float32bits(v))
	}
	return blob
}
