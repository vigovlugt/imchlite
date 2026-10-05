package api

import (
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vigovlugt/imchlite/internal/clients/ffmpeg"
	"github.com/vigovlugt/imchlite/internal/media"
	"github.com/vigovlugt/imchlite/internal/repository"
)

const previewQuality = 90

// browserImageTypes are the image mime types browsers can display natively;
// other images are converted to webp for preview.
var browserImageTypes = map[string]bool{
	"image/jpeg":    true,
	"image/png":     true,
	"image/gif":     true,
	"image/webp":    true,
	"image/avif":    true,
	"image/bmp":     true,
	"image/svg+xml": true,
	"image/x-icon":  true,
}

// registerPreviewRoutes installs the preview endpoint on the mux.
func registerPreviewRoutes(mux *http.ServeMux, assets *repository.Asset, libraryDir string, ff *ffmpeg.FFmpeg) {
	// GET /api/preview/{checksum} serves the original file when the browser
	// can display it, and a native resolution webp conversion otherwise.
	mux.HandleFunc("GET /api/preview/{checksum}", func(w http.ResponseWriter, r *http.Request) {
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
		if mimeType == "" {
			mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(relativePath)))
		}
		source := media.ResolveLibraryPath(libraryDir, relativePath)

		// Videos and displayable images are served as is.
		if !strings.HasPrefix(mimeType, "image/") || browserImageTypes[mimeType] {
			if mimeType != "" {
				w.Header().Set("Content-Type", mimeType)
			}
			http.ServeFile(w, r, source)
			return
		}

		// Converted in full before responding so a failed conversion still
		// gets an error status.
		preview, err := ff.Preview(r.Context(), source, previewQuality)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Content-Length", strconv.Itoa(len(preview)))
		w.Write(preview)
	})
}
