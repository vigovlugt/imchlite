package entity

// OCRLine mirrors the asset_ocr table in migration006: one line of text
// read from an asset.
type OCRLine struct {
	// Box is the rotated box around the line, its corners clockwise from
	// top-left, relative to the image size (0..1).
	Box [4]Point

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
