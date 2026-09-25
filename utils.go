package main

import (
	"path"
	"path/filepath"
	"strings"
)

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

// resolveLibraryPath resolves a path stored relative to the library root for
// filesystem access. Stored paths always use forward slashes; this is the
// single conversion point back to OS-native separators. Keeping this
// conversion at I/O boundaries makes a library movable without invalidating
// paths persisted in its database.
func resolveLibraryPath(libraryLocation, relativePath string) string {
	return filepath.FromSlash(filepath.Join(libraryLocation, relativePath))
}

// toSlashPaths normalizes user-supplied path filter values to forward slashes
// so they match the separator used for paths persisted in the database.
func toSlashPaths(paths []string) []string {
	if paths == nil {
		return nil
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.ToSlash(p)
	}
	return out
}

// Extension lists aligned with immich server mime-types (image + video only;
// sidecar types such as .xmp are excluded from indexing).
var imageExtensions = map[string]struct{}{
	".3fr": {}, ".ari": {}, ".arw": {}, ".avif": {}, ".bmp": {}, ".cap": {},
	".cin": {}, ".cr2": {}, ".cr3": {}, ".crw": {}, ".dcr": {}, ".dng": {},
	".erf": {}, ".fff": {}, ".gif": {}, ".heic": {}, ".heif": {}, ".hif": {},
	".iiq": {}, ".insp": {}, ".jp2": {}, ".jpeg": {}, ".jpg": {}, ".jpe": {},
	".jxl": {}, ".k25": {}, ".kdc": {}, ".mrw": {}, ".mpo": {}, ".nef": {},
	".nrw": {}, ".orf": {}, ".ori": {}, ".pef": {}, ".png": {}, ".psd": {},
	".raf": {}, ".raw": {}, ".rw2": {}, ".rwl": {}, ".sr2": {}, ".srf": {},
	".srw": {}, ".svg": {}, ".tif": {}, ".tiff": {}, ".webp": {}, ".x3f": {},
}

var videoExtensions = map[string]struct{}{
	".3gp": {}, ".3gpp": {}, ".avi": {}, ".flv": {}, ".insv": {}, ".m2t": {},
	".m2ts": {}, ".m4v": {}, ".mkv": {}, ".mov": {}, ".mp4": {}, ".mpe": {},
	".mpeg": {}, ".mpg": {}, ".mts": {}, ".mxf": {}, ".ts": {}, ".vob": {},
	".webm": {}, ".wmv": {},
}

func lowerExtension(p string) (string, bool) {
	ext := path.Ext(p)
	if ext == "" {
		return "", false
	}
	return strings.ToLower(ext), true
}

// isMediaPath reports whether a path looks like a supported photo or video
// file from its extension.
func isMediaPath(p string) bool {
	ext, ok := lowerExtension(p)
	if !ok {
		return false
	}
	_, isImage := imageExtensions[ext]
	_, isVideo := videoExtensions[ext]
	return isImage || isVideo
}

// typeFromPath classifies a media file as image or video from its extension.
// Content-sniffing is a later concern; the extension is enough for the initial
// asset row.
func typeFromPath(p string) AssetType {
	ext, ok := lowerExtension(p)
	if ok {
		if _, isVideo := videoExtensions[ext]; isVideo {
			return AssetTypeVideo
		}
	}
	return AssetTypeImage
}
