package dvd

import "time"

// dispersionPoolSolver ports DispersionPoolSolver.
type dispersionPoolSolver struct{}

func (s *dispersionPoolSolver) solve(constraint *imageConstraint, initialPayloads []byte, initialLba, psnOffset uint32, initialBoundary modulationBoundary, options solverOptions, progress func(iteration, current, best int)) (solverResult, error) {
	if len(initialPayloads) != dvdPayloadBytesPerBlock {
		return solverResult{}, newArgumentErrorParam("Initial payload must contain 32768 bytes.", "initialPayloads")
	}
	stopwatchStart := time.Now()
	firstPsn, err := checkedAddU32(initialLba, psnOffset)
	if err != nil {
		return solverResult{}, err
	}
	if firstPsn&0xF != 0 {
		return solverResult{}, newArgumentError("The first PSN 0x" + formatHexUpper(uint64(firstPsn), 6) + " is not aligned to a 16-sector ECC Block.")
	}

	payloads := append([]byte(nil), initialPayloads...)
	wordTargets := constraint.channelWordTargets
	grayscaleTargets := constraint.channelWordGrayscaleTargets
	controlledWords := 0
	for sector := 0; sector < dvdEccSectorCount; sector++ {
		payloadOffset := sector * dvdPayloadBytesPerSector
		sectorPayload := payloads[payloadOffset : payloadOffset+dvdPayloadBytesPerSector]

		// Convert the initial raw ISO payload P into the scrambled bytes Q seen by EFMPlus.
		if err := transformPayloadScrambling(sectorPayload, firstPsn+uint32(sector)); err != nil {
			return solverResult{}, err
		}
		for byteIndex := 0; byteIndex < len(sectorPayload); byteIndex++ {
			payloadIndex := payloadOffset + byteIndex
			channelOffset, err := payloadDirectChannelOffset(payloadIndex)
			if err != nil {
				return solverResult{}, err
			}
			wordIndex := channelOffset / 16
			var target int8
			if len(grayscaleTargets) == 0 {
				target = wordTargets[wordIndex]
			} else {
				target = grayscaleTargets[wordIndex]
			}
			if target < 0 {
				continue
			}

			if len(grayscaleTargets) == 0 {
				if target < 8 {
					sectorPayload[byteIndex] = dvdBlackScrambledByte
				} else {
					sectorPayload[byteIndex] = dvdWhiteScrambledByte
				}
			} else {
				sectorPayload[byteIndex] = dvdGetScrambledByteForLevel(int(target))
			}
			controlledWords++
		}

		// Scrambling is XOR-based, so applying it again converts Q back into ISO payload P.
		if err := transformPayloadScrambling(sectorPayload, firstPsn+uint32(sector)); err != nil {
			return solverResult{}, err
		}
	}

	recordingFrames, err := buildRecordingFrames(payloads, firstPsn)
	if err != nil {
		return solverResult{}, err
	}
	finalBoundary, err := replayRecordingFrameBoundary(recordingFrames, initialBoundary)
	if err != nil {
		return solverResult{}, err
	}
	if progress != nil {
		progress(1, 0, 0)
	}
	return solverResult{
		Payloads:            payloads,
		Encoded:             encodedEccBlock{ChannelLevels: []byte{}, FinalBoundary: finalBoundary, FirstPhysicalSectorNumber: firstPsn},
		Score:               0,
		ScoreMaximum:        controlledWords,
		ScoreMetric:         "payload-transition-class-error",
		CompletedIterations: 1,
		Elapsed:             time.Since(stopwatchStart),
		GrayScore:           intPtr(0),
		GrayScoreMaximum:    intPtr(controlledWords * 16),
		GrayScoreMetric:     stringPtr("payload-transition-count-error"),
	}, nil
}

// dvdReadCompatibilityGuard ports DvdReadCompatibilityGuard.

// applyReadCompatibilitySector ports ApplySector: the Q-domain every-16 rule.
func applyReadCompatibilitySector(payload, scramblerMask, ownership []byte) (int, error) {
	const sectorLength = dvdPayloadBytesPerSector
	if len(payload) != sectorLength || len(scramblerMask) != sectorLength || len(ownership) != sectorLength {
		return 0, newArgumentError("Payload, scrambler mask, and ownership must each contain one 2048-byte sector.")
	}
	changes := 0
	for index := 0; index < sectorLength; index += 16 {
		if ownership[index] == 0 {
			continue
		}
		q := payload[index] ^ scramblerMask[index]
		if q == 0xA5 || q == 0x9B {
			continue
		}
		payload[index] = 0x9B ^ scramblerMask[index]
		changes++
	}
	return changes, nil
}

// applyReadCompatibilityLegacySector ports ApplyLegacySector (frozen bridge-9b-v1).
// Algorithm based on Ashley-QwQ/disc-probe, tools/bridge_iso.py (GPL-3.0),
// commit 7574eca39e009f70c9b80a9a0ddb5bbe3d392173.
func applyReadCompatibilityLegacySector(payload, scramblerMask, ownership []byte) (int, error) {
	const sectorLength = dvdPayloadBytesPerSector
	const windowLength = 64
	const minimumPaletteBytes = 58
	if len(payload) != sectorLength || len(scramblerMask) != sectorLength || len(ownership) != sectorLength {
		return 0, newArgumentError("Payload, scrambler mask, and ownership must each contain one 2048-byte sector.")
	}

	// All decisions use the original Q. Bridges written earlier in this
	// sector must not alter either later run detection or window counts.
	original := make([]byte, sectorLength)
	palettePrefix := make([]int, sectorLength+1)
	palettePrefix[0] = 0
	for index := 0; index < sectorLength; index++ {
		value := payload[index] ^ scramblerMask[index]
		original[index] = value
		add := 0
		if value == 0xA5 || value == 0x92 {
			add = 1
		}
		palettePrefix[index+1] = palettePrefix[index] + add
	}

	changes := 0
	for index := 0; index < sectorLength; {
		if original[index] != 0x92 {
			index++
			continue
		}
		start := index
		index++
		for index < sectorLength && original[index] == 0x92 {
			index++
		}
		if index-start < 2 || ownership[start] == 0 ||
			(start > 0 && (original[start-1] == 0x75 || original[start-1] == 0x9B)) {
			continue
		}
		backwardStart := start + 1 - windowLength
		forward := start+windowLength <= sectorLength &&
			palettePrefix[start+windowLength]-palettePrefix[start] >= minimumPaletteBytes
		backward := backwardStart >= 0 &&
			palettePrefix[start+1]-palettePrefix[backwardStart] >= minimumPaletteBytes
		if !forward && !backward {
			continue
		}
		payload[start] = 0x9B ^ scramblerMask[start]
		changes++
	}
	return changes, nil
}
