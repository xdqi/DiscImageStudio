// Package dvdmodule ports DiscImageStudio.Dvd/DvdDiscModule.cs: the CLI-facing
// DVD module that delegates to the solver command runner.
package dvdmodule

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"disc-image-studio/internal/core"
	"disc-image-studio/internal/dvd"
)

// ModuleID is the DVD module identifier.
const ModuleID = "dvd-data"

var moduleDescriptor = core.DiscModuleDescriptor{
	ID:                   ModuleID,
	DisplayName:          "DVD data disc",
	Description:          "DVD fast-dispersion ISO writer and clockwise geometry calibration adapter.",
	SupportsCancellation: false,
	Commands: []core.DiscCommandDescriptor{
		{
			Name:         "solve",
			DisplayName:  "Generate DVD image",
			Description:  "Generates a DVD image with the fixed fast dispersion mapping in CW direction.",
			Capabilities: core.CapDataImageGeneration | core.CapHybridFilesystem | core.CapErrorCorrectionEncoding,
			Options: []core.DiscOptionDefinition{
				core.Opt("image", "Source image mapped onto the disc (PNG, JPEG, BMP or GIF)", core.OptionInputFile, true, "", ""),
				core.Opt("iso-output", "Directly burnable 2048-byte/sector ISO image", core.OptionOutputFile, true, "", ""),
				core.Opt("total-sectors", "Reported count of 2048-byte sectors", core.OptionInteger, true, "", ""),
				core.Opt("total-blocks", "Alias for --total-sectors", core.OptionInteger, false, "", ""),
				core.Opt("fill-sectors", "Sectors to fill with the image (multiple of 16; automatic with --data-dir)", core.OptionInteger, false, "16", ""),
				core.Opt("lba", "Initial LBA (automatic with --data-dir)", core.OptionInteger, false, "0", ""),
				core.Opt("psn-offset", "Decimal or 0x-prefixed PSN offset", core.OptionInteger, false, "0x30000", ""),
				core.Opt("data-dir", "Hybrid disc: store this directory together with the drawing", core.OptionInputFolder, false, "", ""),
				core.Opt("file-position", "File placement; outer requires --data-dir", core.OptionChoice, false, "inner", "", "inner", "outer"),
				core.Opt("drawing-start-lba", "Hybrid override; must follow all files and align to an ECC block", core.OptionInteger, false, "", ""),
				core.Opt("volume-label", "Label for a new hybrid ISO", core.OptionString, false, "DVD_IMAGE", ""),
				core.Opt("inner-radius-mm", "Track start radius", core.OptionDecimal, false, "24", "mm"),
				core.Opt("outer-radius-mm", "Track end radius", core.OptionDecimal, false, "58", "mm"),
				core.Opt("channel-bit-nm", "Physical channel-bit length", core.OptionDecimal, false, "133.3", "nm"),
				core.Opt("start-angle-deg", "Track start angle", core.OptionDecimal, false, "0", "degrees"),
				core.Opt("pitch-linear", "Linear radial pitch coefficient", core.OptionDecimal, false, "0", ""),
				core.Opt("pitch-quadratic", "Quadratic radial pitch coefficient", core.OptionDecimal, false, "0", ""),
				core.Opt("pitch-cubic", "Cubic radial pitch coefficient", core.OptionDecimal, false, "0", ""),
				core.Opt("image-threshold", "Binary threshold", core.OptionInteger, false, "128", "0..255"),
				core.Opt("alpha-threshold", "Lower alpha is unconstrained", core.OptionInteger, false, "1", "1..255"),
				core.Opt("constraint-step", "Constrain every Nth channel bit", core.OptionInteger, false, "64", ""),
				core.Opt("tone-dither", "Mix black/white textures per sample; 0 is the hard threshold", core.OptionDecimal, false, "0", "0..1"),
				core.Opt("tone-levels", "DVD dispersion has two textures; only 2 is accepted", core.OptionInteger, false, "2", ""),
				core.Opt("tone-brightness", "Shift the tone ramp", core.OptionDecimal, false, "0", "-64..64"),
				core.Opt("tone-gamma", "Decode sRGB before dithering", core.OptionBoolean, false, "false", ""),
				core.Opt("read-compatibility", "Experimental periodic 0x9B protection for fast dispersion", core.OptionBoolean, false, "false", ""),
				core.Opt("filesystem", "Hybrid filesystem for --data-dir: iso (contiguous) or udf/udf-bridge (dispersed into the picture's gray areas)", core.OptionChoice, false, "iso", "", "iso", "udf", "udf-bridge"),
				core.Opt("boot-image", "El Torito boot image: '/'-separated path of a file inside --data-dir, relative to the image root; placed contiguously and shared by the boot catalog and UDF (requires --filesystem udf-bridge)", core.OptionString, false, "", ""),
				core.Opt("expose", "Expose a regular file (or a directory, recursively; symlinks are skipped) from --data-dir on the ISO9660 bridge at its image path, stored contiguously; repeatable (requires --filesystem udf-bridge)", core.OptionString, false, "", ""),
				core.Opt("gray-band", "Gray-band luminance range T1:T2 for data sectors (default widens automatically from 120:136)", core.OptionString, false, "", ""),
				core.Opt("interior-margin", "Border margin kept at original color inside large white areas", core.OptionDecimal, false, "0.35", "mm"),
				core.Opt("interior-margin-black", "Border margin inside large black areas", core.OptionDecimal, false, "0.5", "mm"),
				core.Opt("data-headroom", "Gray capacity target as a multiple of the data size", core.OptionDecimal, false, "1.10", ""),
				core.Opt("fast-parallelism", "Fast-output workers; 0 selects processor count", core.OptionInteger, false, "0", "0..64"),
				core.Opt("initial", "Initial payload mode", core.OptionChoice, false, "random", "", "random", "zero"),
				core.Opt("seed", "Deterministic random seed", core.OptionInteger, false, "1", ""),
				core.Opt("initial-state", "Initial EFMPlus state", core.OptionInteger, false, "1", "1..4"),
				core.Opt("initial-dsv", "Initial DSV", core.OptionInteger, false, "0", ""),
				core.Opt("initial-level", "Initial physical NRZI level", core.OptionChoice, false, "land", "", "land", "pit"),
			},
			Notes: []string{
				"The CLI fixes --algorithm dispersion --iterations-per-block 1 --fast-output true --spiral-direction cw.",
				"Single-block options --output/--target/--seed-payload/--channel-output/--constraint-output/--iso-template/--iso-sectors are not supported in this mode.",
			},
		},
		{
			Name:         "encode",
			DisplayName:  "Encode DVD payload",
			Description:  "Encodes one DVD ECC block into packed NRZI channel levels.",
			Capabilities: core.CapErrorCorrectionEncoding,
			Options: []core.DiscOptionDefinition{
				core.Opt("input", "Payload, exactly 32768 bytes", core.OptionInputFile, true, "", ""),
				core.Opt("output", "Packed NRZI channel output", core.OptionOutputFile, true, "", ""),
				core.Opt("lba", "Initial LBA; the following 15 sectors are consecutive", core.OptionInteger, false, "0", ""),
				core.Opt("psn-offset", "Decimal or 0x-prefixed PSN offset", core.OptionInteger, false, "0x30000", ""),
				core.Opt("initial-state", "Initial EFMPlus state", core.OptionInteger, false, "1", "1..4"),
				core.Opt("initial-dsv", "Initial DSV", core.OptionInteger, false, "0", ""),
				core.Opt("initial-level", "Initial physical NRZI level", core.OptionChoice, false, "land", "", "land", "pit"),
			},
		},
		{
			Name:         "calibrate",
			DisplayName:  "Preview DVD geometry",
			Description:  "Renders generated geometry through measured disc radii.",
			Capabilities: core.CapGeometryPreview,
			Options: []core.DiscOptionDefinition{
				core.Opt("image", "Source image", core.OptionInputFile, true, "", ""),
				core.Opt("output", "Preview PNG", core.OptionOutputFile, true, "", ""),
				core.Opt("total-sectors", "Disc sector count (or its alias --total-blocks)", core.OptionInteger, true, "", ""),
				core.Opt("generated-inner-radius-mm", "Generated inner radius", core.OptionDecimal, true, "", "mm"),
				core.Opt("generated-outer-radius-mm", "Generated outer radius", core.OptionDecimal, true, "", "mm"),
				core.Opt("actual-inner-radius-mm", "Measured inner radius", core.OptionDecimal, true, "", "mm"),
				core.Opt("actual-outer-radius-mm", "Measured outer radius", core.OptionDecimal, true, "", "mm"),
				core.Opt("lba", "First visible sector", core.OptionInteger, false, "0", ""),
				core.Opt("fill-sectors", "Visible sector count (default: remainder of disc)", core.OptionInteger, false, "", ""),
				core.Opt("channel-bit-nm", "Physical channel-bit length", core.OptionDecimal, false, "133.3", "nm"),
				core.Opt("pitch-linear", "Generated linear pitch coefficient; --generated-pitch-linear also accepted", core.OptionDecimal, false, "0", ""),
				core.Opt("pitch-quadratic", "Generated quadratic pitch coefficient; --generated-pitch-quadratic also accepted", core.OptionDecimal, false, "0", ""),
				core.Opt("pitch-cubic", "Generated cubic pitch coefficient; --generated-pitch-cubic also accepted", core.OptionDecimal, false, "0", ""),
				core.Opt("actual-pitch-linear", "Measured linear pitch coefficient", core.OptionDecimal, false, "0", ""),
				core.Opt("actual-pitch-quadratic", "Measured quadratic pitch coefficient", core.OptionDecimal, false, "0", ""),
				core.Opt("actual-pitch-cubic", "Measured cubic pitch coefficient", core.OptionDecimal, false, "0", ""),
				core.Opt("image-threshold", "Accepted for compatibility; black/white preview uses 128", core.OptionInteger, false, "128", "0..255"),
				core.Opt("alpha-threshold", "Lower alpha is unconstrained", core.OptionInteger, false, "1", "1..255"),
				core.Opt("preview-size", "Square PNG size", core.OptionInteger, false, "1024", "64..8192 px"),
				core.Opt("samples-per-sector", "Forward geometry samples per sector", core.OptionInteger, false, "16", "1..4096"),
				core.Opt("tone-dither", "Mix black/white textures per sample; 0 is the hard threshold", core.OptionDecimal, false, "0", "0..1"),
				core.Opt("tone-levels", "DVD dispersion has two textures; only 2 is accepted", core.OptionInteger, false, "2", ""),
				core.Opt("tone-brightness", "Shift the tone ramp", core.OptionDecimal, false, "0", "-64..64"),
				core.Opt("tone-gamma", "Decode sRGB before dithering", core.OptionBoolean, false, "false", ""),
			},
			Notes: []string{
				"The CLI fixes --spiral-direction cw; there is no rotation-offset calibration.",
			},
		},
		{
			Name:         "simulate",
			DisplayName:  "Simulate DVD read-back",
			Description:  "Renders a generated ISO's payload sectors under measured disc radii.",
			Capabilities: core.CapEncodedOutputPreview,
			Options: []core.DiscOptionDefinition{
				core.Opt("iso", "Generated ISO image", core.OptionInputFile, true, "", ""),
				core.Opt("output", "Preview PNG", core.OptionOutputFile, true, "", ""),
				core.Opt("inner-radius-mm", "Measured inner radius of the burned spiral", core.OptionDecimal, true, "", "mm"),
				core.Opt("outer-radius-mm", "Measured outer radius of the burned spiral", core.OptionDecimal, true, "", "mm"),
				core.Opt("total-sectors", "Disc sector count (or --total-blocks); defaults to the solve sidecar JSON (IMAGE.iso.json) or the ISO file size", core.OptionInteger, false, "", ""),
				core.Opt("lba", "First visible sector; defaults to the sidecar drawing start (0 when absent)", core.OptionInteger, false, "", ""),
				core.Opt("fill-sectors", "Visible sector count; defaults to the sidecar drawing length (remainder of the ISO when absent)", core.OptionInteger, false, "", ""),
				core.Opt("psn-offset", "Decimal or 0x-prefixed PSN offset", core.OptionInteger, false, "0x30000", ""),
				core.Opt("channel-bit-nm", "Physical channel-bit length", core.OptionDecimal, false, "133.3", "nm"),
				core.Opt("pitch-linear", "Measured linear pitch coefficient", core.OptionDecimal, false, "0", ""),
				core.Opt("pitch-quadratic", "Measured quadratic pitch coefficient", core.OptionDecimal, false, "0", ""),
				core.Opt("pitch-cubic", "Measured cubic pitch coefficient", core.OptionDecimal, false, "0", ""),
				core.Opt("preview-size", "Square PNG size", core.OptionInteger, false, "1024", "64..8192 px"),
				core.Opt("samples-per-sector", "Payload bytes sampled per sector", core.OptionInteger, false, "16", "1..2048"),
			},
			Notes: []string{
				"The CLI fixes --spiral-direction cw.",
			},
		},
		{
			Name:         "selftest",
			DisplayName:  "Run DVD engine self-tests",
			Description:  "Runs the DVD engine's deterministic regression suite.",
			Capabilities: core.CapNone,
			Options:      nil,
		},
	},
}

// Module is the DVD module.
type Module struct{}

// NewModule creates the DVD module.
func NewModule() *Module { return &Module{} }

// Descriptor returns the module descriptor.
func (m *Module) Descriptor() core.DiscModuleDescriptor { return moduleDescriptor }

// Execute runs one DVD command.
func (m *Module) Execute(ctx context.Context, req core.DiscJobRequest, progress core.ProgressFunc) (core.DiscJobResult, error) {
	if err := ctx.Err(); err != nil {
		return core.DiscJobResult{}, err
	}
	supported := false
	for _, command := range moduleDescriptor.Commands {
		if strings.EqualFold(command.Name, req.Command) {
			supported = true
			break
		}
	}
	if !supported {
		return core.DiscJobResult{}, fmt.Errorf("DVD module does not support command '%s'", req.Command)
	}

	if progress != nil {
		progress(core.DiscJobProgress{ModuleID: ModuleID, Stage: "execute",
			Message: fmt.Sprintf("Running DVD command %s…", req.Command)})
	}
	normalized := normalizeArguments(req.Command, req.Args)
	commandLine := append([]string{req.Command}, normalized...)
	exitCode := dvd.RunCommand(commandLine)
	outputPath := findOutputPath(req.Args)
	summary := fmt.Sprintf("DVD command %s failed with exit code %d.", req.Command, exitCode)
	if exitCode == 0 {
		summary = fmt.Sprintf("DVD command %s completed.", req.Command)
	}
	return core.DiscJobResult{
		ModuleID: ModuleID, Command: req.Command, ExitCode: exitCode, Summary: summary, OutputPath: outputPath,
	}, nil
}

func findOutputPath(arguments []string) string {
	output := findOption(arguments, "iso-output")
	if output == "" {
		output = findOption(arguments, "output")
	}
	if strings.TrimSpace(output) == "" {
		return ""
	}
	full, err := filepath.Abs(output)
	if err != nil {
		return ""
	}
	return full
}

func normalizeArguments(command string, arguments []string) []string {
	var fixedOptions [][2]string
	switch strings.ToLower(command) {
	case "solve":
		fixedOptions = [][2]string{
			{"algorithm", "dispersion"},
			{"iterations-per-block", "1"},
			{"fast-output", "true"},
			{"spiral-direction", "cw"},
		}
	case "calibrate", "simulate":
		fixedOptions = [][2]string{{"spiral-direction", "cw"}}
	}
	if len(fixedOptions) == 0 {
		return arguments
	}
	fixedKeys := make(map[string]bool)
	for _, option := range fixedOptions {
		fixedKeys[strings.ToLower(option[0])] = true
	}
	var normalized []string
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		optionName, isOption := readOptionName(argument)
		if !isOption || !fixedKeys[strings.ToLower(optionName)] {
			normalized = append(normalized, argument)
			continue
		}
		if !strings.Contains(argument, "=") &&
			index+1 < len(arguments) && !strings.HasPrefix(arguments[index+1], "--") {
			index++
		}
	}
	for _, option := range fixedOptions {
		normalized = append(normalized, "--"+option[0], option[1])
	}
	return normalized
}

func readOptionName(argument string) (string, bool) {
	if !strings.HasPrefix(argument, "--") || len(argument) == 2 {
		return "", false
	}
	if equals := strings.IndexByte(argument, '='); equals >= 0 {
		return argument[2:equals], equals > 2
	}
	return argument[2:], true
}

func findOption(arguments []string, key string) string {
	prefix := "--" + key + "="
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if len(argument) >= len(prefix) && strings.EqualFold(argument[:len(prefix)], prefix) {
			return argument[len(prefix):]
		}
		if strings.EqualFold(argument, "--"+key) && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return ""
}
