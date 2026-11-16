package dvd

import (
	"math"

	"disc-image-studio/internal/raster"
)

// pixelSample ports PixelSample.
type pixelSample struct {
	Luminance byte
	Alpha     byte
}

// rasterImage ports RasterImage.
type rasterImage struct {
	width                     int
	height                    int
	bgra                      []byte
	hasBinaryGrayscalePalette bool
}

func rasterImageLoad(path string) (*rasterImage, error) {
	// RasterCodec.LoadBgra32 defaults: maxFileBytes long.MaxValue, maxPixels int.MaxValue/4.
	raster, err := raster.LoadBgra32(path, math.MaxInt64, math.MaxInt32/4)
	if err != nil {
		return nil, err
	}
	return newRasterImage(raster.Width, raster.Height, raster.Pixels), nil
}

func newRasterImage(width, height int, bgra []byte) *rasterImage {
	return &rasterImage{
		width:                     width,
		height:                    height,
		bgra:                      bgra,
		hasBinaryGrayscalePalette: isOpaqueBinaryGrayscale(bgra),
	}
}

func rasterImageCreateForTest(width, height int, blue, green, red, alpha byte) *rasterImage {
	pixels := make([]byte, width*height*4)
	for offset := 0; offset < len(pixels); offset += 4 {
		pixels[offset] = blue
		pixels[offset+1] = green
		pixels[offset+2] = red
		pixels[offset+3] = alpha
	}
	return newRasterImage(width, height, pixels)
}

func (r *rasterImage) sampleDisc(xMm, yMm, outerRadiusMm float64, preserveDiscreteGrayscale bool) pixelSample {
	scale := float64(minInt(r.width, r.height)-1) / (2.0 * outerRadiusMm)
	pixelX := float64(r.width-1)/2.0 + xMm*scale
	pixelY := float64(r.height-1)/2.0 - yMm*scale
	if pixelX < 0 || pixelY < 0 || pixelX > float64(r.width-1) || pixelY > float64(r.height-1) {
		return pixelSample{}
	}

	// An already dithered image encodes brightness as the spatial proportion
	// of its discrete pixels. Interpolation would invent intermediate tones
	// and requantization would change those proportions.
	if preserveDiscreteGrayscale && r.hasBinaryGrayscalePalette {
		x := int(math.Floor(pixelX + 0.5))
		y := int(math.Floor(pixelY + 0.5))
		return pixelSample{Luminance: r.bgra[((y*r.width)+x)*4], Alpha: 255}
	}

	x0 := int(math.Floor(pixelX))
	y0 := int(math.Floor(pixelY))
	x1 := minInt(x0+1, r.width-1)
	y1 := minInt(y0+1, r.height-1)
	horizontal := pixelX - float64(x0)
	vertical := pixelY - float64(y0)

	totalAlpha := 0.0
	weightedBlue := 0.0
	weightedGreen := 0.0
	weightedRed := 0.0
	accumulate := func(x, y int, weight float64) {
		offset := ((y * r.width) + x) * 4
		weightedAlpha := weight * float64(r.bgra[offset+3])
		totalAlpha += weightedAlpha
		weightedBlue += weightedAlpha * float64(r.bgra[offset])
		weightedGreen += weightedAlpha * float64(r.bgra[offset+1])
		weightedRed += weightedAlpha * float64(r.bgra[offset+2])
	}
	accumulate(x0, y0, (1-horizontal)*(1-vertical))
	accumulate(x1, y0, horizontal*(1-vertical))
	accumulate(x0, y1, (1-horizontal)*vertical)
	accumulate(x1, y1, horizontal*vertical)

	if totalAlpha <= 0 {
		return pixelSample{}
	}

	alpha := byte(clampInt(int(math.RoundToEven(totalAlpha)), 0, 255))
	inverseAlpha := 1.0 / totalAlpha
	blue := weightedBlue * inverseAlpha
	green := weightedGreen * inverseAlpha
	red := weightedRed * inverseAlpha
	luminance := byte(clampInt(
		int(math.RoundToEven(0.2126*red+0.7152*green+0.0722*blue)),
		0,
		255))
	return pixelSample{Luminance: luminance, Alpha: alpha}
}

// tryGetStableRegion ports RasterImage.TryGetStableRegion.
func (r *rasterImage) tryGetStableRegion(xMm, yMm, outerRadiusMm float64,
	preserveDiscreteGrayscale bool) (sample pixelSample, region sampleRectangle, ok bool) {
	scale := float64(minInt(r.width, r.height)-1) / (2.0 * outerRadiusMm)
	centreX := float64(r.width-1) / 2.0
	centreY := float64(r.height-1) / 2.0
	x := centreX + xMm*scale
	y := centreY - yMm*scale
	if math.IsNaN(x) || math.IsInf(x, 0) || math.IsNaN(y) || math.IsInf(y, 0) ||
		math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 ||
		x < 0 || y < 0 || x > float64(r.width-1) || y > float64(r.height-1) {
		return pixelSample{}, sampleRectangle{}, false
	}

	var left, right, top, bottom float64
	var gray byte
	if preserveDiscreteGrayscale && r.hasBinaryGrayscalePalette {
		pixelX := int(math.Floor(x + 0.5))
		pixelY := int(math.Floor(y + 0.5))
		gray = r.bgra[((pixelY*r.width)+pixelX)*4]
		left = math.Max(0, float64(pixelX)-0.5)
		right = math.Min(float64(r.width-1), float64(pixelX)+0.5)
		top = math.Max(0, float64(pixelY)-0.5)
		bottom = math.Min(float64(r.height-1), float64(pixelY)+0.5)
	} else {
		x0 := int(math.Floor(x))
		y0 := int(math.Floor(y))
		if x0 >= r.width-1 || y0 >= r.height-1 {
			return pixelSample{}, sampleRectangle{}, false
		}
		offset := ((y0 * r.width) + x0) * 4
		gray = r.bgra[offset]
		if !isOpaqueGray(r.bgra, offset, gray) || !isOpaqueGray(r.bgra, offset+4, gray) ||
			!isOpaqueGray(r.bgra, offset+r.width*4, gray) ||
			!isOpaqueGray(r.bgra, offset+r.width*4+4, gray) {
			return r.tryGetBilinearRegion(xMm, yMm, x, y, x0, y0, centreX, centreY, scale)
		}
		left = float64(x0)
		right = float64(x0 + 1)
		top = float64(y0)
		bottom = float64(y0 + 1)
		// All four alpha values are 255 and channels are the same integer.
	}
	region, ok = sampleRectangleTryFromPixels(left, right, top, bottom, centreX, centreY, scale, xMm, yMm)
	if !ok {
		return pixelSample{}, sampleRectangle{}, false
	}
	return pixelSample{Luminance: gray, Alpha: 255}, region, true
}

func (r *rasterImage) tryGetBilinearRegion(xMm, yMm, x, y float64, x0, y0 int,
	centreX, centreY, scale float64) (sample pixelSample, region sampleRectangle, ok bool) {
	horizontal := x - float64(x0)
	vertical := y - float64(y0)
	totalAlpha := 0.0
	weightedBlue := 0.0
	weightedGreen := 0.0
	weightedRed := 0.0
	p00 := ((y0 * r.width) + x0) * 4
	p10 := p00 + 4
	p01 := p00 + r.width*4
	p11 := p01 + 4
	accumulate := func(offset int, weight float64) {
		weightedAlpha := weight * float64(r.bgra[offset+3])
		totalAlpha += weightedAlpha
		weightedBlue += weightedAlpha * float64(r.bgra[offset])
		weightedGreen += weightedAlpha * float64(r.bgra[offset+1])
		weightedRed += weightedAlpha * float64(r.bgra[offset+2])
	}
	accumulate(p00, (1-horizontal)*(1-vertical))
	accumulate(p10, horizontal*(1-vertical))
	accumulate(p01, (1-horizontal)*vertical)
	accumulate(p11, horizontal*vertical)
	if totalAlpha < 1 {
		return pixelSample{}, sampleRectangle{}, false
	}
	inverseAlpha := 1.0 / totalAlpha
	blue := weightedBlue * inverseAlpha
	green := weightedGreen * inverseAlpha
	red := weightedRed * inverseAlpha
	luminance := 0.2126*red + 0.7152*green + 0.0722*blue
	a00 := float64(r.bgra[p00+3])
	a10 := float64(r.bgra[p10+3])
	a01 := float64(r.bgra[p01+3])
	a11 := float64(r.bgra[p11+3])
	premultipliedLuminance := func(offset int) float64 {
		return float64(r.bgra[offset+3]) * (0.2126*float64(r.bgra[offset+2]) + 0.7152*float64(r.bgra[offset+1]) + 0.0722*float64(r.bgra[offset]))
	}
	l00 := premultipliedLuminance(p00)
	l10 := premultipliedLuminance(p10)
	l01 := premultipliedLuminance(p01)
	l11 := premultipliedLuminance(p11)
	alphaGradient := math.Max(math.Abs(a10-a00), math.Abs(a11-a01)) +
		math.Max(math.Abs(a01-a00), math.Abs(a11-a10))
	luminanceGradient := math.Max(math.Abs(l10-l00), math.Abs(l11-l01)) +
		math.Max(math.Abs(l01-l00), math.Abs(l11-l10))
	delta, okRadius := bilinearSampleStabilityTryGetRadius(totalAlpha, luminance, alphaGradient, luminanceGradient)
	if !okRadius {
		return pixelSample{}, sampleRectangle{}, false
	}
	region, ok = sampleRectangleTryFromPixels(
		math.Max(float64(x0), x-delta), math.Min(float64(x0+1), x+delta),
		math.Max(float64(y0), y-delta), math.Min(float64(y0+1), y+delta),
		centreX, centreY, scale, xMm, yMm)
	if !ok {
		return pixelSample{}, sampleRectangle{}, false
	}
	return pixelSample{
		Luminance: byte(clampInt(int(math.RoundToEven(luminance)), 0, 255)),
		Alpha:     byte(clampInt(int(math.RoundToEven(totalAlpha)), 0, 255)),
	}, region, true
}

func isOpaqueBinaryGrayscale(pixels []byte) bool {
	for offset := 0; offset < len(pixels); offset += 4 {
		value := pixels[offset]
		if pixels[offset+3] != 255 ||
			pixels[offset+1] != value ||
			pixels[offset+2] != value ||
			(value != 0 && value != 255) {
			return false
		}
	}
	return true
}

func isOpaqueGray(pixels []byte, offset int, gray byte) bool {
	return pixels[offset] == gray && pixels[offset+1] == gray &&
		pixels[offset+2] == gray && pixels[offset+3] == 255
}

// sampleRectangle ports SampleRectangle: strict open rectangle in millimetres.
type sampleRectangle struct {
	MinX, MaxX, MinY, MaxY float64
}

func (r sampleRectangle) contains(x, y float64) bool {
	return x > r.MinX && x < r.MaxX && y > r.MinY && y < r.MaxY
}

func sampleRectangleTryFromPixels(left, right, top, bottom,
	centreX, centreY, scale, xMm, yMm float64) (sampleRectangle, bool) {
	if math.IsNaN(scale) || math.IsInf(scale, 0) || scale <= 0 {
		return sampleRectangle{}, false
	}
	minX := (left - centreX) / scale
	maxX := (right - centreX) / scale
	minY := (centreY - bottom) / scale
	maxY := (centreY - top) / scale
	extent := math.Max(math.Max(math.Abs(minX), math.Abs(maxX)),
		math.Max(math.Abs(minY), math.Abs(maxY)))
	// Cover both this inverse transform and the original forward mapping.
	// The caller separately accounts for geometry/trajectory position error.
	const epsilon = 2.2204460492503131e-16
	guard := 128 * epsilon * (extent + (math.Abs(centreX)+math.Abs(centreY)+1)/scale)
	if math.IsNaN(guard) || math.IsInf(guard, 0) {
		return sampleRectangle{}, false
	}
	region := sampleRectangle{minX + guard, maxX - guard, minY + guard, maxY - guard}
	return region, region.contains(xMm, yMm)
}

// bilinearSampleStabilityTryGetRadius ports BilinearSampleStability.TryGetRadius.
func bilinearSampleStabilityTryGetRadius(alpha, luminance, alphaGradient,
	premultipliedLuminanceGradient float64) (radius float64, ok bool) {
	// Original scalar arithmetic has positive weights and byte-valued channels.
	const roundingBudget = 1e-7
	if math.IsNaN(alpha) || math.IsInf(alpha, 0) || math.IsNaN(luminance) || math.IsInf(luminance, 0) || alpha < 1 {
		return 0, false
	}
	alphaMargin := 0.5 - math.Abs(alpha-math.RoundToEven(alpha)) - roundingBudget
	luminanceMargin := 0.5 - math.Abs(luminance-math.RoundToEven(luminance)) - roundingBudget
	if alphaMargin <= 0 || luminanceMargin <= 0 {
		return 0, false
	}
	minimumAlpha := alpha / 2
	luminanceGradient := (premultipliedLuminanceGradient + 255*alphaGradient + roundingBudget) / minimumAlpha
	radius = luminanceMargin / luminanceGradient
	if alphaGradient > 0 {
		radius = math.Min(radius, math.Min(alphaMargin, minimumAlpha-roundingBudget)/alphaGradient)
	}
	return radius, !math.IsNaN(radius) && !math.IsInf(radius, 0) && radius > 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
