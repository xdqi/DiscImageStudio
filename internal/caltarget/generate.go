package caltarget

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"disc-image-studio/internal/bluray"
	"disc-image-studio/internal/cd"
	"disc-image-studio/internal/core"
	"disc-image-studio/internal/dvdmodule"
)

// GenerateRequest describes one calibration-target generation.
type GenerateRequest struct {
	Params     Parameters
	Stage      Stage
	OutputPath string
	Interleave bool // CD only
}

// Generate renders the embedded/analytic target pattern and writes a burnable
// disc image through the ordinary engine for the disc kind.
func Generate(ctx context.Context, req GenerateRequest, progress core.ProgressFunc) (core.DiscImageResult, error) {
	if req.Params.Kind == DVD && req.Params.UsableSectors != 0 {
		return core.DiscImageResult{}, fmt.Errorf("DVD calibration targets do not take --usable-sectors; the full disc is the drawing area")
	}
	target, err := NewTarget(req.Params, req.Stage)
	if err != nil {
		return core.DiscImageResult{}, err
	}
	if req.Stage == StageCoarse && req.Params.Kind == CD && target.Params.UsableSectors != 0 &&
		target.Params.UsableSectors <= int64(math.Ceil(float64(target.Params.Sectors)*955.0/1000.0)) {
		return core.DiscImageResult{}, fmt.Errorf("the CD usable sector count cannot contain the outermost coarse band edge plus a light sector; increase usable sectors or use the boundary stage")
	}

	// Render the source raster: the embedded reference PNG for the fine
	// stage, otherwise the analytic pattern on the encoding grid.
	var pngBytes []byte
	if req.Stage == StageFine {
		pngBytes = ReferencePatternPng()
	} else {
		pixels, size, err := RenderEncodingImage(target)
		if err != nil {
			return core.DiscImageResult{}, err
		}
		pngBytes, err = EncodePng(pixels, size)
		if err != nil {
			return core.DiscImageResult{}, err
		}
	}

	directory, err := os.MkdirTemp("", "disc-image-studio-calibration-*")
	if err != nil {
		return core.DiscImageResult{}, err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	rasterPath := filepath.Join(directory, "calibration-target.png")
	if err := os.WriteFile(rasterPath, pngBytes, 0o644); err != nil {
		return core.DiscImageResult{}, err
	}

	// Coarse targets are constant-pitch by definition.
	pitchLinear, pitchQuadratic, pitchCubic := req.Params.PitchLinear, req.Params.PitchQuadratic, req.Params.PitchCubic
	if req.Stage == StageCoarse {
		pitchLinear, pitchQuadratic, pitchCubic = 0, 0, 0
	}
	samplingOuter := SamplingOuterRadiusMm(target)

	format := func(v float64) string {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}

	dispatch := func(module core.OpticalDiscModule, command string, args []string) (core.DiscImageResult, error) {
		result, err := module.Execute(ctx, core.DiscJobRequest{Command: command, Args: args}, progress)
		if err != nil {
			return core.DiscImageResult{}, err
		}
		if result.ExitCode != 0 {
			return core.DiscImageResult{}, fmt.Errorf("%s", firstLine(result.Summary))
		}
		return core.DiscImageResult{OutputPath: result.OutputPath, Summary: result.Summary}, nil
	}

	switch req.Params.Kind {
	case CD:
		args := []string{
			"--input", rasterPath,
			"--output", req.OutputPath,
			"--sectors", fmt.Sprint(req.Params.Sectors),
			"--r0", format(req.Params.InnerRadiusMm),
			"--r1", format(req.Params.OuterRadiusMm),
			"--outer", format(samplingOuter),
			"--velocity", format(req.Params.LinearDensity),
			"--pitch-linear", format(pitchLinear),
			"--pitch-quadratic", format(pitchQuadratic),
			"--pitch-cubic", format(pitchCubic),
			"--interleave", fmt.Sprint(req.Interleave),
			"--cue", "true",
		}
		if req.Params.UsableSectors != 0 {
			args = append(args, "--usable-sectors", fmt.Sprint(req.Params.UsableSectors))
		}
		return dispatch(cd.NewModule(), "cd-generate", args)
	case DVD:
		args := []string{
			"--image", rasterPath,
			"--iso-output", req.OutputPath,
			"--total-sectors", fmt.Sprint(req.Params.Sectors),
			"--fill-sectors", fmt.Sprint(req.Params.OutputSectors()),
			"--inner-radius-mm", format(req.Params.InnerRadiusMm),
			"--outer-radius-mm", format(req.Params.OuterRadiusMm),
			"--channel-bit-nm", format(req.Params.LinearDensity),
			"--pitch-linear", format(pitchLinear),
			"--pitch-quadratic", format(pitchQuadratic),
			"--pitch-cubic", format(pitchCubic),
		}
		return dispatch(dvdmodule.NewModule(), "solve", args)
	case BluRay:
		args := []string{
			"--input", rasterPath,
			"--output", req.OutputPath,
			"--total-sectors", fmt.Sprint(req.Params.Sectors),
			"--r0", format(req.Params.InnerRadiusMm),
			"--r1", format(req.Params.OuterRadiusMm),
			"--pitch-linear", format(pitchLinear),
			"--pitch-quadratic", format(pitchQuadratic),
			"--pitch-cubic", format(pitchCubic),
		}
		if req.Params.UsableSectors != 0 {
			args = append(args, "--usable-sectors", fmt.Sprint(req.Params.UsableSectors))
		}
		return dispatch(bluray.NewModule(bluray.LoadGray8), "bd-generate", args)
	}
	return core.DiscImageResult{}, fmt.Errorf("unknown disc kind")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "the engine reported a failure"
	}
	return s
}
