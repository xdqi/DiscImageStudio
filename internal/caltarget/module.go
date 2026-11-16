package caltarget

import (
	"context"
	"fmt"
	"strings"

	"disc-image-studio/internal/core"
	"disc-image-studio/internal/webui"
)

// ModuleID identifies the calibration module.
const ModuleID = "calibration"

var moduleDescriptor = core.DiscModuleDescriptor{
	ID:                   ModuleID,
	DisplayName:          "Calibration",
	Description:          "Calibration target generation and the browser-based measurement tool.",
	SupportsCancellation: true,
	Commands: []core.DiscCommandDescriptor{
		{
			Name:         "calibration-target",
			DisplayName:  "Generate calibration target",
			Description:  "Writes a burnable calibration target image (CD: .wav/.raw, DVD/BD: .iso) with the embedded test pattern.",
			Capabilities: core.CapDataImageGeneration,
			Options: []core.DiscOptionDefinition{
				core.Opt("kind", "Disc kind", core.OptionChoice, true, "", "", "cd", "dvd", "bd"),
				core.Opt("stage", "Calibration stage", core.OptionChoice, true, "", "", "coarse", "boundary", "fine"),
				core.Opt("output", "Target image output (.wav/.raw for CD, .iso for DVD/BD)", core.OptionOutputFile, true, "", ""),
				core.Opt("sectors", "Total sectors", core.OptionInteger, true, "", ""),
				core.Opt("usable-sectors", "Written sectors within the geometry (CD/BD only)", core.OptionInteger, false, "", ""),
				core.Opt("r0", "Inner radius", core.OptionDecimal, false, "24", "mm"),
				core.Opt("r1", "Outer radius", core.OptionDecimal, false, "58", "mm"),
				core.Opt("pitch-linear", "Relative linear pitch coefficient (ignored for coarse)", core.OptionDecimal, false, "0", ""),
				core.Opt("pitch-quadratic", "Relative quadratic pitch coefficient (ignored for coarse)", core.OptionDecimal, false, "0", ""),
				core.Opt("pitch-cubic", "Relative cubic pitch coefficient (ignored for coarse)", core.OptionDecimal, false, "0", ""),
				core.Opt("velocity", "CD linear velocity", core.OptionDecimal, false, "1200", "mm/s"),
				core.Opt("channel-bit-nm", "DVD channel bit length", core.OptionDecimal, false, "133.3", "nm"),
				core.Opt("interleave", "CD-DA delay interleave", core.OptionBoolean, false, "true", ""),
			},
			Notes: []string{
				"The pattern is embedded; no --input image is needed. Burn the output, photograph the disc, then measure it with the browser tool (calibrate-ui).",
			},
		},
		{
			Name:         "calibrate-ui",
			DisplayName:  "Start the calibration web UI",
			Description:  "Serves the browser-based calibration measurement tool on a local HTTP server.",
			Capabilities: core.CapGeometryPreview,
			Options: []core.DiscOptionDefinition{
				core.Opt("listen", "Listen address", core.OptionString, false, "127.0.0.1:8484", ""),
				core.Opt("open", "Open the page in the default browser", core.OptionBoolean, false, "false", ""),
			},
		},
	},
}

// Module is the calibration module.
type Module struct{}

// NewModule creates the calibration module.
func NewModule() *Module { return &Module{} }

// Descriptor returns the module descriptor.
func (m *Module) Descriptor() core.DiscModuleDescriptor { return moduleDescriptor }

// Execute runs one calibration command.
func (m *Module) Execute(ctx context.Context, req core.DiscJobRequest, progress core.ProgressFunc) (core.DiscJobResult, error) {
	if err := ctx.Err(); err != nil {
		return core.DiscJobResult{}, err
	}
	options, err := core.ParseOptions(req.Args)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	switch req.Command {
	case "calibration-target":
		return m.generateTarget(ctx, req, options, progress)
	case "calibrate-ui":
		return m.serveUI(ctx, req, options)
	default:
		return core.DiscJobResult{}, fmt.Errorf("calibration module does not support command '%s'. (Parameter 'request')", req.Command)
	}
}

func (m *Module) generateTarget(ctx context.Context, req core.DiscJobRequest, options core.Options, progress core.ProgressFunc) (core.DiscJobResult, error) {
	kindText, err := options.Require("kind")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	stageText, err := options.Require("stage")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	output, err := options.Require("output")
	if err != nil {
		return core.DiscJobResult{}, err
	}

	var kind DiscKind
	switch strings.ToLower(kindText) {
	case "cd":
		kind = CD
	case "dvd":
		kind = DVD
	case "bd":
		kind = BluRay
	default:
		return core.DiscJobResult{}, fmt.Errorf("--kind must be cd, dvd or bd")
	}
	var stage Stage
	switch strings.ToLower(stageText) {
	case "coarse":
		stage = StageCoarse
	case "boundary":
		stage = StageBoundary
	case "fine":
		stage = StageFine
	default:
		return core.DiscJobResult{}, fmt.Errorf("--stage must be coarse, boundary or fine")
	}

	sectors, err := options.GetLong("sectors", 0)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	r0, err := options.GetDouble("r0", 24)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	r1, err := options.GetDouble("r1", 58)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	pitchLinear, err := options.GetDouble("pitch-linear", 0)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	pitchQuadratic, err := options.GetDouble("pitch-quadratic", 0)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	pitchCubic, err := options.GetDouble("pitch-cubic", 0)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	velocity, err := options.GetDouble("velocity", CdStandardLinearVelocityMmPerSec)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	channelBitNm, err := options.GetDouble("channel-bit-nm", DvdStandardChannelBitLengthNm)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	interleave, err := options.GetBoolean("interleave", true)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	usableSectors := int64(0)
	if options.Contains("usable-sectors") {
		usableSectors, err = options.GetLong("usable-sectors", 0)
		if err != nil {
			return core.DiscJobResult{}, err
		}
	}

	density := velocity
	if kind == DVD {
		density = channelBitNm
	} else if kind == BluRay {
		density = BluRayChannelBitLengthNm
	}

	result, err := Generate(ctx, GenerateRequest{
		Params: Parameters{
			Kind:           kind,
			InnerRadiusMm:  r0,
			OuterRadiusMm:  r1,
			Sectors:        sectors,
			LinearDensity:  density,
			PitchLinear:    pitchLinear,
			PitchQuadratic: pitchQuadratic,
			PitchCubic:     pitchCubic,
			UsableSectors:  usableSectors,
		},
		Stage:      stage,
		OutputPath: output,
		Interleave: interleave,
	}, progress)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	return core.DiscJobResult{
		ModuleID: ModuleID, Command: req.Command, ExitCode: 0,
		Summary: fmt.Sprintf("Calibration target complete: kind=%s, stage=%s, output=%s\n%s",
			kindText, stageText, result.OutputPath, result.Summary),
		OutputPath: result.OutputPath,
	}, nil
}

func (m *Module) serveUI(ctx context.Context, req core.DiscJobRequest, options core.Options) (core.DiscJobResult, error) {
	listen := options.GetString("listen", "127.0.0.1:8484")
	open, err := options.GetBoolean("open", false)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	url, err := webui.Serve(ctx, listen, open)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	return core.DiscJobResult{
		ModuleID: ModuleID, Command: req.Command, ExitCode: 0,
		Summary: fmt.Sprintf("Calibration UI stopped (%s).", url),
	}, nil
}
