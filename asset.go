package main

// Asset mirrors the asset table in migration001.
type Asset struct {
	ID int64

	// content identity: the hash of the file's bytes
	Checksum []byte
	MimeType string

	Type AssetType

	// timestamps (unix epoch seconds)
	FileCreatedAt  int64
	FileModifiedAt int64
	// wall-clock capture time from EXIF, pinned to UTC (no zone info)
	LocalDateTime int64

	// capture location and zone; empty/zero when unknown
	TimeZone  string
	Latitude  float64
	Longitude float64
	City      string
	Country   string

	// media dimensions
	Width       int64
	Height      int64
	DurationMs  int64
	Orientation int64

	// Thumbhash []byte

	// user state
	IsFavorite bool
	DeletedAt  int64

	CreatedAt int64
	UpdatedAt int64
}
