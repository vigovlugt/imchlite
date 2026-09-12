package main

import (
	"database/sql"
)

// assetRepository owns the database pool for asset persistence.
type assetRepository struct {
	db *sql.DB
}

// NewAssetRepository creates an assetRepository backed by the given pool.
func NewAssetRepository(db *sql.DB) *assetRepository {
	return &assetRepository{db: db}
}
