package database

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"log"
)

// migration008 moves the clip embeddings out of the vec1 virtual table into
// asset_clip_embeddings, a plain table keyed by asset id, and drops the
// virtual table. Similarity queries filter the assets first and compute the
// exact distance with vec1_cos_distance for just those; looking a vector up
// by rowid costs milliseconds in vec1 on a large library, against
// microseconds in a plain table.
func migration008(tx *sql.Tx) error {
	if _, err := tx.Exec(`create table asset_clip_embeddings (
		    asset_id integer primary key references assets (id) on delete cascade,

		    -- little-endian float32 vector, blob length implies the dimension
		    embedding blob not null
		)`); err != nil {
		return fmt.Errorf("migration008: %w", err)
	}
	if err := copyFlatVectors(tx); err != nil {
		return fmt.Errorf("migration008: %w", err)
	}
	statements := []string{
		// Whatever copyFlatVectors could not decode is read through the
		// virtual table, which is slow but exact.
		`insert into asset_clip_embeddings (asset_id, embedding)
		    select b.id, v.embedding
		    from asset_clip_embeddings_vec_base b
		    join asset_clip_embeddings_vec v on v.rowid = b.id
		    where b.id in (select id from assets)
		      and b.id not in (select asset_id from asset_clip_embeddings)`,
		`drop trigger assets_clip_embedding_ad`,
		`drop table asset_clip_embeddings_vec`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("migration008: %w", err)
		}
	}
	return nil
}

// copyFlatVectors copies the vectors of asset_clip_embeddings_vec into
// asset_clip_embeddings by decoding its flat index straight from the _idx
// shadow table, since reading them through the virtual table takes
// milliseconds each. The format is vec1's own and undocumented: each row
// holds a big-endian header (int32 flags, int32 count n, int32 deleted
// count, n int32 rowids, deleted ones set to -1) followed by the n vectors.
// Rows that do not fit it are skipped, and
// a sample of the copied vectors is compared with the virtual table; on any
// mismatch everything copied here is removed again.
func copyFlatVectors(tx *sql.Tx) error {
	rows, err := tx.Query(`select first, last, val from asset_clip_embeddings_vec_idx`)
	if err != nil {
		return fmt.Errorf("read flat index: %w", err)
	}
	defer rows.Close()

	insert, err := tx.Prepare(`insert into asset_clip_embeddings (asset_id, embedding)
		select ?1, ?2
		where exists (select 1 from assets where id = ?1)
		  and exists (select 1 from asset_clip_embeddings_vec_base where id = ?1)
		on conflict do nothing`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer insert.Close()

	dim, skipped := 0, 0
	for rows.Next() {
		var first, last int64
		var val []byte
		if err := rows.Scan(&first, &last, &val); err != nil {
			return fmt.Errorf("scan flat index: %w", err)
		}
		ids, vectors, ok := decodeFlatBlock(val, first, last, dim)
		if !ok {
			skipped++
			continue
		}
		dim = len(vectors) / len(ids)
		for i, id := range ids {
			if id < 0 {
				continue
			}
			if _, err := insert.Exec(id, vectors[i*dim:(i+1)*dim]); err != nil {
				return fmt.Errorf("copy vector %d: %w", id, err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read flat index: %w", err)
	}
	if skipped > 0 {
		log.Printf("migration008: %d flat index rows not decoded", skipped)
	}

	var mismatches int
	if err := tx.QueryRow(`select count(*)
		from (select asset_id, embedding from asset_clip_embeddings order by random() limit 100) e
		join asset_clip_embeddings_vec v on v.rowid = e.asset_id
		where v.embedding is not e.embedding`).Scan(&mismatches); err != nil {
		return fmt.Errorf("check copied vectors: %w", err)
	}
	if mismatches > 0 {
		log.Printf("migration008: decoded vectors differ from vec1, copying through vec1 instead")
		if _, err := tx.Exec(`delete from asset_clip_embeddings`); err != nil {
			return fmt.Errorf("remove decoded vectors: %w", err)
		}
	}
	return nil
}

// decodeFlatBlock splits one _idx row into its rowids, -1 for deleted
// entries, and packed vectors, reporting false if it does not fit the
// expected layout. A nonzero dim is the vector size in bytes seen in earlier
// rows, which this one must match.
func decodeFlatBlock(val []byte, first, last int64, dim int) ([]int64, []byte, bool) {
	if len(val) < 12 {
		return nil, nil, false
	}
	n := int(binary.BigEndian.Uint32(val[4:]))
	deleted := int(binary.BigEndian.Uint32(val[8:]))
	header := 12 + 4*n
	if n == 0 || len(val) <= header || (len(val)-header)%n != 0 {
		return nil, nil, false
	}
	size := (len(val) - header) / n
	if size%4 != 0 || (dim != 0 && size != dim) {
		return nil, nil, false
	}
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(int32(binary.BigEndian.Uint32(val[12+4*i:])))
		switch {
		case ids[i] == -1:
			deleted--
		case ids[i] < first || ids[i] > last:
			return nil, nil, false
		}
	}
	if deleted != 0 {
		return nil, nil, false
	}
	return ids, val[header:], true
}
