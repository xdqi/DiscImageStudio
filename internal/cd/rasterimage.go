package cd

import (
	"math"

	"disc-image-studio/internal/raster"
)

// rasterImage is a precomputed nearest-neighbour grayscale source.
type rasterImage struct {
	Width  int
	Height int
	gray   []byte
}

// loadRasterImage loads a source image, converting to luminance with the
// exact integer Rec. 601 expression of the C# original (alpha ignored).
func loadRasterImage(path string) (*rasterImage, error) {
	frame, err := raster.LoadBgra32(path, math.MaxInt64, math.MaxInt32/4)
	if err != nil {
		return nil, err
	}
	gray := make([]byte, frame.Width*frame.Height)
	for index := range gray {
		offset := index * 4
		gray[index] = byte((int(frame.Pixels[offset+2])*299 +
			int(frame.Pixels[offset+1])*587 + int(frame.Pixels[offset])*114) / 1000)
	}
	return &rasterImage{Width: frame.Width, Height: frame.Height, gray: gray}, nil
}

// GrayAt samples nearest-neighbour with C# default rounding (to even).
func (r *rasterImage) GrayAt(pixelX, pixelY float64, outside byte) byte {
	x := int(math.RoundToEven(pixelX))
	y := int(math.RoundToEven(pixelY))
	if x < 0 || y < 0 || x >= r.Width || y >= r.Height {
		return outside
	}
	return r.gray[y*r.Width+x]
}
