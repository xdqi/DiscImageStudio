package core

import (
	"fmt"
	"math"
)

// Tone levels.
const (
	TwoLevels  = 2
	FourLevels = 4
)

// Brightness bounds.
const (
	MinimumBrightness = -64.0
	MaximumBrightness = 64.0
)

// ToneProfile describes how a source grey ramp is translated into the disc's
// recordable levels. Ported from DiscImageStudio.Core/ToneProfile.cs.
type ToneProfile struct {
	DitherStrength    float64
	Levels            int
	Brightness        float64
	SrgbLinearization bool
}

// LegacyBinary is the pre-tone behaviour: one hard threshold at mid-grey.
var LegacyBinary = ToneProfile{DitherStrength: 0, Levels: TwoLevels, Brightness: 0}

var srgbLinearValues = createSrgbLinearValues()

func createSrgbLinearValues() [256]float64 {
	var values [256]float64
	for gray := 0; gray < 256; gray++ {
		value := float64(gray) / 255.0
		if value <= 0.04045 {
			values[gray] = value / 12.92
		} else {
			values[gray] = math.Pow((value+0.055)/1.055, 2.4)
		}
	}
	return values
}

// IsLegacyBinary reports whether mapping is exactly the old grey < 128 rule.
func (t ToneProfile) IsLegacyBinary() bool {
	return t.DitherStrength <= 0 && t.Levels == TwoLevels && t.Brightness == 0 && !t.SrgbLinearization
}

// IsDvdCompatible reports whether the profile has two levels.
func (t ToneProfile) IsDvdCompatible() bool { return t.Levels == TwoLevels }

// AdjustedGray maps source grey through sRGB linearization (when enabled) and
// the brightness offset, rounding away from zero like the C# original.
func (t ToneProfile) AdjustedGray(gray byte) byte {
	var value float64
	if t.SrgbLinearization {
		value = srgbLinearValues[gray]
	} else {
		value = float64(gray) / 255.0
	}
	value = clamp(value+t.Brightness/255.0, 0.0, 1.0)
	return byte(math.Round(value * 255.0)) // Go rounds away from zero, matching MidpointRounding.AwayFromZero.
}

// UseWhite is the binary decision shared with the engines' 128 threshold.
func (t ToneProfile) UseWhite(gray byte, sampleIndex int64, threshold float64) bool {
	if t.IsLegacyBinary() {
		return float64(gray) >= threshold
	}
	strength := t.DitherStrength
	if strength > 1 {
		strength = 1
	}
	shiftedThreshold := threshold + strength*255.0*(ToneDitherPhase(sampleIndex)-0.5)
	return float64(t.AdjustedGray(gray)) >= shiftedThreshold
}

// Validate checks the profile ranges, mirroring the C# exceptions.
func (t ToneProfile) Validate(fieldName string) error {
	if math.IsNaN(t.DitherStrength) || math.IsInf(t.DitherStrength, 0) || t.DitherStrength < 0 || t.DitherStrength > 1 {
		return fmt.Errorf("%s.ditherStrength must be a finite number between 0 and 1. (Parameter '%s')", fieldName, fieldName)
	}
	if t.Levels != TwoLevels && t.Levels != FourLevels {
		return fmt.Errorf("%s.levels must be %d or %d. (Parameter '%s')", fieldName, TwoLevels, FourLevels, fieldName)
	}
	if math.IsNaN(t.Brightness) || math.IsInf(t.Brightness, 0) || t.Brightness < MinimumBrightness || t.Brightness > MaximumBrightness {
		return fmt.Errorf("%s.brightness must be between %v and %v. (Parameter '%s')", fieldName, MinimumBrightness, MaximumBrightness, fieldName)
	}
	return nil
}

// Normalized clamps user-facing values into the supported range.
func (t ToneProfile) Normalized() ToneProfile {
	dither := t.DitherStrength
	if math.IsNaN(dither) || math.IsInf(dither, 0) {
		dither = 0
	}
	brightness := t.Brightness
	if math.IsNaN(brightness) || math.IsInf(brightness, 0) {
		brightness = 0
	}
	levels := TwoLevels
	if t.Levels == FourLevels {
		levels = FourLevels
	}
	return ToneProfile{
		DitherStrength:    clamp(dither, 0, 1),
		Levels:            levels,
		Brightness:        clamp(brightness, MinimumBrightness, MaximumBrightness),
		SrgbLinearization: t.SrgbLinearization,
	}
}

// ToneDitherPhase is the deterministic per-byte dither phase in [0, 1),
// ported from ToneDither.Phase.
func ToneDitherPhase(sampleIndex int64) float64 {
	value := uint32(sampleIndex) * 2654435761
	value ^= value >> 16
	value *= 0x7FEB352D
	value ^= value >> 15
	value *= 0x846CA68B
	value ^= value >> 16
	return float64(value) / 4294967296.0
}

func clamp(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// Clamp exports the Math.Clamp equivalent for other packages.
func Clamp(value, min, max float64) float64 { return clamp(value, min, max) }
