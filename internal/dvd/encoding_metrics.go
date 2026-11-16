package dvd

// nrziCodewordMetric ports NrziCodewordMetric.
type nrziCodewordMetric struct {
	LandCount     byte
	FinalIsLand   bool
	DsvDelta      int8
	LeadingZeros  byte
	TrailingZeros byte
}

const nrziWordCount = 1 << 16

var nrziCodewordMetrics = buildNrziCodewordMetrics()

func nrziMetric(word uint16, initialIsLand bool) nrziCodewordMetric {
	offset := 0
	if initialIsLand {
		offset = nrziWordCount
	}
	return nrziCodewordMetrics[offset+int(word)]
}

func buildNrziCodewordMetrics() []nrziCodewordMetric {
	result := make([]nrziCodewordMetric, nrziWordCount*2)
	for initialLevel := 0; initialLevel < 2; initialLevel++ {
		for wordValue := 0; wordValue < nrziWordCount; wordValue++ {
			word := uint16(wordValue)
			isLand := initialLevel != 0
			landCount := 0
			for bit := 15; bit >= 0; bit-- {
				if (word>>uint(bit))&1 != 0 {
					isLand = !isLand
				}
				if isLand {
					landCount++
				}
			}
			leadingZeros := 0
			for bit := 15; bit >= 0 && (word>>uint(bit))&1 == 0; bit-- {
				leadingZeros++
			}
			trailingZeros := 0
			for bit := 0; bit < 16 && (word>>uint(bit))&1 == 0; bit++ {
				trailingZeros++
			}
			result[initialLevel*nrziWordCount+wordValue] = nrziCodewordMetric{
				LandCount:     byte(landCount),
				FinalIsLand:   isLand,
				DsvDelta:      int8(landCount*2 - 16),
				LeadingZeros:  byte(leadingZeros),
				TrailingZeros: byte(trailingZeros),
			}
		}
	}
	return result
}
