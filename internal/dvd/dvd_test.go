package dvd

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"disc-image-studio/internal/raster"
)

// TestSelftest runs the ported regression suite through the CLI entry point.
func TestSelftest(t *testing.T) {
	if code := RunCommand([]string{"selftest"}); code != 0 {
		t.Fatalf("selftest exit code %d", code)
	}
}

// TestSolveSmoke writes a small PNG, runs the fast dispersion solve and checks
// the ISO size.
func TestSolveSmoke(t *testing.T) {
	directory := t.TempDir()
	pngPath := filepath.Join(directory, "in.png")
	isoPath := filepath.Join(directory, "out.iso")

	const size = 64
	pixels := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			offset := (y*size + x) * 4
			value := byte(255)
			if (x+y)%2 == 0 {
				value = 0
			}
			pixels[offset] = value
			pixels[offset+1] = value
			pixels[offset+2] = value
			pixels[offset+3] = 255
		}
	}
	if err := raster.SavePng(pngPath, size, size, pixels, false); err != nil {
		t.Fatal(err)
	}

	code := RunCommand([]string{
		"solve",
		"--image", pngPath,
		"--iso-output", isoPath,
		"--total-sectors", "160",
		"--fill-sectors", "160",
		"--algorithm", "dispersion",
		"--iterations-per-block", "1",
		"--fast-output", "true",
		"--spiral-direction", "cw",
	})
	if code != 0 {
		t.Fatalf("solve exit code %d", code)
	}
	info, err := os.Stat(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 160*2048 {
		t.Fatalf("ISO size %d, want %d", info.Size(), 160*2048)
	}
}

const goldenDir = "/tmp/opencode/golden"

func goldenAvailable(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(goldenDir); err != nil {
		t.Skip("golden outputs not available")
	}
}

// TestGoldenSolve compares against the .NET golden outputs of
// solve --image input.png --iso-output dvd.iso --total-sectors 160
// --fill-sectors 160 --algorithm dispersion --iterations-per-block 1
// --fast-output true --spiral-direction cw.
func TestGoldenSolve(t *testing.T) {
	goldenAvailable(t)
	directory := t.TempDir()
	isoPath := filepath.Join(directory, "dvd.iso")
	code := RunCommand([]string{
		"solve",
		"--image", filepath.Join(goldenDir, "input.png"),
		"--iso-output", isoPath,
		"--total-sectors", "160",
		"--fill-sectors", "160",
		"--algorithm", "dispersion",
		"--iterations-per-block", "1",
		"--fast-output", "true",
		"--spiral-direction", "cw",
	})
	if code != 0 {
		t.Fatalf("solve exit code %d", code)
	}
	compareFileBytes(t, filepath.Join(goldenDir, "dvd.iso"), isoPath)
	// Path-dependent and timing-dependent fields differ by construction.
	compareJsonSidecar(t, filepath.Join(goldenDir, "dvd.iso.json"), isoPath+".json",
		"sourceImage", "isoOutput", "elapsedSeconds")
}

// TestGoldenEncode compares against the golden output of
// encode --input payload.bin --output channel.bin.
func TestGoldenEncode(t *testing.T) {
	goldenAvailable(t)
	directory := t.TempDir()
	outPath := filepath.Join(directory, "channel.bin")
	code := RunCommand([]string{
		"encode",
		"--input", filepath.Join(goldenDir, "payload.bin"),
		"--output", outPath,
	})
	if code != 0 {
		t.Fatalf("encode exit code %d", code)
	}
	compareFileBytes(t, filepath.Join(goldenDir, "channel.bin"), outPath)
	compareJsonSidecar(t, filepath.Join(goldenDir, "channel.bin.json"), outPath+".json")
}

// TestGoldenCalibrate compares the calibrate command's outputs (pixels for the
// PNG, field-by-field for the JSON sidecar).
func TestGoldenCalibrate(t *testing.T) {
	goldenAvailable(t)
	directory := t.TempDir()
	outPath := filepath.Join(directory, "dvd-cal.png")
	code := RunCommand([]string{
		"calibrate",
		"--image", filepath.Join(goldenDir, "input.png"),
		"--output", outPath,
		"--total-sectors", "160",
		"--generated-inner-radius-mm", "24",
		"--generated-outer-radius-mm", "58",
		"--actual-inner-radius-mm", "23.9",
		"--actual-outer-radius-mm", "58.1",
		"--preview-size", "256",
	})
	if code != 0 {
		t.Fatalf("calibrate exit code %d", code)
	}
	comparePngPixels(t, filepath.Join(goldenDir, "dvd-cal.png"), outPath)
	compareJsonSidecar(t, filepath.Join(goldenDir, "dvd-cal.png.json"), outPath+".json",
		"sourceImage", "outputImage")
}

// TestGoldenSimulate compares the simulate command's outputs. It reads the
// golden dvd.iso and its solve sidecar.
func TestGoldenSimulate(t *testing.T) {
	goldenAvailable(t)
	directory := t.TempDir()
	outPath := filepath.Join(directory, "dvd-sim.png")
	code := RunCommand([]string{
		"simulate",
		"--iso", filepath.Join(goldenDir, "dvd.iso"),
		"--output", outPath,
		"--inner-radius-mm", "24",
		"--outer-radius-mm", "58",
		"--preview-size", "256",
	})
	if code != 0 {
		t.Fatalf("simulate exit code %d", code)
	}
	comparePngPixels(t, filepath.Join(goldenDir, "dvd-sim.png"), outPath)
	compareJsonSidecar(t, filepath.Join(goldenDir, "dvd-sim.png.json"), outPath+".json",
		"outputImage")
}

func compareFileBytes(t *testing.T, expectedPath, actualPath string) {
	t.Helper()
	expected, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(actualPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, actual) {
		t.Fatalf("%s differs from %s (%d vs %d bytes)", actualPath, expectedPath, len(actual), len(expected))
	}
}

// compareJsonSidecar compares two JSON documents field-by-field after removing
// the listed volatile top-level fields (paths, timings), and additionally
// verifies the raw key order matches.
func compareJsonSidecar(t *testing.T, expectedPath, actualPath string, volatileFields ...string) {
	t.Helper()
	expected := readJsonObject(t, expectedPath)
	actual := readJsonObject(t, actualPath)
	if !reflect.DeepEqual(expected.keys, actual.keys) {
		t.Fatalf("JSON key order differs:\nexpected %v\nactual   %v",
			expected.keys, actual.keys)
	}
	for _, field := range volatileFields {
		delete(expected.value, field)
		delete(actual.value, field)
	}
	if !jsonDeepEqual(expected.value, actual.value) {
		expectedJson, _ := json.MarshalIndent(expected.value, "", "  ")
		actualJson, _ := json.MarshalIndent(actual.value, "", "  ")
		t.Fatalf("JSON %s differs from %s:\n--- expected\n%s\n--- actual\n%s",
			actualPath, expectedPath, expectedJson, actualJson)
	}
}

type jsonObject struct {
	keys  []string
	value map[string]any
}

func readJsonObject(t *testing.T, path string) jsonObject {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return jsonObject{keys: topLevelKeys(data), value: value}
}

// topLevelKeys extracts the top-level object keys in document order with a
// minimal scanner (sufficient for these well-formed CLI sidecars).
func topLevelKeys(data []byte) []string {
	var keys []string
	depth := 0
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case '"':
			end := i + 1
			for end < len(data) && data[end] != '"' {
				if data[end] == '\\' {
					end++
				}
				end++
			}
			if end >= len(data) {
				return keys
			}
			token := data[i+1 : end]
			// A string token is a key when a ':' follows at the same depth.
			j := end + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\n' || data[j] == '\r' || data[j] == '\t') {
				j++
			}
			if j < len(data) && data[j] == ':' && depth == 1 {
				keys = append(keys, string(token))
			}
			i = end
		}
	}
	return keys
}

// jsonDeepEqual compares two JSON trees where numbers are json.Number or
// float64 from the two decoders; numbers compare as float64 bit patterns.
func jsonDeepEqual(a, b any) bool {
	a = normalizeJsonNumber(a)
	b = normalizeJsonNumber(b)
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for key, aValue := range av {
			bValue, exists := bv[key]
			if !exists || !jsonDeepEqual(aValue, bValue) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonDeepEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}

func normalizeJsonNumber(value any) any {
	if number, ok := value.(json.Number); ok {
		asFloat, err := number.Float64()
		if err == nil {
			return asFloat
		}
	}
	return value
}

func comparePngPixels(t *testing.T, expectedPath, actualPath string) {
	t.Helper()
	expected := decodePngToRgba(t, expectedPath)
	actual := decodePngToRgba(t, actualPath)
	if expected.width != actual.width || expected.height != actual.height {
		t.Fatalf("PNG sizes differ: %dx%d vs %dx%d", actual.width, actual.height, expected.width, expected.height)
	}
	if !bytes.Equal(expected.pixels, actual.pixels) {
		first := -1
		for i := range expected.pixels {
			if expected.pixels[i] != actual.pixels[i] {
				first = i
				break
			}
		}
		t.Fatalf("PNG pixels differ: %s vs %s (first difference at byte %d of %d)",
			actualPath, expectedPath, first, len(expected.pixels))
	}
}

type rgbaImage struct {
	width, height int
	pixels        []byte
}

func decodePngToRgba(t *testing.T, path string) rgbaImage {
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
	width := bounds.Dx()
	height := bounds.Dy()
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
	return rgbaImage{width: width, height: height, pixels: pixels}
}

var (
	_ = image.Rect
	_ = strings.TrimSpace
)
