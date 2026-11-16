package dvd

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
)

// SelfTests.Run port. testXxx functions panic with selfTestError on failure;
// RunCommand's recover turns that into "error: selftest failed: ...".
func selfTestsRun() error {
	testTables()
	testSy6Encoding()
	testDataFrame()
	testEdc()
	testEcc()
	testPhysicalEncoding()
	testCodewordStateControl()
	testDispersionPool()
	if err := testBinaryDispersion(); err != nil {
		return err
	}
	if err := testDiscreteGrayscaleSampling(); err != nil {
		return err
	}
	testImageMapping()
	if err := testIsoOutput(); err != nil {
		return err
	}
	if err := testCalibrationAndRange(); err != nil {
		return err
	}
	fmt.Println("selftest: all checks passed")
	return nil
}

func selfTestTrue(condition bool, name string) {
	if !condition {
		panic(newInvalidOperationError("selftest failed: " + name))
	}
}

func selfTestEqual[T comparable](expected, actual T, name string) {
	selfTestTrue(expected == actual, fmt.Sprintf("%s: expected %v, got %v", name, expected, actual))
}

func selfTestNear(expected, actual, tolerance float64, name string) {
	selfTestTrue(math.Abs(expected-actual) <= tolerance,
		fmt.Sprintf("%s: expected %v, got %v", name, expected, actual))
}

func selfTestSequenceEqual(expected, actual []byte, name string) {
	selfTestTrue(bytes.Equal(expected, actual), name)
}

func testTables() {
	word, next, err := efmPlusLookupMain(0, 1)
	selfTestTrue(err == nil, "main lookup")
	selfTestEqual(uint16(0x2009), word, "main[0,state1]")
	selfTestEqual(byte(1), next, "main next state")
	word, _, _ = efmPlusLookupMain(0, 2)
	selfTestEqual(uint16(0x4120), word, "main[0,state2]")
	word, _, _ = efmPlusLookupMain(255, 4)
	selfTestEqual(uint16(0x4212), word, "main[255,state4]")
	word, _, _ = efmPlusSubstitution(0, 1)
	selfTestEqual(uint16(0x0480), word, "substitution[0,state1]")

	// ECMA-267, 3rd edition, Table 4: SY0..SY7, independent of the runtime table.
	syncVectors := []struct{ primary12, secondary12, primary34, secondary34 uint32 }{
		{0x12440011, 0x12040011, 0x92040011, 0x92440011},
		{0x04040011, 0x04440011, 0x84440011, 0x84040011},
		{0x10040011, 0x10440011, 0x90440011, 0x90040011},
		{0x08040011, 0x08440011, 0x82440011, 0x82040011},
		{0x20040011, 0x20440011, 0x88440011, 0x88040011},
		{0x22440011, 0x22040011, 0x89040011, 0x81040011},
		{0x24840011, 0x20840011, 0x90840011, 0x80440011},
		{0x24440011, 0x24040011, 0x88840011, 0x80840011},
	}
	for category := 0; category < len(syncVectors); category++ {
		vector := syncVectors[category]
		for state := byte(1); state <= 4; state++ {
			expectedPrimary := vector.primary12
			expectedSecondary := vector.secondary12
			if state > 2 {
				expectedPrimary = vector.primary34
				expectedSecondary = vector.secondary34
			}
			primary, err := efmPlusSync(state, category, true)
			selfTestTrue(err == nil, "primary sync")
			selfTestEqual(expectedPrimary, primary, fmt.Sprintf("ECMA-267 primary SY%d, state %d", category, state))
			secondary, err := efmPlusSync(state, category, false)
			selfTestTrue(err == nil, "secondary sync")
			selfTestEqual(expectedSecondary, secondary, fmt.Sprintf("ECMA-267 secondary SY%d, state %d", category, state))
		}
	}
}

func testSy6Encoding() {
	frame := make([]byte, efmPlusBytesPerSyncFrame)
	for i := range frame {
		frame[i] = 0x92
	}
	desired := make([]int8, len(frame))
	for i := range desired {
		desired[i] = -1
	}
	physical := make([]byte, efmPlusBitsPerSyncFrame)

	for state := byte(3); state <= 4; state++ {
		sawSecondary := false
		for _, isLand := range []bool{false, true} {
			for _, dsv := range []int{-64, 0, 64} {
				input := modulationBoundary{State: state, Dsv: dsv, IsLand: isLand}
				output, err := newEfmPlusEncoder().encodeSyncFrame(frame, 6, input, physical)
				selfTestTrue(err == nil, "encodeSyncFrame")

				var sync uint32
				previousLevel := input.IsLand
				for bit := 0; bit < 32; bit++ {
					currentLevel := physical[bit] != 0
					sync <<= 1
					if currentLevel != previousLevel {
						sync |= 1
					}
					previousLevel = currentLevel
				}

				selfTestTrue(sync == 0x90840011 || sync == 0x80440011,
					fmt.Sprintf("encoded ECMA-267 SY6, state %d, DSV %d, land %v", state, dsv, isLand))
				sawSecondary = sawSecondary || sync == 0x80440011
				scored, err := new(efmPlusWordScorer).scoreSyncFrame(frame, 6, input, desired, -1, -1)
				selfTestTrue(err == nil, "scoreSyncFrame")
				selfTestEqual(output, scored.Boundary, "SY6 word scorer boundary")

				// Check the secondary prefix against the standard bits, not the shared table.
				prefixState, err := efmPlusPrefixStateInitialize(input, 6)
				selfTestTrue(err == nil, "prefix init")
				secondary := prefixState.Stream2
				expectedDsv := input.Dsv
				expectedLand := input.IsLand
				for bit := 31; bit >= 0; bit-- {
					if (0x80440011>>uint(bit))&1 != 0 {
						expectedLand = !expectedLand
					}
					if expectedLand {
						expectedDsv++
					} else {
						expectedDsv--
					}
				}
				selfTestEqual(byte(1), secondary.State, "secondary SY6 next state")
				selfTestEqual(expectedDsv, secondary.Dsv, "secondary SY6 prefix DSV")
				selfTestEqual(expectedLand, secondary.IsLand, "secondary SY6 prefix level")
			}
		}
		selfTestTrue(sawSecondary, fmt.Sprintf("encoder selects secondary SY6 in state %d", state))
	}
}

func testDataFrame() {
	payload := make([]byte, dvdDataFramePayloadBytes)
	frame, err := buildDataFrame(payload, 0x030000)
	selfTestTrue(err == nil, "buildDataFrame")
	selfTestSequenceEqual([]byte{0x00, 0x03, 0x00, 0x00}, frame[0:4], "ID bytes")
	selfTestTrue(allZeroBytes(frame[6:12]), "DVD-R RSV must be zero")
	selfTestTrue(rsHasZeroRemainder(frame[0:6], 2), "IED codeword remainder")
	selfTestTrue(hasValidEdc(frame), "EDC")

	scrambled := append([]byte(nil), frame...)
	selfTestTrue(scrambleMainData(scrambled, 0x030000) == nil, "scramble")
	selfTestTrue(!bytes.Equal(scrambled[12:12+dvdDataFramePayloadBytes], frame[12:12+dvdDataFramePayloadBytes]),
		"scrambler must change zero payload")
	selfTestTrue(scrambleMainData(scrambled, 0x030000) == nil, "scramble back")
	selfTestSequenceEqual(frame, scrambled, "scrambler XOR round trip")
}

func allZeroBytes(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

func testEdc() {
	// ECMA-267 16.4: remainder of the input polynomial multiplied by x^32.
	selfTestEqual(uint32(0x80000011), computeEdc([]byte{0x01}), "EDC x^32 remainder")
	selfTestEqual(uint32(0x80000F0F), computeEdc([]byte{0x80}), "EDC MSB-first order")
	selfTestEqual(uint32(0xDE5E214E), computeEdc([]byte{0x12, 0x34, 0x56, 0x78}), "EDC multi-byte vector")

	expectedEdcs := []uint32{0x02AEBAF1, 0xB219C5AE, 0x086E4847}
	for pattern := 0; pattern < len(expectedEdcs); pattern++ {
		payload := make([]byte, dvdDataFramePayloadBytes)
		for index := range payload {
			switch pattern {
			case 1:
				payload[index] = byte(index)
			case 2:
				payload[index] = 0xFF
			default:
				payload[index] = 0
			}
		}

		frame, err := buildDataFrame(payload, 0x030000)
		selfTestTrue(err == nil, "buildDataFrame")
		selfTestEqual(expectedEdcs[pattern], binary.BigEndian.Uint32(frame[2060:]),
			fmt.Sprintf("EDC complete frame vector %d", pattern))
		selfTestEqual(uint32(0), polynomialRemainder(frame), fmt.Sprintf("EDC full-frame polynomial remainder %d", pattern))
		selfTestTrue(hasValidEdc(frame), fmt.Sprintf("EDC valid frame %d", pattern))
		frame[12] ^= 1
		selfTestTrue(!hasValidEdc(frame), fmt.Sprintf("EDC rejects changed payload %d", pattern))
	}
}

// polynomialRemainder: literal long division, independent of the production
// feedback-register algorithm.
func polynomialRemainder(frame []byte) uint32 {
	polynomial := new(big.Int).SetBytes(frame) // unsigned, big-endian
	generator := big.NewInt(0x180000011)
	for polynomial.BitLen() > 32 {
		shift := uint(polynomial.BitLen() - 33)
		polynomial.Xor(polynomial, new(big.Int).Lsh(generator, shift))
	}
	return uint32(polynomial.Uint64())
}

func testEcc() {
	payloads := make([]byte, dvdPayloadBytesPerBlock)
	ecc, err := buildEccBytes(payloads, 0x030000)
	selfTestTrue(err == nil, "buildEccBytes")
	for row := 0; row < 208; row++ {
		selfTestTrue(rsHasZeroRemainder(ecc[row*182:row*182+182], 10), fmt.Sprintf("PI row %d", row))
	}

	column := make([]byte, 208)
	for columnIndex := 0; columnIndex < 172; columnIndex++ {
		for row := 0; row < 208; row++ {
			column[row] = ecc[row*182+columnIndex]
		}
		selfTestTrue(rsHasZeroRemainder(column, 16), fmt.Sprintf("PO column %d", columnIndex))
	}

	interleaved, err := interleaveRecordingFrames(ecc)
	selfTestTrue(err == nil, "interleave")
	selfTestEqual(len(ecc), len(interleaved), "recording frame size")

	updatedPayloads := append([]byte(nil), payloads...)
	updatedPayloads[17] = 0x5A
	updatedPayloads[7*dvdPayloadBytesPerSector+991] = 0xC3
	updatedPayloads[len(updatedPayloads)-1] = 0x7E
	incrementallyUpdated, err := updateRecordingFrames(payloads, updatedPayloads, interleaved, 0x030000)
	selfTestTrue(err == nil, "updateRecordingFrames")
	fullyRebuilt, err := buildRecordingFrames(updatedPayloads, 0x030000)
	selfTestTrue(err == nil, "rebuild")
	selfTestSequenceEqual(fullyRebuilt, incrementallyUpdated, "incremental ECC recording frames")

	random := newDotnetRandom(731)
	randomPayloads := make([]byte, dvdPayloadBytesPerBlock)
	random.nextBytes(randomPayloads)
	randomFrames, err := buildRecordingFrames(randomPayloads, 0x030000)
	selfTestTrue(err == nil, "random frames")
	mutatedPayloads := append([]byte(nil), randomPayloads...)
	for mutation := 0; mutation < 64; mutation++ {
		index := random.nextMax(int32(len(mutatedPayloads)))
		mutatedPayloads[index] ^= byte(random.nextRange(1, 256))
	}

	incrementallyUpdated, err = updateRecordingFrames(randomPayloads, mutatedPayloads, randomFrames, 0x030000)
	selfTestTrue(err == nil, "incremental random")
	fullyRebuilt, err = buildRecordingFrames(mutatedPayloads, 0x030000)
	selfTestTrue(err == nil, "rebuild random")
	selfTestSequenceEqual(fullyRebuilt, incrementallyUpdated, "incremental ECC random mutations")
}

func testPhysicalEncoding() {
	payloads := make([]byte, dvdPayloadBytesPerBlock)
	encoder := newDvdEccBlockEncoder()
	initial := modulationBoundary{State: 1, Dsv: 0, IsLand: true}
	first, err := encoder.encode(payloads, 0, 0x30000, initial)
	selfTestTrue(err == nil, "first encode")
	second, err := encoder.encode(payloads, 0, 0x30000, initial)
	selfTestTrue(err == nil, "second encode")
	selfTestEqual(dvdChannelBitsPerBlock, len(first.ChannelLevels), "channel bit count")
	selfTestEqual(uint32(0x30000), first.FirstPhysicalSectorNumber, "first PSN")
	for _, level := range first.ChannelLevels {
		selfTestTrue(level == 0 || level == 1, "channel levels are binary")
	}
	selfTestSequenceEqual(first.ChannelLevels, second.ChannelLevels, "deterministic modulation")
	selfTestEqual(first.FinalBoundary, second.FinalBoundary, "deterministic final boundary")
	recordingFrames, err := buildRecordingFrames(payloads, 0x30000)
	selfTestTrue(err == nil, "recording frames")
	replayed, err := replayRecordingFrameBoundary(recordingFrames, initial)
	selfTestTrue(err == nil, "replay")
	selfTestEqual(first.FinalBoundary, replayed, "word replay final boundary")
	validateDataRll(first.ChannelLevels, initial.IsLand)
}

func validateDataRll(levels []byte, initialLand bool) {
	previous := initialLand
	nrz := make([]byte, efmPlusBitsPerSyncFrame)
	for frameOffset := 0; frameOffset < len(levels); frameOffset += efmPlusBitsPerSyncFrame {
		for index := 0; index < len(nrz); index++ {
			current := levels[frameOffset+index] != 0
			if current == previous {
				nrz[index] = 0
			} else {
				nrz[index] = 1
			}
			previous = current
		}

		zeroRun := 0
		for index := 32; index < len(nrz); index++ {
			if nrz[index] == 0 {
				zeroRun++
			} else {
				selfTestTrue(zeroRun >= 2 && zeroRun <= 10,
					fmt.Sprintf("RLL zero run %d at channel offset %d", zeroRun, frameOffset+index))
				zeroRun = 0
			}
		}
	}
}

func countLandBits(levels []byte) int {
	count := 0
	for _, level := range levels {
		count += int(level)
	}
	return count
}

func testDispersionPool() {
	selfTestEqual(byte(0xA5), dvdBlackScrambledByte, "dispersion physical black byte polarity")
	selfTestEqual(byte(0x92), dvdWhiteScrambledByte, "dispersion physical white byte polarity")
	const firstPsn = 0x30000
	black := rasterImageCreateForTest(8, 8, 0, 0, 0, 255)
	mapped, err := imageTargetMapperMapInMemory(black, 0, imageMappingOptions{
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
	selfTestTrue(err == nil, "map black")
	targets := make([]int8, dvdChannelBitsPerBlock)
	for i := range targets {
		targets[i] = constraintUnconstrained
	}
	for payloadIndex := 0; payloadIndex < dvdPayloadBytesPerBlock; payloadIndex++ {
		channelOffset, err := payloadDirectChannelOffset(payloadIndex)
		selfTestTrue(err == nil, "channel offset")
		for i := 0; i < 16; i++ {
			targets[channelOffset+i] = int8(payloadIndex & 1)
		}
	}

	constraint, err := imageConstraintCreate(targets, mapped.imageMapping, nil, nil)
	selfTestTrue(err == nil, "create constraint")
	initialPayload := make([]byte, dvdPayloadBytesPerBlock)
	newDotnetRandom(9025).nextBytes(initialPayload)
	options := solverOptions{
		Iterations:         1,
		MutationBytes:      4,
		RandomSeed:         1,
		InitialTemperature: 64,
		FinalTemperature:   0.25,
		Algorithm:          solverAlgorithmDispersion,
	}
	boundary := modulationBoundary{State: 1, Dsv: 0, IsLand: true}
	result, err := solverEngineSolve(constraint, initialPayload, 0, firstPsn, boundary, options, nil)
	selfTestTrue(err == nil, "dispersion solve")
	frames, err := buildRecordingFrames(result.Payloads, firstPsn)
	selfTestTrue(err == nil, "frames")
	for payloadIndex := 0; payloadIndex < dvdPayloadBytesPerBlock; payloadIndex++ {
		expected := dvdWhiteScrambledByte
		if payloadIndex&1 == 0 {
			expected = dvdBlackScrambledByte
		}
		recordingIndex, err := payloadRecordingFrameIndex(payloadIndex)
		selfTestTrue(err == nil, "recording index")
		selfTestEqual(expected, frames[recordingIndex], fmt.Sprintf("dispersion scrambled payload %d", payloadIndex))
	}

	exact, err := newDvdEccBlockEncoder().encode(result.Payloads, 0, firstPsn, boundary)
	selfTestTrue(err == nil, "exact encode")
	selfTestEqual(exact.FinalBoundary, result.Encoded.FinalBoundary, "dispersion exact final boundary")
	selfTestEqual(0, len(result.Encoded.ChannelLevels), "dispersion avoids channel-bit materialization")
	selfTestEqual(0, result.Score, "dispersion transition class error")
	selfTestEqual(dvdPayloadBytesPerBlock, result.ScoreMaximum, "dispersion controlled word count")
	selfTestEqual("payload-transition-class-error", result.ScoreMetric, "dispersion score metric")
}
