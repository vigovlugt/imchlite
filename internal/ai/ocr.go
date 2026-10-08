package ai

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ort "github.com/microsoft/onnxruntime/go/onnxruntime"
	"golang.org/x/image/draw"

	"github.com/vigovlugt/imchlite/internal/cachedir"
	"github.com/vigovlugt/imchlite/internal/clients/hfmodel"
	"github.com/vigovlugt/imchlite/internal/clients/onnxruntime"
	"github.com/vigovlugt/imchlite/internal/entity"
)

// The PP-OCRv6 small models are downloaded from the immich-app Hugging Face
// repository on first use. Small reads as well as medium on real text at
// about a third of the time; tiny merges icons into text and has a smaller
// alphabet. Each model.onnx keeps its weights in the model.safetensors next
// to it, so both files keep their names in their own directory.
const (
	ocrModelRepo = "https://huggingface.co/immich-app/PP-OCRv6_small/resolve/v2"

	ocrModelFilename   = "model.onnx"
	ocrWeightsFilename = "model.safetensors"
	ocrCharsetFilename = "charset.txt"

	detectionModelSHA256     = "34a1a30c369b6692863ca3ec10ea98b37b3092e7284004353466832438e275c2"
	detectionWeightsSHA256   = "d85beed2bbc36affada7d693edce4d5af9c54e18ca21cc5c7bde233c38b5d9c5"
	recognitionModelSHA256   = "a0aa90b28826c7fc9c3cb1a83cc5aeb8c55ac9f339b8fda072cd352e8004728e"
	recognitionWeightsSHA256 = "aaee673acd640bf99b3d3e82ca70bae02f47c54a60a23eb9420b97a8aa2bdd58"
	recognitionCharsetSHA256 = "b5f2bfe2bdd9448429e3e82b51c789775d9b42f2403d082b00662eb77e401c5d"
)

// The pipeline follows Immich's: the recognizer reads crops from a preview
// whose shortest side is at most OCRPreviewSize, and the detector sees that
// preview shrunk further.
const (
	// OCRPreviewSize is the largest shortest side of the image Read is
	// given (Immich's preview size).
	OCRPreviewSize = 1440

	detShortSide = 736 // shortest side of the image fed to the detector (Immich's "maximum resolution")
	detThreshold = 0.3 // pixel probability above which a pixel is "text"
	boxThreshold = 0.5 // minimum mean probability for a box to be kept
	minRecScore  = 0.8 // minimum mean character confidence for text to be kept
	unclipRatio  = 1.6 // how much to grow each box (DB shrinks text regions)
	recHeight    = 48  // the recognizer expects 48px high crops

	ocrInputName       = "image"
	detectionOutput    = "dbnet_probs"
	recognitionIndices = "ctc_indices"
	recognitionScores  = "ctc_confidence"
)

// OCR reads the text in images with the PP-OCR detection and recognition
// models.
type OCR struct {
	// detection, recognition and chars are set by the load goroutine
	// before ready is closed; read them only after receiving from ready.
	detection   *onnxruntime.Session
	recognition *onnxruntime.Session
	// chars is the recognizer's alphabet: index 0 is the CTC blank and the
	// last class is a space.
	chars []string
	// ready is closed once the model load finished; if loadErr is non-nil
	// the load failed and Read returns it.
	ready   chan struct{}
	loadErr error
}

// SetupOCR downloads the detection and recognition models from the Hugging
// Face hub to the user cache directory on first use and returns the
// directory containing them. The directory persists between runs.
func SetupOCR(ctx context.Context) (string, error) {
	return cachedir.Ensure("ocr-pp-ocrv6-small", func(dir string) error {
		files := []struct{ path, sha256 string }{
			{"detection/" + ocrModelFilename, detectionModelSHA256},
			{"detection/" + ocrWeightsFilename, detectionWeightsSHA256},
			{"recognition/" + ocrModelFilename, recognitionModelSHA256},
			{"recognition/" + ocrWeightsFilename, recognitionWeightsSHA256},
			{"recognition/" + ocrCharsetFilename, recognitionCharsetSHA256},
		}
		for _, f := range files {
			sub, name := filepath.Split(filepath.FromSlash(f.path))
			if _, err := hfmodel.Download(ctx, filepath.Join(dir, sub), name, ocrModelRepo+"/"+f.path, f.sha256); err != nil {
				return err
			}
		}
		return nil
	})
}

// NewOCR creates a new OCR model using the model files in the directory
// created by SetupOCR. The inference sessions are created in the background
// once rt is initialized; Read and Close wait for them to finish.
func NewOCR(dir string, rt *onnxruntime.Runtime) (*OCR, error) {
	detectionPath := filepath.Join(dir, "detection", ocrModelFilename)
	recognitionPath := filepath.Join(dir, "recognition", ocrModelFilename)
	charset, err := os.ReadFile(filepath.Join(dir, "recognition", ocrCharsetFilename))
	if err != nil {
		return nil, fmt.Errorf("ocr charset: %w", err)
	}
	for _, path := range []string{detectionPath, recognitionPath} {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("ocr model: %w", err)
		}
	}

	chars := append([]string{"<blank>"}, strings.Split(strings.TrimSuffix(string(charset), "\n"), "\n")...)
	o := &OCR{chars: append(chars, " "), ready: make(chan struct{})}
	go func() {
		defer close(o.ready)

		if err := rt.Wait(); err != nil {
			o.loadErr = fmt.Errorf("load ocr models: %w", err)
			return
		}

		start := time.Now()
		detection, err := onnxruntime.NewSession(detectionPath)
		if err != nil {
			o.loadErr = fmt.Errorf("load ocr detection model: %w", err)
			return
		}
		recognition, err := onnxruntime.NewSession(recognitionPath)
		if err != nil {
			_ = detection.Close()
			o.loadErr = fmt.Errorf("load ocr recognition model: %w", err)
			return
		}
		o.detection, o.recognition = detection, recognition
		log.Printf("debug: ocr models loaded in %s", time.Since(start))
	}()
	return o, nil
}

// WaitLoad blocks until the background model load finished, or ctx is done.
// It returns the load error, if any.
func (o *OCR) WaitLoad(ctx context.Context) error {
	select {
	case <-o.ready:
		return o.loadErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close releases the underlying inference sessions. It waits for a
// background model load to finish first.
func (o *OCR) Close() error {
	<-o.ready
	if o.detection == nil {
		return nil
	}
	err := o.detection.Close()
	if rerr := o.recognition.Close(); err == nil {
		err = rerr
	}
	return err
}

// OCRTimings holds the wall-clock duration in milliseconds of each stage of
// reading one image.
type OCRTimings struct {
	DetectionMs   int64
	RecognitionMs int64
	// InferenceWaitMs is the time spent waiting for other inference to
	// finish, as sessions run one at a time; InferenceMs excludes it.
	InferenceWaitMs int64
	InferenceMs     int64
}

// Read returns the text boxes in img, in reading order, with their corners
// relative to the image size, along with the per-stage timings. Boxes whose
// text the recognizer is not confident about are dropped. img should be a preview
// whose shortest side is at most OCRPreviewSize. It blocks until the
// background model load finished.
func (o *OCR) Read(ctx context.Context, img *image.NRGBA) (_ []entity.OCRBox, timings OCRTimings, _ error) {
	var inference inferenceTime
	defer func() {
		timings.InferenceWaitMs = inference.wait.Milliseconds()
		timings.InferenceMs = inference.run.Milliseconds()
	}()

	if err := o.WaitLoad(ctx); err != nil {
		return nil, timings, err
	}

	start := time.Now()
	boxes, err := o.detect(ctx, img, &inference)
	timings.DetectionMs = time.Since(start).Milliseconds()
	if err != nil {
		return nil, timings, fmt.Errorf("detect text: %w", err)
	}

	start = time.Now()
	b := img.Bounds()
	var results []entity.OCRBox
	for _, box := range boxes {
		text, score, err := o.recognize(ctx, img, box.quad, &inference)
		if err != nil {
			return nil, timings, fmt.Errorf("recognize text: %w", err)
		}
		text = strings.TrimSpace(text)
		if text == "" || score < minRecScore {
			continue
		}

		result := entity.OCRBox{Text: text, BoxScore: float64(box.score), TextScore: float64(score)}
		for j, p := range box.quad {
			result.Corners[j] = entity.Point{
				X: math.Min(1, math.Max(0, p.x/float64(b.Dx()))),
				Y: math.Min(1, math.Max(0, p.y/float64(b.Dy()))),
			}
		}
		results = append(results, result)
	}
	timings.RecognitionMs = time.Since(start).Milliseconds()
	return results, timings, nil
}

// inferenceTime adds up the time spent running the models and waiting for
// other inference to finish.
type inferenceTime struct {
	wait, run time.Duration
}

// textBox is a text box found by the detector.
type textBox struct {
	// quad is the box in image coordinates.
	quad quad
	// score is the mean text probability inside the box.
	score float32
	// line is the line of text the box is on, counted from the top.
	line int
}

// detect runs the DB text detector and returns rotated text boxes in img
// coordinates, in reading order.
func (o *OCR) detect(ctx context.Context, img *image.NRGBA, inference *inferenceTime) ([]textBox, error) {
	// Scale so the shortest side is at most detShortSide, with both sides a
	// multiple of 32 as the network requires.
	b := img.Bounds()
	scale := math.Min(1, float64(detShortSide)/float64(min(b.Dx(), b.Dy())))
	w := max(32, int(math.Round(float64(b.Dx())*scale/32))*32)
	h := max(32, int(math.Round(float64(b.Dy())*scale/32))*32)

	resized := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(resized, resized.Bounds(), img, b, draw.Src, nil)

	// The output is a [1, h, w] map of per-pixel text probabilities.
	outputs, err := runOCR(ctx, o.detection, resized, inference, detectionOutput)
	if err != nil {
		return nil, err
	}
	defer closeTensors(outputs)
	probs, err := ort.TensorData[float32](outputs[detectionOutput])
	if err != nil {
		return nil, err
	}
	if len(probs) != w*h {
		return nil, fmt.Errorf("detection output has %d values, want %d", len(probs), w*h)
	}

	// A pixel is "text" when it, or its neighbor above, left or above-left,
	// is likely text: thresholding plus the 2x2 dilation Immich does.
	mask := make([]bool, w*h)
	for y := range h {
		for x := range w {
			for _, d := range [][2]int{{0, 0}, {-1, 0}, {0, -1}, {-1, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx >= 0 && ny >= 0 && probs[ny*w+nx] > detThreshold {
					mask[y*w+x] = true
					break
				}
			}
		}
	}

	// Every connected group of text pixels becomes one box.
	sx := float64(b.Dx()) / float64(w)
	sy := float64(b.Dy()) / float64(h)
	var boxes []textBox
	for _, region := range connectedRegions(mask, w, h) {
		box := minAreaBox(region)
		if min(box.width, box.height) < 3 {
			continue
		}
		score := boxScore(box, probs, w, h)
		if score < boxThreshold {
			continue
		}

		// The detector predicts a shrunk text core; grow it back out.
		grow := box.width * box.height * unclipRatio / (box.width + box.height)
		box.width += grow
		box.height += grow

		// Back to img coordinates.
		q := box.corners()
		for i := range q {
			q[i] = q[i].scale(sx, sy)
		}
		boxes = append(boxes, textBox{quad: q, score: score})
	}

	// Reading order: top to bottom, where a box starting within 10px of the
	// one above is on the same line, then left to right within a line.
	sort.Slice(boxes, func(i, j int) bool { return boxes[i].quad[0].y < boxes[j].quad[0].y })
	for i := 1; i < len(boxes); i++ {
		boxes[i].line = boxes[i-1].line
		if boxes[i].quad[0].y-boxes[i-1].quad[0].y >= 10 {
			boxes[i].line++
		}
	}
	sort.SliceStable(boxes, func(i, j int) bool {
		if boxes[i].line != boxes[j].line {
			return boxes[i].line < boxes[j].line
		}
		return boxes[i].quad[0].x < boxes[j].quad[0].x
	})
	return boxes, nil
}

// connectedRegions flood-fills mask and returns the pixels of each
// 8-connected region.
func connectedRegions(mask []bool, w, h int) [][]point {
	visited := make([]bool, w*h)
	var regions [][]point
	for start := range mask {
		if visited[start] || !mask[start] {
			continue
		}

		var pixels []point
		stack := []int{start}
		visited[start] = true
		for len(stack) > 0 {
			i := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := i%w, i/w
			pixels = append(pixels, point{float64(x), float64(y)})

			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if nx < 0 || nx >= w || ny < 0 || ny >= h {
						continue
					}
					j := ny*w + nx
					if !visited[j] && mask[j] {
						visited[j] = true
						stack = append(stack, j)
					}
				}
			}
		}
		regions = append(regions, pixels)
	}
	return regions
}

// boxScore is the mean text probability of the pixels inside box.
func boxScore(box box, probs []float32, w, h int) float32 {
	q := box.corners()
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, p := range q {
		minX, maxX = math.Min(minX, p.x), math.Max(maxX, p.x)
		minY, maxY = math.Min(minY, p.y), math.Max(maxY, p.y)
	}

	var sum float32
	var n int
	for y := max(0, int(math.Floor(minY))); y <= min(h-1, int(math.Ceil(maxY))); y++ {
		for x := max(0, int(math.Floor(minX))); x <= min(w-1, int(math.Ceil(maxX))); x++ {
			if box.contains(point{float64(x), float64(y)}) {
				sum += probs[y*w+x]
				n++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float32(n)
}

// recognize reads the text in q with the recognizer and returns it together
// with its mean character confidence.
func (o *OCR) recognize(ctx context.Context, img *image.NRGBA, q quad, inference *inferenceTime) (string, float32, error) {
	text := crop(img, q)

	// Resize the crop to 48px high, keeping the aspect ratio, and pad it on
	// the right with gray to 1.25x its width: like Immich, as the recognizer
	// reads a line worse without room past the text.
	b := text.Bounds()
	w := max(1, int(math.Ceil(float64(recHeight*b.Dx())/float64(b.Dy()))))
	input := image.NewNRGBA(image.Rect(0, 0, int(math.Ceil(float64(w)*1.25)), recHeight))
	draw.Draw(input, input.Bounds(), image.NewUniform(color.Gray{127}), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(input, image.Rect(0, 0, w, recHeight), text, b, draw.Src, nil)

	outputs, err := runOCR(ctx, o.recognition, input, inference, recognitionIndices, recognitionScores)
	if err != nil {
		return "", 0, err
	}
	defer closeTensors(outputs)
	indices, err := ort.TensorData[int32](outputs[recognitionIndices])
	if err != nil {
		return "", 0, err
	}
	confidences, err := ort.TensorData[float32](outputs[recognitionScores])
	if err != nil {
		return "", 0, err
	}
	if len(indices) != len(confidences) {
		return "", 0, fmt.Errorf("recognition output has %d indices and %d confidences", len(indices), len(confidences))
	}

	// The model already picked the best character per time step; finish CTC
	// decoding by dropping blanks (index 0) and repeats of the previous step.
	var out strings.Builder
	var scoreSum float32
	var n int
	var prev int32
	for i, idx := range indices {
		if idx != 0 && idx != prev {
			if int(idx) >= len(o.chars) {
				return "", 0, fmt.Errorf("recognition output class %d outside the %d character alphabet", int(idx), len(o.chars))
			}
			out.WriteString(o.chars[int(idx)])
			scoreSum += confidences[i]
			n++
		}
		prev = idx
	}
	if n == 0 {
		return "", 0, nil
	}
	return out.String(), scoreSum / float32(n), nil
}

// runOCR feeds img to session as raw RGB bytes in [1, H, W, 3] layout (the
// models normalize internally) and returns the named outputs, which the
// caller must close. It adds the time spent to inference.
func runOCR(ctx context.Context, session *onnxruntime.Session, img *image.NRGBA, inference *inferenceTime, outputNames ...string) (map[string]*ort.Tensor, error) {
	b := img.Bounds()
	pixels := make([]uint8, 0, b.Dx()*b.Dy()*3)
	for i := 0; i < len(img.Pix); i += 4 {
		pixels = append(pixels, img.Pix[i], img.Pix[i+1], img.Pix[i+2])
	}
	input, err := ort.CreateTensor([]int64{1, int64(b.Dy()), int64(b.Dx()), 3}, pixels)
	if err != nil {
		return nil, err
	}
	defer input.Close()

	start := time.Now()
	outputs, wait, err := session.Run(ctx, map[string]*ort.Tensor{ocrInputName: input}, outputNames)
	inference.wait += wait
	inference.run += time.Since(start) - wait
	return outputs, err
}

// closeTensors releases the outputs of a run.
func closeTensors(tensors map[string]*ort.Tensor) {
	for _, t := range tensors {
		_ = t.Close()
	}
}
