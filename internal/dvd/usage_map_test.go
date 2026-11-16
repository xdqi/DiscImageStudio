package dvd

import (
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"disc-image-studio/internal/raster"
)

// writeSolidPng writes an opaque single-color PNG.
func writeSolidPng(t *testing.T, path string, size int, level byte) string {
	t.Helper()
	pixels := make([]byte, size*size*4)
	for o := 0; o < len(pixels); o += 4 {
		pixels[o] = level
		pixels[o+1] = level
		pixels[o+2] = level
		pixels[o+3] = 255
	}
	if err := raster.SavePng(path, size, size, pixels, false); err != nil {
		t.Fatal(err)
	}
	return path
}

func decodePng(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// usageMapProbe reads the map pixel at the renderer's own mid-arc sample
// position of a sector (k=7 of 16), using the default solve geometry
// (24..58 mm, 133.3 nm, CW, start angle 0, 1024 px).
func usageMapProbe(t *testing.T, img image.Image, sector, totalSectors uint32) (r, g, b, a byte) {
	t.Helper()
	const bitLengthMm = 133.3e-6
	trackLengthMm := float64(totalSectors) * dvdChannelBitsPerSector * bitLengthMm
	spiral, err := archimedeanSpiralCreate(24, 58, trackLengthMm, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	arcLengthMm := (float64(sector) + 7.5/usageMapSamplesPerSector) * dvdChannelBitsPerSector * bitLengthMm
	radiusMm, trackAngle := spiral.atArcLengthFast(arcLengthMm)
	polarAngle := -trackAngle // cw, start angle 0
	const centre = float64(usageMapPreviewSize-1) / 2.0
	pixelRadius := centre * radiusMm / 58
	pixelX := int(math.RoundToEven(centre + pixelRadius*math.Cos(polarAngle)))
	pixelY := int(math.RoundToEven(centre - pixelRadius*math.Sin(polarAngle)))
	red, green, blue, alpha := img.At(pixelX, pixelY).RGBA()
	return byte(red >> 8), byte(green >> 8), byte(blue >> 8), byte(alpha >> 8)
}

// countUsagePixels counts opaque pixels equal to (r, g, b).
func countUsagePixels(img image.Image, r, g, b byte) int {
	bounds := img.Bounds()
	count := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			red, green, blue, alpha := img.At(x, y).RGBA()
			if alpha == 0 {
				continue
			}
			if byte(red>>8) == r && byte(green>>8) == g && byte(blue>>8) == b {
				count++
			}
		}
	}
	return count
}

// TestUsageMapPlainSolve checks the map of a plain fast-dispersion solve:
// the filled range renders the picture tone, the rest is unoccupied cyan.
func TestUsageMapPlainSolve(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeSolidPng(t, filepath.Join(dir, "black.png"), 64, 0)
	isoPath := filepath.Join(dir, "out.iso")
	mapPath := filepath.Join(dir, "usage.png")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "320", "--fill-sectors", "160",
		"--algorithm", "dispersion", "--iterations-per-block", "1",
		"--fast-output", "true", "--spiral-direction", "cw",
		"--usage-map", mapPath,
	})
	if code != 0 {
		t.Fatalf("solve exit code %d", code)
	}
	img := decodePng(t, mapPath)
	if img.Bounds().Dx() != usageMapPreviewSize || img.Bounds().Dy() != usageMapPreviewSize {
		t.Fatalf("map size = %v, want %d", img.Bounds(), usageMapPreviewSize)
	}
	// Sector 5 is inside the drawing range: solid black picture.
	if r, g, b, a := usageMapProbe(t, img, 5, 320); r != 0 || g != 0 || b != 0 || a != 255 {
		t.Fatalf("picture sector pixel = %d,%d,%d,%d; want black", r, g, b, a)
	}
	// Sector 300 is beyond the fill: unoccupied cyan.
	if r, g, b, a := usageMapProbe(t, img, 300, 320); r != 0 || g != 255 || b != 255 || a != 255 {
		t.Fatalf("free sector pixel = %d,%d,%d,%d; want cyan", r, g, b, a)
	}
	if n := countUsagePixels(img, 255, 0, 255); n != 0 {
		t.Fatalf("plain solve has no data, but %d magenta pixels", n)
	}
}

// TestUsageMapHybridContiguous checks the map of a contiguous hybrid ISO:
// the filesystem range is data-magenta, the drawing keeps the picture tone.
func TestUsageMapHybridContiguous(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeSolidPng(t, filepath.Join(dir, "black.png"), 64, 0)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "hello.txt"), []byte("hello hybrid map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	isoPath := filepath.Join(dir, "out.iso")
	mapPath := filepath.Join(dir, "usage.png")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "4096", "--data-dir", dataDir,
		"--algorithm", "dispersion", "--iterations-per-block", "1",
		"--fast-output", "true", "--spiral-direction", "cw",
		"--usage-map", mapPath,
	})
	if code != 0 {
		t.Fatalf("solve exit code %d", code)
	}
	img := decodePng(t, mapPath)
	// Sector 2 is inside the reserved/filesystem range: data magenta
	// (dominance thresholds: the pitch-wide rendering blends turn boundaries).
	if r, g, b, a := usageMapProbe(t, img, 2, 4096); a != 255 || r < 120 || b < 120 || r < g+60 || b < g+60 {
		t.Fatalf("data sector pixel = %d,%d,%d,%d; want magenta-dominant", r, g, b, a)
	}
	// Sector 2000 is inside the drawing range: black picture.
	if r, g, b, a := usageMapProbe(t, img, 2000, 4096); a != 255 || r+g+b > 200 {
		t.Fatalf("picture sector pixel = %d,%d,%d,%d; want black", r, g, b, a)
	}
	if n := countUsagePixels(img, 255, 0, 255); n == 0 {
		t.Fatal("hybrid solve has data, but no magenta pixels")
	}
}

// TestUsageMapDispersed checks the map of a dispersed UDF solve: reserved
// metadata is magenta, surplus gray capacity is cyan, and protected sectors
// render the picture tones.
func TestUsageMapDispersed(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeGradientPng(t, filepath.Join(dir, "gradient.png"), 64)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "hello.txt"), []byte("hello dispersed map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	isoPath := filepath.Join(dir, "disc.iso")
	mapPath := filepath.Join(dir, "usage.png")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "4096", "--data-dir", dataDir, "--filesystem", "udf",
		"--usage-map", mapPath,
	})
	if code != 0 {
		t.Fatalf("solve exit code %d", code)
	}
	img := decodePng(t, mapPath)
	// Sector 2 is inside the reserved metadata range: data magenta.
	if r, g, b, a := usageMapProbe(t, img, 2, 4096); r != 255 || g != 0 || b != 255 || a != 255 {
		t.Fatalf("metadata sector pixel = %d,%d,%d,%d; want magenta", r, g, b, a)
	}
	if n := countUsagePixels(img, 255, 0, 255); n == 0 {
		t.Error("dispersed solve has file data, but no magenta pixels")
	}
	if n := countUsagePixels(img, 0, 255, 255); n == 0 {
		t.Error("dispersed solve leaves gray headroom, but no cyan pixels")
	}
	if n := countUsagePixels(img, 0, 0, 0); n == 0 {
		t.Error("gradient has a black region, but no black picture pixels")
	}
	if n := countUsagePixels(img, 255, 255, 255); n == 0 {
		t.Error("gradient has a white region, but no white picture pixels")
	}
}

// TestUsageMapSingleBlockRejected: the single-block path has no map support.
func TestUsageMapSingleBlockRejected(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeSolidPng(t, filepath.Join(dir, "black.png"), 64, 0)
	code := RunCommand([]string{
		"solve", "--image", pngPath,
		"--iso-output", filepath.Join(dir, "out.iso"),
		"--total-sectors", "160", "--fill-sectors", "16",
		"--usage-map", filepath.Join(dir, "usage.png"),
	})
	if code == 0 {
		t.Fatal("single-block --usage-map should fail")
	}
}
