//go:build windows && amd64

package ai

/*
#cgo LDFLAGS: -L${SRCDIR}/../clients/tokenizers/lib/windows_amd64 -ltokenizers -lntdll
*/
import "C"
