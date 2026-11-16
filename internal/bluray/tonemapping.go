package bluray

import (
	"context"

	"disc-image-studio/internal/core"
)

// ApplyToneMapping ports BluRayToneMapping.Apply: applies the shared tone
// profile once, at source-image resolution: sRGB linearization, brightness
// and byte-level dither decide each pixel's binary state before the 3T/6T
// mapping. Payload positions are far denser than any practical raster, so
// this is the only place a BD picture is quantized.
func ApplyToneMapping(source *Gray8Raster, tone core.ToneProfile, ctx context.Context) (*Gray8Raster, error) {
	if source == nil {
		return nil, argNull("source")
	}
	effective := tone.Normalized()
	if effective.IsLegacyBinary() {
		return source, nil
	}

	pixels := make([]byte, len(source.Pixels))
	copy(pixels, source.Pixels)
	for index := range pixels {
		if index&0xFFFF == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if effective.UseWhite(pixels[index], int64(index), 128) {
			pixels[index] = 255
		} else {
			pixels[index] = 0
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Gray8Raster{Width: source.Width, Height: source.Height, Pixels: pixels}, nil
}
