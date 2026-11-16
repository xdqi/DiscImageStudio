package dvd

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"disc-image-studio/internal/core"
)

// Ports of the Program.cs argument-reading helpers.

func readFilesAtOuterRing(arguments *cliArguments) (bool, error) {
	var outer bool
	switch arguments.Get("file-position", "inner") {
	case "inner":
		outer = false
	case "outer":
		outer = true
	default:
		return false, newArgumentError("--file-position must be 'inner' or 'outer'.")
	}
	if outer && !arguments.Has("data-dir") {
		return false, newArgumentError("--file-position outer requires --data-dir.")
	}
	return outer, nil
}

func readConstraint(arguments *cliArguments, initialLba uint32) (*imageConstraint, error) {
	hasTarget := arguments.Has("target")
	hasImage := arguments.Has("image")
	if hasTarget == hasImage {
		return nil, newArgumentError("Specify exactly one of --target or --image.")
	}

	if hasTarget {
		target, err := arguments.Require("target")
		if err != nil {
			return nil, err
		}
		targetSector, err := arguments.GetInt("target-sector", 0)
		if err != nil {
			return nil, err
		}
		return imageConstraintLoad(target, targetSector)
	}

	options, err := readImageMappingOptions(arguments, 1)
	if err != nil {
		return nil, err
	}
	image, err := arguments.Require("image")
	if err != nil {
		return nil, err
	}
	return imageConstraintLoadImage(image, initialLba, options)
}

func readImageMappingOptions(arguments *cliArguments, defaultConstraintStep int) (imageMappingOptions, error) {
	threshold, err := readByteRange(arguments, "image-threshold", 128, 0)
	if err != nil {
		return imageMappingOptions{}, err
	}
	alphaThreshold, err := readByteRange(arguments, "alpha-threshold", 1, 1)
	if err != nil {
		return imageMappingOptions{}, err
	}
	totalSectors, err := readTotalSectors(arguments)
	if err != nil {
		return imageMappingOptions{}, err
	}
	innerRadius, err := arguments.GetDouble("inner-radius-mm", 24.0)
	if err != nil {
		return imageMappingOptions{}, err
	}
	outerRadius, err := arguments.GetDouble("outer-radius-mm", 58.0)
	if err != nil {
		return imageMappingOptions{}, err
	}
	channelBitNm, err := arguments.GetDouble("channel-bit-nm", 133.3)
	if err != nil {
		return imageMappingOptions{}, err
	}
	startAngle, err := arguments.GetDouble("start-angle-deg", 0.0)
	if err != nil {
		return imageMappingOptions{}, err
	}
	clockwise, err := readClockwise(arguments)
	if err != nil {
		return imageMappingOptions{}, err
	}
	constraintStep, err := arguments.GetInt("constraint-step", defaultConstraintStep)
	if err != nil {
		return imageMappingOptions{}, err
	}
	pitchLinear, err := arguments.GetDouble("pitch-linear", 0)
	if err != nil {
		return imageMappingOptions{}, err
	}
	pitchQuadratic, err := arguments.GetDouble("pitch-quadratic", 0)
	if err != nil {
		return imageMappingOptions{}, err
	}
	pitchCubic, err := arguments.GetDouble("pitch-cubic", 0)
	if err != nil {
		return imageMappingOptions{}, err
	}
	tone, err := readToneProfile(arguments)
	if err != nil {
		return imageMappingOptions{}, err
	}
	return imageMappingOptions{
		TotalSectors:           totalSectors,
		InnerRadiusMm:          innerRadius,
		OuterRadiusMm:          outerRadius,
		ChannelBitLengthNm:     channelBitNm,
		StartAngleDegrees:      startAngle,
		Clockwise:              clockwise,
		LuminanceThreshold:     byte(threshold),
		AlphaThreshold:         byte(alphaThreshold),
		SampleEveryChannelBits: constraintStep,
		PitchLinear:            pitchLinear,
		PitchQuadratic:         pitchQuadratic,
		PitchCubic:             pitchCubic,
		Tone:                   tone,
	}, nil
}

// readToneProfile ports ReadToneProfile: absent tone options mean the
// historical hard threshold, so existing command lines produce the same bytes.
func readToneProfile(arguments *cliArguments) (*core.ToneProfile, error) {
	if !arguments.Has("tone-dither") &&
		!arguments.Has("tone-levels") &&
		!arguments.Has("tone-brightness") &&
		!arguments.Has("tone-gamma") {
		return nil, nil
	}

	dither, err := arguments.GetDouble("tone-dither", 0)
	if err != nil {
		return nil, err
	}
	levels, err := arguments.GetInt("tone-levels", core.TwoLevels)
	if err != nil {
		return nil, err
	}
	brightness, err := arguments.GetDouble("tone-brightness", 0)
	if err != nil {
		return nil, err
	}
	gamma, err := arguments.GetBoolean("tone-gamma", false)
	if err != nil {
		return nil, err
	}
	tone := &core.ToneProfile{
		DitherStrength:    dither,
		Levels:            levels,
		Brightness:        brightness,
		SrgbLinearization: gamma,
	}
	if err := validateToneProfile(tone); err != nil {
		return nil, err
	}
	if !tone.IsDvdCompatible() {
		return nil, newArgumentError("DVD dispersion uses two textures; --tone-levels must be 2.")
	}
	return tone, nil
}

// validateToneProfile ports ToneProfile.Validate() with its default field name.
func validateToneProfile(tone *core.ToneProfile) error {
	const fieldName = "tone"
	if math.IsNaN(tone.DitherStrength) || math.IsInf(tone.DitherStrength, 0) || tone.DitherStrength < 0 || tone.DitherStrength > 1 {
		return &argumentOutOfRangeError{param: fieldName, msg: fieldName + ".ditherStrength must be a finite number between 0 and 1."}
	}
	if tone.Levels != core.TwoLevels && tone.Levels != core.FourLevels {
		return &argumentOutOfRangeError{param: fieldName, msg: fieldName + ".levels must be 2 or 4."}
	}
	if math.IsNaN(tone.Brightness) || math.IsInf(tone.Brightness, 0) ||
		tone.Brightness < core.MinimumBrightness || tone.Brightness > core.MaximumBrightness {
		return &argumentOutOfRangeError{param: fieldName, msg: fieldName + ".brightness must be between -64 and 64."}
	}
	return nil
}

func readGeneratedPitch(arguments *cliArguments, order string) (float64, error) {
	plain := "pitch-" + order
	generated := "generated-pitch-" + order
	if arguments.Has(plain) && arguments.Has(generated) {
		return 0, newArgumentError("Use only one of --" + plain + " and --" + generated + ".")
	}
	key := plain
	if arguments.Has(generated) {
		key = generated
	}
	return arguments.GetDouble(key, 0)
}

func readTotalSectors(arguments *cliArguments) (uint32, error) {
	hasSectors := arguments.Has("total-sectors")
	hasBlocks := arguments.Has("total-blocks")
	if hasSectors && hasBlocks {
		return 0, newArgumentError("Use only one of --total-sectors and --total-blocks.")
	}
	if !hasSectors && !hasBlocks {
		return 0, newArgumentError("Image mapping requires --total-sectors (or its alias --total-blocks).")
	}
	key := "total-blocks"
	if hasSectors {
		key = "total-sectors"
	}
	return arguments.GetUInt(key, 0)
}

func alignToEccBlock(minimumLba, psnOffset uint32) (uint32, error) {
	sum, err := checkedAddU32(minimumLba, psnOffset)
	if err != nil {
		return 0, err
	}
	remainder := sum & 0xF
	if remainder == 0 {
		return minimumLba, nil
	}
	return checkedAddU32(minimumLba, dvdEccSectorCount-remainder)
}

// replayIsoBoundary ports Program.ReplayIsoBoundary.
func replayIsoBoundary(isoPath string, sectorCount, psnOffset uint32, initialBoundary modulationBoundary) (modulationBoundary, error) {
	if sectorCount%dvdEccSectorCount != 0 {
		return modulationBoundary{}, newArgumentError("Boundary replay sector count must be aligned to 16 sectors.")
	}

	totalBlocks := int(sectorCount / dvdEccSectorCount)
	progressEvery := maxInt(1, totalBlocks/100)
	payloads := make([]byte, dvdPayloadBytesPerBlock)
	boundary := initialBoundary
	iso, err := os.Open(isoPath)
	if err != nil {
		return modulationBoundary{}, translateReadError(err, isoPath)
	}
	defer iso.Close()
	for blockIndex := 0; blockIndex < totalBlocks; blockIndex++ {
		if _, err := io.ReadFull(iso, payloads); err != nil {
			return modulationBoundary{}, err
		}
		lba := uint32(blockIndex) * dvdEccSectorCount
		firstPsn, err := checkedAddU32(lba, psnOffset)
		if err != nil {
			return modulationBoundary{}, err
		}
		recordingFrames, err := buildRecordingFrames(payloads, firstPsn)
		if err != nil {
			return modulationBoundary{}, err
		}
		boundary, err = replayRecordingFrameBoundary(recordingFrames, boundary)
		if err != nil {
			return modulationBoundary{}, err
		}
		completed := blockIndex + 1
		if completed == 1 || completed == totalBlocks || completed%progressEvery == 0 {
			fmt.Printf("inner boundary block %d/%d, state=%d, DSV=%d, level=%s\n",
				completed, totalBlocks, boundary.State, boundary.Dsv, boundary.LevelName())
		}
	}
	return boundary, nil
}

// writeIso ports Program.WriteIso.
func writeIso(arguments *cliArguments, isoOutputPath *string, payloads []byte, lba uint32, imageMapping *imageMappingSummary) (*isoWriteSummary, error) {
	if isoOutputPath == nil {
		if arguments.Has("iso-template") || arguments.Has("iso-sectors") {
			return nil, newArgumentError("--iso-template and --iso-sectors require --iso-output.")
		}
		return nil, nil
	}

	if arguments.Has("iso-template") {
		if arguments.Has("iso-sectors") {
			return nil, newArgumentError("--iso-sectors cannot be combined with --iso-template.")
		}
		templatePath, err := arguments.Require("iso-template")
		if err != nil {
			return nil, err
		}
		templateSectors, err := isoGetTemplateSectorCount(templatePath)
		if err != nil {
			return nil, err
		}
		if imageMapping != nil && templateSectors != imageMapping.TotalSectors {
			return nil, newArgumentError(
				"ISO template has " + itoa(int64(templateSectors)) + " sectors, but image mapping used " +
					itoa(int64(imageMapping.TotalSectors)) + " sectors.")
		}
		return isoPatchTemplate(templatePath, *isoOutputPath, payloads, lba)
	}

	var volumeSectors uint32
	if arguments.Has("iso-sectors") {
		v, err := arguments.GetUInt("iso-sectors", 0)
		if err != nil {
			return nil, err
		}
		volumeSectors = v
		if imageMapping != nil && volumeSectors != imageMapping.TotalSectors {
			return nil, newArgumentError(
				"--iso-sectors is " + itoa(int64(volumeSectors)) + ", but image mapping used " +
					itoa(int64(imageMapping.TotalSectors)) + " sectors.")
		}
	} else if imageMapping != nil {
		volumeSectors = imageMapping.TotalSectors
	} else {
		return nil, newArgumentError("Minimal ISO output requires --iso-sectors when the constraint does not come from an image.")
	}

	volumeLabel := arguments.Get("volume-label", "DVD_IMAGE")
	return isoCreateMinimal(*isoOutputPath, payloads, lba, volumeSectors, volumeLabel)
}

func readBoundary(arguments *cliArguments) (modulationBoundary, error) {
	state, err := arguments.GetInt("initial-state", 1)
	if err != nil {
		return modulationBoundary{}, err
	}
	dsv, err := arguments.GetInt("initial-dsv", 0)
	if err != nil {
		return modulationBoundary{}, err
	}
	level := arguments.Get("initial-level", "land")
	var isLand bool
	if strings.EqualFold(level, "land") {
		isLand = true
	} else if strings.EqualFold(level, "pit") {
		isLand = false
	} else {
		return modulationBoundary{}, newArgumentError("--initial-level must be 'pit' or 'land'.")
	}
	if state < 1 || state > 4 {
		return modulationBoundary{}, &argumentOutOfRangeError{param: "state", msg: "EFMPlus state must be 1..4."}
	}
	return validateModulationBoundary(byte(state), dsv, isLand)
}

func readClockwise(arguments *cliArguments) (bool, error) {
	direction := arguments.Get("spiral-direction", "cw")
	if strings.EqualFold(direction, "cw") {
		return true, nil
	}
	if strings.EqualFold(direction, "ccw") {
		return false, nil
	}
	return false, newArgumentError("--spiral-direction must be 'ccw' or 'cw'.")
}

func readSolverAlgorithm(arguments *cliArguments, fallback string) (solverAlgorithm, error) {
	value := arguments.Get("algorithm", fallback)
	switch strings.ToLower(value) {
	case "full", "exact":
		return solverAlgorithmFull, nil
	case "state-control", "control":
		return solverAlgorithmStateControl, nil
	case "dispersion", "byte-pool":
		return solverAlgorithmDispersion, nil
	default:
		return solverAlgorithmFull, newArgumentError(
			"--algorithm must be 'full' (alias 'exact'), 'state-control' " +
				"(alias 'control'), or 'dispersion' (alias 'byte-pool').")
	}
}

func algorithmName(algorithm solverAlgorithm) string {
	switch algorithm {
	case solverAlgorithmStateControl:
		return "state-control"
	case solverAlgorithmDispersion:
		return "dispersion"
	default:
		return "full"
	}
}

func validateRetiredControlOptions(arguments *cliArguments, algorithm solverAlgorithm) error {
	if arguments.Has("frame-candidates") ||
		arguments.Has("parallel-candidates") ||
		arguments.Has("parallelism") ||
		arguments.Has("word-candidates") {
		return newArgumentError("--frame-candidates, --parallel-candidates, --parallelism and --word-candidates belonged to removed search modes; state-control now uses state/level/current-DSV binary byte pairs and --beam-width.")
	}

	if algorithm != solverAlgorithmStateControl {
		return nil
	}

	if arguments.Has("control-bytes") {
		return newArgumentError("--control-bytes belonged to the removed control-pool algorithm; use --bridge-bytes.")
	}

	if arguments.Has("mutation-bytes") {
		return newArgumentError("--mutation-bytes is only used by --algorithm full; state-control uses deterministic codeword selection.")
	}
	return nil
}

func readByteRange(arguments *cliArguments, key string, fallback, minimum int) (int, error) {
	value, err := arguments.GetInt(key, fallback)
	if err != nil {
		return 0, err
	}
	if value < minimum || value > 255 {
		return 0, &argumentOutOfRangeError{param: "arguments", msg: "--" + key + " must be " + itoa(int64(minimum)) + "..255."}
	}
	return value, nil
}

func readBoolean(arguments *cliArguments, key string, fallback bool) (bool, error) {
	if !arguments.Has(key) {
		return fallback, nil
	}
	value, err := arguments.Require(key)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(value, "true") || strings.EqualFold(value, "yes") || value == "1" {
		return true, nil
	}
	if strings.EqualFold(value, "false") || strings.EqualFold(value, "no") || value == "0" {
		return false, nil
	}
	return false, newArgumentError("--" + key + " must be true/false, yes/no, or 1/0.")
}

func requireDouble(arguments *cliArguments, key string) (float64, error) {
	if arguments.Has(key) {
		return arguments.GetDouble(key, 0)
	}
	return 0, newArgumentError("Missing required option --" + key + ".")
}

func dispersionPaletteDescription() string {
	parts := make([]string, 2)
	for level := 0; level < 2; level++ {
		parts[level] = "0x" + formatHexUpper(uint64(dvdGetScrambledByteForLevel(level)), 2)
	}
	return "dispersion black/white pool: Q=" +
		strings.Join(parts, ",") +
		"; nominal luminance=0,255; threshold=128"
}

// loadOrCreatePayload ports Program.LoadOrCreatePayload.
func loadOrCreatePayload(arguments *cliArguments, randomSeed int32) ([]byte, error) {
	if arguments.Has("seed-payload") {
		path, err := arguments.Require("seed-payload")
		if err != nil {
			return nil, err
		}
		payload, err := dotnetReadAllBytes(path)
		if err != nil {
			return nil, err
		}
		if len(payload) != dvdPayloadBytesPerBlock {
			return nil, newArgumentError("Seed payload must contain exactly 32768 bytes.")
		}
		return payload, nil
	}

	generated := make([]byte, dvdPayloadBytesPerBlock)
	mode := arguments.Get("initial", "random")
	if strings.EqualFold(mode, "random") {
		newDotnetRandom(randomSeed).nextBytes(generated)
	} else if !strings.EqualFold(mode, "zero") {
		return nil, newArgumentError("--initial must be 'random' or 'zero'.")
	}
	return generated, nil
}
