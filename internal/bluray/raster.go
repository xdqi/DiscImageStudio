package bluray

import (
	"math"
)

// Gray8Raster ports BluRayGray8Raster: an immutable, tightly packed Gray8
// source raster. Values are image intensities, not measured disc reflectance.
// Source data is copied on construction.
type Gray8Raster struct {
	Width  int
	Height int
	Pixels []byte
}

// NewGray8Raster validates the dimensions and copies the source pixels.
func NewGray8Raster(width, height int, pixels []byte) (*Gray8Raster, error) {
	if width <= 0 {
		return nil, argNegativeOrZero("width", width)
	}
	if height <= 0 {
		return nil, argNegativeOrZero("height", height)
	}
	length := int64(width) * int64(height)
	if length > math.MaxInt32 {
		return nil, argOutOfRangeMsg("width", "Raster dimensions are too large.")
	}
	if int64(len(pixels)) != length {
		return nil, argErr("A Gray8 raster needs exactly width × height bytes.", "pixels")
	}
	copied := make([]byte, length)
	copy(copied, pixels)
	return &Gray8Raster{Width: width, Height: height, Pixels: copied}, nil
}

// SampleDiscPoint fits the longest image dimension to the disc diameter,
// preserving aspect ratio. Coordinates are millimetres, with positive Y toward
// the image top. Samples use the nearest pixel centre; regions outside the
// fitted image are white.
func (r *Gray8Raster) SampleDiscPoint(xMm, yMm, outerRadiusMm float64) (byte, error) {
	if isNotFinite(xMm) {
		return 0, argOutOfRange("xMm")
	}
	if isNotFinite(yMm) {
		return 0, argOutOfRange("yMm")
	}
	if isNotFinite(outerRadiusMm) || outerRadiusMm <= 0 {
		return 0, argOutOfRange("outerRadiusMm")
	}
	scale := float64(maxInt(r.Width, r.Height)) / (2 * outerRadiusMm)
	return r.sampleWithScale(xMm, yMm, scale), nil
}

// sampleWithScale is only called by the validated, finite geometry fast path.
func (r *Gray8Raster) sampleWithScale(xMm, yMm, scale float64) byte {
	x := float64(r.Width)/2.0 + xMm*scale
	y := float64(r.Height)/2.0 - yMm*scale
	if x < 0 || y < 0 || x >= float64(r.Width) || y >= float64(r.Height) {
		return 255
	}
	return r.Pixels[int(y)*r.Width+int(x)]
}

func (r *Gray8Raster) sampleWithTravelMargin(xMm, yMm, scale,
	tangentX, tangentY, curvatureBound, marginMm,
	tangentMargin float64) (byte, float64) {
	x := float64(r.Width)/2.0 + xMm*scale
	y := float64(r.Height)/2.0 - yMm*scale
	// Image Y points down, opposite to the physical tangent's Y component.
	tangentY = -tangentY
	if x < 0 || y < 0 || x >= float64(r.Width) || y >= float64(r.Height) {
		// Staying outside any one edge is enough to remain in white space.
		// Only edges actually crossed by the anchor participate in this max.
		travelMm := 0.0
		if x < 0 {
			travelMm = math.Max(travelMm, safeTravel(-x/scale-marginMm, tangentX+tangentMargin, curvatureBound))
		}
		if x >= float64(r.Width) {
			travelMm = math.Max(travelMm, safeTravel((x-float64(r.Width))/scale-marginMm, -tangentX+tangentMargin, curvatureBound))
		}
		if y < 0 {
			travelMm = math.Max(travelMm, safeTravel(-y/scale-marginMm, tangentY+tangentMargin, curvatureBound))
		}
		if y >= float64(r.Height) {
			travelMm = math.Max(travelMm, safeTravel((y-float64(r.Height))/scale-marginMm, -tangentY+tangentMargin, curvatureBound))
		}
		return 255, travelMm
	}
	ix, iy := int(x), int(y)
	left := (x-float64(ix))/scale - marginMm
	right := (float64(ix)+1.0-x)/scale - marginMm
	top := (y-float64(iy))/scale - marginMm
	bottom := (float64(iy)+1.0-y)/scale - marginMm
	if left <= 0 || right <= 0 || top <= 0 || bottom <= 0 {
		return r.Pixels[iy*r.Width+ix], 0
	}
	travelMm := math.Min(
		math.Min(safeTravel(left, -tangentX+tangentMargin, curvatureBound),
			safeTravel(right, tangentX+tangentMargin, curvatureBound)),
		math.Min(safeTravel(top, -tangentY+tangentMargin, curvatureBound),
			safeTravel(bottom, tangentY+tangentMargin, curvatureBound)))
	return r.Pixels[iy*r.Width+ix], travelMm
}

func safeTravel(edgeDistance, tangent, curvatureBound float64) float64 {
	if edgeDistance <= 0 {
		return 0
	}
	// curvatureBound is half the coordinate acceleration bound for the
	// sampled curve, so its Taylor remainder is at most k*s*s.
	// Solve k*s*s + v*s < distance. Use the stable form for each sign of v
	// so a boundary behind the tangent does not suffer subtractive cancellation.
	root := math.Sqrt(tangent*tangent + 4*curvatureBound*edgeDistance)
	if tangent >= 0 {
		return 2 * edgeDistance / (root + tangent)
	}
	return (root - tangent) / (2 * curvatureBound)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
