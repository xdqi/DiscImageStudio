package dvd

import (
	"os"
	"unicode"
)

const constraintUnconstrained int8 = -1

// imageConstraint ports ImageConstraint.
type imageConstraint struct {
	targets                     []int8
	channelWordTargets          []int8
	channelWordGrayscaleTargets []int8
	constrainedSectors          []int
	constrainedBits             int
	imageMapping                *imageMappingSummary
}

func newImageConstraint(targets []int8, constrainedBits int, imageMapping *imageMappingSummary, channelWordTargets, channelWordGrayscaleTargets []int8) (*imageConstraint, error) {
	c := &imageConstraint{}
	if channelWordTargets == nil {
		channelWordTargets = buildChannelWordTargets(targets, imageMapping != nil)
	}
	if len(channelWordTargets) != dvdChannelBitsPerBlock/16 {
		return nil, newArgumentErrorParam("Unexpected channel-word target count.", "channelWordTargets")
	}
	// Dispersion uses sampled black/white levels; the full/state-control solvers
	// retain their original binary channel-level and land-count targets.
	if channelWordGrayscaleTargets == nil {
		channelWordGrayscaleTargets = []int8{}
	}
	if len(channelWordGrayscaleTargets) != 0 {
		if len(channelWordGrayscaleTargets) != len(channelWordTargets) {
			return nil, newArgumentErrorParam("Grayscale targets must contain one level (-1 or 0..1) per channel word.", "channelWordGrayscaleTargets")
		}
		for _, value := range channelWordGrayscaleTargets {
			if value < -1 || value > 1 {
				return nil, newArgumentErrorParam("Grayscale targets must contain one level (-1 or 0..1) per channel word.", "channelWordGrayscaleTargets")
			}
		}
	}
	c.targets = targets
	c.channelWordTargets = channelWordTargets
	c.channelWordGrayscaleTargets = channelWordGrayscaleTargets
	c.constrainedBits = constrainedBits
	c.imageMapping = imageMapping
	for sector := 0; sector < dvdEccSectorCount; sector++ {
		span := targets[sector*dvdChannelBitsPerSector : sector*dvdChannelBitsPerSector+dvdChannelBitsPerSector]
		constrained := false
		for _, v := range span {
			if v != constraintUnconstrained {
				constrained = true
				break
			}
		}
		if constrained {
			c.constrainedSectors = append(c.constrainedSectors, sector)
		}
	}
	return c, nil
}

func imageConstraintLoad(path string, targetSector int) (*imageConstraint, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, translateReadError(err, path)
	}
	var symbols []int8
	for _, character := range string(text) {
		switch character {
		case '0', 'p', 'P':
			symbols = append(symbols, 0) // Pit, black.
		case '1', 'l', 'L':
			symbols = append(symbols, 1) // Land, white.
		case '?':
			symbols = append(symbols, constraintUnconstrained)
		default:
			if !unicode.IsSpace(character) && character != '_' {
				return nil, &formatError{"Unsupported target character '" + string(character) + "'. Use 0/P, 1/L, or ?."}
			}
		}
	}

	targets := make([]int8, dvdChannelBitsPerBlock)
	for i := range targets {
		targets[i] = constraintUnconstrained
	}
	if len(symbols) == dvdChannelBitsPerBlock {
		copy(targets, symbols)
	} else if len(symbols) == dvdChannelBitsPerSector {
		if targetSector < 0 || targetSector >= dvdEccSectorCount {
			return nil, &argumentOutOfRangeError{param: "targetSector", msg: "Target sector must be 0..15."}
		}
		copy(targets[targetSector*dvdChannelBitsPerSector:], symbols)
	} else {
		return nil, &formatError{
			"Target contains " + itoa(int64(len(symbols))) + " symbols; expected either " +
				"38688 (one sector) or 619008 (one ECC Block)."}
	}

	constrained := 0
	for _, value := range targets {
		if value != constraintUnconstrained {
			constrained++
		}
	}
	if constrained == 0 {
		return nil, &formatError{"Target does not constrain any channel level."}
	}
	return newImageConstraint(targets, constrained, nil, nil, nil)
}

func imageConstraintLoadImage(path string, initialLba uint32, options imageMappingOptions) (*imageConstraint, error) {
	return imageTargetMapperMapPath(path, initialLba, options)
}

func (c *imageConstraint) saveText(path string) error {
	symbols := make([]byte, len(c.targets))
	for index := 0; index < len(c.targets); index++ {
		switch c.targets[index] {
		case 0:
			symbols[index] = 'P'
		case 1:
			symbols[index] = 'L'
		default:
			symbols[index] = '?'
		}
	}
	return os.WriteFile(path, symbols, 0o666)
}

func (c *imageConstraint) score(channelLevels []byte) (int, error) {
	if len(channelLevels) != len(c.targets) {
		return 0, newArgumentErrorParam("Unexpected channel-level count.", "channelLevels")
	}
	mismatches := 0
	for index := 0; index < len(c.targets); index++ {
		target := c.targets[index]
		if target != constraintUnconstrained && int8(channelLevels[index]) != target {
			mismatches++
		}
	}
	return mismatches, nil
}

func (c *imageConstraint) samplePayloadIndex(random *dotnetRandom) int {
	// Most proposals should touch a sector that is visibly constrained. A smaller
	// global share remains necessary because PO parity couples all 16 sectors.
	if len(c.constrainedSectors) > 0 &&
		len(c.constrainedSectors) != dvdEccSectorCount &&
		random.nextDouble() < 0.8 {
		sector := c.constrainedSectors[random.nextMax(int32(len(c.constrainedSectors)))]
		return sector*dvdPayloadBytesPerSector + int(random.nextMax(dvdPayloadBytesPerSector))
	}
	return int(random.nextMax(dvdPayloadBytesPerBlock))
}

func imageConstraintCreate(targets []int8, imageMapping *imageMappingSummary, channelWordTargets, channelWordGrayscaleTargets []int8) (*imageConstraint, error) {
	if len(targets) != dvdChannelBitsPerBlock {
		return nil, newArgumentErrorParam("Unexpected image target size.", "targets")
	}
	constrained := 0
	for _, value := range targets {
		if value != constraintUnconstrained {
			constrained++
		}
	}
	return newImageConstraint(targets, constrained, imageMapping, channelWordTargets, channelWordGrayscaleTargets)
}

func (c *imageConstraint) targetAt(index int) int8 { return c.targets[index] }

func buildChannelWordTargets(channelTargets []int8, binaryTargets bool) []int8 {
	result := make([]int8, dvdChannelBitsPerBlock/16)
	for i := range result {
		result[i] = constraintUnconstrained
	}
	for wordIndex := 0; wordIndex < len(result); wordIndex++ {
		channelOffset := wordIndex * 16
		samples := 0
		landSamples := 0
		tieBreaker := constraintUnconstrained
		tieBreakerDistance := int(^uint(0) >> 1)
		for bit := 0; bit < 16; bit++ {
			target := channelTargets[channelOffset+bit]
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
		if binaryTargets {
			if landSamples*2 == samples {
				result[wordIndex] = tieBreaker * 16
			} else if landSamples*2 > samples {
				result[wordIndex] = 16
			} else {
				result[wordIndex] = 0
			}
		} else {
			result[wordIndex] = int8((landSamples*16 + samples/2) / samples)
		}
	}
	return result
}
