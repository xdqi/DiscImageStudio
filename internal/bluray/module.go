package bluray

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"disc-image-studio/internal/core"
)

// ModuleID ports BluRayDiscModule.ModuleId.
const ModuleID = "bluray-data"

// ImageLoaderFunc loads a source image as Gray8 (the port of the
// Func<string, CancellationToken, BluRayGray8Raster> delegate).
type ImageLoaderFunc func(path string, ctx context.Context) (*Gray8Raster, error)

// Module ports BluRayDiscModule.
type Module struct {
	imageLoader ImageLoaderFunc
	descriptor  core.DiscModuleDescriptor
}

// geometryOptions ports the private GeometryOptions array.
func geometryOptions() []core.DiscOptionDefinition {
	presetIDs := make([]string, 0, len(BuiltInPresets))
	for _, preset := range BuiltInPresets {
		presetIDs = append(presetIDs, preset.ID)
	}
	return []core.DiscOptionDefinition{
		core.Opt("preset", "BD-R preset", core.OptionChoice, false, "bd-r-sl-25gb", "", presetIDs...),
		core.Opt("total-sectors", "Geometry total sectors", core.OptionInteger, false, "12219392", ""),
		core.Opt("usable-sectors", "Output sectors within the geometry", core.OptionInteger, false, "", ""),
		core.Opt("r0", "Inner radius", core.OptionDecimal, false, "24", "mm"),
		core.Opt("r1", "Outer radius", core.OptionDecimal, false, "58", "mm"),
		core.Opt("start-angle", "Start angle", core.OptionDecimal, false, "0", "degrees"),
		core.Opt("pitch-linear", "Linear radial pitch coefficient", core.OptionDecimal, false, "0", ""),
		core.Opt("pitch-quadratic", "Quadratic radial pitch coefficient", core.OptionDecimal, false, "0", ""),
		core.Opt("pitch-cubic", "Cubic radial pitch coefficient", core.OptionDecimal, false, "0", ""),
	}
}

// NewModule ports BluRayDiscModule(imageLoader).
func NewModule(loader ImageLoaderFunc) core.OpticalDiscModule {
	geometry := geometryOptions()
	withExtras := func(extra ...core.DiscOptionDefinition) []core.DiscOptionDefinition {
		options := append([]core.DiscOptionDefinition{}, geometry...)
		return append(options, extra...)
	}
	descriptor := core.DiscModuleDescriptor{
		ID:                   ModuleID,
		DisplayName:          "Blu-ray BD-R",
		Description:          "ISO/IEC 30190 single-layer BD-R image generation using 3T/6T run-count bias, payload probes and ECC diagnostics.",
		SupportsCancellation: true,
		Commands: []core.DiscCommandDescriptor{
			{
				Name: "bd-info", DisplayName: "BD-R parameters",
				Description:  "Describe nominal BD-R geometry; optionally export JSON.",
				Capabilities: core.CapNone,
				Options: withExtras(
					core.Opt("output", "JSON output", core.OptionOutputFile, false, "", ""),
					core.Opt("invert", "Swap 3T/6T assignment", core.OptionBoolean, false, "false", "")),
			},
			{
				Name: "bd-probe", DisplayName: "BD-R payload probe",
				Description:  "Export raw user sectors for an explicit scrambled-byte probe (no filesystem).",
				Capabilities: core.CapDataImageGeneration,
				Options: withExtras(
					core.Opt("output", "Payload output", core.OptionOutputFile, true, "", ""),
					core.Opt("start-sector", "First sector", core.OptionInteger, false, "0", ""),
					core.Opt("sectors", "Sector count", core.OptionInteger, false, "32", ""),
					core.Opt("byte", "Scrambled byte", core.OptionString, true, "", "")),
			},
			{
				Name: "bd-encode", DisplayName: "BD-R ECC diagnostic",
				Description:  "Encode exactly 65536 payload bytes into 76880 ECC bytes; not a burn image.",
				Capabilities: core.CapErrorCorrectionEncoding,
				Options: []core.DiscOptionDefinition{
					core.Opt("input", "Payload cluster", core.OptionInputFile, true, "", ""),
					core.Opt("output", "ECC cluster", core.OptionOutputFile, true, "", ""),
					core.Opt("psn", "First physical sector", core.OptionString, false, "0x100000", ""),
				},
			},
			{
				Name: "bd-generate", DisplayName: "BD-R picture generation",
				Description:  "Draw the full user-data area; selecting a files directory adds a UDF 2.01 filesystem at the inner or outer ring.",
				Capabilities: core.CapDataImageGeneration,
				Options: withExtras(
					core.Opt("input", "Source image", core.OptionInputFile, true, "", ""),
					core.Opt("output", "ISO output", core.OptionOutputFile, true, "", ""),
					core.Opt("data-dir", "Files directory", core.OptionInputFolder, false, "", ""),
					core.Opt("file-position", "File placement", core.OptionChoice, false, "inner", "", "inner", "outer"),
					core.Opt("volume-label", "ISO volume label", core.OptionString, false, "BD_IMAGE", ""),
					core.Opt("invert", "Swap 3T/6T assignment", core.OptionBoolean, false, "false", ""),
					core.Opt("tone-dither", "Dither strength 0..1", core.OptionDecimal, false, "0", ""),
					core.Opt("tone-levels", "Tone levels (BD is binary)", core.OptionInteger, false, "2", ""),
					core.Opt("tone-brightness", "Brightness offset -64..64", core.OptionDecimal, false, "0", ""),
					core.Opt("tone-gamma", "sRGB linearization", core.OptionBoolean, false, "false", "")),
			},
		},
	}
	return &Module{imageLoader: loader, descriptor: descriptor}
}

// Descriptor ports the C# property.
func (m *Module) Descriptor() core.DiscModuleDescriptor { return m.descriptor }

// Execute ports the private BluRayDiscModule.Execute (the C# Task.Run wrapper
// is an artifact of the async contract; the Go port runs synchronously).
func (m *Module) Execute(ctx context.Context, req core.DiscJobRequest, progress core.ProgressFunc) (core.DiscJobResult, error) {
	if err := ctx.Err(); err != nil {
		return core.DiscJobResult{}, err
	}
	var command *core.DiscCommandDescriptor
	for i := range m.descriptor.Commands {
		if m.descriptor.Commands[i].Name == req.Command {
			command = &m.descriptor.Commands[i]
			break
		}
	}
	if command == nil {
		return core.DiscJobResult{}, fmt.Errorf("Unknown Blu-ray command '%s'.", req.Command)
	}
	options, err := parseModuleOptions(req.Args, command)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	if req.OutputPath != "" {
		if _, ok := options["output"]; !ok {
			options["output"] = req.OutputPath
		}
	}
	for _, option := range command.Options {
		if option.Required {
			if _, ok := options[option.Name]; !ok {
				return core.DiscJobResult{}, fmt.Errorf("Missing --%s.", option.Name)
			}
		}
	}
	output, hasOutput := options["output"]
	if hasOutput && isNullOrWhiteSpace(output) {
		return core.DiscJobResult{}, argNullOrWhiteSpace("output")
	}

	if command.Name == "bd-encode" {
		input := options["input"]
		inputFull, err := filepath.Abs(input)
		if err != nil {
			return core.DiscJobResult{}, err
		}
		outputFull, err := filepath.Abs(output)
		if err != nil {
			return core.DiscJobResult{}, err
		}
		if strings.EqualFold(inputFull, outputFull) {
			return core.DiscJobResult{}, errInputAndOutputSame
		}
		psn, err := parseUint(optionOr(options, "psn", "0x100000"), "psn")
		if err != nil {
			return core.DiscJobResult{}, err
		}
		validPsn := psn >= FirstLayer0PhysicalSector && psn <= 0x00CA73E0
		if !validPsn || psn%32 != 0 {
			return core.DiscJobResult{}, argOutOfRangeMsg("psn",
				"PSN must start a complete, aligned BD-R data-zone cluster.")
		}
		payload := make([]byte, PayloadBytesPerCluster)
		source, err := os.Open(input)
		if err != nil {
			if os.IsNotExist(err) {
				abs, absErr := filepath.Abs(input)
				if absErr != nil {
					abs = input
				}
				return core.DiscJobResult{}, fmt.Errorf("Could not find file '%s'.", abs)
			}
			return core.DiscJobResult{}, err
		}
		info, err := source.Stat()
		if err != nil {
			_ = source.Close()
			return core.DiscJobResult{}, err
		}
		if info.Size() != int64(len(payload)) {
			_ = source.Close()
			return core.DiscJobResult{}, errEncodePayloadLength
		}
		if _, err := io.ReadFull(source, payload); err != nil {
			_ = source.Close()
			return core.DiscJobResult{}, err
		}
		_ = source.Close()
		if err := ctx.Err(); err != nil {
			return core.DiscJobResult{}, err
		}
		encoded, err := EccEncode(payload, psn, nil, nil)
		if err != nil {
			return core.DiscJobResult{}, err
		}
		if err := outputPublisherWrite(output, func(stream io.Writer) error {
			_, err := stream.Write(encoded)
			return err
		}, ctx); err != nil {
			return core.DiscJobResult{}, err
		}
		return core.DiscJobResult{
			ModuleID: ModuleID, Command: command.Name, ExitCode: 0,
			Summary: fmt.Sprintf("BD ECC diagnostic: %d bytes; no 17PP/RUB or filesystem in this file.",
				len(encoded)),
			OutputPath: outputFull,
		}, nil
	}

	preset, err := FindPreset(optionOr(options, "preset", "bd-r-sl-25gb"))
	if err != nil {
		return core.DiscJobResult{}, err
	}
	defaults := preset.CreateParameters()
	disc := &DiscParameters{LayerCount: preset.LayerCount}
	if totalSectors, ok := options["total-sectors"]; ok {
		v, err := parseUint(totalSectors, "total-sectors")
		if err != nil {
			return core.DiscJobResult{}, err
		}
		disc.TotalSectors = v
	} else {
		disc.TotalSectors = preset.TotalSectors()
	}
	if v, err := parseNumberOption(options, "r0", defaults.InnerRadiusMm); err != nil {
		return core.DiscJobResult{}, err
	} else {
		disc.InnerRadiusMm = v
	}
	if v, err := parseNumberOption(options, "r1", defaults.OuterRadiusMm); err != nil {
		return core.DiscJobResult{}, err
	} else {
		disc.OuterRadiusMm = v
	}
	if v, err := parseNumberOption(options, "start-angle", defaults.StartAngleDegrees); err != nil {
		return core.DiscJobResult{}, err
	} else {
		disc.StartAngleDegrees = v
	}
	if v, err := parseNumberOption(options, "pitch-linear", defaults.PitchLinear); err != nil {
		return core.DiscJobResult{}, err
	} else {
		disc.PitchLinear = v
	}
	if v, err := parseNumberOption(options, "pitch-quadratic", defaults.PitchQuadratic); err != nil {
		return core.DiscJobResult{}, err
	} else {
		disc.PitchQuadratic = v
	}
	if v, err := parseNumberOption(options, "pitch-cubic", defaults.PitchCubic); err != nil {
		return core.DiscJobResult{}, err
	} else {
		disc.PitchCubic = v
	}
	if usable, ok := options["usable-sectors"]; ok {
		v, err := parseUint(usable, "usable-sectors")
		if err != nil {
			return core.DiscJobResult{}, err
		}
		disc.UsableSectors = &v
	} else if _, hasTotal := options["total-sectors"]; hasTotal {
		disc.UsableSectors = nil
	} else {
		disc.UsableSectors = defaults.UsableSectors
	}
	if err := disc.Validate(); err != nil {
		return core.DiscJobResult{}, err
	}

	if command.Name == "bd-info" {
		invert := preset.InvertMapping
		if polarity, ok := options["invert"]; ok {
			v, err := parseBool(polarity, "invert")
			if err != nil {
				return core.DiscJobResult{}, err
			}
			invert = v
		}
		json, err := renderBdInfoJSON(disc, preset, invert)
		if err != nil {
			return core.DiscJobResult{}, err
		}
		outputPath := ""
		if hasOutput {
			if err := outputPublisherWrite(output, func(stream io.Writer) error {
				_, err := stream.Write([]byte(json))
				return err
			}, ctx); err != nil {
				return core.DiscJobResult{}, err
			}
			full, err := filepath.Abs(output)
			if err != nil {
				return core.DiscJobResult{}, err
			}
			outputPath = full
		}
		return core.DiscJobResult{
			ModuleID: ModuleID, Command: command.Name, ExitCode: 0,
			Summary: json, OutputPath: outputPath,
		}, nil
	}

	if command.Name == "bd-generate" {
		invert := preset.InvertMapping
		if polarity, ok := options["invert"]; ok {
			v, err := parseBool(polarity, "invert")
			if err != nil {
				return core.DiscJobResult{}, err
			}
			invert = v
		}
		var filesAtOuterRing bool
		switch optionOr(options, "file-position", "inner") {
		case "inner":
			filesAtOuterRing = false
		case "outer":
			filesAtOuterRing = true
		default:
			return core.DiscJobResult{}, errFilePosition
		}
		levels := core.TwoLevels
		if toneLevels, ok := options["tone-levels"]; ok {
			v, err := parseUint(toneLevels, "tone-levels")
			if err != nil {
				return core.DiscJobResult{}, err
			}
			levels = int(int32(v))
		}
		dither := 0.0
		if v, ok := options["tone-dither"]; ok {
			parsed, err := parseNumber(v, "tone-dither")
			if err != nil {
				return core.DiscJobResult{}, err
			}
			dither = parsed
		}
		brightness := 0.0
		if v, ok := options["tone-brightness"]; ok {
			parsed, err := parseNumber(v, "tone-brightness")
			if err != nil {
				return core.DiscJobResult{}, err
			}
			brightness = parsed
		}
		gamma := false
		if v, ok := options["tone-gamma"]; ok {
			parsed, err := parseBool(v, "tone-gamma")
			if err != nil {
				return core.DiscJobResult{}, err
			}
			gamma = parsed
		}
		tone := core.ToneProfile{
			DitherStrength: dither, Levels: levels,
			Brightness: brightness, SrgbLinearization: gamma,
		}
		if err := tone.Validate("tone"); err != nil {
			return core.DiscJobResult{}, err
		}
		if levels != core.TwoLevels {
			return core.DiscJobResult{}, errToneLevels
		}
		image := &ImageRequest{
			SourceImage: options["input"], OutputPath: output, Parameters: disc,
			Tone: tone, InvertMapping: invert,
			DataDirectory:    optionOr(options, "data-dir", ""),
			VolumeLabel:      optionOr(options, "volume-label", "BD_IMAGE"),
			FilesAtOuterRing: filesAtOuterRing,
		}
		if m.imageLoader == nil {
			if _, err := PrepareImage(image); err != nil {
				return core.DiscJobResult{}, err
			}
			if err := ctx.Err(); err != nil {
				return core.DiscJobResult{}, err
			}
			return core.DiscJobResult{
				ModuleID: ModuleID, Command: command.Name, ExitCode: 2,
				Summary: "BD picture generation requires an image loader. Run bd-generate through the application or provide a BluRayGray8Raster loader.",
			}, nil
		}
		generator, err := NewImageGenerator(m.imageLoader)
		if err != nil {
			return core.DiscJobResult{}, err
		}
		result, err := generator.Generate(image, progress, ctx)
		if err != nil {
			return core.DiscJobResult{}, err
		}
		return core.DiscJobResult{
			ModuleID: ModuleID, Command: command.Name, ExitCode: 0,
			Summary: result.Summary, OutputPath: result.OutputPath,
		}, nil
	}

	// bd-probe
	start, err := parseUint(optionOr(options, "start-sector", "0"), "start-sector")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	if start >= disc.OutputSectors() {
		return core.DiscJobResult{}, argOutOfRangeMsg("start-sector", "First sector must be inside the disc.")
	}
	count := uint32(32)
	if sectorCount, ok := options["sectors"]; ok {
		v, err := parseUint(sectorCount, "sectors")
		if err != nil {
			return core.DiscJobResult{}, err
		}
		count = v
	}
	streaming := &StreamingOptions{Disc: disc, StartSector: start, SectorCount: count}
	if err := streaming.Validate(); err != nil {
		return core.DiscJobResult{}, err
	}
	value, err := parseUint(options["byte"], "byte")
	if err != nil {
		return core.DiscJobResult{}, err
	}
	if value > 255 {
		return core.DiscJobResult{}, argOutOfRangeMsg("byte", "Probe byte must be 0..255 or 0x00..0xFF.")
	}
	mapping := &ConstantByteMapping{Value: byte(value)}
	interpretation := fmt.Sprintf("scrambled byte 0x%02X; no black/white interpretation", value)
	nextProgressReport := time.Duration(0)
	report := func(p StreamingProgress) {
		if p.Elapsed < nextProgressReport && p.CompletedBytes != p.TotalBytes {
			return
		}
		nextProgressReport = p.Elapsed + 250*time.Millisecond
		if progress != nil {
			progress(core.DiscJobProgress{
				ModuleID: ModuleID, Stage: "payload",
				Message:  fmt.Sprintf("BD %d/%d bytes", p.CompletedBytes, p.TotalBytes),
				Fraction: p.Fraction(), HasFraction: true,
				CompletedUnits: p.CompletedBytes, TotalUnits: p.TotalBytes, HasUnits: true,
			})
		}
	}
	summary, err := GenerateStreamingFile(output, streaming, mapping, report, ctx)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	outputFull, err := filepath.Abs(output)
	if err != nil {
		return core.DiscJobResult{}, err
	}
	return core.DiscJobResult{
		ModuleID: ModuleID, Command: command.Name, ExitCode: 0,
		Summary: fmt.Sprintf("BD probe: %d raw payload bytes, sectors %d..%d; %s. No filesystem.",
			summary.BytesWritten, start, start+count-1, interpretation),
		OutputPath: outputFull,
	}, nil
}

var (
	errInputAndOutputSame  = errors.New("Input and output paths must be different.")
	errEncodePayloadLength = errors.New("bd-encode input must contain exactly 65536 bytes (32 user sectors).")
	errFilePosition        = errors.New("--file-position must be inner or outer.")
	errToneLevels          = errors.New("bd-generate accepts only --tone-levels 2.")
)

// parseModuleOptions ports the private BluRayDiscModule.Parse: a
// case-insensitive --key value store that only accepts declared option names.
func parseModuleOptions(args []string, command *core.DiscCommandDescriptor) (map[string]string, error) {
	result := make(map[string]string)
	allowed := make(map[string]bool)
	for _, option := range command.Options {
		allowed[strings.ToLower(option.Name)] = true
	}
	for i := 0; i < len(args); i++ {
		argument := args[i]
		if !strings.HasPrefix(argument, "--") {
			return nil, fmt.Errorf("Expected a named option, got '%s'.", argument)
		}
		separator := strings.IndexByte(argument, '=')
		var name string
		if separator < 0 {
			name = argument[2:]
		} else {
			name = argument[2:separator]
		}
		if command.Name == "bd-generate" && (strings.EqualFold(name, "sectors") ||
			strings.EqualFold(name, "start-sector")) {
			return nil, errors.New("bd-generate now outputs a complete ISO. Use --total-sectors for disc capacity; raw ranges remain available in bd-probe.")
		}
		if !allowed[strings.ToLower(name)] {
			return nil, fmt.Errorf("Unknown option --%s for %s.", name, command.Name)
		}
		var value string
		if separator >= 0 {
			value = argument[separator+1:]
		} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
			value = args[i]
		} else {
			return nil, fmt.Errorf("Missing value for --%s.", name)
		}
		if isNullOrWhiteSpace(value) {
			return nil, fmt.Errorf("Empty value for --%s.", name)
		}
		key := strings.ToLower(name)
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("Duplicate option --%s.", name)
		}
		result[key] = value
	}
	return result, nil
}

func optionOr(options map[string]string, name, fallback string) string {
	if value, ok := options[name]; ok {
		return value
	}
	return fallback
}

// parseUint ports the private UInt helper: decimal or 0x hex with
// NumberStyles.None semantics (no signs, no whitespace).
func parseUint(value, name string) (uint32, error) {
	fail := func() (uint32, error) {
		return 0, fmt.Errorf("--%s must be an unsigned integer (decimal or 0x hex).", name)
	}
	hex := len(value) >= 2 && value[0] == '0' && (value[1] == 'x' || value[1] == 'X')
	digits := value
	base := 10
	if hex {
		digits = value[2:]
		base = 16
	}
	if digits == "" {
		return fail()
	}
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		valid := c >= '0' && c <= '9'
		if base == 16 {
			valid = valid || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		}
		if !valid {
			return fail()
		}
	}
	result, err := strconv.ParseUint(digits, base, 32)
	if err != nil {
		return fail()
	}
	return uint32(result), nil
}

// parseNumber ports the private Number helper: NumberStyles.Float,
// invariant culture, finite only.
func parseNumber(value, name string) (float64, error) {
	fail := func() (float64, error) {
		return 0, fmt.Errorf("--%s must be a finite number.", name)
	}
	trimmed := strings.TrimSpace(value)
	if !isDotNetFloatLiteral(trimmed) {
		return fail()
	}
	result, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(result) || math.IsInf(result, 0) {
		return fail()
	}
	return result, nil
}

// isDotNetFloatLiteral matches NumberStyles.Float: an optional sign, decimal
// digits with an optional point, and an optional exponent.
func isDotNetFloatLiteral(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digitsBefore := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digitsBefore++
	}
	digitsAfter := 0
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digitsAfter++
		}
	}
	if digitsBefore+digitsAfter == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		exponentDigits := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			exponentDigits++
		}
		if exponentDigits == 0 {
			return false
		}
	}
	return i == len(s)
}

// parseBool ports the private Boolean helper (bool.TryParse: case-insensitive
// "true"/"false", surrounding whitespace tolerated).
func parseBool(value, name string) (bool, error) {
	trimmed := strings.TrimSpace(value)
	if strings.EqualFold(trimmed, "true") {
		return true, nil
	}
	if strings.EqualFold(trimmed, "false") {
		return false, nil
	}
	return false, fmt.Errorf("--%s must be true or false.", name)
}

func parseNumberOption(options map[string]string, name string, fallback float64) (float64, error) {
	if value, ok := options[name]; ok {
		return parseNumber(value, name)
	}
	return fallback, nil
}
