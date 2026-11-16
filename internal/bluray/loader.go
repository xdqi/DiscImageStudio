package bluray

import (
	"context"

	"disc-image-studio/internal/core"
	"disc-image-studio/internal/raster"
)

// Image loader resource limits (BluRayImageLoader).
const (
	maxImageFileBytes = 256 * 1024 * 1024
	maxImagePixels    = 64 * 1024 * 1024
)

// LoadGray8 ports BluRayImageLoader.Load(path, tone: null, ct): loads the
// source image as Gray8 using encoded-space Rec. 709 weights, compositing
// straight alpha over white. Without a tone profile the conversion preserves
// source dimensions and performs no thresholding, dithering, ICC conversion
// or optical-reflectance calibration.
func LoadGray8(path string, ctx context.Context) (*Gray8Raster, error) {
	return LoadGray8WithTone(path, core.ToneProfile{}, false, ctx)
}

// LoadGray8WithTone ports BluRayImageLoader.Load(path, tone, ct); useTone
// selects the non-null tone path. A tone profile is applied at source
// resolution: BD payload positions are far denser than any practical raster,
// so dithering per byte is neither needed nor reproducible.
func LoadGray8WithTone(path string, tone core.ToneProfile, useTone bool, ctx context.Context) (*Gray8Raster, error) {
	if isNullOrWhiteSpace(path) {
		return nil, argNullOrWhiteSpace("path")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	frame, err := raster.LoadBgra32(path, maxImageFileBytes, maxImagePixels)
	if err != nil {
		return nil, err
	}
	width, height := frame.Width, frame.Height
	bgra := frame.Pixels
	gray := make([]byte, width*height)
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := 0; x < width; x++ {
			pixel := y*width + x
			offset := pixel * 4
			weighted := 2126*int(bgra[offset+2]) + 7152*int(bgra[offset+1]) + 722*int(bgra[offset])
			alpha := int(bgra[offset+3])
			gray[pixel] = byte((weighted*alpha + 2550000*(255-alpha) + 1275000) / 2550000)
		}
	}

	if useTone && !tone.IsLegacyBinary() {
		for pixel := range gray {
			if pixel&0xFFFF == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if tone.UseWhite(gray[pixel], int64(pixel), 128) {
				gray[pixel] = 255
			} else {
				gray[pixel] = 0
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Gray8Raster{Width: width, Height: height, Pixels: gray}, nil
}

// ValidateImageSource ports BluRayImageLoader.ValidateSource: checks file
// size and first-frame dimensions without copying or decoding the pixel
// buffer, so preprocessing cannot bypass the source resource limits.
func ValidateImageSource(path string, ctx context.Context) error {
	if isNullOrWhiteSpace(path) {
		return argNullOrWhiteSpace("path")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := raster.ValidateSource(path, maxImageFileBytes, maxImagePixels); err != nil {
		return err
	}
	return ctx.Err()
}

// CreateImageMapping ports BluRayImageLoader.CreateMapping.
func CreateImageMapping(path string, disc *DiscParameters, mapping LuminanceMapping,
	tone core.ToneProfile, useTone bool, ctx context.Context) (*ImagePayloadMapping, error) {
	// Validate the unresolved strategy before loading any source file.
	if mapping == nil {
		return nil, argNull("mapping")
	}
	if disc == nil {
		return nil, argNull("disc")
	}
	if err := disc.Validate(); err != nil {
		return nil, err
	}
	rasterImage, err := LoadGray8WithTone(path, tone, useTone, ctx)
	if err != nil {
		return nil, err
	}
	return NewImagePayloadMapping(disc, rasterImage, mapping)
}
