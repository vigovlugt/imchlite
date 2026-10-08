package ai

import (
	"image"
	"math"
	"sort"
)

type point struct{ x, y float64 }

func (p point) add(q point) point          { return point{p.x + q.x, p.y + q.y} }
func (p point) sub(q point) point          { return point{p.x - q.x, p.y - q.y} }
func (p point) mul(f float64) point        { return point{p.x * f, p.y * f} }
func (p point) dot(q point) float64        { return p.x*q.x + p.y*q.y }
func (p point) dist(q point) float64       { return math.Hypot(p.x-q.x, p.y-q.y) }
func (p point) scale(sx, sy float64) point { return point{p.x * sx, p.y * sy} }

// box is a rotated rectangle: a center, a unit vector u along the text
// (pointing right), and its size along u and across it.
type box struct {
	center        point
	u             point
	width, height float64
}

// v is the unit vector across the text, pointing down.
func (b box) v() point { return point{-b.u.y, b.u.x} }

// corners returns the top-left, top-right, bottom-right and bottom-left
// corners, as seen when reading the text.
func (b box) corners() quad {
	du, dv := b.u.mul(b.width/2), b.v().mul(b.height/2)
	return quad{
		b.center.sub(du).sub(dv),
		b.center.add(du).sub(dv),
		b.center.add(du).add(dv),
		b.center.sub(du).add(dv),
	}
}

// contains reports whether p lies inside the box.
func (b box) contains(p point) bool {
	d := p.sub(b.center)
	return math.Abs(d.dot(b.u)) <= b.width/2 && math.Abs(d.dot(b.v())) <= b.height/2
}

// minAreaBox returns the smallest rotated rectangle that contains points,
// like OpenCV's minAreaRect. The smallest one always has a side along an
// edge of the convex hull, so only those directions need to be tried.
func minAreaBox(points []point) box {
	hull := convexHull(points)
	best := box{u: point{1, 0}, width: math.Inf(1), height: math.Inf(1)}
	for i := range hull {
		edge := hull[(i+1)%len(hull)].sub(hull[i])
		if length := math.Hypot(edge.x, edge.y); length > 0 {
			b := fit(hull, edge.mul(1/length))
			if b.width*b.height < best.width*best.height {
				best = b
			}
		}
	}
	if math.IsInf(best.width, 1) { // a single point
		best = fit(hull, point{1, 0})
	}

	// Make u run along the text from left to right: text is read along the
	// longer side unless the box is tilted more than 45 degrees.
	if math.Abs(best.u.y) > math.Abs(best.u.x) {
		best.u = best.v()
		best.width, best.height = best.height, best.width
	}
	if best.u.x < 0 {
		best.u = best.u.mul(-1)
	}
	return best
}

// fit returns the rectangle with a side along u that tightly contains points.
func fit(points []point, u point) box {
	b := box{u: u}
	v := b.v()
	minU, maxU := math.Inf(1), math.Inf(-1)
	minV, maxV := math.Inf(1), math.Inf(-1)
	for _, p := range points {
		minU, maxU = math.Min(minU, p.dot(u)), math.Max(maxU, p.dot(u))
		minV, maxV = math.Min(minV, p.dot(v)), math.Max(maxV, p.dot(v))
	}
	b.center = u.mul((minU + maxU) / 2).add(v.mul((minV + maxV) / 2))
	b.width, b.height = maxU-minU, maxV-minV
	return b
}

// convexHull returns the convex hull of points (Andrew's monotone chain).
func convexHull(points []point) []point {
	sorted := append([]point(nil), points...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].x != sorted[j].x {
			return sorted[i].x < sorted[j].x
		}
		return sorted[i].y < sorted[j].y
	})
	cross := func(o, a, b point) float64 {
		return (a.x-o.x)*(b.y-o.y) - (a.y-o.y)*(b.x-o.x)
	}

	var hull []point
	for range 2 { // lower half, then upper half
		start := len(hull)
		for _, p := range sorted {
			for len(hull) >= start+2 && cross(hull[len(hull)-2], hull[len(hull)-1], p) <= 0 {
				hull = hull[:len(hull)-1]
			}
			hull = append(hull, p)
		}
		hull = hull[:len(hull)-1] // the last point starts the other half
		for i, j := 0, len(sorted)-1; i < j; i, j = i+1, j-1 {
			sorted[i], sorted[j] = sorted[j], sorted[i]
		}
	}
	if len(hull) == 0 {
		return sorted[:1]
	}
	return hull
}

// quad is a text box in the image: its top-left, top-right, bottom-right and
// bottom-left corners as seen when reading the text.
type quad [4]point

// crop samples q out of img as an upright image, turning it a quarter turn
// when it is 1.5x taller than wide, like PP-OCR does for vertical text.
func crop(img *image.NRGBA, q quad) *image.NRGBA {
	w := max(1, int(math.Max(q[0].dist(q[1]), q[3].dist(q[2]))))
	h := max(1, int(math.Max(q[0].dist(q[3]), q[1].dist(q[2]))))
	across, down := q[1].sub(q[0]), q[3].sub(q[0])

	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			p := q[0].add(across.mul((float64(x) + 0.5) / float64(w))).add(down.mul((float64(y) + 0.5) / float64(h)))
			copy(out.Pix[out.PixOffset(x, y):], bilinear(img, p))
		}
	}
	if h*2 < w*3 {
		return out
	}

	// Rotate a quarter turn counterclockwise.
	rotated := image.NewNRGBA(image.Rect(0, 0, h, w))
	for y := range w {
		for x := range h {
			copy(rotated.Pix[rotated.PixOffset(x, y):], out.Pix[out.PixOffset(w-1-y, x):][:4])
		}
	}
	return rotated
}

// bilinear returns the RGBA color at p, blending the four nearest pixels.
func bilinear(img *image.NRGBA, p point) []uint8 {
	b := img.Bounds()
	x, y := p.x-0.5, p.y-0.5 // pixel centers sit at +0.5
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	at := func(x, y int) []uint8 {
		x = min(max(x, b.Min.X), b.Max.X-1)
		y = min(max(y, b.Min.Y), b.Max.Y-1)
		return img.Pix[img.PixOffset(x, y):]
	}

	c00, c10, c01, c11 := at(x0, y0), at(x0+1, y0), at(x0, y0+1), at(x0+1, y0+1)
	out := make([]uint8, 4)
	for i := range out {
		top := float64(c00[i])*(1-fx) + float64(c10[i])*fx
		bottom := float64(c01[i])*(1-fx) + float64(c11[i])*fx
		out[i] = uint8(math.Round(top*(1-fy) + bottom*fy))
	}
	return out
}
