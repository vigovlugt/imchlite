package database

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/vigovlugt/imchlite/internal/vec1"
)

func float32Blob(values []float32) []byte {
	blob := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(blob[4*i:], math.Float32bits(v))
	}
	return blob
}

func TestVec1ExtensionSmoke(t *testing.T) {
	dir, err := vec1.Setup()
	if err != nil {
		t.Skipf("no embedded vec1 library: %v", err)
	}

	lib := vec1.LibraryPath(dir)
	db, err := Open(t.TempDir(), lib)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var info string
	if err := db.QueryRow("select vec1_info()").Scan(&info); err != nil {
		t.Fatalf("vec1_info: %v", err)
	}
	t.Logf("vec1: %s", info)

	for i := range 3 {
		if _, err := db.Exec(
			`insert into assets (checksum, type, file_modified_at) values (?, 0, 0)`, []byte{byte(i)}); err != nil {
			t.Fatalf("insert asset: %v", err)
		}
		embedding := []float32{1, 0, 0, 0}
		if i == 1 {
			embedding = []float32{0, 1, 0, 0}
		}
		if _, err := db.Exec(
			`insert into asset_clip_embeddings (asset_id, embedding) values (?, ?)`,
			i+1, float32Blob(embedding)); err != nil {
			t.Fatalf("insert embedding: %v", err)
		}
	}

	// With the flat index configured, the hidden distance column holds the
	// real distance. Assets 1 and 3 share the identical vector, so the order
	// between them is arbitrary; asset 2 (orthogonal) must be excluded.
	rows, err := db.Query(
		`select v.rowid, v.distance
		 from asset_clip_embeddings_vec(?1, '{k:2}') v`,
		float32Blob([]float32{1, 0, 0, 0}))
	if err != nil {
		t.Fatalf("knn query: %v", err)
	}
	defer rows.Close()

	got := map[int64]float64{}
	for rows.Next() {
		var id int64
		var distance float64
		if err := rows.Scan(&id, &distance); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = distance
		t.Logf("rowid=%d distance=%f", id, distance)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 2 || got[1] != 0 || got[3] != 0 {
		t.Fatalf("unexpected neighbors: %v", got)
	}
}
