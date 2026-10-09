package database

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"testing"

	"github.com/vigovlugt/imchlite/internal/clients/vec1"
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

	db, err := Open(t.TempDir(), vec1.LibraryPath(dir))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var info string
	if err := db.QueryRow("select vec1_info()").Scan(&info); err != nil {
		t.Fatalf("vec1_info: %v", err)
	}
	t.Logf("vec1: %s", info)

	var distance float64
	if err := db.QueryRow("select vec1_cos_distance(?, ?)",
		float32Blob([]float32{1, 0, 0, 0}), float32Blob([]float32{0, 1, 0, 0})).Scan(&distance); err != nil {
		t.Fatalf("vec1_cos_distance: %v", err)
	}
	if math.Abs(distance-1) > 1e-6 {
		t.Fatalf("cos distance of orthogonal vectors = %f, want 1", distance)
	}
}

// TestMigration008 fills the vec1 virtual table as it was before migration
// 8, across several flat index blocks and with deleted vectors, and checks
// that the migration copies every vector unchanged.
func TestMigration008(t *testing.T) {
	dir, err := vec1.Setup()
	if err != nil {
		t.Skipf("no embedded vec1 library: %v", err)
	}

	db, err := Open(t.TempDir(), vec1.LibraryPath(dir))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	all := migrations
	migrations = all[:7]
	err = Migrate(ctx, db)
	migrations = all
	if err != nil {
		t.Fatalf("migrate to 7: %v", err)
	}

	const total = 20
	want := map[int64][]byte{}
	for i := range total {
		res, err := db.Exec(
			`insert into assets (checksum, type, file_modified_at) values (?, 0, 0)`, []byte{byte(i)})
		if err != nil {
			t.Fatalf("insert asset: %v", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("last insert id: %v", err)
		}
		embedding := float32Blob([]float32{float32(i), 1, -float32(i), 0.5, 0, 2, 3, float32(i) / 7})
		if _, err := db.Exec(
			`insert into asset_clip_embeddings_vec (rowid, embedding) values (?, ?)`, id, embedding); err != nil {
			t.Fatalf("insert embedding: %v", err)
		}
		want[id] = embedding
	}
	for _, id := range []int64{3, 11} {
		if _, err := db.Exec(`delete from assets where id = ?`, id); err != nil {
			t.Fatalf("delete asset: %v", err)
		}
		delete(want, id)
	}

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rows, err := db.Query(`select asset_id, embedding from asset_clip_embeddings`)
	if err != nil {
		t.Fatalf("read embeddings: %v", err)
	}
	defer rows.Close()
	got := 0
	for rows.Next() {
		var id int64
		var embedding []byte
		if err := rows.Scan(&id, &embedding); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if !bytes.Equal(embedding, want[id]) {
			t.Fatalf("asset %d: embedding %x, want %x", id, embedding, want[id])
		}
		got++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if got != len(want) {
		t.Fatalf("got %d embeddings, want %d", got, len(want))
	}

	var tables int
	if err := db.QueryRow(`select count(*) from sqlite_master
		where name like 'asset_clip_embeddings_vec%'`).Scan(&tables); err != nil {
		t.Fatalf("count vec1 tables: %v", err)
	}
	if tables != 0 {
		t.Fatalf("%d vec1 tables left after migration", tables)
	}

	// Deleting an asset removes its embedding through the cascade.
	if _, err := db.Exec(`delete from assets where id = 1`); err != nil {
		t.Fatalf("delete asset: %v", err)
	}
	var count int
	if err := db.QueryRow(`select count(*) from asset_clip_embeddings`).Scan(&count); err != nil {
		t.Fatalf("count embeddings: %v", err)
	}
	if count != len(want)-1 {
		t.Fatalf("got %d embeddings after delete, want %d", count, len(want)-1)
	}
}
