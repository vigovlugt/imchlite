package onnxruntime

import (
	"testing"

	ort "github.com/microsoft/onnxruntime/go/onnxruntime"
)

// TestSessionOptionsHandle fails when a binding update changes the private
// ort.SessionOptions layout that sessionOptionsHandle depends on.
func TestSessionOptionsHandle(t *testing.T) {
	if err := Setup(); err != nil {
		t.Skipf("onnxruntime: %v", err)
	}
	opts, err := ort.NewSessionOptions()
	if err != nil {
		t.Fatal(err)
	}
	defer opts.Close()

	if _, err := sessionOptionsHandle(opts); err != nil {
		t.Fatal(err)
	}
}
