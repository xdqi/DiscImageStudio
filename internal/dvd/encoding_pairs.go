package dvd

// efmPlusBinaryBytePair ports EfmPlusBinaryBytePair.
type efmPlusBinaryBytePair struct {
	BlackByte                   byte
	WhiteByte                   byte
	BlackWorstLandCount         byte
	WhiteWorstLandCount         byte
	ReferenceDsv                int
	BlackWorstAbsoluteOutputDsv int64
	WhiteWorstAbsoluteOutputDsv int64
}

const (
	grayErrorDsvUnits    = 16
	minimumCachedDsv     = -256
	maximumCachedDsv     = 256
	dsvBucketCount       = maximumCachedDsv - minimumCachedDsv + 1
	binaryByteLevelCount = 8
)

type pairByteLevels struct {
	FirstLandCount  int
	SecondLandCount int
	Count           int
}

type pairCandidateScore struct {
	Value       byte
	BinaryWorst int
	Combined    int64
	GrayWorst   int
	DsvWorst    int64
	GrayTotal   int
	DsvTotal    int64
}

func newPairCandidateScore(value byte, levels pairByteLevels, dsv int, white bool) pairCandidateScore {
	firstGray := levels.FirstLandCount
	secondGray := levels.SecondLandCount
	if white {
		firstGray = 16 - levels.FirstLandCount
		secondGray = 16 - levels.SecondLandCount
	}
	var firstBinary, secondBinary int
	if white {
		if levels.FirstLandCount > 8 {
			firstBinary = 0
		} else {
			firstBinary = 1
		}
		if levels.SecondLandCount > 8 {
			secondBinary = 0
		} else {
			secondBinary = 1
		}
	} else {
		if levels.FirstLandCount < 8 {
			firstBinary = 0
		} else {
			firstBinary = 1
		}
		if levels.SecondLandCount < 8 {
			secondBinary = 0
		} else {
			secondBinary = 1
		}
	}
	firstDsv := absLong(int64(dsv) + int64(levels.FirstLandCount*2-16))
	secondDsv := absLong(int64(dsv) + int64(levels.SecondLandCount*2-16))
	grayWorst := maxInt(firstGray, secondGray)
	dsvWorst := maxLong(firstDsv, secondDsv)
	grayTotal := firstGray
	dsvTotal := firstDsv
	if levels.Count == 2 {
		grayTotal += secondGray
		dsvTotal += secondDsv
	}
	return pairCandidateScore{
		Value:       value,
		BinaryWorst: maxInt(firstBinary, secondBinary),
		Combined:    int64(grayWorst*grayErrorDsvUnits) + dsvWorst,
		GrayWorst:   grayWorst,
		DsvWorst:    dsvWorst,
		GrayTotal:   grayTotal,
		DsvTotal:    dsvTotal,
	}
}

func comparePairCandidateScore(a, b pairCandidateScore) int {
	if c := compareInt(a.BinaryWorst, b.BinaryWorst); c != 0 {
		return c
	}
	if c := compareLong(a.Combined, b.Combined); c != 0 {
		return c
	}
	if c := compareInt(a.GrayWorst, b.GrayWorst); c != 0 {
		return c
	}
	if c := compareLong(a.DsvWorst, b.DsvWorst); c != 0 {
		return c
	}
	if c := compareInt(a.GrayTotal, b.GrayTotal); c != 0 {
		return c
	}
	if c := compareLong(a.DsvTotal, b.DsvTotal); c != 0 {
		return c
	}
	return compareInt(int(a.Value), int(b.Value))
}

type pairPairScore struct {
	Black pairCandidateScore
	White pairCandidateScore
}

func comparePairScore(a, b pairPairScore) int {
	if c := compareInt(a.Black.BinaryWorst+a.White.BinaryWorst, b.Black.BinaryWorst+b.White.BinaryWorst); c != 0 {
		return c
	}
	if c := compareLong(a.Black.Combined+a.White.Combined, b.Black.Combined+b.White.Combined); c != 0 {
		return c
	}
	if c := compareInt(a.Black.GrayWorst+a.White.GrayWorst, b.Black.GrayWorst+b.White.GrayWorst); c != 0 {
		return c
	}
	if c := compareLong(a.Black.DsvWorst+a.White.DsvWorst, b.Black.DsvWorst+b.White.DsvWorst); c != 0 {
		return c
	}
	if c := compareInt(int(a.Black.Value), int(b.Black.Value)); c != 0 {
		return c
	}
	return compareInt(int(a.White.Value), int(b.White.Value))
}

var binaryBytePairMeasurements = buildPairMeasurements()
var binaryBytePairs = buildBinaryBytePairs()

// efmPlusBinaryBytePairGet ports EfmPlusBinaryBytePairs.Get.
func efmPlusBinaryBytePairGet(state byte, initialIsLand bool, dsv int) (efmPlusBinaryBytePair, error) {
	if state < 1 || state > 4 {
		return efmPlusBinaryBytePair{}, &argumentOutOfRangeError{param: "state"}
	}
	stateLevel := int(state-1)*2 + boolToInt(initialIsLand)
	if dsv < minimumCachedDsv || dsv > maximumCachedDsv {
		return findBinaryBytePair(binaryBytePairMeasurements[stateLevel], dsv)
	}
	return binaryBytePairs[stateLevel*dsvBucketCount+dsv-minimumCachedDsv], nil
}

// efmPlusBinaryBytePairAtZeroDsv ports EfmPlusBinaryBytePairs.AtZeroDsv.
type binaryBytePairEntry struct {
	State         byte
	InitialIsLand bool
	Pair          efmPlusBinaryBytePair
}

func efmPlusBinaryBytePairAtZeroDsv() []binaryBytePairEntry {
	var result []binaryBytePairEntry
	for state := 1; state <= 4; state++ {
		for level := 0; level < 2; level++ {
			pair, err := efmPlusBinaryBytePairGet(byte(state), level != 0, 0)
			if err != nil {
				panic(err)
			}
			result = append(result, binaryBytePairEntry{
				State:         byte(state),
				InitialIsLand: level != 0,
				Pair:          pair,
			})
		}
	}
	return result
}

func buildBinaryBytePairs() []efmPlusBinaryBytePair {
	result := make([]efmPlusBinaryBytePair, 8*dsvBucketCount)
	for stateLevel := 0; stateLevel < len(binaryBytePairMeasurements); stateLevel++ {
		for dsv := minimumCachedDsv; dsv <= maximumCachedDsv; dsv++ {
			pair, err := findBinaryBytePair(binaryBytePairMeasurements[stateLevel], dsv)
			if err != nil {
				panic(err)
			}
			result[stateLevel*dsvBucketCount+dsv-minimumCachedDsv] = pair
		}
	}
	return result
}

func buildPairMeasurements() [][]pairByteLevels {
	result := make([][]pairByteLevels, 8)
	for state := 1; state <= 4; state++ {
		for level := 0; level < 2; level++ {
			stateLevel := (state-1)*2 + level
			result[stateLevel] = make([]pairByteLevels, 256)
			for value := 0; value < 256; value++ {
				result[stateLevel][value] = measurePairByte(byte(value), byte(state), level != 0)
			}
		}
	}
	return result
}

func findBinaryBytePair(measurements []pairByteLevels, dsv int) (efmPlusBinaryBytePair, error) {
	var bestBlack, secondBlack, bestWhite, secondWhite pairCandidateScore
	var hasBlack, hasSecondBlack, hasWhite, hasSecondWhite bool
	for value := 0; value < len(measurements); value++ {
		byteValue := byte(value)
		levels := measurements[value]
		trackPairCandidate(newPairCandidateScore(byteValue, levels, dsv, false), &bestBlack, &secondBlack, &hasBlack, &hasSecondBlack)
		trackPairCandidate(newPairCandidateScore(byteValue, levels, dsv, true), &bestWhite, &secondWhite, &hasWhite, &hasSecondWhite)
	}

	var bestPair pairPairScore
	hasPair := false
	considerPair(bestBlack, bestWhite, &bestPair, &hasPair)
	if hasSecondWhite {
		considerPair(bestBlack, secondWhite, &bestPair, &hasPair)
	}
	if hasSecondBlack {
		considerPair(secondBlack, bestWhite, &bestPair, &hasPair)
	}
	if !hasPair {
		return efmPlusBinaryBytePair{}, newInvalidOperationError("Could not construct a distinct EFMPlus binary byte pair.")
	}
	return efmPlusBinaryBytePair{
		BlackByte:                   bestPair.Black.Value,
		WhiteByte:                   bestPair.White.Value,
		BlackWorstLandCount:         byte(bestPair.Black.GrayWorst),
		WhiteWorstLandCount:         byte(16 - bestPair.White.GrayWorst),
		ReferenceDsv:                dsv,
		BlackWorstAbsoluteOutputDsv: bestPair.Black.DsvWorst,
		WhiteWorstAbsoluteOutputDsv: bestPair.White.DsvWorst,
	}, nil
}

func trackPairCandidate(candidate pairCandidateScore, best, second *pairCandidateScore, hasBest, hasSecond *bool) {
	if !*hasBest || comparePairCandidateScore(candidate, *best) < 0 {
		*second = *best
		*hasSecond = *hasBest
		*best = candidate
		*hasBest = true
	} else if !*hasSecond || comparePairCandidateScore(candidate, *second) < 0 {
		*second = candidate
		*hasSecond = true
	}
}

func considerPair(black, white pairCandidateScore, best *pairPairScore, hasBest *bool) {
	if black.Value == white.Value {
		return
	}
	candidate := pairPairScore{Black: black, White: white}
	if !*hasBest || comparePairScore(candidate, *best) < 0 {
		*best = candidate
		*hasBest = true
	}
}

func measurePairByte(value, state byte, initialIsLand bool) pairByteLevels {
	mainWord, _ := efmPlusLookupMainMust(value, state)
	first := int(nrziMetric(mainWord, initialIsLand).LandCount)
	second := first
	count := 1
	if value <= 87 {
		subWord, _ := efmPlusSubstitutionMust(value, state)
		second = int(nrziMetric(subWord, initialIsLand).LandCount)
		count = 2
	} else if state == 1 || state == 4 {
		stateOneWord, _ := efmPlusLookupMainMust(value, 1)
		stateFourWord, _ := efmPlusLookupMainMust(value, 4)
		first = int(nrziMetric(stateOneWord, initialIsLand).LandCount)
		second = int(nrziMetric(stateFourWord, initialIsLand).LandCount)
		count = 2
	}
	return pairByteLevels{FirstLandCount: first, SecondLandCount: second, Count: count}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxLong(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func compareLong(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
