package dvd

// codewordBlockEvaluation / codewordFrameEvaluation port the record structs.
type codewordBlockEvaluation struct {
	BinaryErrors  int
	GrayError     int
	FinalBoundary modulationBoundary
}

type codewordFrameEvaluation struct {
	BinaryErrors  int
	GrayError     int
	FinalBoundary modulationBoundary
}

// codewordGrayTarget ports CodewordGrayTarget.
type codewordGrayTarget struct {
	desiredLandCounts  []int8
	observedLandCounts []int8
	scoredWords        int
	observedWords      int
}

func (t *codewordGrayTarget) maximumGrayError() int         { return t.scoredWords * 16 }
func (t *codewordGrayTarget) maximumObservedGrayError() int { return t.observedWords * 16 }

func codewordGrayTargetCreate(constraint *imageConstraint, bridgeBytes int) (*codewordGrayTarget, error) {
	if bridgeBytes < 0 || bridgeBytes > efmPlusBytesPerSyncFrame {
		return nil, &argumentOutOfRangeError{param: "bridgeBytes"}
	}
	desired := make([]int8, 208*182)
	for i := range desired {
		desired[i] = -1
	}
	scoredWords := 0
	binaryImageTargets := constraint.imageMapping != nil
	for payloadIndex := 0; payloadIndex < dvdPayloadBytesPerBlock; payloadIndex++ {
		channelOffset, err := payloadDirectChannelOffset(payloadIndex)
		if err != nil {
			return nil, err
		}
		samples := 0
		landSamples := 0
		tieBreaker := int8(-1)
		tieBreakerDistance := int(^uint(0) >> 1)
		for bit := 0; bit < 16; bit++ {
			target := constraint.targetAt(channelOffset + bit)
			if target < 0 {
				continue
			}
			samples++
			landSamples += int(target)
			centerDistance := absInt(bit - 8)
			if centerDistance < tieBreakerDistance {
				tieBreaker = target
				tieBreakerDistance = centerDistance
			}
		}
		if samples == 0 {
			continue
		}
		recordingIndex, err := payloadRecordingFrameIndex(payloadIndex)
		if err != nil {
			return nil, err
		}
		if binaryImageTargets {
			if landSamples*2 == samples {
				desired[recordingIndex] = tieBreaker * 16
			} else if landSamples*2 > samples {
				desired[recordingIndex] = 16
			} else {
				desired[recordingIndex] = 0
			}
		} else {
			desired[recordingIndex] = int8((landSamples*16 + samples/2) / samples)
		}
		scoredWords++
	}

	if bridgeBytes > 0 {
		payloadWordsBySyncFrame := make([][]int, dvdSyncFrameCount)
		for i := range payloadWordsBySyncFrame {
			payloadWordsBySyncFrame[i] = []int{}
		}
		for payloadIndex := 0; payloadIndex < dvdPayloadBytesPerBlock; payloadIndex++ {
			recordingIndex, err := payloadRecordingFrameIndex(payloadIndex)
			if err != nil {
				return nil, err
			}
			payloadWordsBySyncFrame[recordingIndex/efmPlusBytesPerSyncFrame] =
				append(payloadWordsBySyncFrame[recordingIndex/efmPlusBytesPerSyncFrame], recordingIndex)
		}
		for _, payloadWords := range payloadWordsBySyncFrame {
			count := minInt(bridgeBytes, len(payloadWords))
			for index := 0; index < count; index++ {
				scoredWords = clearCodewordTarget(payloadWords[index], desired, scoredWords)
				scoredWords = clearCodewordTarget(payloadWords[len(payloadWords)-(index+1)], desired, scoredWords)
			}
		}
	}

	observed := append([]int8(nil), constraint.channelWordTargets...)
	observedWords := 0
	for _, target := range observed {
		if target >= 0 {
			observedWords++
		}
	}
	return &codewordGrayTarget{
		desiredLandCounts:  desired,
		scoredWords:        scoredWords,
		observedLandCounts: observed,
		observedWords:      observedWords,
	}, nil
}

func clearCodewordTarget(recordingIndex int, desired []int8, scoredWords int) int {
	if desired[recordingIndex] < 0 {
		return scoredWords
	}
	desired[recordingIndex] = -1
	return scoredWords - 1
}

func (t *codewordGrayTarget) frameTargets(syncFrameIndex int) []int8 {
	return t.desiredLandCounts[syncFrameIndex*efmPlusBytesPerSyncFrame : syncFrameIndex*efmPlusBytesPerSyncFrame+efmPlusBytesPerSyncFrame]
}

func (t *codewordGrayTarget) desiredAtRecordingIndex(recordingIndex int) int8 {
	return t.desiredLandCounts[recordingIndex]
}

func (t *codewordGrayTarget) evaluate(recordingFrames []byte, initialBoundary modulationBoundary, syncFrameInputs []modulationBoundary, syncFrameEvaluations []codewordFrameEvaluation) (codewordBlockEvaluation, error) {
	return t.evaluateCore(recordingFrames, initialBoundary, false, syncFrameInputs, syncFrameEvaluations)
}

func (t *codewordGrayTarget) evaluateObserved(recordingFrames []byte, initialBoundary modulationBoundary) (codewordBlockEvaluation, error) {
	return t.evaluateCore(recordingFrames, initialBoundary, true, nil, nil)
}

func (t *codewordGrayTarget) evaluateCore(recordingFrames []byte, initialBoundary modulationBoundary, observeAllWords bool, syncFrameInputs []modulationBoundary, syncFrameEvaluations []codewordFrameEvaluation) (codewordBlockEvaluation, error) {
	if len(recordingFrames) != len(t.desiredLandCounts) {
		return codewordBlockEvaluation{}, newArgumentErrorParam("Unexpected recording-frame byte count.", "recordingFrames")
	}
	if syncFrameInputs != nil && len(syncFrameInputs) != dvdSyncFrameCount {
		return codewordBlockEvaluation{}, newArgumentErrorParam("Unexpected Sync Frame trace length.", "syncFrameInputs")
	}
	if syncFrameEvaluations != nil && len(syncFrameEvaluations) != dvdSyncFrameCount {
		return codewordBlockEvaluation{}, newArgumentErrorParam("Unexpected Sync Frame evaluation length.", "syncFrameEvaluations")
	}

	scorer := &efmPlusWordScorer{}
	boundary := initialBoundary
	binaryErrors := 0
	grayError := 0
	for syncFrame := 0; syncFrame < dvdSyncFrameCount; syncFrame++ {
		if syncFrameInputs != nil {
			syncFrameInputs[syncFrame] = boundary
		}
		recordingOffset := syncFrame * efmPlusBytesPerSyncFrame
		channelWordOffset := syncFrame * (efmPlusBitsPerSyncFrame / 16)
		var desired []int8
		if observeAllWords {
			desired = t.observedLandCounts[channelWordOffset+2 : channelWordOffset+2+efmPlusBytesPerSyncFrame]
		} else {
			desired = t.frameTargets(syncFrame)
		}
		firstSyncTarget := int8(-1)
		secondSyncTarget := int8(-1)
		if observeAllWords {
			firstSyncTarget = t.observedLandCounts[channelWordOffset]
			secondSyncTarget = t.observedLandCounts[channelWordOffset+1]
		}
		category, err := syncCategory(syncFrame)
		if err != nil {
			return codewordBlockEvaluation{}, err
		}
		score, err := scorer.scoreSyncFrame(
			recordingFrames[recordingOffset:recordingOffset+efmPlusBytesPerSyncFrame],
			category,
			boundary,
			desired,
			firstSyncTarget,
			secondSyncTarget)
		if err != nil {
			return codewordBlockEvaluation{}, err
		}
		if syncFrameEvaluations != nil {
			syncFrameEvaluations[syncFrame] = codewordFrameEvaluation{
				BinaryErrors:  score.BinaryErrors,
				GrayError:     score.GrayError,
				FinalBoundary: score.Boundary,
			}
		}
		binaryErrors += score.BinaryErrors
		grayError += score.GrayError
		boundary = score.Boundary
	}

	return codewordBlockEvaluation{BinaryErrors: binaryErrors, GrayError: grayError, FinalBoundary: boundary}, nil
}
