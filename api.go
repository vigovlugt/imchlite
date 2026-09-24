package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// assetResponse is the wire format of an asset for the api.
type assetResponse struct {
	ID       int64  `json:"id"`
	Checksum string `json:"checksum"`
	MimeType string `json:"mimeType,omitempty"`
	Type     string `json:"type"`
	// wall-clock capture time pinned to UTC
	LocalDateTime *int64 `json:"localDateTime,omitempty"`
	// true capture instant in UTC
	DateTime    *int64   `json:"dateTime,omitempty"`
	TimeZone    string   `json:"timeZone,omitempty"`
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	City        string   `json:"city,omitempty"`
	Country     string   `json:"country,omitempty"`
	Width       *int64   `json:"width,omitempty"`
	Height      *int64   `json:"height,omitempty"`
	DurationMs  *int64   `json:"durationMs,omitempty"`
	Orientation *int64   `json:"orientation,omitempty"`
	IsFavorite  bool     `json:"isFavorite"`
	// Paths are the relative paths of the asset's online files.
	Paths []string `json:"paths,omitempty"`
}

// assetPage is a page of assets plus the cursor to fetch the next one.
type assetPage struct {
	Assets     []assetResponse `json:"assets"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

func assetTypeName(t AssetType) string {
	if t == AssetTypeVideo {
		return "video"
	}
	return "image"
}

func newAssetResponse(a Asset) assetResponse {
	checksum := fmt.Sprintf("%x", a.Checksum)
	r := assetResponse{
		ID:         a.ID,
		Checksum:   checksum,
		MimeType:   a.MimeType,
		Type:       assetTypeName(a.Type),
		IsFavorite: a.IsFavorite,
		Paths:      a.Paths,
	}
	if a.LocalDateTime != 0 {
		localDateTime := a.LocalDateTime
		r.LocalDateTime = &localDateTime
	}
	if a.DateTime != 0 {
		dateTime := a.DateTime
		r.DateTime = &dateTime
	}
	if a.TimeZone != "" {
		r.TimeZone = a.TimeZone
	}
	if a.Latitude != 0 || a.Longitude != 0 {
		latitude, longitude := a.Latitude, a.Longitude
		r.Latitude, r.Longitude = &latitude, &longitude
	}
	if a.City != "" {
		r.City = a.City
	}
	if a.Country != "" {
		r.Country = a.Country
	}
	if a.Width != 0 {
		width := a.Width
		r.Width = &width
	}
	if a.Height != 0 {
		height := a.Height
		r.Height = &height
	}
	if a.DurationMs != 0 {
		durationMs := a.DurationMs
		r.DurationMs = &durationMs
	}
	if a.Orientation != 0 {
		orientation := a.Orientation
		r.Orientation = &orientation
	}
	return r
}

// encodeCursor encodes a pagination cursor for use in a url.
func encodeCursor(c assetCursor) string {
	raw := fmt.Sprintf("%d:%d", c.Time, c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor parses a cursor produced by encodeCursor.
func decodeCursor(s string) (assetCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return assetCursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	var c assetCursor
	if _, err := fmt.Sscanf(string(raw), "%d:%d", &c.Time, &c.ID); err != nil {
		return assetCursor{}, fmt.Errorf("parse cursor: %w", err)
	}
	return c, nil
}

// parseAssetQuery reads the asset filters from query parameters. All
// parameters are optional. include_path/exclude_path values are SQLite GLOB
// patterns matched against each file path.
func parseAssetQuery(vals url.Values) (assetQuery, error) {
	q := assetQuery{
		IncludePaths: toSlashPaths(vals["include_path"]),
		ExcludePaths: toSlashPaths(vals["exclude_path"]),
	}

	switch t := vals.Get("type"); t {
	case "", "all":
	case "image":
		v := AssetTypeImage
		q.Type = &v
	case "video":
		v := AssetTypeVideo
		q.Type = &v
	default:
		return q, fmt.Errorf("invalid type %q: want image, video or all", t)
	}

	if c := vals.Get("city"); c != "" {
		q.City = &c
	}
	if c := vals.Get("country"); c != "" {
		q.Country = &c
	}

	intParam := func(name string) (*int64, error) {
		s := vals.Get(name)
		if s == "" {
			return nil, nil
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", name, s, err)
		}
		return &v, nil
	}

	var err error
	if q.From, err = intParam("from"); err != nil {
		return q, err
	}
	if q.Until, err = intParam("until"); err != nil {
		return q, err
	}

	if s := vals.Get("limit"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			return q, fmt.Errorf("invalid limit %q: %w", s, err)
		}
		q.Limit = v
	}

	if s := vals.Get("cursor"); s != "" {
		cursor, err := decodeCursor(s)
		if err != nil {
			return q, err
		}
		q.Cursor = &cursor
	}

	return q, nil
}

// parseChecksum decodes the hex checksum from a path value.
func parseChecksum(s string) ([]byte, bool) {
	if len(s) != 64 {
		return nil, false
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, true
}

// registerAssetRoutes installs the asset endpoints on the mux.
func registerAssetRoutes(mux *http.ServeMux, assets *assetRepository, libraryLocation string) {
	mux.HandleFunc("GET /api/facets", func(w http.ResponseWriter, r *http.Request) {
		f, err := assets.getFacets(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, f)
	})

	// GET /api/media/{checksum} streams the original file, with range
	// support for video seeking.
	mux.HandleFunc("GET /api/media/{checksum}", func(w http.ResponseWriter, r *http.Request) {
		checksum, ok := parseChecksum(r.PathValue("checksum"))
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid checksum"})
			return
		}

		relativePath, mimeType, err := assets.liveFileForChecksum(r.Context(), checksum)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "asset not found"})
			return
		}

		// ServeFile handles ranges and content length; set the stored mime
		// type since extensions alone can be ambiguous.
		if mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		} else if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(relativePath))); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		http.ServeFile(w, r, resolveLibraryPath(libraryLocation, relativePath))
	})

	// GET /api/thumb/{checksum} serves the generated webp thumbnail.
	mux.HandleFunc("GET /api/thumb/{checksum}", func(w http.ResponseWriter, r *http.Request) {
		checksum, ok := parseChecksum(r.PathValue("checksum"))
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid checksum"})
			return
		}

		hexChecksum := hex.EncodeToString(checksum)
		thumbPath := filepath.Join(libraryLocation, ".imchlite", "thumbnails",
			hexChecksum[0:2], hexChecksum[2:4], hexChecksum+".webp")
		w.Header().Set("Content-Type", "image/webp")
		http.ServeFile(w, r, thumbPath)
	})

	mux.HandleFunc("GET /api/assets", func(w http.ResponseWriter, r *http.Request) {
		q, err := parseAssetQuery(r.URL.Query())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		found, err := assets.query(r.Context(), q)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		limit := q.Limit
		if limit <= 0 {
			limit = 100
		} else if limit > 1000 {
			limit = 1000
		}

		page := assetPage{Assets: make([]assetResponse, 0, len(found))}
		for _, a := range found {
			page.Assets = append(page.Assets, newAssetResponse(a))
		}

		// A full page may have more rows; hand back a cursor positioned on
		// the last asset.
		if len(found) == limit {
			last := found[len(found)-1]
			page.NextCursor = encodeCursor(assetCursor{Time: last.CaptureTime(), ID: last.ID})
		}

		writeJSON(w, http.StatusOK, page)
	})
}
