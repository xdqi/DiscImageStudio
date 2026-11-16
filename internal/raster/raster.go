// Package raster ports DiscImageStudio.ImageIO: image loading to straight
// BGRA32 and PNG writing (Gray8 or BGRA32). Decoding supports PNG, JPEG,
// GIF and BMP like the StbImageSharp-based original.
package raster

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/image/bmp"
)

func init() {
	image.RegisterFormat("png", "\x89PNG\r\n\x1a\n", png.Decode, png.DecodeConfig)
	image.RegisterFormat("jpeg", "\xff\xd8", jpeg.Decode, jpeg.DecodeConfig)
	image.RegisterFormat("gif", "GIF8", gif.Decode, gif.DecodeConfig)
	image.RegisterFormat("bmp", "BM", bmp.Decode, bmp.DecodeConfig)
}

// BgraRaster is a straight-alpha BGRA32 pixel buffer.
type BgraRaster struct {
	Width  int
	Height int
	Pixels []byte
}

// LoadBgra32 reads the first frame as straight BGRA32.
func LoadBgra32(path string, maxFileBytes int64, maxPixels int64) (*BgraRaster, error) {
	full := filepath.Clean(path)
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			abs, absErr := filepath.Abs(full)
			if absErr != nil {
				abs = full
			}
			return nil, fmt.Errorf("Could not find file '%s'.", abs)
		}
		return nil, err
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("the source image must be no larger than %g MiB", float64(maxFileBytes)/1048576.0)
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return decode(file, maxPixels)
}

// ValidateSource checks the file size and header without decoding pixels.
func ValidateSource(path string, maxFileBytes int64, maxPixels int64) error {
	full := filepath.Clean(path)
	info, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			abs, absErr := filepath.Abs(full)
			if absErr != nil {
				abs = full
			}
			return fmt.Errorf("Could not find file '%s'.", abs)
		}
		return err
	}
	if info.Size() > maxFileBytes {
		return fmt.Errorf("the source image must be no larger than %g MiB", float64(maxFileBytes)/1048576.0)
	}
	file, err := os.Open(full)
	if err != nil {
		return err
	}
	defer file.Close()
	_, width, height, err := imageInfo(file)
	if err != nil {
		return err
	}
	return checkDimensions(width, height, maxPixels)
}

func imageInfo(r io.Reader) (format string, width, height int, err error) {
	config, format, err := image.DecodeConfig(r)
	if err != nil {
		return "", 0, 0, fmt.Errorf("unsupported or invalid image. Use a PNG, JPEG, BMP or GIF")
	}
	return format, config.Width, config.Height, nil
}

func decode(r io.Reader, maxPixels int64) (*BgraRaster, error) {
	// Buffer the stream so the header is validated before the pixel buffer is
	// allocated, like the C# ReadHeader + rewind + Decode sequence.
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	_, width, height, err := imageInfo(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if err := checkDimensions(width, height, maxPixels); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("unsupported or invalid image. Use a PNG, JPEG, BMP or GIF")
	}
	pixels := make([]byte, width*height*4)
	// Convert every pixel through NRGBA (straight alpha), then swap R/B to BGRA.
	bounds := img.Bounds()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			nrgba := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			offset := (y*width + x) * 4
			pixels[offset] = nrgba.B
			pixels[offset+1] = nrgba.G
			pixels[offset+2] = nrgba.R
			pixels[offset+3] = nrgba.A
		}
	}
	return &BgraRaster{Width: width, Height: height, Pixels: pixels}, nil
}

func checkDimensions(width, height int, maxPixels int64) error {
	limit := maxPixels
	if max := int64(int(^uint(0)>>1)) / 4; limit > max {
		limit = max
	}
	if width <= 0 || height <= 0 || int64(width)*int64(height) > limit {
		return fmt.Errorf("the source image must contain between 1 and %d pixels", limit)
	}
	return nil
}

// SavePng writes a Gray8 or BGRA32 PNG.
func SavePng(path string, width, height int, pixels []byte, grayscale bool) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("image dimensions must be positive")
	}
	perPixel := 4
	if grayscale {
		perPixel = 1
	}
	if len(pixels) != width*height*perPixel {
		return fmt.Errorf("pixel buffer length does not match the dimensions and format")
	}
	var img image.Image
	if grayscale {
		g := image.NewGray(image.Rect(0, 0, width, height))
		copy(g.Pix, pixels)
		img = g
	} else {
		n := image.NewNRGBA(image.Rect(0, 0, width, height))
		for i := 0; i < len(pixels); i += 4 {
			n.Pix[i] = pixels[i+2]
			n.Pix[i+1] = pixels[i+1]
			n.Pix[i+2] = pixels[i]
			n.Pix[i+3] = pixels[i+3]
		}
		img = n
	}
	if dir := filepath.Dir(filepath.Clean(path)); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	output, err := os.Create(path)
	if err != nil {
		return err
	}
	defer output.Close()
	encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
	return encoder.Encode(output, img)
}
