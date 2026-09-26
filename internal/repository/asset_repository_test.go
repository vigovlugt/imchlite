package repository

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/vigovlugt/imchlite/internal/database"
	"github.com/vigovlugt/imchlite/internal/utils"
	"github.com/vigovlugt/imchlite/internal/vec1"
)

func TestQuerySimilar(t *testing.T) {
	dir, err := vec1.Setup()
	if err != nil {
		t.Skipf("no embedded vec1 library: %v", err)
	}

	db, err := database.Open(t.TempDir(), vec1.LibraryPath(dir))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	embeddings := [][]float32{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
		{0.9, 0.1, 0, 0},
	}
	ids := make([]int64, len(embeddings))
	for i := range embeddings {
		res, err := db.ExecContext(ctx,
			`insert into assets (checksum, type, file_modified_at) values (?, 0, 0)`,
			[]byte{byte(i)})
		if err != nil {
			t.Fatalf("insert asset: %v", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("last insert id: %v", err)
		}
		ids[i] = id
		if _, err := db.ExecContext(ctx,
			`insert into files (asset_id, path, inode, size, mtime_s, mtime_ns)
			 values (?, ?, ?, 0, 0, 0)`,
			id, fmt.Sprintf("p%d.jpg", i), i); err != nil {
			t.Fatalf("insert file: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into asset_clip_embeddings (asset_id, embedding) values (?, ?)`,
			id, utils.EncodeEmbedding(embeddings[i])); err != nil {
			t.Fatalf("insert embedding: %v", err)
		}
	}

	repo := NewAsset(db)
	got, err := repo.QuerySimilar(ctx, utils.EncodeEmbedding([]float32{1, 0, 0, 0}), AssetQuery{Limit: 2})
	if err != nil {
		t.Fatalf("query similar: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d assets, want 2", len(got))
	}
	if got[0].Asset.ID != ids[0] || got[1].Asset.ID != ids[2] {
		t.Fatalf("wrong order: got ids %d, %d; want %d, %d", got[0].Asset.ID, got[1].Asset.ID, ids[0], ids[2])
	}
}

func TestQuerySimilarPagination(t *testing.T) {
	dir, err := vec1.Setup()
	if err != nil {
		t.Skipf("no embedded vec1 library: %v", err)
	}

	db, err := database.Open(t.TempDir(), vec1.LibraryPath(dir))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const total = 10
	for i := range total {
		res, err := db.ExecContext(ctx,
			`insert into assets (checksum, type, file_modified_at) values (?, 0, 0)`,
			[]byte{byte(i)})
		if err != nil {
			t.Fatalf("insert asset: %v", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("last insert id: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into files (asset_id, path, inode, size, mtime_s, mtime_ns)
			 values (?, ?, ?, 0, 0, 0)`,
			id, fmt.Sprintf("p%d.jpg", i), i); err != nil {
			t.Fatalf("insert file: %v", err)
		}
		// Angles increase with i, so cosine distance to {1,0,0,0} increases.
		angle := float64(i) * 0.05
		embedding := []float32{float32(math.Cos(angle)), float32(math.Sin(angle)), 0, 0}
		if _, err := db.ExecContext(ctx,
			`insert into asset_clip_embeddings (asset_id, embedding) values (?, ?)`,
			id, utils.EncodeEmbedding(embedding)); err != nil {
			t.Fatalf("insert embedding: %v", err)
		}
	}

	repo := NewAsset(db)
	query := utils.EncodeEmbedding([]float32{1, 0, 0, 0})

	seen := []int64{}
	var cursor *SimilarCursor
	for page := 0; page < total; page++ {
		got, err := repo.QuerySimilar(ctx, query, AssetQuery{Limit: 3, SimilarCursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(got) == 0 {
			break
		}
		for _, s := range got {
			seen = append(seen, s.Asset.ID)
		}
		if len(got) < 3 {
			break
		}
		last := got[len(got)-1]
		cursor = &SimilarCursor{Distance: last.Distance, ID: last.Asset.ID}
	}

	if len(seen) != total {
		t.Fatalf("saw %d assets, want %d: %v", len(seen), total, seen)
	}
	unique := map[int64]bool{}
	for i, id := range seen {
		if unique[id] {
			t.Fatalf("duplicate asset %d in paged results %v", id, seen)
		}
		unique[id] = true
		if i > 0 && seen[i-1] >= id {
			t.Fatalf("assets not in ascending distance order: %v", seen)
		}
	}
}

func TestQuerySimilarSkipsOfflineAssets(t *testing.T) {
	dir, err := vec1.Setup()
	if err != nil {
		t.Skipf("no embedded vec1 library: %v", err)
	}

	db, err := database.Open(t.TempDir(), vec1.LibraryPath(dir))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ids := make([]int64, 2)
	for i := range ids {
		res, err := db.ExecContext(ctx,
			`insert into assets (checksum, type, file_modified_at) values (?, 0, 0)`,
			[]byte{byte(i)})
		if err != nil {
			t.Fatalf("insert asset: %v", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("last insert id: %v", err)
		}
		ids[i] = id
		offline := 0
		if i == 0 {
			offline = 1
		}
		if _, err := db.ExecContext(ctx,
			`insert into files (asset_id, path, inode, size, mtime_s, mtime_ns, is_offline)
			 values (?, ?, ?, 0, 0, 0, ?)`,
			id, fmt.Sprintf("p%d.jpg", i), i, offline); err != nil {
			t.Fatalf("insert file: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into asset_clip_embeddings (asset_id, embedding) values (?, ?)`,
			id, utils.EncodeEmbedding([]float32{1, 0, 0, 0})); err != nil {
			t.Fatalf("insert embedding: %v", err)
		}
	}

	repo := NewAsset(db)
	got, err := repo.QuerySimilar(ctx, utils.EncodeEmbedding([]float32{1, 0, 0, 0}), AssetQuery{Limit: 10})
	if err != nil {
		t.Fatalf("query similar: %v", err)
	}
	if len(got) != 1 || got[0].Asset.ID != ids[1] {
		t.Fatalf("got %d assets, want only online asset %d", len(got), ids[1])
	}
}
