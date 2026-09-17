package main

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func applySidecars(libraryLocation, mediaPath string, asset *Asset) {
	absolutePath := resolveLibraryPath(libraryLocation, mediaPath)

	applySnapchatSidecar(absolutePath, asset)
	applyGoogleSidecar(absolutePath, asset)
	applyICloudSidecar(absolutePath, asset)
	applyImmichSidecar(absolutePath, asset)
}

// Google Takeout exports put a supplemental JSON sidecar next to every media
// file. The JSON carries the media's original filename in its title field,
// which is what these sidecars are matched on: sidecar filenames themselves
// are unreliable, since Windows path-length limits truncate the
// ".supplemental-metadata.json" suffix during disk copies ("X.jpg.supp.json",
// "X.j.json", ...).
type googleSidecarMetadata struct {
	Title          string `json:"title"`
	PhotoTakenTime *struct {
		Timestamp string `json:"timestamp"`
	} `json:"photoTakenTime"`
	GeoDataExif *googleGeoData `json:"geoDataExif"`
	GeoData     *googleGeoData `json:"geoData"`
}

type googleGeoData struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

var googleDupSuffixRe = regexp.MustCompile(`^(.*)\(\d+\)(\.[^.]+)$`)

var (
	googleJSONCacheMu sync.Mutex
	// dir -> media filename key -> sidecar metadata.
	googleJSONCache = map[string]map[string]googleSidecarInfo{}
)

// googleSidecarInfo holds the capture time (UTC seconds) and coordinates a
// Google Takeout sidecar records for a media file. Takeout only knows the
// UTC instant; the camera's wall clock is not recorded.
type googleSidecarInfo struct {
	TakenAt   int64
	Latitude  float64
	Longitude float64
}

func applyGoogleSidecar(absolutePath string, asset *Asset) {
	dir := filepath.Dir(absolutePath)
	info, ok := googleSidecarLookup(dir, filepath.Base(absolutePath))
	if !ok {
		return
	}

	// The sidecar time is authoritative: re-encoded media in the export
	// can carry a processing date in its EXIF instead of the capture date.
	if info.TakenAt != 0 {
		asset.DateTime = info.TakenAt
	}
	// Takeout often records geoData as all zeros and keeps the real EXIF
	// coordinates in geoDataExif; (0,0) is treated as "no location".
	if info.Latitude != 0 || info.Longitude != 0 {
		asset.Latitude = info.Latitude
		asset.Longitude = info.Longitude
	}
}

// Lookups try the exact name first, then the name with a Google duplicate
// marker removed: for duplicates Takeout renames the media to "X(1).jpg"
// but the sidecar's title stays "X.jpg" (and both copies share the same
// photoTakenTime).
func googleSidecarLookup(dir, fileName string) (googleSidecarInfo, bool) {
	index := googleJSONIndex(dir)

	if info, ok := index[strings.ToLower(fileName)]; ok {
		return info, true
	}
	if info, ok := index[strings.ToLower(googleDupSuffixRe.ReplaceAllString(fileName, "$1$2"))]; ok {
		return info, true
	}
	return googleSidecarInfo{}, false
}

func googleJSONIndex(dir string) map[string]googleSidecarInfo {
	googleJSONCacheMu.Lock()
	defer googleJSONCacheMu.Unlock()

	if index, ok := googleJSONCache[dir]; ok {
		return index
	}

	index := map[string]googleSidecarInfo{}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		indexGoogleJSON(filepath.Join(dir, entry.Name()), index)
	}

	googleJSONCache[dir] = index
	return index
}

func indexGoogleJSON(path string, index map[string]googleSidecarInfo) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var sidecar googleSidecarMetadata
	if err := json.Unmarshal(data, &sidecar); err != nil {
		return
	}
	if sidecar.Title == "" {
		return
	}

	var info googleSidecarInfo
	if sidecar.PhotoTakenTime != nil {
		taken, err := strconv.ParseInt(strings.TrimSpace(sidecar.PhotoTakenTime.Timestamp), 10, 64)
		if err != nil {
			return
		}
		info.TakenAt = taken
	}

	geo := sidecar.GeoDataExif
	if geo == nil || (geo.Latitude == 0 && geo.Longitude == 0) {
		geo = sidecar.GeoData
	}
	if geo != nil {
		info.Latitude = geo.Latitude
		info.Longitude = geo.Longitude
	}

	title := strings.ToLower(strings.TrimSpace(sidecar.Title))
	index[title] = info
	index[strings.ToLower(googleDupSuffixRe.ReplaceAllString(title, "$1$2"))] = info
}

func applyImmichSidecar(absolutePath string, asset *Asset) {
	type immichSidecarMetadata struct {
		DateTaken string  `json:"dateTaken"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	}

	for _, ext := range []string{".JSON", ".json"} {
		data, err := os.ReadFile(absolutePath + ext)
		if err != nil {
			continue
		}

		var sidecar immichSidecarMetadata
		if err := json.Unmarshal(data, &sidecar); err != nil {
			continue
		}

		if taken, err := time.Parse(time.RFC3339, sidecar.DateTaken); err == nil {
			// RFC3339 carries the offset, so both the wall clock and the
			// true instant are derivable. The sidecar time is
			// authoritative: re-encoded media in the export can carry a
			// processing date in its EXIF instead of the capture date.
			asset.LocalDateTime = time.Date(
				taken.Year(), taken.Month(), taken.Day(),
				taken.Hour(), taken.Minute(), taken.Second(), 0, time.UTC,
			).Unix()
			asset.DateTime = taken.Unix()
			if _, offset := taken.Zone(); offset != 0 && asset.TimeZone == "" {
				asset.TimeZone = taken.Format("-07:00")
			}
		}

		if sidecar.Latitude != 0 || sidecar.Longitude != 0 {
			asset.Latitude = sidecar.Latitude
			asset.Longitude = sidecar.Longitude
		}
	}
}

var snapchatDateRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})_`)

// Snapchat memory files start with the capture date, without a time or
// zone; only the wall-clock day is known. It is only used as a fallback,
// since snapchat re-encodes media with the capture time in its QuickTime
// CreateDate.
func applySnapchatSidecar(absolutePath string, asset *Asset) {
	if asset.LocalDateTime != 0 || asset.DateTime != 0 {
		return
	}
	m := snapchatDateRe.FindStringSubmatch(filepath.Base(absolutePath))
	if m == nil {
		return
	}

	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])

	asset.LocalDateTime = time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).Unix()
}

const iCloudCSVDateLayout = "January 2,2006 3:04 PM MST"

var iCloudWeekdays = map[string]bool{
	"Monday": true, "Tuesday": true, "Wednesday": true, "Thursday": true,
	"Friday": true, "Saturday": true, "Sunday": true,
}

var (
	iCloudCSVCacheMu sync.Mutex
	// dir -> normalized imgName -> capture time with its zone (zero when
	// the row's date could not be parsed). Exports use GMT, i.e. the
	// parsed value is the UTC instant.
	iCloudCSVCache = map[string]map[string]time.Time{}
)

func applyICloudSidecar(absolutePath string, asset *Asset) {
	if asset.DateTime != 0 {
		return
	}

	dir := filepath.Dir(absolutePath)
	taken, ok := iCloudTakenAt(dir, filepath.Base(absolutePath))
	if !ok {
		return
	}

	asset.DateTime = taken.Unix()
	if _, offset := taken.Zone(); offset != 0 {
		if asset.LocalDateTime == 0 {
			asset.LocalDateTime = time.Date(
				taken.Year(), taken.Month(), taken.Day(),
				taken.Hour(), taken.Minute(), taken.Second(), 0, time.UTC,
			).Unix()
		}
		if asset.TimeZone == "" {
			asset.TimeZone = taken.Format("-07:00")
		}
	}
}

func iCloudTakenAt(dir, fileName string) (time.Time, bool) {
	index := iCloudCSVIndex(dir)

	if taken, ok := index[strings.ToLower(fileName)]; ok {
		return taken, !taken.IsZero()
	}
	if taken, ok := index[normalizeICloudName(fileName)]; ok {
		return taken, !taken.IsZero()
	}
	return time.Time{}, false
}

func iCloudCSVIndex(dir string) map[string]time.Time {
	iCloudCSVCacheMu.Lock()
	defer iCloudCSVCacheMu.Unlock()

	if index, ok := iCloudCSVCache[dir]; ok {
		return index
	}

	index := map[string]time.Time{}
	matches, _ := filepath.Glob(filepath.Join(dir, "Photo Details*.csv"))
	for _, path := range matches {
		indexICloudCSV(path, index)
	}

	iCloudCSVCache[dir] = index
	return index
}

func indexICloudCSV(path string, index map[string]time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return
	}

	nameCol, dateCol := -1, -1
	for i, h := range header {
		switch strings.TrimSpace(h) {
		case "imgName":
			nameCol = i
		case "originalCreationDate":
			dateCol = i
		}
	}
	if nameCol < 0 || dateCol < 0 {
		return
	}

	for {
		row, err := r.Read()
		if err != nil { // includes io.EOF
			return
		}
		if len(row) <= nameCol || len(row) <= dateCol {
			continue
		}

		name := strings.TrimSpace(row[nameCol])
		if name == "" {
			continue
		}

		taken, _ := parseICloudCSVDate(strings.TrimSpace(row[dateCol]))
		index[strings.ToLower(name)] = taken
		index[normalizeICloudName(name)] = taken
	}
}

func parseICloudCSVDate(s string) (time.Time, bool) {
	if fields := strings.SplitN(s, " ", 2); len(fields) == 2 && iCloudWeekdays[fields[0]] {
		s = fields[1]
	}

	taken, err := time.Parse(iCloudCSVDateLayout, s)
	if err != nil {
		return time.Time{}, false
	}
	return taken, true
}

func normalizeICloudName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		}
	}
	return b.String()
}
