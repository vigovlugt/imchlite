package api

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vigovlugt/imchlite/internal/ai"
	"github.com/vigovlugt/imchlite/internal/entity"
	"github.com/vigovlugt/imchlite/internal/media"
	"github.com/vigovlugt/imchlite/internal/repository"
	"github.com/vigovlugt/imchlite/internal/utils"
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
	// Similarity is the cosine similarity to the query embedding, set only
	// for context_query and similar_to queries.
	Similarity *float64 `json:"similarity,omitempty"`
}

// assetPage is a page of assets plus the cursor to fetch the next one.
type assetPage struct {
	Assets     []assetResponse `json:"assets"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

func assetTypeName(t entity.AssetType) string {
	if t == entity.AssetTypeVideo {
		return "video"
	}
	return "image"
}

func newAssetResponse(a entity.Asset) assetResponse {
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
	// Stored dimensions are as encoded; the api reports them as displayed,
	// so orientations that rotate by 90° swap width and height.
	width, height := a.Width, a.Height
	if a.Orientation >= 5 && a.Orientation <= 8 {
		width, height = height, width
	}
	if width != 0 {
		r.Width = &width
	}
	if height != 0 {
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
func encodeCursor(c repository.AssetCursor) string {
	raw := fmt.Sprintf("%d:%d", c.Time, c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor parses a cursor produced by encodeCursor.
func decodeCursor(s string) (repository.AssetCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return repository.AssetCursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	var c repository.AssetCursor
	if _, err := fmt.Sscanf(string(raw), "%d:%d", &c.Time, &c.ID); err != nil {
		return repository.AssetCursor{}, fmt.Errorf("parse cursor: %w", err)
	}
	return c, nil
}

// encodeSimilarCursor encodes the keyset position of a similarity page: the
// cosine distance and id of its last asset.
func encodeSimilarCursor(c repository.SimilarCursor) string {
	raw := strconv.FormatFloat(c.Distance, 'g', -1, 64) + ":" + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeSimilarCursor parses a cursor produced by encodeSimilarCursor.
func decodeSimilarCursor(s string) (repository.SimilarCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return repository.SimilarCursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	distanceStr, idStr, ok := strings.Cut(string(raw), ":")
	if !ok {
		return repository.SimilarCursor{}, fmt.Errorf("parse cursor %q", s)
	}
	distance, err := strconv.ParseFloat(distanceStr, 64)
	if err != nil {
		return repository.SimilarCursor{}, fmt.Errorf("parse cursor distance: %w", err)
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return repository.SimilarCursor{}, fmt.Errorf("parse cursor id: %w", err)
	}
	return repository.SimilarCursor{Distance: distance, ID: id}, nil
}

// parseAssetQuery reads the asset filters from query parameters. All
// parameters are optional. include_path/exclude_path values are SQLite GLOB
// patterns matched against each file path. When similarity is set the cursor
// is decoded as a similarity position rather than a capture-time one.
func parseAssetQuery(vals url.Values, similarity bool) (repository.AssetQuery, error) {
	q := repository.AssetQuery{
		IncludePaths: media.ToSlashPaths(vals["include_path"]),
		ExcludePaths: media.ToSlashPaths(vals["exclude_path"]),
	}

	switch t := vals.Get("type"); t {
	case "", "all":
	case "image":
		v := entity.AssetTypeImage
		q.Type = &v
	case "video":
		v := entity.AssetTypeVideo
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
		if similarity {
			cursor, err := decodeSimilarCursor(s)
			if err != nil {
				return q, err
			}
			q.SimilarCursor = &cursor
		} else {
			cursor, err := decodeCursor(s)
			if err != nil {
				return q, err
			}
			q.Cursor = &cursor
		}
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
func registerAssetRoutes(mux *http.ServeMux, assets *repository.Asset, libraryDir, dataDir string, textual *ai.ClipTextual) {
	mux.HandleFunc("GET /api/facets", func(w http.ResponseWriter, r *http.Request) {
		f, err := assets.GetFacets(r.Context())
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

		relativePath, mimeType, err := assets.LiveFileForChecksum(r.Context(), checksum)
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
		// ?download=1 forces a save dialog with the original filename.
		if r.URL.Query().Get("download") != "" {
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
				"filename": filepath.Base(relativePath),
			}))
		}
		http.ServeFile(w, r, media.ResolveLibraryPath(libraryDir, relativePath))
	})

	// GET /api/thumb/{checksum} serves the generated webp thumbnail.
	mux.HandleFunc("GET /api/thumb/{checksum}", func(w http.ResponseWriter, r *http.Request) {
		checksum, ok := parseChecksum(r.PathValue("checksum"))
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid checksum"})
			return
		}

		hexChecksum := hex.EncodeToString(checksum)
		thumbPath := filepath.Join(dataDir, "thumbnails",
			hexChecksum[0:2], hexChecksum[2:4], hexChecksum+".webp")
		w.Header().Set("Content-Type", "image/webp")
		http.ServeFile(w, r, thumbPath)
	})

	mux.HandleFunc("GET /api/assets", func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		vals := r.URL.Query()
		contextQuery := strings.TrimSpace(vals.Get("context_query"))
		similarTo := vals.Get("similar_to")

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		w = rec
		var dbTime, embedTime time.Duration
		count := 0
		defer func() {
			mode := "date"
			switch {
			case contextQuery != "":
				mode = "text"
			case similarTo != "":
				mode = "similar"
			}
			log.Printf("GET /api/assets mode=%s status=%d count=%d total_ms=%.1f db_ms=%.1f embed_ms=%.1f query=%q",
				mode, rec.status, count, ms(time.Since(started)), ms(dbTime), ms(embedTime), r.URL.RawQuery)
		}()
		if contextQuery != "" && similarTo != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "context_query and similar_to are mutually exclusive"})
			return
		}
		q, err := parseAssetQuery(vals, contextQuery != "" || similarTo != "")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		limit := q.Limit
		if limit <= 0 {
			limit = 100
		} else if limit > 1000 {
			limit = 1000
		}

		// context_query ranks assets by clip similarity to a text query, and
		// similar_to by similarity to another asset's image, instead of
		// filtering them by date.
		var embedding []byte
		switch {
		case contextQuery != "":
			if textual == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "text search unavailable"})
				return
			}
			embedStart := time.Now()
			vec, err := textual.Embed(r.Context(), contextQuery)
			embedTime = time.Since(embedStart)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			embedding = utils.EncodeEmbedding(vec)
		case similarTo != "":
			checksum, ok := parseChecksum(similarTo)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid similar_to checksum"})
				return
			}
			dbStart := time.Now()
			id, stored, ok, err := assets.ClipEmbeddingByChecksum(r.Context(), checksum)
			dbTime += time.Since(dbStart)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "asset not found"})
				return
			}
			if stored == nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "asset has not been processed for search yet"})
				return
			}
			// The source asset would always rank first; leave it out.
			embedding, q.ExcludeID = stored, id
		}

		if embedding != nil {
			dbStart := time.Now()
			found, err := assets.QuerySimilar(r.Context(), embedding, q)
			dbTime += time.Since(dbStart)
			count = len(found)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			page := assetPage{Assets: make([]assetResponse, 0, len(found))}
			for _, a := range found {
				resp := newAssetResponse(a.Asset)
				similarity := 1 - a.Distance
				resp.Similarity = &similarity
				page.Assets = append(page.Assets, resp)
			}
			// A full page may have more neighbors; hand back a cursor
			// positioned on the last one.
			if len(found) == limit {
				last := found[len(found)-1]
				page.NextCursor = encodeSimilarCursor(repository.SimilarCursor{
					Distance: last.Distance,
					ID:       last.Asset.ID,
				})
			}
			writeJSON(w, http.StatusOK, page)
			return
		}

		dbStart := time.Now()
		found, err := assets.Query(r.Context(), q)
		dbTime += time.Since(dbStart)
		count = len(found)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		page := assetPage{Assets: make([]assetResponse, 0, len(found))}
		for _, a := range found {
			page.Assets = append(page.Assets, newAssetResponse(a))
		}

		// A full page may have more rows; hand back a cursor positioned on
		// the last asset.
		if len(found) == limit {
			last := found[len(found)-1]
			page.NextCursor = encodeCursor(repository.AssetCursor{Time: last.CaptureTime(), ID: last.ID})
		}

		writeJSON(w, http.StatusOK, page)
	})
}

// statusRecorder remembers the status code written through it, for request
// logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// ms converts a duration to fractional milliseconds.
func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
