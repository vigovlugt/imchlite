package entity

// OCRBox mirrors the asset_ocr_boxes table in migration006: the text read
// from one box the detector found in an asset.
type OCRBox struct {
	// Corners are the corners of the rotated box around the text, clockwise
	// from top-left, relative to the image size (0..1).
	Corners [4]Point
	// Line is the line of text the box is on, from 0 at the top; boxes on
	// one line share it.
	Line int

	Text string

	// BoxScore is the mean detection probability inside the box.
	BoxScore float64
	// TextScore is the mean recognizer confidence of the characters.
	TextScore float64
}

// Point is a position relative to the image size (0..1).
type Point struct {
	X, Y float64
}
