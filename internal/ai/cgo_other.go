//go:build !(linux && amd64) && !(windows && amd64)

package ai

// No prebuilt libtokenizers is embedded for this platform, so linking the
// textual encoder will fail here. Only linux/amd64 and windows/amd64 are
// supported.
import "C"
