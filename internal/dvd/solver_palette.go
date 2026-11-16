package dvd

import "disc-image-studio/internal/core"

// DvdGrayscalePalette port: black/white DVD tones as scrambled bytes Q.
const (
	dvdBlackScrambledByte byte = 0xA5
	dvdWhiteScrambledByte byte = 0x92
)

var dvdPaletteScrambledBytes = []byte{dvdBlackScrambledByte, dvdWhiteScrambledByte}

func dvdGetScrambledByte(luminance byte) byte {
	return dvdGetScrambledByteForLevel(core.BinaryLevelIndex(luminance))
}

func dvdGetScrambledByteForLevel(levelIndex int) byte {
	switch levelIndex {
	case 0:
		return dvdBlackScrambledByte
	case 1:
		return dvdWhiteScrambledByte
	default:
		panic(&argumentOutOfRangeError{param: "levelIndex"})
	}
}

func dvdTryGetLuminance(scrambledByte byte) (byte, bool) {
	value := -1
	switch scrambledByte {
	case dvdBlackScrambledByte:
		value = 0
	case dvdWhiteScrambledByte:
		value = 255
	// Legacy four-tone ISOs used Q=66 as dark gray. On the tested disc it
	// merges with A5 at ordinary viewing angles; never generate it anew.
	case 0x66:
		value = 0
	// Historical three-/four-tone output is still readable. Its B9 tone
	// retains the nominal dark appearance rather than being called white.
	case 0xB9:
		value = 85
	}
	if value < 0 {
		return 0, false
	}
	return byte(value), true
}

// dvdToneUseWhite ports DvdToneMapping.UseWhite.
func dvdToneUseWhite(luminance, threshold byte, tone *core.ToneProfile, sampleIndex int64) bool {
	if tone != nil {
		return tone.UseWhite(luminance, sampleIndex, float64(threshold))
	}
	return int(luminance) >= int(threshold)
}
