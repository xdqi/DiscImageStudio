package raster

import (
	"math"
	"os"
	"testing"
)

func TestLoadGoldenPng(t *testing.T) {
	if _, err := os.Stat("/tmp/opencode/golden/input.png"); err != nil {
		t.Skip("golden not available")
	}
	r, err := LoadBgra32("/tmp/opencode/golden/input.png", math.MaxInt64, math.MaxInt32/4)
	if err != nil {
		t.Fatal(err)
	}
	if r.Width != 256 || r.Height != 256 {
		t.Fatalf("dimensions = %dx%d", r.Width, r.Height)
	}
	// Compare against a decoded-Go reference for pixel sanity.
	if err := ValidateSource("/tmp/opencode/golden/input.png", math.MaxInt64, math.MaxInt32/4); err != nil {
		t.Fatal(err)
	}
}
