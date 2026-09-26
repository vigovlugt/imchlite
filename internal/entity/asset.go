package entity

// AssetType: 0 = image, 1 = video
type AssetType int

const (
	AssetTypeImage AssetType = 0
	AssetTypeVideo AssetType = 1
)

// ThumbnailStatus: 0 = ok, 1 = failed
type ThumbnailStatus int

const (
	ThumbnailStatusOK     ThumbnailStatus = 0
	ThumbnailStatusFailed ThumbnailStatus = 1
)

// Asset mirrors the asset table in migration001.
type Asset struct {
	ID int64

	// content identity: the hash of the file's bytes
	Checksum []byte
	MimeType string

	// Paths are the relative paths of all of the asset's online files.
	// Populated by list queries only; nil elsewhere.
	Paths []string

	Type AssetType

	// timestamps (unix epoch seconds)
	FileCreatedAt  int64
	FileModifiedAt int64
	// wall-clock capture time pinned to UTC (no zone info); 0 when only a
	// UTC instant is known
	LocalDateTime int64
	// true capture instant in UTC; 0 when unknown
	DateTime int64

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

	// ThumbnailStatus records whether the webp thumbnail was generated.
	ThumbnailStatus ThumbnailStatus

	// ClipEmbeddedAt is the unix time the CLIP embedding was computed;
	// 0 means the clip task is still pending.
	ClipEmbeddedAt int64

	// Thumbhash []byte

	// user state
	IsFavorite bool
	DeletedAt  int64

	CreatedAt int64
	UpdatedAt int64
}

// CaptureTime is the time the frontend sorts and groups on: the wall-clock
// time when known, otherwise the UTC instant.
func (a Asset) CaptureTime() int64 {
	if a.LocalDateTime != 0 {
		return a.LocalDateTime
	}
	return a.DateTime
}
