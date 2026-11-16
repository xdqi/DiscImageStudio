package dvd

import (
	"math"
	"time"
)

// multiBlockImageSolverSolve ports MultiBlockImageSolver.Solve.
func multiBlockImageSolverSolve(imagePath, isoOutputPath string, initialBoundary modulationBoundary, options multiBlockSolveOptions, progress func(multiBlockProgress)) (multiBlockSolveResult, error) {
	if err := multiBlockValidate(options); err != nil {
		return multiBlockSolveResult{}, err
	}
	image, err := rasterImageLoad(imagePath)
	if err != nil {
		return multiBlockSolveResult{}, err
	}
	totalBlocks := int(options.FillSectors / dvdEccSectorCount)
	boundary := initialBoundary
	var totalScore, totalScoreMaximum int64
	var totalGrayScore, totalGrayScoreMaximum int64
	var totalControlledScore, totalControlledScoreMaximum int64
	var totalControlledGrayScore, totalControlledGrayScoreMaximum int64
	stopwatchStart := time.Now()
	var isoSectors uint32
	if options.PreserveExistingIso {
		isoSectors = options.ImageMapping.outputSectors()
	} else {
		isoSectors, err = checkedAddU32(options.StartLba, options.FillSectors)
		if err != nil {
			return multiBlockSolveResult{}, err
		}
	}
	iso, err := newRawIsoImageWriter(isoOutputPath, isoSectors, options.PreserveExistingIso)
	if err != nil {
		return multiBlockSolveResult{}, err
	}
	for blockIndex := 0; blockIndex < totalBlocks; blockIndex++ {
		lba := options.StartLba + uint32(blockIndex)*dvdEccSectorCount
		constraint, err := imageTargetMapperMapInMemory(image, lba, options.ImageMapping)
		if err != nil {
			iso.close()
			return multiBlockSolveResult{}, err
		}
		blockSeed := options.RandomSeed + int32(blockIndex)*1_000_003
		initialPayload := make([]byte, dvdPayloadBytesPerBlock)
		if !options.ZeroInitialPayload {
			newDotnetRandom(blockSeed).nextBytes(initialPayload)
		}

		solverOpts := solverOptions{
			Iterations:         options.IterationsPerBlock,
			MutationBytes:      options.MutationBytes,
			RandomSeed:         blockSeed,
			InitialTemperature: options.InitialTemperature,
			FinalTemperature:   options.FinalTemperature,
			Algorithm:          options.Algorithm,
			BridgeBytes:        options.BridgeBytes,
			BeamWidth:          options.BeamWidth,
			RetryBeamWidth:     options.RetryBeamWidth,
			RetryFrameFraction: options.RetryFrameFraction,
			BatchFrames:        options.BatchFrames,
			BatchCandidates:    options.BatchCandidates,
			SearchLanes:        options.SearchLanes,
		}
		result, err := solverEngineSolve(constraint, initialPayload, lba, options.PsnOffset, boundary, solverOpts, nil)
		if err != nil {
			iso.close()
			return multiBlockSolveResult{}, err
		}
		if err := iso.writeBlock(lba, result.Payloads); err != nil {
			iso.close()
			return multiBlockSolveResult{}, err
		}
		boundary = result.Encoded.FinalBoundary
		totalScore += int64(result.Score)
		totalScoreMaximum += int64(result.ScoreMaximum)
		if result.GrayScore != nil {
			totalGrayScore += int64(*result.GrayScore)
		}
		if result.GrayScoreMaximum != nil {
			totalGrayScoreMaximum += int64(*result.GrayScoreMaximum)
		}
		if result.ControlledScore != nil {
			totalControlledScore += int64(*result.ControlledScore)
		}
		if result.ControlledScoreMaximum != nil {
			totalControlledScoreMaximum += int64(*result.ControlledScoreMaximum)
		}
		if result.ControlledGrayScore != nil {
			totalControlledGrayScore += int64(*result.ControlledGrayScore)
		}
		if result.ControlledGrayScoreMaximum != nil {
			totalControlledGrayScoreMaximum += int64(*result.ControlledGrayScoreMaximum)
		}
		if progress != nil {
			progress(multiBlockProgress{
				CompletedBlocks:            blockIndex + 1,
				TotalBlocks:                totalBlocks,
				BlockLba:                   lba,
				Score:                      result.Score,
				ScoreMaximum:               result.ScoreMaximum,
				ScoreMetric:                result.ScoreMetric,
				FinalBoundary:              boundary,
				Elapsed:                    time.Since(stopwatchStart),
				GrayScore:                  result.GrayScore,
				GrayScoreMaximum:           result.GrayScoreMaximum,
				GrayScoreMetric:            result.GrayScoreMetric,
				ControlledScore:            result.ControlledScore,
				ControlledScoreMaximum:     result.ControlledScoreMaximum,
				ControlledGrayScore:        result.ControlledGrayScore,
				ControlledGrayScoreMaximum: result.ControlledGrayScoreMaximum,
			})
		}
	}

	sparse := iso.sparseFile
	if err := iso.close(); err != nil {
		return multiBlockSolveResult{}, err
	}
	hasGrayScore := options.Algorithm == solverAlgorithmStateControl || options.Algorithm == solverAlgorithmDispersion
	var scoreMetric string
	switch options.Algorithm {
	case solverAlgorithmStateControl:
		scoreMetric = "all-channel-word-majority-error"
	case solverAlgorithmDispersion:
		scoreMetric = "payload-transition-class-error"
	default:
		scoreMetric = "bit-mismatch"
	}
	var grayMetric *string
	switch options.Algorithm {
	case solverAlgorithmStateControl:
		grayMetric = stringPtr("all-channel-word-gray-error")
	case solverAlgorithmDispersion:
		grayMetric = stringPtr("payload-transition-count-error")
	}
	result := multiBlockSolveResult{
		CompletedBlocks:   totalBlocks,
		FilledSectors:     options.FillSectors,
		TotalScore:        totalScore,
		TotalScoreMaximum: totalScoreMaximum,
		ScoreMetric:       scoreMetric,
		FinalBoundary:     boundary,
		Elapsed:           time.Since(stopwatchStart),
		SparseIso:         sparse,
		IsoSectors:        isoSectors,
		GrayScoreMetric:   grayMetric,
	}
	if hasGrayScore {
		result.TotalGrayScore = longPtr(totalGrayScore)
		result.TotalGrayScoreMaximum = longPtr(totalGrayScoreMaximum)
	}
	if options.Algorithm == solverAlgorithmStateControl {
		result.TotalControlledScore = longPtr(totalControlledScore)
		result.TotalControlledScoreMaximum = longPtr(totalControlledScoreMaximum)
		result.TotalControlledGrayScore = longPtr(totalControlledGrayScore)
		result.TotalControlledGrayScoreMaximum = longPtr(totalControlledGrayScoreMaximum)
	}
	return result, nil
}

func multiBlockValidate(options multiBlockSolveOptions) error {
	if options.ReadCompatibility {
		return newArgumentErrorParam("Read compatibility requires fast dispersion output.", "options")
	}
	if options.FillSectors < dvdEccSectorCount || options.FillSectors%dvdEccSectorCount != 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Range fill sector count must be a positive multiple of 16."}
	}
	samplingOuter := options.ImageMapping.samplingOuterRadiusMm()
	if options.ImageMapping.outputSectors() == 0 ||
		options.ImageMapping.outputSectors() > options.ImageMapping.TotalSectors ||
		math.IsNaN(samplingOuter) || math.IsInf(samplingOuter, 0) ||
		samplingOuter <= 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Output capacity and image radius must lie inside the disc geometry."}
	}
	if uint64(options.StartLba)+uint64(options.FillSectors) > uint64(options.ImageMapping.outputSectors()) {
		return &argumentOutOfRangeError{param: "options", msg: "Filled range exceeds the reported disc size."}
	}
	firstPsn, err := checkedAddU32(options.StartLba, options.PsnOffset)
	if err != nil {
		return err
	}
	if firstPsn&0xF != 0 {
		return newArgumentError("The first PSN must be aligned to a 16-sector ECC Block.")
	}
	if options.IterationsPerBlock < 0 ||
		options.MutationBytes < 1 ||
		options.BridgeBytes < 0 || options.BridgeBytes > efmPlusBytesPerSyncFrame ||
		options.BeamWidth < 1 || options.BeamWidth > 4096 ||
		options.RetryBeamWidth < 0 || options.RetryBeamWidth > 4096 ||
		math.IsNaN(options.RetryFrameFraction) ||
		options.RetryFrameFraction < 0 || options.RetryFrameFraction > 1 ||
		options.BatchFrames < 1 || options.BatchFrames > dvdSyncFrameCount ||
		options.BatchCandidates < 1 || options.BatchCandidates > 64 ||
		options.SearchLanes < 1 || options.SearchLanes > 8 {
		return &argumentOutOfRangeError{param: "options", msg: "Invalid solver iteration, mutation, bridge, beam, or adaptive retry option."}
	}
	return nil
}
