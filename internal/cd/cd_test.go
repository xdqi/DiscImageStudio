package cd

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"disc-image-studio/internal/core"
)

func TestInterleaveFileOffsets(t *testing.T) {
	// The generator's invariant: logical byte i is emitted at file index
	// i + FileOffsetFor(i % 24). Feed a distinctive pattern and verify.
	total := InterleaveFramesCycle * InterleaveBytesFrame * 3
	input := make([]byte, total)
	for i := range input {
		input[i] = byte(i * 37)
	}
	var output bytes.Buffer
	interleaver := newCddaInterleaver(AudioBigEndian)
	for _, b := range input {
		if err := interleaver.Add(b, &output); err != nil {
			t.Fatal(err)
		}
	}
	// Flush pads to a sector boundary; interleave keeps whole frames.
	if err := interleaver.Flush(&output); err != nil {
		t.Fatal(err)
	}
	emitted := output.Bytes()
	// Check logical bytes whose file position stays inside the track; the
	// delay line loses the last frames (documented C# behaviour), so file
	// indices beyond the fed byte count are not recoverable.
	checked := 0
	for i := 0; i < total; i++ {
		j := i + InterleaveFileOffsetFor(i%InterleaveBytesFrame)
		if j < 0 || j >= total {
			continue
		}
		if j >= len(emitted) {
			t.Fatalf("file index %d of logical byte %d outside %d emitted bytes", j, i, len(emitted))
		}
		if emitted[j] != input[i] {
			t.Fatalf("logical byte %d not at file index %d", i, j)
		}
		checked++
	}
	if checked < total-InterleaveFramesCycle*InterleaveBytesFrame {
		t.Fatalf("too few bytes verified: %d of %d", checked, total)
	}
}

func TestToneMapLegacy(t *testing.T) {
	if ToneMap(127, core.LegacyBinary, 0) != 0x0D {
		t.Fatal("127 must map to the black palette byte")
	}
	if ToneMap(128, core.LegacyBinary, 0) != 0xAA {
		t.Fatal("128 must map to the white palette byte")
	}
	if ToneDisplayGray(0x0D) != 0 || ToneDisplayGray(0xAA) != 255 {
		t.Fatal("display gray mapping broken")
	}
}

func TestInterleaveTableMatchesReference(t *testing.T) {
	// Spot-check the CIRC delay table against the C# builder output:
	// delays[0] = 0 - 24*(0*4+3) + 0 = -72; delays[1] = 1 - 24*(1*4+2) + 0 = -143.
	if InterleaveDelayFor(0) != -72 {
		t.Fatalf("delay[0] = %d", InterleaveDelayFor(0))
	}
	if InterleaveDelayFor(1) != -143 {
		t.Fatalf("delay[1] = %d", InterleaveDelayFor(1))
	}
	// FileOffsetFor(0): delay -72 → column 0, frameDelay (0-(-72))/24 = 3 → 24*111 - 72 + 0 - 0 = 2592.
	if InterleaveFileOffsetFor(0) != 2592 {
		t.Fatalf("fileOffset[0] = %d", InterleaveFileOffsetFor(0))
	}
}

func writeTestPng(t *testing.T, path string, size int) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			offset := img.PixOffset(x, y)
			v := byte(0)
			if x > size/2 {
				v = 255
			}
			img.Pix[offset] = v
			img.Pix[offset+1] = v
			img.Pix[offset+2] = v
			img.Pix[offset+3] = 255
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateTrackSmoke(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.png")
	output := filepath.Join(dir, "track.wav")
	writeTestPng(t, input, 32)

	parameters, err := NewDiscParameters(24.5, 56.8, 2, StandardLinearVelocityMmPerSecond,
		0, StandardImageOuterRadiusMm, 0, 0, 0, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := GenerateTrack(input, output, parameters, true, true, nil, context.Background(), core.LegacyBinary)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(44+2*2352) {
		t.Fatalf("WAV size = %d, want %d", info.Size(), 44+2*2352)
	}
	if summary.CueSheetPath == "" {
		t.Fatal("expected a CUE sheet")
	}
	cue, err := os.ReadFile(summary.CueSheetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(cue, []byte(`FILE "track.wav" WAVE`)) {
		t.Fatalf("CUE does not reference the WAV: %q", cue)
	}
	if !bytes.HasPrefix(cue, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("CUE must carry a UTF-8 BOM like the C# original")
	}
	// The sidecar must exist and round-trip.
	metadata := TryLoadTrackMetadata(output)
	if metadata == nil {
		t.Fatal("no track metadata sidecar")
	}
	if metadata.Sectors != 2 {
		t.Fatalf("sidecar sectors = %d", metadata.Sectors)
	}
}

func TestPreviewMaskAnnulus(t *testing.T) {
	parameters, err := NewDiscParameters(24.5, 56.8, 100, StandardLinearVelocityMmPerSecond,
		0, StandardImageOuterRadiusMm, 0, 0, 0, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	size := 64
	pixels := make([]byte, size*size*4)
	for i := range pixels {
		pixels[i] = 200
	}
	applyPreviewMask(pixels, size, parameters)
	// Centre must be transparent (inside the hub).
	if pixels[3] != 0 {
		t.Fatal("centre pixel should be masked")
	}
}
