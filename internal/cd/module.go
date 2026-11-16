package cd

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"disc-image-studio/internal/core"
)

// ModuleID is the CD module identifier.
const ModuleID = "cd-da"

var geometryOptions = []core.DiscOptionDefinition{
	core.Opt("r0", "Inner radius", core.OptionDecimal, false, "24.5", "mm"),
	core.Opt("r1", "Outer radius", core.OptionDecimal, false, "56.8", "mm"),
	core.Opt("sectors", "Geometry sectors", core.OptionInteger, false, "359849", ""),
	core.Opt("usable-sectors", "Output sectors within the geometry", core.OptionInteger, false, "", ""),
	core.Opt("velocity", "Linear velocity", core.OptionDecimal, false, "1200", "mm/s"),
	core.Opt("theta0", "Start angle", core.OptionDecimal, false, "0", "degrees"),
	core.Opt("outer", "Image outer radius", core.OptionDecimal, false, "57.5", "mm"),
	core.Opt("pitch-linear", "Relative linear pitch", core.OptionDecimal, false, "0", ""),
	core.Opt("pitch-quadratic", "Relative quadratic pitch", core.OptionDecimal, false, "0", ""),
	core.Opt("pitch-cubic", "Relative cubic pitch", core.OptionDecimal, false, "0", ""),
}

var measuredGeometryOptions = []core.DiscOptionDefinition{
	core.Opt("actual-r0", "Measured inner radius", core.OptionDecimal, false, "24.3", "mm"),
	core.Opt("actual-r1", "Measured outer radius", core.OptionDecimal, false, "56.6", "mm"),
	core.Opt("actual-pitch-linear", "Measured relative linear pitch", core.OptionDecimal, false, "0", ""),
	core.Opt("actual-pitch-quadratic", "Measured relative quadratic pitch", core.OptionDecimal, false, "0", ""),
	core.Opt("actual-pitch-cubic", "Measured relative cubic pitch", core.OptionDecimal, false, "0", ""),
}

var toneOptions = []core.DiscOptionDefinition{
	core.Opt("tone-dither", "Dither strength", core.OptionDecimal, false, "0", "0..1"),
	core.Opt("tone-levels", "Palette levels", core.OptionInteger, false, "2", "2 or 4"),
	core.Opt("tone-brightness", "Tone brightness", core.OptionDecimal, false, "0", "-64..64"),
	core.Opt("tone-gamma", "sRGB linearization", core.OptionBoolean, false, "false", ""),
}

var moduleDescriptor = core.DiscModuleDescriptor{
	ID:                   ModuleID,
	DisplayName:          "CD",
	Description:          "CD audio tracks or Mode 1 ISO9660/Joliet data images with CLV geometry previews.",
	SupportsCancellation: true,
	Commands: []core.DiscCommandDescriptor{
		{
			Name:         "cd-generate",
			DisplayName:  "Generate CD audio or data image",
			Description:  "Without files, writes WAV/CUE or RAW audio; with --data-dir, writes a Mode 1 ISO with inner/outer-ring files.",
			Capabilities: core.CapRawTrackGeneration | core.CapDataImageGeneration | core.CapHybridFilesystem,
			Options: joinOptions(
				[]core.DiscOptionDefinition{
					core.Opt("input", "Source image", core.OptionInputFile, true, "", ""),
					core.Opt("output", "Audio track (.wav/.raw) or data image (.iso)", core.OptionOutputFile, true, "", ""),
					core.Opt("data-dir", "Files to include; omitted for audio", core.OptionInputFolder, false, "", ""),
					core.Opt("volume-label", "Data volume label", core.OptionString, false, "CD_IMAGE", ""),
					core.Opt("file-position", "File position", core.OptionChoice, false, "inner", "", "inner", "outer"),
				},
				geometryOptions,
				[]core.DiscOptionDefinition{
					core.Opt("interleave", "CD-DA delay interleave", core.OptionBoolean, false, "true", ""),
					core.Opt("cue", "Write a CUE sheet next to the WAV track", core.OptionBoolean, false, "true", ""),
				},
				toneOptions,
			),
		},
		{
			Name:         "cd-preview-warp",
			DisplayName:  "Preview CD geometry",
			Description:  "Projects generated geometry onto measured disc geometry.",
			Capabilities: core.CapGeometryPreview,
			Options: joinOptions(
				[]core.DiscOptionDefinition{
					core.Opt("input", "Source image", core.OptionInputFile, true, "", ""),
					core.Opt("output", "Preview PNG", core.OptionOutputFile, true, "", ""),
				},
				geometryOptions,
				measuredGeometryOptions,
				toneOptions,
				[]core.DiscOptionDefinition{
					core.Opt("data-dir", "Data files for drawing-range preview", core.OptionInputFolder, false, "", ""),
					core.Opt("volume-label", "Data volume label", core.OptionString, false, "CD_IMAGE", ""),
					core.Opt("file-position", "File position", core.OptionChoice, false, "inner", "", "inner", "outer"),
					core.Opt("size", "Preview size", core.OptionInteger, false, "1600", "px"),
					core.Opt("samples-per-sector", "Samples per sector", core.OptionInteger, false, "16", ""),
				},
			),
		},
		{
			Name:         "cd-preview-track",
			DisplayName:  "Preview CD audio or data image",
			Description:  "Renders WAV/RAW audio or a Mode 1 ISO with measured geometry.",
			Capabilities: core.CapEncodedOutputPreview,
			Options: joinOptions(
				[]core.DiscOptionDefinition{
					core.Opt("track", "WAV/RAW track or Mode 1 ISO", core.OptionInputFile, true, "", ""),
					core.Opt("output", "Preview PNG", core.OptionOutputFile, true, "", ""),
				},
				geometryOptions,
				[]core.DiscOptionDefinition{
					core.Opt("size", "Preview size", core.OptionInteger, false, "1600", "px"),
					core.Opt("byte-step", "Byte sampling interval", core.OptionInteger, false, "48", ""),
					core.Opt("deinterleave", "Undo CD-DA delay interleave", core.OptionBoolean, false, "true", ""),
				},
			),
		},
	},
}

func joinOptions(groups ...[]core.DiscOptionDefinition) []core.DiscOptionDefinition {
	var result []core.DiscOptionDefinition
	for _, group := range groups {
		result = append(result, group...)
	}
	return result
}

// Module is the CD module.
type Module struct{}

// NewModule creates the CD module.
func NewModule() *Module { return &Module{} }

// Descriptor returns the module descriptor.
func (m *Module) Descriptor() core.DiscModuleDescriptor { return moduleDescriptor }

// Execute runs one CD command.
func (m *Module) Execute(ctx context.Context, req core.DiscJobRequest, progress core.ProgressFunc) (core.DiscJobResult, error) {
	if err := ctx.Err(); err != nil {
		return core.DiscJobResult{}, err
	}
	options, err := core.ParseOptions(req.Args)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	switch req.Command {
	case "cd-generate":
		return m.generate(ctx, req, options, progress)
	case "cd-preview-warp":
		return m.previewWarp(ctx, req, options, progress)
	case "cd-preview-track":
		return m.previewTrack(ctx, req, options, progress)
	default:
		return core.DiscJobResult{}, fmt.Errorf("CD module does not support command '%s'. (Parameter 'request')", req.Command)
	}
}

func (m *Module) generate(ctx context.Context, req core.DiscJobRequest, options core.Options, progress core.ProgressFunc) (core.DiscJobResult, error) {
	input, err := options.Require("input")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	output, err := options.Require("output")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	parameters, err := readParameters(options, nil)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	tone, err := readTone(options)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	interleave, err := options.GetBoolean("interleave", true)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	cue, err := options.GetBoolean("cue", true)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	filesAtOuterRing, err := readFilePosition(options)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	result, err := GenerateImage(ctx, ImageRequest{
		SourceImage:      input,
		OutputPath:       output,
		Parameters:       parameters,
		Tone:             tone,
		Interleave:       interleave,
		WriteCue:         cue,
		DataDirectory:    options.GetString("data-dir", ""),
		VolumeLabel:      options.GetString("volume-label", "CD_IMAGE"),
		FilesAtOuterRing: filesAtOuterRing,
	}, progress)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	return core.DiscJobResult{ModuleID: ModuleID, Command: req.Command, ExitCode: 0, Summary: result.Summary, OutputPath: result.OutputPath}, nil
}

func (m *Module) previewWarp(ctx context.Context, req core.DiscJobRequest, options core.Options, progress core.ProgressFunc) (core.DiscJobResult, error) {
	outputRaw, err := options.Require("output")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	output, err := filepath.Abs(outputRaw)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	input, err := options.Require("input")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	if progress != nil {
		progress(core.DiscJobProgress{ModuleID: ModuleID, Stage: "preview", Message: "Rendering CD geometry preview…"})
	}
	generated, err := readParameters(options, nil)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	dataDirectory := strings.TrimSpace(options.GetString("data-dir", ""))
	filesAtOuterRing, err := readFilePosition(options)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	tone, err := readTone(options)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	size, err := options.GetInt("size", 1600)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	samplesPerSector, err := options.GetInt("samples-per-sector", 16)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	measured, err := readMeasuredParameters(options, generated)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	if dataDirectory != "" {
		plan, err := PrepareDataLayout(dataDirectory, generated, options.GetString("volume-label", "CD_IMAGE"), filesAtOuterRing)
		if err != nil {
			return core.DiscJobResult{}, err
		}
		if err := PreviewDataWarp(ctx, input, output, generated, measured, size, samplesPerSector, tone, plan); err != nil {
			return core.DiscJobResult{}, err
		}
		return core.DiscJobResult{ModuleID: ModuleID, Command: req.Command, ExitCode: 0,
			Summary: fmt.Sprintf("CD data geometry preview complete: output=%s", output), OutputPath: output}, nil
	}
	if err := PreviewWarp(ctx, input, output, generated, measured, size, samplesPerSector, tone); err != nil {
		return core.DiscJobResult{}, err
	}
	return core.DiscJobResult{ModuleID: ModuleID, Command: req.Command, ExitCode: 0,
		Summary: fmt.Sprintf("CD geometry preview complete: output=%s", output), OutputPath: output}, nil
}

func (m *Module) previewTrack(ctx context.Context, req core.DiscJobRequest, options core.Options, progress core.ProgressFunc) (core.DiscJobResult, error) {
	outputRaw, err := options.Require("output")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	output, err := filepath.Abs(outputRaw)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	if progress != nil {
		progress(core.DiscJobProgress{ModuleID: ModuleID, Stage: "preview", Message: "Rendering CD track preview…"})
	}
	track, err := options.Require("track")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	size, err := options.GetInt("size", 1600)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	byteStep, err := options.GetInt("byte-step", 48)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	if strings.EqualFold(filepath.Ext(track), ".iso") {
		metadata := TryLoadDataImageMetadata(track)
		var fallback *DiscParameters
		if metadata != nil {
			fallback = metadata.Parameters
		}
		parameters, err := readParameters(options, fallback)
		if err != nil {
			return core.DiscJobResult{}, err
		}
		if err := PreviewDataTrack(ctx, track, output, parameters, size, byteStep); err != nil {
			return core.DiscJobResult{}, err
		}
		return core.DiscJobResult{ModuleID: ModuleID, Command: req.Command, ExitCode: 0,
			Summary: fmt.Sprintf("CD data readback preview complete: output=%s", output), OutputPath: output}, nil
	}
	deinterleave, err := options.GetBoolean("deinterleave", true)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	var fallback *DiscParameters
	if metadata := TryLoadTrackMetadata(track); metadata != nil {
		if parameters, err := metadata.CreateParameters(); err == nil {
			fallback = parameters
		}
	}
	parameters, err := readParameters(options, fallback)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	if err := PreviewTrack(ctx, track, output, parameters, size, byteStep, deinterleave); err != nil {
		return core.DiscJobResult{}, err
	}
	return core.DiscJobResult{ModuleID: ModuleID, Command: req.Command, ExitCode: 0,
		Summary: fmt.Sprintf("CD track preview complete: output=%s", output), OutputPath: output}, nil
}

func readParameters(options core.Options, fallback *DiscParameters) (*DiscParameters, error) {
	fallbackStartAngle := 0.0
	fallbackR0 := 24.5
	fallbackR1 := 56.8
	fallbackSectors := int64(359849)
	fallbackVelocity := StandardLinearVelocityMmPerSecond
	fallbackOuter := StandardImageOuterRadiusMm
	fallbackLinear := 0.0
	fallbackQuadratic := 0.0
	fallbackCubic := 0.0
	var fallbackUsable int64
	var fallbackSourceOuter float64
	if fallback != nil {
		fallbackStartAngle = fallback.StartAngleRadians * 180.0 / math.Pi
		fallbackR0 = fallback.InnerRadiusMm
		fallbackR1 = fallback.OuterRadiusMm
		fallbackSectors = fallback.Sectors
		fallbackVelocity = fallback.LinearVelocityMmPerSecond
		fallbackOuter = fallback.ImageOuterRadiusMm
		fallbackLinear = fallback.PitchLinear
		fallbackQuadratic = fallback.PitchQuadratic
		fallbackCubic = fallback.PitchCubic
		fallbackUsable = fallback.UsableSectors
		fallbackSourceOuter = fallback.SourceImageOuterRadiusMm
	}

	startAngleDegrees, err := options.GetDouble("theta0", fallbackStartAngle)
	if err != nil {
		return nil, err
	}
	r0, err := options.GetDouble("r0", fallbackR0)
	if err != nil {
		return nil, err
	}
	r1, err := options.GetDouble("r1", fallbackR1)
	if err != nil {
		return nil, err
	}
	sectors, err := options.GetLong("sectors", fallbackSectors)
	if err != nil {
		return nil, err
	}
	velocity, err := options.GetDouble("velocity", fallbackVelocity)
	if err != nil {
		return nil, err
	}
	outer, err := options.GetDouble("outer", fallbackOuter)
	if err != nil {
		return nil, err
	}
	linear, err := options.GetDouble("pitch-linear", fallbackLinear)
	if err != nil {
		return nil, err
	}
	quadratic, err := options.GetDouble("pitch-quadratic", fallbackQuadratic)
	if err != nil {
		return nil, err
	}
	cubic, err := options.GetDouble("pitch-cubic", fallbackCubic)
	if err != nil {
		return nil, err
	}

	var usable int64
	hasUsable := false
	switch {
	case options.Contains("usable-sectors"):
		usable, err = options.GetLong("usable-sectors", 0)
		if err != nil {
			return nil, err
		}
		hasUsable = true
	case options.Contains("sectors"):
		// null
	case fallback != nil && fallback.HasUsableSectors:
		usable = fallbackUsable
		hasUsable = true
	}
	var sourceOuter float64
	hasSourceOuter := false
	if !options.Contains("sectors") && !options.Contains("usable-sectors") && fallback != nil && fallback.HasSourceImageOuterRadius {
		sourceOuter = fallbackSourceOuter
		hasSourceOuter = true
	}

	return NewDiscParameters(r0, r1, sectors, velocity, startAngleDegrees*math.Pi/180.0, outer,
		linear, quadratic, cubic, usable, hasUsable, sourceOuter, hasSourceOuter)
}

func readFilePosition(options core.Options) (bool, error) {
	switch strings.ToLower(options.GetString("file-position", "inner")) {
	case "inner":
		return false, nil
	case "outer":
		return true, nil
	default:
		return false, fmt.Errorf("--file-position must be inner or outer")
	}
}

func readTone(options core.Options) (core.ToneProfile, error) {
	dither, err := options.GetDouble("tone-dither", 0.0)
	if err != nil {
		return core.ToneProfile{}, err
	}
	levels, err := options.GetInt("tone-levels", core.TwoLevels)
	if err != nil {
		return core.ToneProfile{}, err
	}
	brightness, err := options.GetDouble("tone-brightness", 0.0)
	if err != nil {
		return core.ToneProfile{}, err
	}
	gamma, err := options.GetBoolean("tone-gamma", false)
	if err != nil {
		return core.ToneProfile{}, err
	}
	tone := core.ToneProfile{DitherStrength: dither, Levels: levels, Brightness: brightness, SrgbLinearization: gamma}
	if err := tone.Validate("tone"); err != nil {
		return core.ToneProfile{}, err
	}
	return tone, nil
}

func readMeasuredParameters(options core.Options, generated *DiscParameters) (*DiscParameters, error) {
	r0, err := options.GetDouble("actual-r0", 24.3)
	if err != nil {
		return nil, err
	}
	r1, err := options.GetDouble("actual-r1", 56.6)
	if err != nil {
		return nil, err
	}
	linear, err := options.GetDouble("actual-pitch-linear", 0)
	if err != nil {
		return nil, err
	}
	quadratic, err := options.GetDouble("actual-pitch-quadratic", 0)
	if err != nil {
		return nil, err
	}
	cubic, err := options.GetDouble("actual-pitch-cubic", 0)
	if err != nil {
		return nil, err
	}
	return NewDiscParameters(r0, r1, generated.Sectors, generated.LinearVelocityMmPerSecond,
		generated.StartAngleRadians, generated.ImageOuterRadiusMm, linear, quadratic, cubic,
		generated.UsableSectors, generated.HasUsableSectors,
		generated.SourceImageOuterRadiusMm, generated.HasSourceImageOuterRadius)
}
