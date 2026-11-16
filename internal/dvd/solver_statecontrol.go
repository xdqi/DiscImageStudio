package dvd

import (
	"math"
	"sort"
	"time"
)

// stateControlSolver ports StateControlSolver.
type stateControlSolver struct{}

var payloadByRecordingIndex = buildPayloadMap()

func buildPayloadMap() []int {
	result := make([]int, 208*182)
	for i := range result {
		result[i] = -1
	}
	for payloadIndex := 0; payloadIndex < dvdPayloadBytesPerBlock; payloadIndex++ {
		idx, err := payloadRecordingFrameIndex(payloadIndex)
		if err != nil {
			panic(err)
		}
		result[idx] = payloadIndex
	}
	return result
}

type frameChoice struct {
	PayloadIndexes []int
	PayloadValues  []byte
	FinalBoundary  modulationBoundary
}

type frameCandidate struct {
	Bytes []byte
	Score efmPlusWordScore
}

type batchCandidateResult struct {
	Payload          []byte
	Frames           []byte
	FrameInputs      []modulationBoundary
	FrameEvaluations []codewordFrameEvaluation
	Evaluation       codewordBlockEvaluation
}

type laneResult struct {
	State           *scSearchState
	CompletedPasses int
}

type acceptancePolicy struct {
	Random      *dotnetRandom
	Temperature float64
}

type scSearchState struct {
	Payload          []byte
	Frames           []byte
	FrameInputs      []modulationBoundary
	FrameEvaluations []codewordFrameEvaluation
	Evaluation       codewordBlockEvaluation
}

type beamNode struct {
	Prefix efmPlusPrefixState
}

type beamExpansion struct {
	Prefix      efmPlusPrefixState
	ParentIndex int
	Value       byte
}

type frameRank struct {
	FrameIndex   int
	BinaryErrors int
	GrayError    int
	TargetWords  int
}

func (s *stateControlSolver) solve(constraint *imageConstraint, initialPayloads []byte, initialLba, psnOffset uint32, initialBoundary modulationBoundary, options solverOptions, progress func(iteration, current, best int)) (solverResult, error) {
	if err := stateControlValidate(initialPayloads, options); err != nil {
		return solverResult{}, err
	}
	firstPsn, err := checkedAddU32(initialLba, psnOffset)
	if err != nil {
		return solverResult{}, err
	}
	if firstPsn&0xF != 0 {
		return solverResult{}, newArgumentError("The first PSN must be aligned to a 16-sector ECC Block.")
	}

	stopwatchStart := time.Now()
	target, err := codewordGrayTargetCreate(constraint, options.BridgeBytes)
	if err != nil {
		return solverResult{}, err
	}
	initialPayloadCopy := append([]byte(nil), initialPayloads...)
	initialState, err := scCreateInitialState(initialPayloadCopy, firstPsn, initialBoundary, target)
	if err != nil {
		return solverResult{}, err
	}
	lanes := make([]laneResult, options.SearchLanes)
	for laneIndex := 0; laneIndex < options.SearchLanes; laneIndex++ {
		laneSeed := options.RandomSeed + int32(laneIndex)*0x4F1BBCDC
		var laneState *scSearchState
		if laneIndex == 0 {
			laneState = scCloneState(initialState)
		} else {
			restartPayload := append([]byte(nil), initialPayloadCopy...)
			newDotnetRandom(laneSeed).nextBytes(restartPayload)
			laneState, err = scCreateInitialState(restartPayload, firstPsn, initialBoundary, target)
			if err != nil {
				return solverResult{}, err
			}
		}
		var policy *acceptancePolicy
		if laneIndex != 0 {
			policy = &acceptancePolicy{Random: newDotnetRandom(laneSeed)}
		}
		var laneProgress func(int, int, int)
		if laneIndex == 0 {
			laneProgress = progress
		}
		lanes[laneIndex], err = scRunLane(laneState, firstPsn, initialBoundary, target, options, false, policy, laneProgress)
		if err != nil {
			return solverResult{}, err
		}
	}

	bestLane := lanes[0]
	for laneIndex := 1; laneIndex < len(lanes); laneIndex++ {
		if scIsBetter(lanes[laneIndex].State.Evaluation, bestLane.State.Evaluation) {
			bestLane = lanes[laneIndex]
		}
	}

	state := bestLane.State
	bestPayload := state.Payload
	best := state.Evaluation
	completed := bestLane.CompletedPasses
	bestRecordingFrames, err := buildRecordingFrames(bestPayload, firstPsn)
	if err != nil {
		return solverResult{}, err
	}
	observed, err := target.evaluateObserved(bestRecordingFrames, initialBoundary)
	if err != nil {
		return solverResult{}, err
	}
	summary := encodedEccBlock{ChannelLevels: []byte{}, FinalBoundary: observed.FinalBoundary, FirstPhysicalSectorNumber: firstPsn}
	return solverResult{
		Payloads:                   bestPayload,
		Encoded:                    summary,
		Score:                      observed.BinaryErrors,
		ScoreMaximum:               target.observedWords,
		ScoreMetric:                "all-channel-word-majority-error",
		CompletedIterations:        completed,
		Elapsed:                    time.Since(stopwatchStart),
		GrayScore:                  intPtr(observed.GrayError),
		GrayScoreMaximum:           intPtr(target.maximumObservedGrayError()),
		GrayScoreMetric:            stringPtr("all-channel-word-gray-error"),
		ControlledScore:            intPtr(best.BinaryErrors),
		ControlledScoreMaximum:     intPtr(target.scoredWords),
		ControlledScoreMetric:      stringPtr("controlled-payload-majority-error"),
		ControlledGrayScore:        intPtr(best.GrayError),
		ControlledGrayScoreMaximum: intPtr(target.maximumGrayError()),
		ControlledGrayScoreMetric:  stringPtr("controlled-payload-gray-error"),
	}, nil
}

func scCreateInitialState(initialPayloads []byte, firstPsn uint32, initialBoundary modulationBoundary, target *codewordGrayTarget) (*scSearchState, error) {
	payload := append([]byte(nil), initialPayloads...)
	frames, err := buildRecordingFrames(payload, firstPsn)
	if err != nil {
		return nil, err
	}
	frameInputs := make([]modulationBoundary, dvdSyncFrameCount)
	frameEvaluations := make([]codewordFrameEvaluation, dvdSyncFrameCount)
	evaluation, err := target.evaluate(frames, initialBoundary, frameInputs, frameEvaluations)
	if err != nil {
		return nil, err
	}
	return &scSearchState{Payload: payload, Frames: frames, FrameInputs: frameInputs, FrameEvaluations: frameEvaluations, Evaluation: evaluation}, nil
}

func scCloneState(state *scSearchState) *scSearchState {
	return &scSearchState{
		Payload:          append([]byte(nil), state.Payload...),
		Frames:           append([]byte(nil), state.Frames...),
		FrameInputs:      append([]modulationBoundary(nil), state.FrameInputs...),
		FrameEvaluations: append([]codewordFrameEvaluation(nil), state.FrameEvaluations...),
		Evaluation:       state.Evaluation,
	}
}

func scRunLane(state *scSearchState, firstPsn uint32, initialBoundary modulationBoundary, target *codewordGrayTarget, options solverOptions, groupedWarmup bool, policy *acceptancePolicy, progress func(iteration, current, best int)) (laneResult, error) {
	bestState := scCloneState(state)
	completed := 0
	for pass := 0; pass < options.Iterations &&
		(state.Evaluation.BinaryErrors != 0 || state.Evaluation.GrayError != 0); pass++ {
		if policy != nil {
			fraction := 1.0
			if options.Iterations > 1 {
				fraction = float64(pass) / float64(options.Iterations-1)
			}
			policy.Temperature = math.Max(
				math.SmallestNonzeroFloat64,
				(options.InitialTemperature+
					(options.FinalTemperature-options.InitialTemperature)*fraction)/64.0)
		}

		ranked := scRankFrames(state.FrameEvaluations, target)
		if len(ranked) == 0 {
			break
		}

		var selectedCount int
		if options.RetryFrameFraction > 0 {
			selectedCount = clampInt(int(math.Ceil(float64(len(ranked))*options.RetryFrameFraction)), 1, len(ranked))
		} else {
			selectedCount = len(ranked)
		}
		selectedFrames := make([]int, selectedCount)
		for i := 0; i < selectedCount; i++ {
			selectedFrames[i] = ranked[i].FrameIndex
		}
		improvedPass, err := scTryBatchCandidates(
			state,
			selectedFrames,
			firstPsn,
			initialBoundary,
			target,
			options,
			options.BeamWidth,
			groupedWarmup,
			true,
			policy)
		if err != nil {
			return laneResult{}, err
		}
		bestState = scCaptureBest(state, bestState)
		for offset := 0; offset < selectedCount; offset += options.BatchFrames {
			count := minInt(options.BatchFrames, selectedCount-offset)
			batch := make([]int, count)
			for i := 0; i < count; i++ {
				batch[i] = ranked[offset+i].FrameIndex
			}
			improvedBatch, err := scTryBatch(
				state,
				batch,
				firstPsn,
				initialBoundary,
				target,
				options,
				options.BeamWidth,
				!groupedWarmup,
				policy)
			if err != nil {
				return laneResult{}, err
			}
			improvedPass = improvedPass || improvedBatch
			bestState = scCaptureBest(state, bestState)
		}

		completed = pass + 1
		if progress != nil {
			progress(completed, state.Evaluation.BinaryErrors, bestState.Evaluation.BinaryErrors)
		}
		if !improvedPass {
			break
		}
	}

	return laneResult{State: bestState, CompletedPasses: completed}, nil
}

func scCaptureBest(state *scSearchState, bestState *scSearchState) *scSearchState {
	if scIsBetter(state.Evaluation, bestState.Evaluation) {
		return scCloneState(state)
	}
	return bestState
}

func scTryBatch(state *scSearchState, frameIndexes []int, firstPsn uint32, initialBoundary modulationBoundary, target *codewordGrayTarget, options solverOptions, beamWidth int, rankedSingles bool, policy *acceptancePolicy) (bool, error) {
	ok, err := scTryBatchCandidates(state, frameIndexes, firstPsn, initialBoundary, target, options, beamWidth, false, rankedSingles, policy)
	if err != nil || ok {
		return ok, err
	}

	if len(frameIndexes) == 1 {
		if options.RetryBeamWidth > beamWidth {
			return scTryBatchCandidates(state, frameIndexes, firstPsn, initialBoundary, target, options, options.RetryBeamWidth, false, rankedSingles, policy)
		}
		return false, nil
	}

	middle := len(frameIndexes) / 2
	left := append([]int(nil), frameIndexes[:middle]...)
	ok, err = scTryBatch(state, left, firstPsn, initialBoundary, target, options, beamWidth, rankedSingles, policy)
	if err != nil || ok {
		return ok, err
	}
	right := append([]int(nil), frameIndexes[middle:]...)
	return scTryBatch(state, right, firstPsn, initialBoundary, target, options, beamWidth, rankedSingles, policy)
}

func scTryBatchCandidates(state *scSearchState, frameIndexes []int, firstPsn uint32, initialBoundary modulationBoundary, target *codewordGrayTarget, options solverOptions, beamWidth int, groupedAlternates, rankedSingles bool, policy *acceptancePolicy) (bool, error) {
	framesToSolve := make([]bool, dvdSyncFrameCount)
	for _, frameIndex := range frameIndexes {
		framesToSolve[frameIndex] = true
	}

	candidates := make([]*batchCandidateResult, options.BatchCandidates)
	for candidateIndex := 0; candidateIndex < options.BatchCandidates; candidateIndex++ {
		candidatePayload, err := scSolveSweep(
			state.Payload,
			state.Frames,
			state.FrameInputs,
			initialBoundary,
			target,
			options,
			beamWidth,
			framesToSolve,
			frameIndexes,
			candidateIndex,
			options.BatchCandidates,
			groupedAlternates,
			rankedSingles)
		if err != nil {
			return false, err
		}

		if bytesEqual(candidatePayload, state.Payload) {
			continue
		}

		candidateFrames, err := updateRecordingFrames(state.Payload, candidatePayload, state.Frames, firstPsn)
		if err != nil {
			return false, err
		}
		candidateInputs := make([]modulationBoundary, dvdSyncFrameCount)
		candidateFrameEvaluations := make([]codewordFrameEvaluation, dvdSyncFrameCount)
		candidateEvaluation, err := target.evaluate(candidateFrames, initialBoundary, candidateInputs, candidateFrameEvaluations)
		if err != nil {
			return false, err
		}
		candidates[candidateIndex] = &batchCandidateResult{
			Payload:          candidatePayload,
			Frames:           candidateFrames,
			FrameInputs:      candidateInputs,
			FrameEvaluations: candidateFrameEvaluations,
			Evaluation:       candidateEvaluation,
		}
	}

	var bestCandidate *batchCandidateResult
	for _, candidate := range candidates {
		if candidate == nil ||
			(bestCandidate != nil && !scIsBetter(candidate.Evaluation, bestCandidate.Evaluation)) {
			continue
		}
		bestCandidate = candidate
	}

	if bestCandidate == nil ||
		(!scIsBetter(bestCandidate.Evaluation, state.Evaluation) &&
			!scAcceptExploratory(bestCandidate.Evaluation, state.Evaluation, policy)) {
		return false, nil
	}

	state.Payload = bestCandidate.Payload
	state.Frames = bestCandidate.Frames
	state.FrameInputs = bestCandidate.FrameInputs
	state.FrameEvaluations = bestCandidate.FrameEvaluations
	state.Evaluation = bestCandidate.Evaluation
	return true, nil
}

func scAcceptExploratory(candidate, current codewordBlockEvaluation, policy *acceptancePolicy) bool {
	if policy == nil {
		return false
	}
	delta := float64(candidate.BinaryErrors - current.BinaryErrors)
	if delta == 0 {
		delta = float64(candidate.GrayError-current.GrayError) / 16.0
	}
	if delta == 0 {
		delta = float64(absInt(candidate.FinalBoundary.Dsv)-absInt(current.FinalBoundary.Dsv)) / 16.0
	}
	return delta <= 0 ||
		policy.Random.nextDouble() < math.Exp(-delta/policy.Temperature)
}

func scSolveSweep(currentPayload, currentFrames []byte, expectedFrameInputs []modulationBoundary, initialBoundary modulationBoundary, target *codewordGrayTarget, options solverOptions, beamWidth int, framesToSolve []bool, selectedFrames []int, candidateVariant, candidateVariantCount int, groupedAlternates, rankedSingles bool) ([]byte, error) {
	nextPayload := append([]byte(nil), currentPayload...)
	passBoundary := initialBoundary
	scorer := &efmPlusWordScorer{}
	selectedOrdinal := 0
	for syncFrame := 0; syncFrame < dvdSyncFrameCount; syncFrame++ {
		recordingOffset := syncFrame * efmPlusBytesPerSyncFrame
		category, err := syncCategory(syncFrame)
		if err != nil {
			return nil, err
		}
		if framesToSolve != nil && !framesToSolve[syncFrame] {
			score, err := scorer.scoreSyncFrame(
				currentFrames[recordingOffset:recordingOffset+efmPlusBytesPerSyncFrame],
				category,
				passBoundary,
				target.frameTargets(syncFrame),
				-1, -1)
			if err != nil {
				return nil, err
			}
			passBoundary = score.Boundary
			continue
		}

		var expectedOutput modulationBoundary
		if syncFrame+1 < len(expectedFrameInputs) {
			expectedOutput = expectedFrameInputs[syncFrame+1]
		} else if len(expectedFrameInputs) == 0 {
			expectedOutput = passBoundary
		} else {
			score, err := scorer.scoreSyncFrame(
				currentFrames[recordingOffset:recordingOffset+efmPlusBytesPerSyncFrame],
				category,
				passBoundary,
				target.frameTargets(syncFrame),
				-1, -1)
			if err != nil {
				return nil, err
			}
			expectedOutput = score.Boundary
		}
		choiceRank := scCandidateChoiceRank(
			syncFrame,
			selectedOrdinal,
			selectedFrames,
			candidateVariant,
			candidateVariantCount,
			groupedAlternates,
			rankedSingles)
		choice, err := scSolveFrame(
			syncFrame,
			currentPayload,
			currentFrames,
			passBoundary,
			expectedOutput,
			payloadByRecordingIndex,
			target,
			options,
			beamWidth,
			choiceRank)
		if err != nil {
			return nil, err
		}
		selectedOrdinal++

		for index := 0; index < len(choice.PayloadIndexes); index++ {
			nextPayload[choice.PayloadIndexes[index]] = choice.PayloadValues[index]
		}

		passBoundary = choice.FinalBoundary
	}

	return nextPayload, nil
}

func scCandidateChoiceRank(syncFrame, selectedOrdinal int, selectedFrames []int, candidateVariant, candidateVariantCount int, groupedAlternates, rankedSingles bool) int {
	if candidateVariant == 0 {
		return 0
	}
	if len(selectedFrames) == 1 {
		return candidateVariant
	}
	if !groupedAlternates {
		singleVariant := candidateVariant - 1
		var selected bool
		if rankedSingles {
			selected = syncFrame == selectedFrames[singleVariant%len(selectedFrames)]
		} else {
			selected = selectedOrdinal == singleVariant%len(selectedFrames)
		}
		if selected {
			return 1 + singleVariant/len(selectedFrames)
		}
		return 0
	}

	phaseCount := minInt(candidateVariantCount-1, len(selectedFrames))
	zeroBasedVariant := candidateVariant - 1
	phase := zeroBasedVariant % phaseCount
	if selectedOrdinal%phaseCount != phase {
		return 0
	}
	return 1 + zeroBasedVariant/phaseCount
}

func scRankFrames(evaluations []codewordFrameEvaluation, target *codewordGrayTarget) []frameRank {
	var ranked []frameRank
	hasMajorityErrors := false
	for _, evaluation := range evaluations {
		if evaluation.BinaryErrors > 0 {
			hasMajorityErrors = true
			break
		}
	}
	for frame := 0; frame < len(evaluations); frame++ {
		targetWords := scCountTargets(target.frameTargets(frame))
		evaluation := evaluations[frame]
		var isRetryCandidate bool
		if hasMajorityErrors {
			isRetryCandidate = evaluation.BinaryErrors > 0
		} else {
			isRetryCandidate = evaluation.GrayError > 0
		}
		if targetWords > 0 && isRetryCandidate {
			ranked = append(ranked, frameRank{
				FrameIndex:   frame,
				BinaryErrors: evaluation.BinaryErrors,
				GrayError:    evaluation.GrayError,
				TargetWords:  targetWords,
			})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		return scCompareFrameRanks(ranked[i], ranked[j]) < 0
	})
	return ranked
}

func scCountTargets(targets []int8) int {
	count := 0
	for _, target := range targets {
		if target >= 0 {
			count++
		}
	}
	return count
}

func scCompareFrameRanks(left, right frameRank) int {
	if c := compareLong(int64(right.BinaryErrors)*int64(left.TargetWords), int64(left.BinaryErrors)*int64(right.TargetWords)); c != 0 {
		return c
	}
	if c := compareLong(int64(right.GrayError)*int64(left.TargetWords), int64(left.GrayError)*int64(right.TargetWords)); c != 0 {
		return c
	}
	return compareInt(left.FrameIndex, right.FrameIndex)
}

func scSolveFrame(syncFrame int, currentPayload, currentFrames []byte, input, expectedOutput modulationBoundary, payloadByRecordingIndex []int, target *codewordGrayTarget, options solverOptions, beamWidth, choiceRank int) (frameChoice, error) {
	recordingOffset := syncFrame * efmPlusBytesPerSyncFrame
	var freeOffsets []int
	var targetOffsets []int
	for byteOffset := 0; byteOffset < efmPlusBytesPerSyncFrame; byteOffset++ {
		recordingIndex := recordingOffset + byteOffset
		if payloadByRecordingIndex[recordingIndex] < 0 {
			continue
		}
		freeOffsets = append(freeOffsets, byteOffset)
		if target.desiredAtRecordingIndex(recordingIndex) >= 0 {
			targetOffsets = append(targetOffsets, byteOffset)
		}
	}

	activeOffsets := map[int]struct{}{}
	for _, o := range targetOffsets {
		activeOffsets[o] = struct{}{}
	}
	bridgeCount := minInt(options.BridgeBytes, len(freeOffsets))
	for index := 0; index < bridgeCount; index++ {
		activeOffsets[freeOffsets[index]] = struct{}{}
		activeOffsets[freeOffsets[len(freeOffsets)-(index+1)]] = struct{}{}
	}

	active := make([]int, 0, len(activeOffsets))
	for o := range activeOffsets {
		active = append(active, o)
	}
	sort.Ints(active)
	baseBytes := append([]byte(nil), currentFrames[recordingOffset:recordingOffset+efmPlusBytesPerSyncFrame]...)
	desired := append([]int8(nil), target.frameTargets(syncFrame)...)
	scorer := &efmPlusWordScorer{}
	category, err := syncCategory(syncFrame)
	if err != nil {
		return frameChoice{}, err
	}
	baseline, err := scorer.scoreSyncFrame(baseBytes, category, input, desired, -1, -1)
	if err != nil {
		return frameChoice{}, err
	}
	if len(targetOffsets) == 0 {
		return frameChoice{PayloadIndexes: []int{}, PayloadValues: []byte{}, FinalBoundary: baseline.Boundary}, nil
	}

	currentNodes := make([]beamNode, beamWidth)
	nextNodes := make([]beamNode, beamWidth)
	currentHistory := make([]byte, beamWidth*efmPlusBytesPerSyncFrame)
	nextHistory := make([]byte, beamWidth*efmPlusBytesPerSyncFrame)
	initialPrefix, err := efmPlusPrefixStateInitialize(input, category)
	if err != nil {
		return frameChoice{}, err
	}
	currentNodes[0] = beamNode{Prefix: initialPrefix}
	currentCount := 1
	expansions := make([]beamExpansion, 0, beamWidth*256)
	candidateValues := make([]byte, 256)
	seenValues := make([]bool, 256)

	for byteOffset := 0; byteOffset < efmPlusBytesPerSyncFrame; byteOffset++ {
		recordingIndex := recordingOffset + byteOffset
		wordTarget := desired[byteOffset]
		isPayload := payloadByRecordingIndex[recordingIndex] >= 0
		_, inActive := activeOffsets[byteOffset]
		isBridge := isPayload && inActive && wordTarget < 0
		expansions = expansions[:0]
		for parentIndex := 0; parentIndex < currentCount; parentIndex++ {
			prefix := currentNodes[parentIndex].Prefix
			candidateCount := scBuildCandidateValues(
				baseBytes[byteOffset],
				wordTarget,
				isBridge,
				prefix,
				candidateValues,
				seenValues)
			for candidateIndex := 0; candidateIndex < candidateCount; candidateIndex++ {
				value := candidateValues[candidateIndex]
				advanced, err := prefix.advance(value, wordTarget)
				if err != nil {
					return frameChoice{}, err
				}
				expansions = append(expansions, beamExpansion{
					Prefix:      advanced,
					ParentIndex: parentIndex,
					Value:       value,
				})
			}
		}

		sort.Slice(expansions, func(i, j int) bool {
			return scCompareExpansions(expansions[i], expansions[j]) < 0
		})
		nextCount := minInt(beamWidth, len(expansions))
		for index := 0; index < nextCount; index++ {
			expansion := expansions[index]
			nextNodes[index] = beamNode{Prefix: expansion.Prefix}
			copy(nextHistory[index*efmPlusBytesPerSyncFrame:index*efmPlusBytesPerSyncFrame+byteOffset],
				currentHistory[expansion.ParentIndex*efmPlusBytesPerSyncFrame:expansion.ParentIndex*efmPlusBytesPerSyncFrame+byteOffset])
			nextHistory[index*efmPlusBytesPerSyncFrame+byteOffset] = expansion.Value
		}

		currentNodes, nextNodes = nextNodes, currentNodes
		currentHistory, nextHistory = nextHistory, currentHistory
		currentCount = nextCount
	}

	frameCandidates := []frameCandidate{{Bytes: baseBytes, Score: baseline}}
	seenCandidates := map[string]struct{}{hexDumpUpper(baseBytes): {}}
	for index := 0; index < currentCount; index++ {
		candidate := currentHistory[index*efmPlusBytesPerSyncFrame : index*efmPlusBytesPerSyncFrame+efmPlusBytesPerSyncFrame]
		score, err := scorer.scoreSyncFrame(candidate, category, input, desired, -1, -1)
		if err != nil {
			return frameChoice{}, err
		}
		candidateBytes := append([]byte(nil), candidate...)
		key := hexDumpUpper(candidateBytes)
		if _, seen := seenCandidates[key]; !seen {
			seenCandidates[key] = struct{}{}
			frameCandidates = append(frameCandidates, frameCandidate{Bytes: candidateBytes, Score: score})
		}
	}

	sort.Slice(frameCandidates, func(i, j int) bool {
		return scCompareFrameCandidates(frameCandidates[i], frameCandidates[j], expectedOutput) < 0
	})
	selectedIndex := 0
	if choiceRank > 0 && len(frameCandidates) != 1 {
		selectedIndex = 1 + (choiceRank-1)%(len(frameCandidates)-1)
	}
	selected := frameCandidates[selectedIndex]
	bestBytes := selected.Bytes
	bestBoundary := selected.Score.Boundary

	payloadIndexes := make([]int, len(active))
	payloadValues := make([]byte, len(active))
	for index := 0; index < len(active); index++ {
		recordingIndex := recordingOffset + active[index]
		payloadIndex := payloadByRecordingIndex[recordingIndex]
		scramblerByte := currentFrames[recordingIndex] ^ currentPayload[payloadIndex]
		payloadIndexes[index] = payloadIndex
		payloadValues[index] = bestBytes[active[index]] ^ scramblerByte
	}

	return frameChoice{PayloadIndexes: payloadIndexes, PayloadValues: payloadValues, FinalBoundary: bestBoundary}, nil
}

func scCompareFrameCandidates(left, right frameCandidate, expectedOutput modulationBoundary) int {
	if c := compareInt(left.Score.BinaryErrors, right.Score.BinaryErrors); c != 0 {
		return c
	}
	if c := compareInt(left.Score.GrayError, right.Score.GrayError); c != 0 {
		return c
	}
	if c := compareInt(scBoundaryPenalty(left.Score.Boundary, expectedOutput), scBoundaryPenalty(right.Score.Boundary, expectedOutput)); c != 0 {
		return c
	}
	return bytesCompare(left.Bytes, right.Bytes)
}

func scBuildCandidateValues(baseline byte, target int8, isBridge bool, prefix efmPlusPrefixState, destination []byte, seen []bool) int {
	for i := range seen {
		seen[i] = false
	}
	count := 0
	if isBridge {
		count = scAddPair(prefix.Stream1, destination, seen, count)
		count = scAddPair(prefix.Stream2, destination, seen, count)
		return count
	}

	if target < 0 {
		destination[0] = baseline
		return 1
	}

	count = scAddTargetCandidate(prefix.Stream1, target, destination, seen, count)
	count = scAddTargetCandidate(prefix.Stream2, target, destination, seen, count)
	return count
}

func scAddPair(stream efmStreamPrefix, destination []byte, seen []bool, count int) int {
	pair, err := efmPlusBinaryBytePairGet(stream.State, stream.IsLand, stream.Dsv)
	if err != nil {
		panic(err)
	}
	count = scAddCandidate(pair.BlackByte, destination, seen, count)
	count = scAddCandidate(pair.WhiteByte, destination, seen, count)
	return count
}

func scAddTargetCandidate(stream efmStreamPrefix, target int8, destination []byte, seen []bool, count int) int {
	pair, err := efmPlusBinaryBytePairGet(stream.State, stream.IsLand, stream.Dsv)
	if err != nil {
		panic(err)
	}
	if target <= 0 {
		count = scAddCandidate(pair.BlackByte, destination, seen, count)
	} else if target >= 16 {
		count = scAddCandidate(pair.WhiteByte, destination, seen, count)
	} else {
		count = scAddCandidate(pair.BlackByte, destination, seen, count)
		count = scAddCandidate(pair.WhiteByte, destination, seen, count)
	}
	return count
}

func scAddCandidate(value byte, destination []byte, seen []bool, count int) int {
	if seen[value] {
		return count
	}
	seen[value] = true
	destination[count] = value
	return count + 1
}

func scCompareExpansions(left, right beamExpansion) int {
	leftSelected := left.Prefix.selected()
	rightSelected := right.Prefix.selected()
	if c := compareInt(leftSelected.BinaryErrors, rightSelected.BinaryErrors); c != 0 {
		return c
	}
	if c := compareInt(
		minInt(left.Prefix.Stream1.BinaryErrors, left.Prefix.Stream2.BinaryErrors),
		minInt(right.Prefix.Stream1.BinaryErrors, right.Prefix.Stream2.BinaryErrors)); c != 0 {
		return c
	}
	if c := compareInt(leftSelected.GrayError, rightSelected.GrayError); c != 0 {
		return c
	}
	if c := compareInt(
		minInt(left.Prefix.Stream1.GrayError, left.Prefix.Stream2.GrayError),
		minInt(right.Prefix.Stream1.GrayError, right.Prefix.Stream2.GrayError)); c != 0 {
		return c
	}
	if c := compareInt(absInt(leftSelected.Dsv), absInt(rightSelected.Dsv)); c != 0 {
		return c
	}
	if c := compareInt(left.ParentIndex, right.ParentIndex); c != 0 {
		return c
	}
	return compareInt(int(left.Value), int(right.Value))
}

func scIsBetter(candidate, currentBest codewordBlockEvaluation) bool {
	return candidate.BinaryErrors < currentBest.BinaryErrors ||
		(candidate.BinaryErrors == currentBest.BinaryErrors &&
			(candidate.GrayError < currentBest.GrayError ||
				(candidate.GrayError == currentBest.GrayError &&
					absInt(candidate.FinalBoundary.Dsv) < absInt(currentBest.FinalBoundary.Dsv))))
}

func scBoundaryPenalty(actual, expected modulationBoundary) int {
	penalty := 0
	if actual.State != expected.State {
		penalty += 1_000_000
	}
	if actual.IsLand != expected.IsLand {
		penalty += 100_000
	}
	return penalty + absInt(actual.Dsv-expected.Dsv)
}

func bytesCompare(a, b []byte) int {
	n := minInt(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return compareInt(int(a[i]), int(b[i]))
		}
	}
	return compareInt(len(a), len(b))
}

func stateControlValidate(initialPayloads []byte, options solverOptions) error {
	if len(initialPayloads) != dvdPayloadBytesPerBlock {
		return newArgumentErrorParam("Initial payload must contain one complete 16-sector ECC Block.", "initialPayloads")
	}
	if options.Iterations < 0 ||
		options.BridgeBytes < 0 || options.BridgeBytes > efmPlusBytesPerSyncFrame ||
		options.BeamWidth < 1 || options.BeamWidth > 4096 ||
		options.RetryBeamWidth < 0 || options.RetryBeamWidth > 4096 ||
		math.IsNaN(options.RetryFrameFraction) ||
		options.RetryFrameFraction < 0 || options.RetryFrameFraction > 1 ||
		options.BatchFrames < 1 || options.BatchFrames > dvdSyncFrameCount ||
		options.BatchCandidates < 1 || options.BatchCandidates > 64 ||
		options.SearchLanes < 1 || options.SearchLanes > 8 {
		return &argumentOutOfRangeError{param: "options", msg: "Invalid state-control pass, bridge, beam, or adaptive retry option."}
	}
	return nil
}
