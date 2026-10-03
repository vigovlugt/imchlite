package library

import (
	"path"
	"strings"
)

// Excludes are case-insensitive glob patterns (path.Match syntax) for
// library paths the indexer skips. A path is excluded when the pattern
// matches it, one of its parent directories, or the name of either, so
// "*.mov" skips those files anywhere and "Directory" skips every directory
// of that name.
type Excludes []string

// Match reports whether the slash-separated library-relative path is
// excluded.
func (e Excludes) Match(relativePath string) bool {
	for p := strings.ToLower(relativePath); p != "." && p != "/"; p = path.Dir(p) {
		for _, pattern := range e {
			pattern = strings.ToLower(pattern)
			if ok, _ := path.Match(pattern, p); ok {
				return true
			}
			if ok, _ := path.Match(pattern, path.Base(p)); ok {
				return true
			}
		}
	}
	return false
}
