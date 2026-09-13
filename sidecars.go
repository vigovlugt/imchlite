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
	applyICloudSidecar(absolutePath, asset)
	applyImmichSidecar(absolutePath, asset)
}

// applyImmichSidecar reads an Immich-style JSON sidecar ("<path>.json" or
// "<path>.JSON") next to the media file and uses its dateTaken as the capture
// time when present.
func applyImmichSidecar(absolutePath string, asset *Asset) {
	type immichSidecarMetadata struct {
		DateTaken string `json:"dateTaken"`
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

		taken, err := time.Parse(time.RFC3339, sidecar.DateTaken)
		if err != nil {
			continue
		}

		asset.LocalDateTime = time.Date(
			taken.Year(), taken.Month(), taken.Day(),
			taken.Hour(), taken.Minute(), taken.Second(), 0, time.UTC,
		).Unix()
	}
}

var snapchatDateRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})_`)

func applySnapchatSidecar(absolutePath string, asset *Asset) {
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
	// dir -> normalized imgName -> capture time as wall-clock UTC seconds
	// (0 when the row's date could not be parsed).
	iCloudCSVCache = map[string]map[string]int64{}
)

func applyICloudSidecar(absolutePath string, asset *Asset) {
	if asset.LocalDateTime != 0 {
		return
	}

	dir := filepath.Dir(absolutePath)
	taken, ok := iCloudTakenAt(dir, filepath.Base(absolutePath))
	if !ok {
		return
	}

	asset.LocalDateTime = taken
}

func iCloudTakenAt(dir, fileName string) (int64, bool) {
	index := iCloudCSVIndex(dir)

	if taken, ok := index[strings.ToLower(fileName)]; ok {
		return taken, taken != 0
	}
	if taken, ok := index[normalizeICloudName(fileName)]; ok {
		return taken, taken != 0
	}
	return 0, false
}

func iCloudCSVIndex(dir string) map[string]int64 {
	iCloudCSVCacheMu.Lock()
	defer iCloudCSVCacheMu.Unlock()

	if index, ok := iCloudCSVCache[dir]; ok {
		return index
	}

	index := map[string]int64{}
	matches, _ := filepath.Glob(filepath.Join(dir, "Photo Details*.csv"))
	for _, path := range matches {
		indexICloudCSV(path, index)
	}

	iCloudCSVCache[dir] = index
	return index
}

func indexICloudCSV(path string, index map[string]int64) {
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

func parseICloudCSVDate(s string) (int64, bool) {
	if fields := strings.SplitN(s, " ", 2); len(fields) == 2 && iCloudWeekdays[fields[0]] {
		s = fields[1]
	}

	taken, err := time.Parse(iCloudCSVDateLayout, s)
	if err != nil {
		return 0, false
	}

	return time.Date(
		taken.Year(), taken.Month(), taken.Day(),
		taken.Hour(), taken.Minute(), taken.Second(), 0, time.UTC,
	).Unix(), true
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
