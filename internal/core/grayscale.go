package core

import "fmt"

// BinaryLevels is the shared black/white palette.
var BinaryLevels = []byte{0, 255}

// BinaryLevelIndex maps luminance to 0 (dark) or 1 (light).
func BinaryLevelIndex(luminance byte) int {
	if luminance < 128 {
		return 0
	}
	return 1
}

// BinaryLuminance returns the nominal luminance of a binary level index.
func BinaryLuminance(levelIndex int) (byte, error) {
	switch levelIndex {
	case 0:
		return 0, nil
	case 1:
		return 255, nil
	default:
		return 0, fmt.Errorf("grayscale level must be 0..1")
	}
}

// BinaryQuantize snaps luminance to 0 or 255.
func BinaryQuantize(luminance byte) byte {
	v, _ := BinaryLuminance(BinaryLevelIndex(luminance))
	return v
}

// FourLevelLevelIndex maps luminance into equal-width bins at 64/128/192.
func FourLevelLevelIndex(luminance byte) int { return int(luminance) >> 6 }

// FourLevelLuminance returns the nominal display luminance of a 4-level index.
func FourLevelLuminance(levelIndex int) (byte, error) {
	if levelIndex < 0 || levelIndex > 3 {
		return 0, fmt.Errorf("grayscale level must be 0..3")
	}
	return byte(levelIndex * 85), nil
}

// FourLevelQuantize snaps luminance to the nearest bin's nominal level.
func FourLevelQuantize(luminance byte) byte {
	v, _ := FourLevelLuminance(FourLevelLevelIndex(luminance))
	return v
}

// ThreeLevelMiddleLuminance is the nonuniform middle tone.
const ThreeLevelMiddleLuminance byte = 85

// ThreeLevelLevels is the nonuniform nominal palette.
var ThreeLevelLevels = []byte{0, ThreeLevelMiddleLuminance, 255}

// ThreeLevelIndex maps to the nearest nominal level; ties go lighter.
func ThreeLevelIndex(luminance byte) int {
	switch l := int(luminance) * 2; {
	case l < int(ThreeLevelMiddleLuminance):
		return 0
	case l < int(ThreeLevelMiddleLuminance)+255:
		return 1
	default:
		return 2
	}
}

// ThreeLevelLuminance returns the nominal luminance of a 3-level index.
func ThreeLevelLuminance(levelIndex int) (byte, error) {
	switch levelIndex {
	case 0:
		return 0, nil
	case 1:
		return ThreeLevelMiddleLuminance, nil
	case 2:
		return 255, nil
	default:
		return 0, fmt.Errorf("grayscale level must be 0..2")
	}
}

// ThreeLevelQuantize snaps luminance to the nearest nominal level.
func ThreeLevelQuantize(luminance byte) byte {
	v, _ := ThreeLevelLuminance(ThreeLevelIndex(luminance))
	return v
}
