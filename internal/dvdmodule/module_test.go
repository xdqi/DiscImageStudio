package dvdmodule

import (
	"strings"
	"testing"

	"disc-image-studio/internal/core"
)

func optionNames(command core.DiscCommandDescriptor) map[string]bool {
	names := make(map[string]bool)
	for _, option := range command.Options {
		names[option.Name] = true
	}
	return names
}

func findCommand(t *testing.T, name string) core.DiscCommandDescriptor {
	t.Helper()
	for _, command := range NewModule().Descriptor().Commands {
		if command.Name == name {
			return command
		}
	}
	t.Fatalf("command %s not found", name)
	return core.DiscCommandDescriptor{}
}

func TestCalibrateHelpIsComplete(t *testing.T) {
	command := findCommand(t, "calibrate")
	// The option surface the DVD solver's calibrate actually reads, per
	// DvdImageSolver Program.cs and its help text.
	for _, required := range []string{
		"image", "output", "total-sectors",
		"generated-inner-radius-mm", "generated-outer-radius-mm",
		"actual-inner-radius-mm", "actual-outer-radius-mm",
		"lba", "fill-sectors", "channel-bit-nm",
		"pitch-linear", "pitch-quadratic", "pitch-cubic",
		"actual-pitch-linear", "actual-pitch-quadratic", "actual-pitch-cubic",
		"image-threshold", "alpha-threshold", "preview-size", "samples-per-sector",
		"tone-dither", "tone-levels", "tone-brightness", "tone-gamma",
	} {
		if !optionNames(command)[required] {
			t.Fatalf("calibrate is missing --%s", required)
		}
	}
	if len(command.Notes) == 0 || !strings.Contains(command.Notes[0], "spiral-direction") {
		t.Fatal("calibrate should note the fixed spiral direction")
	}
}

func TestSimulateHelpIsComplete(t *testing.T) {
	command := findCommand(t, "simulate")
	for _, required := range []string{
		"iso", "output", "inner-radius-mm", "outer-radius-mm",
		"total-sectors", "lba", "fill-sectors", "psn-offset", "channel-bit-nm",
		"pitch-linear", "pitch-quadratic", "pitch-cubic",
		"preview-size", "samples-per-sector",
	} {
		if !optionNames(command)[required] {
			t.Fatalf("simulate is missing --%s", required)
		}
	}
}

func TestSolveHelpIsComplete(t *testing.T) {
	command := findCommand(t, "solve")
	for _, required := range []string{
		"image", "iso-output", "usage-map", "total-sectors", "total-blocks", "fill-sectors",
		"lba", "psn-offset", "data-dir", "file-position", "drawing-start-lba",
		"volume-label", "inner-radius-mm", "outer-radius-mm", "channel-bit-nm",
		"start-angle-deg", "pitch-linear", "pitch-quadratic", "pitch-cubic",
		"image-threshold", "alpha-threshold", "constraint-step",
		"tone-dither", "tone-levels", "tone-brightness", "tone-gamma",
		"read-compatibility", "fast-parallelism", "initial", "seed",
		"initial-state", "initial-dsv", "initial-level",
	} {
		if !optionNames(command)[required] {
			t.Fatalf("solve is missing --%s", required)
		}
	}
}

func TestEncodeHelpIsComplete(t *testing.T) {
	command := findCommand(t, "encode")
	for _, required := range []string{
		"input", "output", "lba", "psn-offset", "initial-state", "initial-dsv", "initial-level",
	} {
		if !optionNames(command)[required] {
			t.Fatalf("encode is missing --%s", required)
		}
	}
}
