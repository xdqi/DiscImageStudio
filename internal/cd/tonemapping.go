package cd

import (
	"disc-image-studio/internal/core"
)

// Display greys of the palette bytes; also the tone-mapping endpoints.
const (
	BlackGray = 0
	WhiteGray = 255
)

var twoLevelPalette = []byte{0x0D, 0xAA}
var fourLevelPalette = []byte{0x0D, 0x10, 0x63, 0xAA}

// TonePalette returns the palette for the given number of levels.
func TonePalette(levels int) []byte {
	if levels == core.FourLevels {
		return fourLevelPalette
	}
	return twoLevelPalette
}

// ToneMap maps one source grey to the palette byte that encodes it.
// Ported from CdToneMapping.Map.
func ToneMap(gray byte, tone core.ToneProfile, sampleIndex int64) byte {
	if tone.IsLegacyBinary() {
		if gray < 128 {
			return twoLevelPalette[0]
		}
		return twoLevelPalette[1]
	}

	palette := TonePalette(tone.Levels)
	last := len(palette) - 1
	position := clampF(float64(tone.AdjustedGray(gray)-BlackGray)/float64(WhiteGray-BlackGray), 0.0, 1.0) * float64(last)
	index := int(position)
	if index >= last {
		return palette[last]
	}

	fraction := position - float64(index)
	threshold := 0.5
	if tone.DitherStrength > 0 {
		strength := tone.DitherStrength
		if strength > 1 {
			strength = 1
		}
		threshold += strength * (core.ToneDitherPhase(sampleIndex) - 0.5)
	}

	if fraction >= threshold {
		return palette[index+1]
	}
	return palette[index]
}

// ToneDisplayGray is the display grey of a palette byte.
func ToneDisplayGray(paletteByte byte) byte {
	switch paletteByte {
	case 0x0D:
		return 0
	case 0x10:
		return 85
	case 0x21:
		return 96
	case 0x28:
		return 160
	case 0x63:
		return 170
	case 0xAA:
		return 255
	default:
		return paletteByte
	}
}
