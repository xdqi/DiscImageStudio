package cd

import (
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"disc-image-studio/internal/core"
)

// Golden outputs produced by the original .NET CLI; the test is skipped when
// they are absent.
const goldenDir = "/tmp/opencode/golden"

func goldenAvailable(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(goldenDir); err != nil {
		t.Skip("golden outputs not available")
	}
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGoldenTrackWav(t *testing.T) {
	goldenAvailable(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "input.png")
	if err := os.WriteFile(input, readGolden(t, "input.png"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "track32.wav")
	parameters, err := NewDiscParameters(24.5, 56.8, 32, StandardLinearVelocityMmPerSecond,
		0, StandardImageOuterRadiusMm, 0, 0, 0, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateTrack(input, output, parameters, true, true, nil, context.Background(), core.LegacyBinary); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := readGolden(t, "track32.wav")
	if !bytes.Equal(got, want) {
		for i := 0; i < len(got) && i < len(want); i++ {
			if got[i] != want[i] {
				t.Fatalf("first difference at byte %d: got %02x want %02x (lengths %d/%d)", i, got[i], want[i], len(got), len(want))
			}
		}
		t.Fatalf("lengths differ: %d vs %d", len(got), len(want))
	}

	// The CUE sheet must be byte-identical too.
	gotCue, err := os.ReadFile(output[:len(output)-4] + ".cue")
	if err != nil {
		t.Fatal(err)
	}
	wantCue := readGolden(t, "track32.cue")
	// The CUE references the WAV base name, which differs (track32.wav vs the
	// golden directory's track32.wav) — same name here, contents must match.
	if !bytes.Equal(gotCue, wantCue) {
		t.Fatalf("CUE differs:\n%q\nwant:\n%q", gotCue, wantCue)
	}
}

func TestGoldenTrackRawNoInterleave(t *testing.T) {
	goldenAvailable(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "input.png")
	if err := os.WriteFile(input, readGolden(t, "input.png"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "track32.raw")
	parameters, err := NewDiscParameters(24.5, 56.8, 32, StandardLinearVelocityMmPerSecond,
		0, StandardImageOuterRadiusMm, 0, 0, 0, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateTrack(input, output, parameters, false, true, nil, context.Background(), core.LegacyBinary); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if want := readGolden(t, "track32.raw"); !bytes.Equal(got, want) {
		t.Fatalf("RAW output differs (%d vs %d bytes)", len(got), len(want))
	}
}

func TestGoldenTrackTone4Pitch(t *testing.T) {
	goldenAvailable(t)
	dir := t.TempDir()
	input := filepath.Join(dir, "input.png")
	if err := os.WriteFile(input, readGolden(t, "input.png"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "track4.wav")
	parameters, err := NewDiscParameters(24.5, 56.8, 32, StandardLinearVelocityMmPerSecond,
		40*math.Pi/180.0, StandardImageOuterRadiusMm, 0.2, -0.1, 0.05, 0, false, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	tone := core.ToneProfile{DitherStrength: 0.75, Levels: 4, Brightness: 0, SrgbLinearization: false}
	if _, err := GenerateTrack(input, output, parameters, true, true, nil, context.Background(), tone); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if want := readGolden(t, "track4.wav"); !bytes.Equal(got, want) {
		for i := 0; i < len(got) && i < len(want); i++ {
			if got[i] != want[i] {
				t.Fatalf("first difference at byte %d: got %02x want %02x", i, got[i], want[i])
			}
		}
		t.Fatalf("lengths differ: %d vs %d", len(got), len(want))
	}
}
