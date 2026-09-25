package ai

import (
	"context"
	"testing"

	ort "github.com/microsoft/onnxruntime/go/onnxruntime"
	"github.com/vigovlugt/imchlite/internal/onnxruntime"
)

func TestRunAddF32(t *testing.T) {
	if err := onnxruntime.Setup(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = onnxruntime.Shutdown() }()

	session, err := ort.NewSession("testdata/add_f32.onnx", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	opts, err := ort.NewRunOptions()
	if err != nil {
		t.Fatal(err)
	}
	defer opts.Close()

	a, err := ort.CreateTensor[float32]([]int64{2, 3}, []float32{1, 2, 3, 4, 5, 6})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	b, err := ort.CreateTensor[float32]([]int64{2, 3}, []float32{10, 20, 30, 40, 50, 60})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	results, err := session.RunWithOptions(context.Background(), opts, map[string]*ort.Tensor{"A": a, "B": b}, []string{"C"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, r := range results {
			r.Close()
		}
	}()

	data, err := ort.TensorData[float32](results["C"])
	if err != nil {
		t.Fatal(err)
	}
	if data[0] != 11 {
		t.Fatalf("expected 11, got %f", data[0])
	}
}
