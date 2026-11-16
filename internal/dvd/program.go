package dvd

import (
	"fmt"
	"os"
	"strings"
)

// RunCommand ports Program.RunCommand / DvdCommandRunner.Run.
func RunCommand(args []string) (code int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if err, ok := recovered.(error); ok {
				fmt.Fprintln(os.Stderr, "error: "+err.Error())
			} else {
				fmt.Fprintln(os.Stderr, "error:", recovered)
			}
			code = 1
		}
	}()
	if err := runCommand(args); err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		return 1
	}
	return 0
}

func runCommand(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp()
		return nil
	}

	var err error
	switch strings.ToLower(args[0]) {
	case "solve":
		err = solveCommand(mustParseCli(args[1:], "expose"))
	case "encode":
		err = encodeCommand(mustParseCli(args[1:]))
	case "calibrate":
		err = calibrateCommand(mustParseCli(args[1:]))
	case "simulate":
		err = simulateCommand(mustParseCli(args[1:]))
	case "selftest":
		err = runSelfTests()
	default:
		err = newArgumentError("Unknown command '" + args[0] + "'.")
	}
	return err
}

func mustParseCli(args []string, repeatableKeys ...string) *cliArguments {
	parsed, err := parseCliArguments(args, repeatableKeys...)
	if err != nil {
		panic(err)
	}
	return parsed
}

func solveCommand(arguments *cliArguments) error {
	if _, err := readFilesAtOuterRing(arguments); err != nil {
		return err
	}
	fillSectors, err := arguments.GetUInt("fill-sectors", dvdEccSectorCount)
	if err != nil {
		return err
	}
	fastOutput, err := readBoolean(arguments, "fast-output", false)
	if err != nil {
		return err
	}
	if arguments.Has("data-dir") || fillSectors != dvdEccSectorCount || fastOutput {
		return solveMultipleBlocks(arguments, fillSectors)
	}

	readCompatibility, err := readBoolean(arguments, "read-compatibility", false)
	if err != nil {
		return err
	}
	if readCompatibility {
		return newArgumentError("--read-compatibility true requires --fast-output true and --algorithm dispersion.")
	}

	var outputPath *string
	if arguments.Has("output") {
		value, err := arguments.Require("output")
		if err != nil {
			return err
		}
		outputPath = &value
	}
	var isoOutputPath *string
	if arguments.Has("iso-output") {
		value, err := arguments.Require("iso-output")
		if err != nil {
			return err
		}
		isoOutputPath = &value
	}
	if outputPath == nil && isoOutputPath == nil {
		return newArgumentError("Specify --output, --iso-output, or both.")
	}

	if outputPath != nil && isoOutputPath != nil && pathEquals(*outputPath, *isoOutputPath) {
		return newArgumentError("--output and --iso-output must be different files.")
	}

	lba, err := arguments.GetUInt("lba", 0)
	if err != nil {
		return err
	}
	psnOffset, err := arguments.GetUInt("psn-offset", 0x30000)
	if err != nil {
		return err
	}
	randomSeed, err := arguments.GetInt("seed", 1)
	if err != nil {
		return err
	}
	initial, err := readBoundary(arguments)
	if err != nil {
		return err
	}
	constraint, err := readConstraint(arguments, lba)
	if err != nil {
		return err
	}
	initialPayload, err := loadOrCreatePayload(arguments, int32(randomSeed))
	if err != nil {
		return err
	}
	algorithm, err := readSolverAlgorithm(arguments, "full")
	if err != nil {
		return err
	}
	if err := validateRetiredControlOptions(arguments, algorithm); err != nil {
		return err
	}
	iterationsDefault := 1000
	if algorithm == solverAlgorithmStateControl {
		iterationsDefault = 3
	} else if algorithm == solverAlgorithmDispersion {
		iterationsDefault = 1
	}
	iterations, err := arguments.GetInt("iterations", iterationsDefault)
	if err != nil {
		return err
	}
	mutationBytes, err := arguments.GetInt("mutation-bytes", 4)
	if err != nil {
		return err
	}
	initialTemperature, err := arguments.GetDouble("initial-temperature", 64.0)
	if err != nil {
		return err
	}
	finalTemperature, err := arguments.GetDouble("final-temperature", 0.25)
	if err != nil {
		return err
	}
	bridgeBytes, err := arguments.GetInt("bridge-bytes", 2)
	if err != nil {
		return err
	}
	beamWidth, err := arguments.GetInt("beam-width", 8)
	if err != nil {
		return err
	}
	retryBeamWidth, err := arguments.GetInt("retry-beam-width", 32)
	if err != nil {
		return err
	}
	retryFrameFraction, err := arguments.GetDouble("retry-frame-fraction", 0.25)
	if err != nil {
		return err
	}
	batchFrames, err := arguments.GetInt("batch-frames", 8)
	if err != nil {
		return err
	}
	batchCandidates, err := arguments.GetInt("batch-candidates", 4)
	if err != nil {
		return err
	}
	searchLanes, err := arguments.GetInt("search-lanes", 1)
	if err != nil {
		return err
	}
	options := solverOptions{
		Iterations:         iterations,
		MutationBytes:      mutationBytes,
		RandomSeed:         int32(randomSeed),
		InitialTemperature: initialTemperature,
		FinalTemperature:   finalTemperature,
		Algorithm:          algorithm,
		BridgeBytes:        bridgeBytes,
		BeamWidth:          beamWidth,
		RetryBeamWidth:     retryBeamWidth,
		RetryFrameFraction: retryFrameFraction,
		BatchFrames:        batchFrames,
		BatchCandidates:    batchCandidates,
		SearchLanes:        searchLanes,
	}

	firstPsn, err := checkedAddU32(lba, psnOffset)
	if err != nil {
		return err
	}
	fmt.Printf("ECC Block: LBA %d, PSN 0x%s, constraints %d, algorithm=%s, iterations %d\n",
		lba, formatHexUpper(uint64(firstPsn), 6),
		constraint.constrainedBits, algorithmName(algorithm),
		options.Iterations)
	if algorithm == solverAlgorithmStateControl {
		adaptive := "disabled"
		if options.adaptiveRetryEnabled() {
			adaptive = fmt.Sprintf("beam %d on failed single frames", options.RetryBeamWidth)
		}
		fmt.Printf("bridge bytes/edge=%d, beam width=%d, frame fraction/pass=%s, singleton retry=%s, ECC-closed batch=%d frames/%d candidates, search lanes=%d, payload candidates=state/level/DSV binary byte pairs, scoring=majority error then gray error\n",
			options.BridgeBytes, options.BeamWidth,
			formatPercentP0(options.RetryFrameFraction), adaptive,
			options.BatchFrames, options.BatchCandidates,
			options.SearchLanes)
	} else if algorithm == solverAlgorithmDispersion {
		fmt.Println(dispersionPaletteDescription() + ", " +
			"image sampled at every payload code-word center")
	}
	if mapping := constraint.imageMapping; mapping != nil {
		fmt.Printf("image: %dx%d, pitch=%s um, block radius=%s..%s mm\n",
			mapping.ImageWidth, mapping.ImageHeight,
			formatFixed(mapping.TrackPitchMicrometres, 4),
			formatFixed(mapping.BlockStartRadiusMm, 6), formatFixed(mapping.BlockEndRadiusMm, 6))
	}

	if arguments.Has("constraint-output") {
		constraintPath, err := arguments.Require("constraint-output")
		if err != nil {
			return err
		}
		if err := ensureParentDirectory(constraintPath); err != nil {
			return err
		}
		if err := constraint.saveText(constraintPath); err != nil {
			return err
		}
	}

	progressEvery := maxInt(1, options.Iterations/20)
	result, err := solverEngineSolve(
		constraint,
		initialPayload,
		lba,
		psnOffset,
		initial,
		options,
		func(iteration, current, best int) {
			if iteration == 1 || iteration%progressEvery == 0 || best == 0 {
				fmt.Printf("iteration %d: current=%d, best=%d\n", iteration, current, best)
			}
		})
	if err != nil {
		return err
	}

	if outputPath != nil {
		if err := ensureParentDirectory(*outputPath); err != nil {
			return err
		}
		if err := dotnetWriteAllBytes(*outputPath, result.Payloads); err != nil {
			return err
		}
	}

	if arguments.Has("channel-output") {
		channelPath, err := arguments.Require("channel-output")
		if err != nil {
			return err
		}
		if err := ensureParentDirectory(channelPath); err != nil {
			return err
		}
		channelEncoded := result.Encoded
		if len(channelEncoded.ChannelLevels) == 0 {
			channelEncoded, err = newDvdEccBlockEncoder().encode(result.Payloads, lba, psnOffset, initial)
			if err != nil {
				return err
			}
		}
		if err := dotnetWriteAllBytes(channelPath, packMsbFirst(channelEncoded.ChannelLevels)); err != nil {
			return err
		}
	}

	iso, err := writeIso(arguments, isoOutputPath, result.Payloads, lba, constraint.imageMapping)
	if err != nil {
		return err
	}
	elapsedSeconds := result.Elapsed.Seconds()
	if outputPath != nil {
		if err := writeMetadata(
			*outputPath+".json",
			lba, psnOffset, initial, result.Encoded.FinalBoundary,
			intPtr(result.Score), intPtr(result.ScoreMaximum), &result.ScoreMetric,
			result.GrayScore, result.GrayScoreMaximum, result.GrayScoreMetric,
			result.ControlledScore, result.ControlledScoreMaximum, result.ControlledScoreMetric,
			result.ControlledGrayScore, result.ControlledGrayScoreMaximum, result.ControlledGrayScoreMetric,
			intPtr(result.CompletedIterations), &elapsedSeconds, int32Ptr(int32(randomSeed)),
			&options, constraint.imageMapping, iso); err != nil {
			return err
		}
	}
	if isoOutputPath != nil {
		if err := writeMetadata(
			*isoOutputPath+".json",
			lba, psnOffset, initial, result.Encoded.FinalBoundary,
			intPtr(result.Score), intPtr(result.ScoreMaximum), &result.ScoreMetric,
			result.GrayScore, result.GrayScoreMaximum, result.GrayScoreMetric,
			result.ControlledScore, result.ControlledScoreMaximum, result.ControlledScoreMetric,
			result.ControlledGrayScore, result.ControlledGrayScoreMaximum, result.ControlledGrayScoreMetric,
			intPtr(result.CompletedIterations), &elapsedSeconds, int32Ptr(int32(randomSeed)),
			&options, constraint.imageMapping, iso); err != nil {
			return err
		}
	}

	var outputList []string
	if outputPath != nil {
		outputList = append(outputList, *outputPath)
	}
	if isoOutputPath != nil {
		outputList = append(outputList, *isoOutputPath)
	}
	outputs := strings.Join(outputList, ", ")
	gray := ""
	if result.GrayScore != nil && result.GrayScoreMaximum != nil {
		grayMetric := "gray-error"
		if result.GrayScoreMetric != nil {
			grayMetric = *result.GrayScoreMetric
		}
		gray = fmt.Sprintf(", %s=%d/%d", grayMetric, *result.GrayScore, *result.GrayScoreMaximum)
	}
	controlled := ""
	if result.ControlledScore != nil && result.ControlledScoreMaximum != nil {
		controlled = fmt.Sprintf(", controlled-majority-error=%d/%d", *result.ControlledScore, *result.ControlledScoreMaximum)
		if result.ControlledGrayScore != nil && result.ControlledGrayScoreMaximum != nil {
			controlled += fmt.Sprintf(", controlled-gray-error=%d/%d", *result.ControlledGrayScore, *result.ControlledGrayScoreMaximum)
		}
	}
	fmt.Printf("done: %s=%d/%d%s%s, elapsed=%ss, output=%s\n",
		result.ScoreMetric, result.Score, result.ScoreMaximum, gray, controlled,
		formatFixed(result.Elapsed.Seconds(), 2), outputs)
	return nil
}

func encodeCommand(arguments *cliArguments) error {
	inputPath, err := arguments.Require("input")
	if err != nil {
		return err
	}
	outputPath, err := arguments.Require("output")
	if err != nil {
		return err
	}
	payloads, err := dotnetReadAllBytes(inputPath)
	if err != nil {
		return err
	}
	lba, err := arguments.GetUInt("lba", 0)
	if err != nil {
		return err
	}
	psnOffset, err := arguments.GetUInt("psn-offset", 0x30000)
	if err != nil {
		return err
	}
	initial, err := readBoundary(arguments)
	if err != nil {
		return err
	}
	encoder := newDvdEccBlockEncoder()
	result, err := encoder.encode(payloads, lba, psnOffset, initial)
	if err != nil {
		return err
	}
	if err := ensureParentDirectory(outputPath); err != nil {
		return err
	}
	if err := dotnetWriteAllBytes(outputPath, packMsbFirst(result.ChannelLevels)); err != nil {
		return err
	}
	if err := writeMetadata(
		outputPath+".json",
		lba, psnOffset, initial, result.FinalBoundary,
		nil, nil, nil,
		nil, nil, nil,
		nil, nil, nil,
		nil, nil, nil,
		nil, nil, nil,
		nil, nil, nil); err != nil {
		return err
	}
	length, err := fileLength(outputPath)
	if err != nil {
		return err
	}
	fmt.Printf("encoded %d payload bytes into %d channel levels (%d packed bytes)\n",
		len(payloads), len(result.ChannelLevels), length)
	return nil
}

func calibrateCommand(arguments *cliArguments) error {
	imagePath, err := arguments.Require("image")
	if err != nil {
		return err
	}
	outputPath, err := arguments.Require("output")
	if err != nil {
		return err
	}
	threshold, err := readByteRange(arguments, "image-threshold", 128, 0)
	if err != nil {
		return err
	}
	alphaThreshold, err := readByteRange(arguments, "alpha-threshold", 1, 1)
	if err != nil {
		return err
	}
	totalSectors, err := readTotalSectors(arguments)
	if err != nil {
		return err
	}
	startLba, err := arguments.GetUInt("lba", 0)
	if err != nil {
		return err
	}
	var fillSectors uint32
	if arguments.Has("fill-sectors") {
		fillSectors, err = arguments.GetUInt("fill-sectors", 0)
		if err != nil {
			return err
		}
	} else {
		fillSectors, err = checkedSubU32(totalSectors, startLba)
		if err != nil {
			return err
		}
	}
	generatedInner, err := requireDouble(arguments, "generated-inner-radius-mm")
	if err != nil {
		return err
	}
	generatedOuter, err := requireDouble(arguments, "generated-outer-radius-mm")
	if err != nil {
		return err
	}
	actualInner, err := requireDouble(arguments, "actual-inner-radius-mm")
	if err != nil {
		return err
	}
	actualOuter, err := requireDouble(arguments, "actual-outer-radius-mm")
	if err != nil {
		return err
	}
	channelBitNm, err := arguments.GetDouble("channel-bit-nm", 133.3)
	if err != nil {
		return err
	}
	clockwise, err := readClockwise(arguments)
	if err != nil {
		return err
	}
	previewSize, err := arguments.GetInt("preview-size", 1024)
	if err != nil {
		return err
	}
	samplesPerSector, err := arguments.GetInt("samples-per-sector", 16)
	if err != nil {
		return err
	}
	generatedPitchLinear, err := readGeneratedPitch(arguments, "linear")
	if err != nil {
		return err
	}
	generatedPitchQuadratic, err := readGeneratedPitch(arguments, "quadratic")
	if err != nil {
		return err
	}
	generatedPitchCubic, err := readGeneratedPitch(arguments, "cubic")
	if err != nil {
		return err
	}
	actualPitchLinear, err := arguments.GetDouble("actual-pitch-linear", 0)
	if err != nil {
		return err
	}
	actualPitchQuadratic, err := arguments.GetDouble("actual-pitch-quadratic", 0)
	if err != nil {
		return err
	}
	actualPitchCubic, err := arguments.GetDouble("actual-pitch-cubic", 0)
	if err != nil {
		return err
	}
	tone, err := readToneProfile(arguments)
	if err != nil {
		return err
	}
	options := calibrationPreviewOptions{
		TotalSectors:            totalSectors,
		StartLba:                startLba,
		FillSectors:             fillSectors,
		GeneratedInnerRadiusMm:  generatedInner,
		GeneratedOuterRadiusMm:  generatedOuter,
		ActualInnerRadiusMm:     actualInner,
		ActualOuterRadiusMm:     actualOuter,
		ChannelBitLengthNm:      channelBitNm,
		Clockwise:               clockwise,
		LuminanceThreshold:      byte(threshold),
		AlphaThreshold:          byte(alphaThreshold),
		PreviewSize:             previewSize,
		SamplesPerSector:        samplesPerSector,
		GeneratedPitchLinear:    generatedPitchLinear,
		GeneratedPitchQuadratic: generatedPitchQuadratic,
		GeneratedPitchCubic:     generatedPitchCubic,
		ActualPitchLinear:       actualPitchLinear,
		ActualPitchQuadratic:    actualPitchQuadratic,
		ActualPitchCubic:        actualPitchCubic,
		Tone:                    tone,
	}
	summary, err := calibrationPreviewRender(imagePath, outputPath, options)
	if err != nil {
		return err
	}
	if err := writeCalibrateJson(outputPath+".json", summary); err != nil {
		return err
	}
	fmt.Printf("preview: %dx%d, generated pitch=%s um, actual pitch=%s um, output=%s\n",
		summary.PreviewSize, summary.PreviewSize,
		formatFixed(summary.GeneratedTrackPitchMicrometres, 4),
		formatFixed(summary.ActualTrackPitchMicrometres, 4), outputPath)
	return nil
}

func simulateCommand(arguments *cliArguments) error {
	isoPath, err := arguments.Require("iso")
	if err != nil {
		return err
	}
	outputPath, err := arguments.Require("output")
	if err != nil {
		return err
	}
	sidecarStartLba, sidecarFillSectors, sidecarTotalSectors, err := isoReadbackReadSolveSummary(isoPath + ".json")
	if err != nil {
		return err
	}
	isoLength, err := fileLength(isoPath)
	if err != nil {
		return err
	}
	isoSectors := isoLength / dvdPayloadBytesPerSector
	var totalSectors uint32
	if arguments.Has("total-sectors") || arguments.Has("total-blocks") {
		totalSectors, err = readTotalSectors(arguments)
		if err != nil {
			return err
		}
	} else if sidecarTotalSectors != 0 {
		totalSectors = sidecarTotalSectors
	} else {
		if isoSectors > 0xFFFFFFFF {
			return newOverflowError("Arithmetic operation resulted in an overflow.")
		}
		totalSectors = uint32(isoSectors)
	}

	startLba, err := arguments.GetUInt("lba", sidecarStartLba)
	if err != nil {
		return err
	}
	var fillSectors uint32
	if arguments.Has("fill-sectors") {
		fillSectors, err = arguments.GetUInt("fill-sectors", 0)
		if err != nil {
			return err
		}
	} else if sidecarFillSectors != 0 {
		fillSectors = sidecarFillSectors
	} else {
		fillSectors, err = checkedSubU32(uint32(minInt64(isoSectors, int64(totalSectors))), startLba)
		if err != nil {
			return err
		}
	}

	psnOffset, err := arguments.GetUInt("psn-offset", 0x30000)
	if err != nil {
		return err
	}
	innerRadius, err := requireDouble(arguments, "inner-radius-mm")
	if err != nil {
		return err
	}
	outerRadius, err := requireDouble(arguments, "outer-radius-mm")
	if err != nil {
		return err
	}
	channelBitNm, err := arguments.GetDouble("channel-bit-nm", 133.3)
	if err != nil {
		return err
	}
	clockwise, err := readClockwise(arguments)
	if err != nil {
		return err
	}
	previewSize, err := arguments.GetInt("preview-size", 1024)
	if err != nil {
		return err
	}
	samplesPerSector, err := arguments.GetInt("samples-per-sector", 16)
	if err != nil {
		return err
	}
	pitchLinear, err := arguments.GetDouble("pitch-linear", 0)
	if err != nil {
		return err
	}
	pitchQuadratic, err := arguments.GetDouble("pitch-quadratic", 0)
	if err != nil {
		return err
	}
	pitchCubic, err := arguments.GetDouble("pitch-cubic", 0)
	if err != nil {
		return err
	}
	options := isoReadbackOptions{
		TotalSectors:       totalSectors,
		StartLba:           startLba,
		FillSectors:        fillSectors,
		PsnOffset:          psnOffset,
		InnerRadiusMm:      innerRadius,
		OuterRadiusMm:      outerRadius,
		ChannelBitLengthNm: channelBitNm,
		Clockwise:          clockwise,
		PreviewSize:        previewSize,
		SamplesPerSector:   samplesPerSector,
		PitchLinear:        pitchLinear,
		PitchQuadratic:     pitchQuadratic,
		PitchCubic:         pitchCubic,
	}
	summary, err := isoReadbackRender(isoPath, outputPath, options)
	if err != nil {
		return err
	}
	if err := writeReadbackJson(outputPath+".json", summary); err != nil {
		return err
	}
	fmt.Printf("readback: %dx%d, classified=%d/%d, pitch=%s um, output=%s\n",
		summary.PreviewSize, summary.PreviewSize,
		summary.ClassifiedSamples, summary.TotalSamples,
		formatFixed(summary.TrackPitchMicrometres, 4), outputPath)
	return nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func int32Ptr(v int32) *int32 { return &v }
