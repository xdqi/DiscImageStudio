package caltarget

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// ReferencePixelSize is the embedded fine-stage pattern size.
const ReferencePixelSize = 2400

// HighResolutionSize is the v14+ encoding-image size.
const HighResolutionSize = 8192

//go:embed assets/target_slim.png
var referencePatternPng []byte

// ReferencePatternPng returns the embedded fine-stage reference artwork
// (target_slim.png, SHA-256 c98ffe87…, byte-identical to the .NET resource).
func ReferencePatternPng() []byte {
	out := make([]byte, len(referencePatternPng))
	copy(out, referencePatternPng)
	return out
}

// RenderEncodingImage ports CalibrationPatternRenderer.RenderEncodingImage for
// the coarse and boundary stages: a Gray8 raster sampled on the encoding
// grid, in the ordinary encoder's image coordinates.
func RenderEncodingImage(t *Target) ([]byte, int, error) {
	size := ReferencePixelSize
	// The boundary stage renders at the high-resolution size.
	if len(t.bands) == 0 {
		size = HighResolutionSize
	}

	pixels := make([]byte, size*size)
	centre := float64(size) / 2.0
	scale := t.Params.OuterRadiusMm / (float64(size) / 2.0)
	if t.Params.Kind == DVD {
		centre = float64(size-1) / 2.0
		scale = t.Params.OuterRadiusMm / (float64(size-1) / 2.0)
	}
	for y := 0; y < size; y++ {
		ym := (float64(y) - centre) * scale
		row := y * size
		for x := 0; x < size; x++ {
			xm := (float64(x) - centre) * scale
			pixels[row+x] = t.Sample(xm, ym)
		}
	}
	return pixels, size, nil
}

// EncodePng wraps the Gray8 raster into PNG bytes.
func EncodePng(pixels []byte, size int) ([]byte, error) {
	img := image.NewGray(image.Rect(0, 0, size, size))
	copy(img.Pix, pixels)
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// DecodeReferencePattern decodes the embedded Gray8 reference PNG.
func DecodeReferencePattern() ([]byte, int, error) {
	img, err := png.Decode(bytes.NewReader(referencePatternPng))
	if err != nil {
		return nil, 0, fmt.Errorf("the embedded calibration pattern is invalid: %w", err)
	}
	size := img.Bounds().Dx()
	if img.Bounds().Dy() != size || size != ReferencePixelSize {
		return nil, 0, fmt.Errorf("the embedded calibration pattern has unexpected dimensions")
	}
	pixels := make([]byte, size*size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			pixels[y*size+x] = color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y
		}
	}
	return pixels, size, nil
}

// SamplingOuterRadiusMm computes the engine's source-image scale reference for
// these parameters (the radius the raster edge maps to).
func SamplingOuterRadiusMm(t *Target) float64 {
	if t.Params.UsableSectors == 0 {
		return t.Params.OuterRadiusMm
	}
	// With a reduced usable range the engines sample against the output outer
	// radius (the radius at the last written sector).
	r, _ := t.geometry.AtProgress(float64(t.Params.UsableSectors) / float64(t.Params.Sectors))
	return r
}
