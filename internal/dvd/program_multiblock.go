package dvd

import (
	"context"
	"fmt"
	"strings"
)

// solveMultipleBlocks ports Program.SolveMultipleBlocks.
func solveMultipleBlocks(arguments *cliArguments, fillSectors uint32) error {
	hybridMode := arguments.Has("data-dir")
	filesAtOuterRing, err := readFilesAtOuterRing(arguments)
	if err != nil {
		return err
	}
	if !arguments.Has("image") || arguments.Has("target") {
		return newArgumentError("Range or fast output requires --image and does not accept --target.")
	}

	isoOutputPath, err := arguments.Require("iso-output")
	if err != nil {
		return err
	}
	unsupported := []string{
		"output",
		"iso-template",
		"iso-sectors",
		"seed-payload",
		"channel-output",
		"constraint-output",
	}
	for _, option := range unsupported {
		if arguments.Has(option) {
			return newArgumentError("--" + option + " is not supported with multi-block --fill-sectors.")
		}
	}

	if hybridMode && (arguments.Has("lba") || arguments.Has("fill-sectors")) {
		return newArgumentError(
			"Hybrid --data-dir mode calculates --lba and --fill-sectors automatically; " +
				"use --drawing-start-lba only when extra inner-ring space is required.")
	}

	if !hybridMode && arguments.Has("drawing-start-lba") {
		return newArgumentError("--drawing-start-lba requires --data-dir.")
	}

	psnOffset, err := arguments.GetUInt("psn-offset", 0x30000)
	if err != nil {
		return err
	}
	randomSeed, err := arguments.GetInt("seed", 1)
	if err != nil {
		return err
	}
	initialMode := arguments.Get("initial", "random")
	var zeroInitial bool
	if strings.EqualFold(initialMode, "zero") {
		zeroInitial = true
	} else if strings.EqualFold(initialMode, "random") {
		zeroInitial = false
	} else {
		return newArgumentError("--initial must be 'random' or 'zero'.")
	}
	algorithmFallback := "full"
	if hybridMode {
		algorithmFallback = "dispersion"
	}
	algorithm, err := readSolverAlgorithm(arguments, algorithmFallback)
	if err != nil {
		return err
	}
	fastOutput, err := readBoolean(arguments, "fast-output", hybridMode)
	if err != nil {
		return err
	}
	if fastOutput && algorithm != solverAlgorithmDispersion {
		return newArgumentError("--fast-output true requires --algorithm dispersion.")
	}

	readCompatibility, err := readBoolean(arguments, "read-compatibility", false)
	if err != nil {
		return err
	}
	if readCompatibility && !fastOutput {
		return newArgumentError("--read-compatibility true requires --fast-output true and --algorithm dispersion.")
	}

	var iterationsPerBlock int
	if arguments.Has("iterations-per-block") {
		iterationsPerBlock, err = arguments.GetInt("iterations-per-block", 0)
		if err != nil {
			return err
		}
	} else {
		iterationsDefault := 100
		if algorithm == solverAlgorithmStateControl {
			iterationsDefault = 3
		} else if algorithm == solverAlgorithmDispersion {
			iterationsDefault = 1
		}
		iterationsPerBlock, err = arguments.GetInt("iterations", iterationsDefault)
		if err != nil {
			return err
		}
	}
	if err := validateRetiredControlOptions(arguments, algorithm); err != nil {
		return err
	}
	imageMapping, err := readImageMappingOptions(arguments, 64)
	if err != nil {
		return err
	}
	discInitialBoundary, err := readBoundary(arguments)
	if err != nil {
		return err
	}
	drawingInitialBoundary := discInitialBoundary
	var hybridIso *hybridIsoWriteSummary
	var lba uint32
	if hybridMode {
		if psnOffset&0xF != 0 {
			return newArgumentError(
				"Hybrid mode requires --psn-offset to be aligned to a 16-sector ECC Block.")
		}

		dataDir, err := arguments.Require("data-dir")
		if err != nil {
			return err
		}
		isoLayout, err := hybridIsoPlan(
			dataDir,
			imageMapping.TotalSectors,
			arguments.Get("volume-label", "DVD_IMAGE"),
			filesAtOuterRing)
		if err != nil {
			return err
		}
		alignBase := isoLayout.DataEndLbaExclusive()
		if filesAtOuterRing {
			alignBase = isoLayout.MetadataEndLbaExclusive()
		}
		minimumDrawingLba, err := alignToEccBlock(alignBase, psnOffset)
		if err != nil {
			return err
		}
		if arguments.Has("drawing-start-lba") {
			lba, err = arguments.GetUInt("drawing-start-lba", 0)
			if err != nil {
				return err
			}
		} else {
			lba = minimumDrawingLba
		}
		if lba < minimumDrawingLba {
			return &argumentOutOfRangeError{param: "arguments", msg: "--drawing-start-lba must be at least " + itoa(int64(minimumDrawingLba)) + " so it does not overwrite files."}
		}

		lbaPlusPsn, err := checkedAddU32(lba, psnOffset)
		if err != nil {
			return err
		}
		if lbaPlusPsn&0xF != 0 {
			return newArgumentError(
				"--drawing-start-lba plus --psn-offset must be aligned to 16 sectors.")
		}

		if lba > imageMapping.TotalSectors {
			return &argumentOutOfRangeError{param: "arguments", msg: "--drawing-start-lba exceeds the reported disc size."}
		}

		var drawingEndLba uint32
		if filesAtOuterRing {
			drawingEndLba = isoLayout.FileDataStartLba() / dvdEccSectorCount * dvdEccSectorCount
		} else {
			drawingEndLba = imageMapping.TotalSectors
		}
		if drawingEndLba < lba {
			return newArgumentError("The selected drawing range overlaps the outer-ring files.")
		}
		fillSectors, err = checkedSubU32(drawingEndLba, lba)
		if err != nil {
			return err
		}
		fillSectors = fillSectors / dvdEccSectorCount * dvdEccSectorCount
		if fillSectors < dvdEccSectorCount {
			return newArgumentError(
				"The filesystem leaves no complete ECC Block for drawing in the selected file layout.")
		}

		hybridIso, err = hybridIsoCreate(isoLayout, isoOutputPath, context.Background())
		if err != nil {
			return err
		}

		fmt.Printf("hybrid ISO: files=%d, directories=%d, data=%d bytes, filesystem end LBA=%d, files=%s, drawing LBA %d..%d\n",
			hybridIso.FileCount, hybridIso.DirectoryCount,
			hybridIso.FileBytes, hybridIso.DataEndLbaExclusive,
			map[bool]string{true: "outer", false: "inner"}[filesAtOuterRing], lba, lba+fillSectors-1)
		if !fastOutput {
			fmt.Printf("replaying inner-ring modulation boundary: 0..%d\n", lba-1)
			drawingInitialBoundary, err = replayIsoBoundary(
				isoOutputPath,
				lba,
				psnOffset,
				discInitialBoundary)
			if err != nil {
				return err
			}
			fmt.Printf("drawing boundary: state=%d, DSV=%d, level=%s\n",
				drawingInitialBoundary.State, drawingInitialBoundary.Dsv, drawingInitialBoundary.LevelName())
		}
	} else {
		lba, err = arguments.GetUInt("lba", 0)
		if err != nil {
			return err
		}
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
	fastParallelism, err := arguments.GetInt("fast-parallelism", 0)
	if err != nil {
		return err
	}
	options := multiBlockSolveOptions{
		StartLba:              lba,
		FillSectors:           fillSectors,
		PsnOffset:             psnOffset,
		IterationsPerBlock:    iterationsPerBlock,
		MutationBytes:         mutationBytes,
		RandomSeed:            int32(randomSeed),
		InitialTemperature:    initialTemperature,
		FinalTemperature:      finalTemperature,
		ZeroInitialPayload:    zeroInitial,
		ImageMapping:          imageMapping,
		Algorithm:             algorithm,
		BridgeBytes:           bridgeBytes,
		BeamWidth:             beamWidth,
		RetryBeamWidth:        retryBeamWidth,
		RetryFrameFraction:    retryFrameFraction,
		BatchFrames:           batchFrames,
		BatchCandidates:       batchCandidates,
		SearchLanes:           searchLanes,
		FastParallelism:       fastParallelism,
		PreserveExistingIso:   hybridMode,
		UseSampleReuse:        true,
		ReadCompatibility:     readCompatibility,
		ReadCompatibilityMode: dvdReadCompatibilityCurrent,
	}
	totalBlocks := int(fillSectors / dvdEccSectorCount)
	progressEvery := maxInt(1, totalBlocks/100)
	if fastOutput {
		return writeFastDispersionImage(arguments, isoOutputPath, options, progressEvery, hybridIso)
	}

	lastLba, err := checkedAddU32(lba, fillSectors-1)
	if err != nil {
		return err
	}
	fmt.Printf("raw ISO fill: LBA %d..%d, ECC blocks=%d, iterations/block=%d, algorithm=%s, constraint step=%d\n",
		lba, lastLba, totalBlocks, iterationsPerBlock, algorithmName(algorithm), imageMapping.SampleEveryChannelBits)
	if algorithm == solverAlgorithmDispersion {
		fmt.Println(dispersionPaletteDescription())
	}
	imagePath, err := arguments.Require("image")
	if err != nil {
		return err
	}
	result, err := multiBlockImageSolverSolve(
		imagePath,
		isoOutputPath,
		drawingInitialBoundary,
		options,
		func(progress multiBlockProgress) {
			if progress.CompletedBlocks == 1 ||
				progress.CompletedBlocks == progress.TotalBlocks ||
				progress.CompletedBlocks%progressEvery == 0 {
				gray := ", "
				if progress.GrayScore != nil && progress.GrayScoreMaximum != nil {
					grayMetric := "gray-error"
					if progress.GrayScoreMetric != nil {
						grayMetric = *progress.GrayScoreMetric
					}
					gray = fmt.Sprintf(", %s=%d/%d, ", grayMetric, *progress.GrayScore, *progress.GrayScoreMaximum)
				}
				controlled := ""
				if progress.ControlledScore != nil && progress.ControlledScoreMaximum != nil {
					controlled = fmt.Sprintf("controlled-majority=%d/%d", *progress.ControlledScore, *progress.ControlledScoreMaximum)
					if progress.ControlledGrayScore != nil && progress.ControlledGrayScoreMaximum != nil {
						controlled += fmt.Sprintf(", controlled-gray=%d/%d, ", *progress.ControlledGrayScore, *progress.ControlledGrayScoreMaximum)
					} else {
						controlled += ", "
					}
				}
				fmt.Printf("block %d/%d, LBA=%d, %s=%d/%d%s%sstate=%d, DSV=%d, elapsed=%ss\n",
					progress.CompletedBlocks, progress.TotalBlocks, progress.BlockLba,
					progress.ScoreMetric, progress.Score, progress.ScoreMaximum,
					gray, controlled,
					progress.FinalBoundary.State, progress.FinalBoundary.Dsv,
					formatFixed(progress.Elapsed.Seconds(), 1))
			}
		})
	if err != nil {
		return err
	}

	if err := writeMultiBlockMetadata(
		isoOutputPath+".json",
		fullPath(imagePath),
		fullPath(isoOutputPath),
		lba,
		hybridIso,
		fillSectors,
		imageMapping,
		psnOffset,
		result,
		algorithm,
		iterationsPerBlock,
		options,
		drawingInitialBoundary,
		discInitialBoundary); err != nil {
		return err
	}
	gray := ", "
	if result.TotalGrayScore != nil && result.TotalGrayScoreMaximum != nil {
		grayMetric := "gray-error"
		if result.GrayScoreMetric != nil {
			grayMetric = *result.GrayScoreMetric
		}
		gray = fmt.Sprintf(", %s=%d/%d, ", grayMetric, *result.TotalGrayScore, *result.TotalGrayScoreMaximum)
	}
	controlled := ""
	if result.TotalControlledScore != nil && result.TotalControlledScoreMaximum != nil {
		controlled = fmt.Sprintf("controlled-majority=%d/%d", *result.TotalControlledScore, *result.TotalControlledScoreMaximum)
		if result.TotalControlledGrayScore != nil && result.TotalControlledGrayScoreMaximum != nil {
			controlled += fmt.Sprintf(", controlled-gray=%d/%d, ", *result.TotalControlledGrayScore, *result.TotalControlledGrayScoreMaximum)
		} else {
			controlled += ", "
		}
	}
	fmt.Printf("done: blocks=%d, %s=%d/%d%s%selapsed=%ss, output=%s\n",
		result.CompletedBlocks,
		result.ScoreMetric, result.TotalScore, result.TotalScoreMaximum,
		gray, controlled,
		formatFixed(result.Elapsed.Seconds(), 1), isoOutputPath)
	return nil
}

// writeFastDispersionImage ports Program.WriteFastDispersionImage.
func writeFastDispersionImage(arguments *cliArguments, isoOutputPath string, options multiBlockSolveOptions, progressEvery int, hybridIso *hybridIsoWriteSummary) error {
	workers := options.FastParallelism
	if workers == 0 {
		workers = processorCount()
	}
	lastLba, err := checkedAddU32(options.StartLba, options.FillSectors-1)
	if err != nil {
		return err
	}
	fmt.Printf("fast raw ISO fill: LBA %d..%d, ECC blocks=%d, algorithm=dispersion, ECC/EFM replay=skipped, workers=%d\n",
		options.StartLba, lastLba, options.FillSectors/dvdEccSectorCount, workers)
	fmt.Println(dispersionPaletteDescription() + ", " +
		"image sampled at every payload code-word center")

	if options.ReadCompatibility {
		fmt.Printf("read compatibility=%s (experimental drawing-byte protection)\n", options.ReadCompatibilityMode)
	}

	imagePath, err := arguments.Require("image")
	if err != nil {
		return err
	}
	result, err := fastDvdImageFileWriterWritePrepared(
		imagePath,
		isoOutputPath,
		options,
		hybridIso,
		func(progress fastDispersionProgress) {
			if progress.CompletedBlocks == 1 ||
				progress.CompletedBlocks == progress.TotalBlocks ||
				progress.CompletedBlocks%progressEvery == 0 {
				mebibytes := float64(progress.CompletedBlocks) *
					dvdPayloadBytesPerBlock /
					(1024 * 1024)
				rate := 0.0
				if progress.Elapsed.Seconds() > 0 {
					rate = mebibytes / progress.Elapsed.Seconds()
				}
				fmt.Printf("block %d/%d, LBA=%d, controlled=%d, rate=%s MiB/s, elapsed=%ss\n",
					progress.CompletedBlocks, progress.TotalBlocks,
					progress.BlockLba, progress.ControlledWords,
					formatFixed(rate, 2), formatFixed(progress.Elapsed.Seconds(), 1))
			}
		},
		context.Background())
	if err != nil {
		return err
	}

	totalMebibytes := float64(result.FilledSectors) *
		isoLogicalSectorBytes /
		(1024 * 1024)
	finalRate := 0.0
	if result.Elapsed.Seconds() > 0 {
		finalRate = totalMebibytes / result.Elapsed.Seconds()
	}
	compat := "payload-transition-class-error=0, "
	if options.ReadCompatibility {
		compat = fmt.Sprintf("compatibility changes=%d, payload-transition-class-error=not evaluated, ", result.CompatibilityChanges)
	}
	fmt.Printf("done: blocks=%d, controlled=%d, %sboundary=not replayed, rate=%s MiB/s, elapsed=%ss, output=%s\n",
		result.CompletedBlocks, result.ControlledWords,
		compat,
		formatFixed(finalRate, 2), formatFixed(result.Elapsed.Seconds(), 1),
		isoOutputPath)
	return nil
}
