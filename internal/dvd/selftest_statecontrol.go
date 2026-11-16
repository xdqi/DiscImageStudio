package dvd

import "fmt"

// testCodewordStateControl ports TestCodewordStateControl.
func testCodewordStateControl() {
	const firstPsn = 0x30000
	for _, entry := range efmPlusBinaryBytePairAtZeroDsv() {
		selfTestTrue(entry.Pair.BlackByte != entry.Pair.WhiteByte,
			fmt.Sprintf("binary pair bytes differ for state %d/%v", entry.State, entry.InitialIsLand))
		selfTestTrue(entry.Pair.BlackWorstLandCount < entry.Pair.WhiteWorstLandCount,
			fmt.Sprintf("binary pair preserves black/white order for state %d/%v", entry.State, entry.InitialIsLand))
	}

	negativeDsvPair, err := efmPlusBinaryBytePairGet(1, false, -32)
	selfTestTrue(err == nil, "negative DSV pair")
	positiveDsvPair, err := efmPlusBinaryBytePairGet(1, false, 32)
	selfTestTrue(err == nil, "positive DSV pair")
	selfTestTrue(negativeDsvPair.BlackByte != positiveDsvPair.BlackByte,
		"binary pair reacts to DSV direction")
	farPair, err := efmPlusBinaryBytePairGet(2, true, 1000)
	selfTestTrue(err == nil, "far pair")
	selfTestEqual(1000, farPair.ReferenceDsv, "binary pair uses exact out-of-cache DSV")

	random := newDotnetRandom(7819)
	for test := 0; test < 64; test++ {
		frame := make([]byte, efmPlusBytesPerSyncFrame)
		random.nextBytes(frame)
		desired := make([]int8, efmPlusBytesPerSyncFrame)
		for index := range desired {
			if random.nextDouble() < 0.7 {
				desired[index] = -1
			} else {
				desired[index] = int8(random.nextMax(17))
			}
		}

		input := modulationBoundary{
			State:  byte(random.nextRange(1, 5)),
			Dsv:    int(random.nextRange(-128, 129)),
			IsLand: random.nextMax(2) != 0,
		}
		syncCategory := int(random.nextMax(8))
		physical := make([]byte, efmPlusBitsPerSyncFrame)
		exactBoundary, err := newEfmPlusEncoder().encodeSyncFrame(frame, syncCategory, input, physical)
		selfTestTrue(err == nil, "encodeSyncFrame")
		exactBinaryErrors := 0
		exactGrayError := 0
		for byteIndex := 0; byteIndex < len(desired); byteIndex++ {
			if desired[byteIndex] < 0 {
				continue
			}

			landCount := 0
			channelOffset := 32 + byteIndex*16
			for bit := 0; bit < 16; bit++ {
				landCount += int(physical[channelOffset+bit])
			}

			if (desired[byteIndex] < 8 && landCount >= 8) ||
				(desired[byteIndex] > 8 && landCount <= 8) {
				exactBinaryErrors++
			}

			exactGrayError += absInt(landCount - int(desired[byteIndex]))
		}

		wordScore, err := new(efmPlusWordScorer).scoreSyncFrame(frame, syncCategory, input, desired, -1, -1)
		selfTestTrue(err == nil, "scoreSyncFrame")
		selfTestEqual(exactBoundary, wordScore.Boundary, fmt.Sprintf("word scorer SYNC/DSV boundary %d", test))
		selfTestEqual(exactBinaryErrors, wordScore.BinaryErrors, fmt.Sprintf("word scorer majority error %d", test))
		selfTestEqual(exactGrayError, wordScore.GrayError, fmt.Sprintf("word scorer gray error %d", test))

		firstSyncTarget := int8(random.nextMax(2) * 16)
		secondSyncTarget := int8(random.nextMax(2) * 16)
		syncBinaryErrors := 0
		syncGrayError := 0
		for syncWord := 0; syncWord < 2; syncWord++ {
			syncTarget := firstSyncTarget
			if syncWord == 1 {
				syncTarget = secondSyncTarget
			}
			landCount := countLandBits(physical[syncWord*16 : syncWord*16+16])
			if (syncTarget < 8 && landCount >= 8) ||
				(syncTarget > 8 && landCount <= 8) {
				syncBinaryErrors++
			}

			syncGrayError += absInt(landCount - int(syncTarget))
		}

		syncScore, err := new(efmPlusWordScorer).scoreSyncFrame(
			frame,
			syncCategory,
			input,
			desired,
			firstSyncTarget,
			secondSyncTarget)
		selfTestTrue(err == nil, "scoreSyncFrame observed")
		selfTestEqual(exactBoundary, syncScore.Boundary, fmt.Sprintf("word scorer observed SYNC boundary %d", test))
		selfTestEqual(exactBinaryErrors+syncBinaryErrors, syncScore.BinaryErrors,
			fmt.Sprintf("word scorer observed SYNC majority %d", test))
		selfTestEqual(exactGrayError+syncGrayError, syncScore.GrayError,
			fmt.Sprintf("word scorer observed SYNC gray %d", test))

		prefix, err := efmPlusPrefixStateInitialize(input, syncCategory)
		selfTestTrue(err == nil, "prefix init")
		for index := 0; index < len(frame); index++ {
			prefix, err = prefix.advance(frame[index], desired[index])
			selfTestTrue(err == nil, "prefix advance")
		}

		selected := prefix.selected()
		if selected.Dsv >= -64 && selected.Dsv <= 63 {
			selfTestEqual(modulationBoundary{State: selected.State, Dsv: selected.Dsv, IsLand: selected.IsLand}, exactBoundary,
				fmt.Sprintf("prefix state boundary %d", test))
			selfTestEqual(selected.GrayError, exactGrayError, fmt.Sprintf("prefix state gray error %d", test))
			selfTestEqual(selected.BinaryErrors, exactBinaryErrors, fmt.Sprintf("prefix state majority error %d", test))
		}
	}

	black := rasterImageCreateForTest(8, 8, 0, 0, 0, 255)
	mappedBlack, err := imageTargetMapperMapInMemory(black, 0, imageMappingOptions{
		TotalSectors:           16,
		InnerRadiusMm:          24,
		OuterRadiusMm:          24.1,
		ChannelBitLengthNm:     133.33,
		StartAngleDegrees:      0,
		Clockwise:              false,
		LuminanceThreshold:     128,
		AlphaThreshold:         1,
		SampleEveryChannelBits: 16,
	})
	selfTestTrue(err == nil, "map black")
	observedCount := 0
	for _, target := range mappedBlack.channelWordTargets {
		if target >= 0 {
			observedCount++
		}
	}
	selfTestEqual(dvdChannelBitsPerBlock/16, observedCount,
		"opaque image observes every data and SYNC channel word")
	alternatingTargets := make([]int8, dvdChannelBitsPerBlock)
	for i := range alternatingTargets {
		alternatingTargets[i] = constraintUnconstrained
	}
	for payloadIndex := 0; payloadIndex < dvdPayloadBytesPerBlock; payloadIndex += 16 {
		channelOffset, err := payloadDirectChannelOffset(payloadIndex)
		selfTestTrue(err == nil, "channel offset")
		for i := 0; i < 16; i++ {
			alternatingTargets[channelOffset+i] = int8((payloadIndex / 16) & 1)
		}
	}

	constraint, err := imageConstraintCreate(alternatingTargets, mappedBlack.imageMapping, nil, nil)
	selfTestTrue(err == nil, "create alternating constraint")
	tiedImageTargets := make([]int8, dvdChannelBitsPerBlock)
	for i := range tiedImageTargets {
		tiedImageTargets[i] = constraintUnconstrained
	}
	tiedChannelOffset, err := payloadDirectChannelOffset(100)
	selfTestTrue(err == nil, "channel offset 100")
	for i := 0; i < 8; i++ {
		tiedImageTargets[tiedChannelOffset+i] = 0
	}
	for i := 0; i < 8; i++ {
		tiedImageTargets[tiedChannelOffset+8+i] = 1
	}
	tiedConstraint, err := imageConstraintCreate(tiedImageTargets, mappedBlack.imageMapping, nil, nil)
	selfTestTrue(err == nil, "create tied constraint")
	tiedImageTarget, err := codewordGrayTargetCreate(tiedConstraint, 0)
	selfTestTrue(err == nil, "tied gray target")
	tiedRecordingIndex, err := payloadRecordingFrameIndex(100)
	selfTestTrue(err == nil, "recording index 100")
	selfTestEqual(int8(16), tiedImageTarget.desiredAtRecordingIndex(tiedRecordingIndex),
		"binary image tie uses center sample")

	initialPayload := make([]byte, dvdPayloadBytesPerBlock)
	random.nextBytes(initialPayload)
	options := solverOptions{
		Iterations:         3,
		MutationBytes:      4,
		RandomSeed:         31,
		InitialTemperature: 64,
		FinalTemperature:   0.25,
		Algorithm:          solverAlgorithmStateControl,
		BridgeBytes:        2,
		// Preserve the quality threshold for sparse alternating targets after ECC propagation.
		BeamWidth:          16,
		RetryBeamWidth:     24,
		RetryFrameFraction: 0.25,
		BatchFrames:        8,
		BatchCandidates:    4,
		SearchLanes:        2,
	}
	boundary := modulationBoundary{State: 1, Dsv: 0, IsLand: true}
	grayTarget, err := codewordGrayTargetCreate(constraint, options.BridgeBytes)
	selfTestTrue(err == nil, "gray target create")
	initialFrames, err := buildRecordingFrames(initialPayload, firstPsn)
	selfTestTrue(err == nil, "initial frames")
	initialEvaluation, err := grayTarget.evaluate(initialFrames, boundary, nil, nil)
	selfTestTrue(err == nil, "initial evaluate")
	result, err := solverEngineSolve(constraint, initialPayload, 0, firstPsn, boundary, options, nil)
	selfTestTrue(err == nil, "state-control solve")
	selfTestTrue(result.ControlledScore != nil, "Missing controlled majority score.")
	selfTestTrue(result.ControlledGrayScore != nil, "Missing controlled gray score.")
	selfTestTrue(result.ControlledScoreMaximum != nil, "Missing controlled majority maximum.")
	controlledScore := *result.ControlledScore
	controlledGrayError := *result.ControlledGrayScore
	controlledMaximum := *result.ControlledScoreMaximum
	selfTestTrue(
		controlledScore < initialEvaluation.BinaryErrors ||
			(controlledScore == initialEvaluation.BinaryErrors &&
				controlledGrayError <= initialEvaluation.GrayError),
		"batch search never worsens the exact block score")
	selfTestEqual(0, len(result.Encoded.ChannelLevels), "state-control avoids final bit materialization")
	selfTestEqual("all-channel-word-majority-error", result.ScoreMetric, "state-control score metric")
	selfTestTrue(result.GrayScoreMetric != nil, "gray metric presence")
	selfTestEqual("all-channel-word-gray-error", *result.GrayScoreMetric, "state-control gray metric")
	selfTestTrue(result.GrayScore != nil, "Missing state-control gray score.")
	resultGrayError := *result.GrayScore
	selfTestTrue(
		controlledScore < initialEvaluation.BinaryErrors ||
			(controlledScore == initialEvaluation.BinaryErrors &&
				controlledGrayError < initialEvaluation.GrayError),
		"state-control improves majority/gray error lexicographically")
	selfTestTrue(int64(controlledScore)*2 < int64(controlledMaximum),
		"ECC-closed batch search beats random controlled majority")
	exact, err := newDvdEccBlockEncoder().encode(result.Payloads, 0, firstPsn, boundary)
	selfTestTrue(err == nil, "exact encode")
	selfTestEqual(exact.FinalBoundary, result.Encoded.FinalBoundary, "state-control exact final boundary")
	resultFrames, err := buildRecordingFrames(result.Payloads, firstPsn)
	selfTestTrue(err == nil, "result frames")
	frameInputs := make([]modulationBoundary, dvdSyncFrameCount)
	frameEvaluations := make([]codewordFrameEvaluation, dvdSyncFrameCount)
	evaluated, err := grayTarget.evaluate(resultFrames, boundary, frameInputs, frameEvaluations)
	selfTestTrue(err == nil, "evaluate")
	selfTestEqual(evaluated.BinaryErrors, controlledScore, "state-control controlled majority score")
	selfTestEqual(evaluated.GrayError, controlledGrayError, "state-control controlled gray score")
	sumBinary := 0
	sumGray := 0
	for _, frame := range frameEvaluations {
		sumBinary += frame.BinaryErrors
		sumGray += frame.GrayError
	}
	selfTestEqual(evaluated.BinaryErrors, sumBinary, "adaptive frame majority scores sum to block score")
	selfTestEqual(evaluated.GrayError, sumGray, "adaptive frame gray scores sum to block score")
	selfTestEqual(evaluated.FinalBoundary, result.Encoded.FinalBoundary, "state-control word boundary")
	selfTestTrue(result.ControlledScoreMaximum != nil, "controlled maximum presence")
	selfTestEqual(grayTarget.scoredWords, *result.ControlledScoreMaximum, "state-control controlled score maximum")
	selfTestTrue(result.ControlledGrayScoreMaximum != nil, "controlled gray maximum presence")
	selfTestEqual(grayTarget.maximumGrayError(), *result.ControlledGrayScoreMaximum,
		"state-control controlled gray maximum")

	observed, err := grayTarget.evaluateObserved(resultFrames, boundary)
	selfTestTrue(err == nil, "evaluate observed")
	selfTestEqual(observed.BinaryErrors, result.Score, "state-control all-word majority score")
	selfTestEqual(observed.GrayError, resultGrayError, "state-control all-word gray score")
	selfTestEqual(grayTarget.observedWords, result.ScoreMaximum, "state-control all-word maximum")
	selfTestTrue(result.GrayScoreMaximum != nil, "gray maximum presence")
	selfTestEqual(grayTarget.maximumObservedGrayError(), *result.GrayScoreMaximum,
		"state-control all-word gray maximum")
	exactObservedBinaryErrors := 0
	exactObservedGrayError := 0
	observationTargets := constraint.channelWordTargets
	for wordIndex := 0; wordIndex < len(observationTargets); wordIndex++ {
		wordTarget := observationTargets[wordIndex]
		if wordTarget < 0 {
			continue
		}

		landCount := countLandBits(exact.ChannelLevels[wordIndex*16 : wordIndex*16+16])
		if (wordTarget < 8 && landCount >= 8) ||
			(wordTarget > 8 && landCount <= 8) {
			exactObservedBinaryErrors++
		}

		exactObservedGrayError += absInt(landCount - int(wordTarget))
	}

	selfTestEqual(exactObservedBinaryErrors, result.Score, "all-word majority matches channel levels")
	selfTestEqual(exactObservedGrayError, resultGrayError, "all-word gray matches channel levels")

	transparent := rasterImageCreateForTest(8, 8, 0, 0, 0, 0)
	emptyConstraint, err := imageTargetMapperMapInMemory(transparent, 0, imageMappingOptions{
		TotalSectors:           16,
		InnerRadiusMm:          24,
		OuterRadiusMm:          24.1,
		ChannelBitLengthNm:     133.33,
		StartAngleDegrees:      0,
		Clockwise:              false,
		LuminanceThreshold:     128,
		AlphaThreshold:         1,
		SampleEveryChannelBits: 256,
	})
	selfTestTrue(err == nil, "map transparent")
	emptyResult, err := solverEngineSolve(emptyConstraint, initialPayload, 0, firstPsn, boundary, options, nil)
	selfTestTrue(err == nil, "transparent solve")
	selfTestEqual(0, emptyResult.Score, "transparent control block score")
	selfTestEqual(0, emptyResult.CompletedIterations, "transparent control block iterations")
	selfTestEqual(0, emptyResult.ScoreMaximum, "transparent control block score maximum")
	selfTestTrue(emptyResult.GrayScoreMaximum != nil, "gray maximum presence")
	selfTestEqual(0, *emptyResult.GrayScoreMaximum, "transparent gray score maximum")
}
