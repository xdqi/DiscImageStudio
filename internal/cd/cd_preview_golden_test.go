package cd

import (
	"bytes"
	"context"
	_ "image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"disc-image-studio/internal/core"
)

// decodePngPixels decodes a PNG to raw RGBA bytes for pixel comparison (PNG
// encoder bytes differ between .NET and Go; pixels must not).
func decodePngPixels(t *testing.T, path string) (int, int, []byte) {
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
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	pixels := make([]byte, width*height*4)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, a := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			offset := (y*width + x) * 4
			pixels[offset] = byte(r >> 8)
			pixels[offset+1] = byte(g >> 8)
			pixels[offset+2] = byte(b >> 8)
			pixels[offset+3] = byte(a >> 8)
		}
	}
	return width, height, pixels
}

func comparePngToGolden(t *testing.T, gotPath, goldenName string) {
	t.Helper()
	goldenPath := filepath.Join(goldenDir, goldenName)
	if _, err := os.Stat(goldenPath); err != nil {
		t.Skip("golden preview not available")
	}
	gw, gh, golden := decodePngPixels(t, goldenPath)
	w, h, got := decodePngPixels(t, gotPath)
	if w != gw || h != gh {
		t.Fatalf("dimensions differ: %dx%d vs golden %dx%d", w, h, gw, gh)
	}
	if !bytes.Equal(got, golden) {
		different := 0
		var firstDiff int
		for i := range got {
			if got[i] != golden[i] {
				if different == 0 {
					firstDiff = i
				}
				different++
			}
		}
		t.Fatalf("%d channel bytes differ (first at %d: got %02x want %02x)", different, firstDiff, got[firstDiff], golden[firstDiff])
	}
}

func TestGoldenPreviewWarp(t *testing.T) {
	goldenAvailable(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "input.png")
	if err := os.WriteFile(input, readGolden(t, "input.png"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "warp.png")
	generated, err := NewDiscParameters(24.5, 56.8, 400, StandardLinearVelocityMmPerSecond,
		0, StandardImageOuterRadiusMm, 0, 0, 0, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	measured, err := NewDiscParameters(24.3, 56.6, 400, StandardLinearVelocityMmPerSecond,
		0, StandardImageOuterRadiusMm, 0, 0, 0, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreviewWarp(context.Background(), input, output, generated, measured, 128, 64, core.LegacyBinary); err != nil {
		t.Fatal(err)
	}
	comparePngToGolden(t, output, "warp.png")
}

func TestGoldenPreviewTrackWav(t *testing.T) {
	goldenAvailable(t)
	dir := t.TempDir()
	// Regenerate the track (the golden WAV is byte-identical, proven elsewhere).
	input := filepath.Join(dir, "input.png")
	if err := os.WriteFile(input, readGolden(t, "input.png"), 0o644); err != nil {
		t.Fatal(err)
	}
	track := filepath.Join(dir, "track32.wav")
	parameters, err := NewDiscParameters(24.5, 56.8, 32, StandardLinearVelocityMmPerSecond,
		0, StandardImageOuterRadiusMm, 0, 0, 0, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateTrack(input, track, parameters, true, true, nil, context.Background(), core.LegacyBinary); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "trackview.png")
	actual, err := NewDiscParameters(24.5, 56.8, 32, StandardLinearVelocityMmPerSecond,
		0, StandardImageOuterRadiusMm, 0, 0, 0, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreviewTrack(context.Background(), track, output, actual, 128, 48, true); err != nil {
		t.Fatal(err)
	}
	comparePngToGolden(t, output, "trackview.png")
}

// TestGoldenDataIsoDrawing verifies the drawing region of a Mode 1 data ISO.
// The filesystem prefix is covered byte-exactly by internal/iso tests; the
// volume timestamp makes a whole-file comparison impossible after the fact.
func TestGoldenDataIsoDrawing(t *testing.T) {
	goldenAvailable(t)
	goldenIso := filepath.Join(goldenDir, "data32.iso")
	if _, err := os.Stat(goldenIso); err != nil {
		t.Skip("golden data ISO not available")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "input.png")
	if err := os.WriteFile(input, readGolden(t, "input.png"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "hello.txt"), []byte("hello-world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "sub", "blob.bin"), []byte("binary\x00data"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "data32.iso")
	parameters, err := NewDiscParameters(24.5, 56.8, 400, StandardLinearVelocityMmPerSecond,
		0, StandardImageOuterRadiusMm, 0, 0, 0, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareDataLayout(dataDir, parameters, "MYDISC", false)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := GenerateDataImage(context.Background(), input, output, parameters, plan, nil, core.LegacyBinary)
	if err != nil {
		t.Fatal(err)
	}
	if summary.BytesWritten != 400*DataBytesPerSector {
		t.Fatalf("bytes written = %d", summary.BytesWritten)
	}
	// The golden's drawing range starts at LBA 29 (per data32.iso.json); our
	// layout must agree, then the drawing bytes must match exactly.
	goldenMeta := TryLoadDataImageMetadata(goldenIso)
	if goldenMeta == nil {
		t.Fatal("golden sidecar unreadable")
	}
	if plan.DrawingStartLba != goldenMeta.DrawingStartLba ||
		plan.DrawingEndLbaExclusive != goldenMeta.DrawingEndLbaExclusive {
		t.Fatalf("drawing range = %d..%d, golden %d..%d",
			plan.DrawingStartLba, plan.DrawingEndLbaExclusive,
			goldenMeta.DrawingStartLba, goldenMeta.DrawingEndLbaExclusive)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(goldenIso)
	if err != nil {
		t.Fatal(err)
	}
	start := int64(plan.DrawingStartLba) * DataBytesPerSector
	if !bytes.Equal(got[start:], want[start:]) {
		for i := start; i < int64(len(got)); i++ {
			if got[i] != want[i] {
				t.Fatalf("first drawing difference at byte %d (LBA %d): got %02x want %02x", i, i/DataBytesPerSector, got[i], want[i])
			}
		}
		t.Fatal("drawing regions differ")
	}
	// The data-side preview rendered by the original CLI must match too.
	preview := filepath.Join(dir, "isoview.png")
	if err := PreviewDataTrack(context.Background(), output, preview, parameters, 128, 48); err != nil {
		t.Fatal(err)
	}
	comparePngToGolden(t, preview, "isoview.png")
}
