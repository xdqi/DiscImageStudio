package dvd

import (
	"fmt"
	"math"

	"disc-image-studio/internal/raster"
)

// This file implements the solve --usage-map preview: a disc rendering where
// every sector is painted by its usage —
//   - data sectors (filesystem metadata or file content): #FF00FF,
//   - picture sectors (drawn by the image, including protected victims whose
//     arc only grazes a picture feature): the image's own tone — black,
//     white, or the gray of the dispersed gray band,
//   - unoccupied sectors: #00FFFF.

// Sector usage classes rendered by renderSectorUsageMap.
const (
	sectorUsageFree    = 0 // unoccupied → #00FFFF
	sectorUsageData    = 1 // filesystem metadata or file content → #FF00FF
	sectorUsagePicture = 2 // drawn by the picture (or a protected victim) → image tone
)

const (
	usageMapPreviewSize      = 1024
	usageMapSamplesPerSector = 16
)

// classifySolveSectors builds the per-sector usage classes for the plain and
// contiguous-hybrid solve paths: the hybrid ISO's written ranges are data,
// the drawing range is picture, everything else is unoccupied.
func classifySolveSectors(totalSectors, startLba, fillSectors uint32, hybrid *hybridIsoWriteSummary) []uint8 {
	classes := make([]uint8, totalSectors) // sectorUsageFree
	if hybrid != nil {
		if hybrid.FilesAtOuterRing {
			markUsageRange(classes, 0, hybrid.MetadataEndLbaExclusive, sectorUsageData)
			markUsageRange(classes, hybrid.FileDataStartLba, hybrid.VolumeSectors, sectorUsageData)
		} else {
			markUsageRange(classes, 0, hybrid.DataEndLbaExclusive, sectorUsageData)
		}
	}
	markUsageRange(classes, startLba, startLba+fillSectors, sectorUsagePicture)
	return classes
}

// classifyDispersedSectors builds the per-sector usage classes for the
// dispersed UDF pipeline: the reserved metadata ranges and the placed file
// extents are data, eligible-but-unused gray capacity is unoccupied, and
// every remaining sector is picture (directly drawn or a protected victim).
func classifyDispersedSectors(totalSectors, metaEnd uint32, eligible []bool, extents []fileExtent) []uint8 {
	classes := make([]uint8, totalSectors)
	reserveFrom := uint32(0)
	if totalSectors >= 257 {
		reserveFrom = totalSectors - 257
	}
	for lba := uint32(0); lba < totalSectors; lba++ {
		class := uint8(sectorUsagePicture)
		if lba < metaEnd || lba >= reserveFrom {
			class = sectorUsageData
		}
		classes[lba] = class
	}
	for _, extent := range extents {
		markUsageRange(classes, extent.lba, extent.lba+extent.sectors, sectorUsageData)
	}
	for lba := metaEnd; lba < reserveFrom && lba < uint32(len(eligible)); lba++ {
		if classes[lba] == sectorUsagePicture && eligible[lba] {
			classes[lba] = sectorUsageFree
		}
	}
	return classes
}

func markUsageRange(classes []uint8, from, to uint32, class uint8) {
	if to > uint32(len(classes)) {
		to = uint32(len(classes))
	}
	for lba := from; lba < to; lba++ {
		classes[lba] = class
	}
}

// maybeRenderUsageMap renders the --usage-map PNG when the option is present.
func maybeRenderUsageMap(arguments *cliArguments, mapping imageMappingOptions, classes []uint8, grayBand *[2]byte) error {
	if !arguments.Has("usage-map") {
		return nil
	}
	outputPath, err := arguments.Require("usage-map")
	if err != nil {
		return err
	}
	imagePath, err := arguments.Require("image")
	if err != nil {
		return err
	}
	image, err := rasterImageLoad(imagePath)
	if err != nil {
		return err
	}
	if err := renderSectorUsageMap(image, mapping, classes, grayBand, outputPath); err != nil {
		return err
	}
	fmt.Printf("usage map: %dx%d, output=%s\n", usageMapPreviewSize, usageMapPreviewSize, outputPath)
	return nil
}

// renderSectorUsageMap walks every sector's track arc and paints each sample
// as an arc segment one track pitch wide, so the bands read solid at any disc
// size (a plain point splat leaves the between-turn gaps visible on small
// images). Samples sharing a preview pixel are averaged, like the calibration
// preview. Disc-surface pixels the arc walk did not touch count as
// unoccupied.
func renderSectorUsageMap(image *rasterImage, options imageMappingOptions, classes []uint8, grayBand *[2]byte, outputPath string) error {
	if uint32(len(classes)) != options.TotalSectors {
		return fmt.Errorf("internal: usage classes cover %d sectors, but the disc has %d", len(classes), options.TotalSectors)
	}
	bitLengthMm := options.ChannelBitLengthNm * 1e-6
	trackLengthMm := float64(options.TotalSectors) * dvdChannelBitsPerSector * bitLengthMm
	spiral, err := archimedeanSpiralCreate(
		options.InnerRadiusMm,
		options.OuterRadiusMm,
		trackLengthMm,
		options.PitchLinear,
		options.PitchQuadratic,
		options.PitchCubic)
	if err != nil {
		return err
	}
	const size = usageMapPreviewSize
	pixels := make([]byte, size*size*4)
	sums := make([]uint32, size*size*3)
	counts := make([]uint32, size*size)
	centre := float64(size-1) / 2.0
	direction := 1.0
	if options.Clockwise {
		direction = -1.0
	}
	startAngleRadians := options.StartAngleDegrees * math.Pi / 180.0
	samplingOuter := options.samplingOuterRadiusMm()
	millimetresPerPixel := (2 * options.OuterRadiusMm) / float64(size-1)
	threshold := options.LuminanceThreshold
	if threshold == 0 {
		threshold = 128
	}
	sampleArcMm := dvdChannelBitsPerSector * bitLengthMm / usageMapSamplesPerSector
	for sector := uint32(0); sector < options.TotalSectors; sector++ {
		class := classes[sector]
		for k := 0; k < usageMapSamplesPerSector; k++ {
			globalChannelBit := (float64(sector) + (float64(k)+0.5)/usageMapSamplesPerSector) * dvdChannelBitsPerSector
			arcLengthMm := globalChannelBit * bitLengthMm
			radiusMm, trackAngle := spiral.atArcLengthFast(arcLengthMm)
			nextArcMm := arcLengthMm + sampleArcMm
			if nextArcMm > trackLengthMm {
				nextArcMm = trackLengthMm
			}
			nextRadiusMm, nextTrackAngle := spiral.atArcLengthFast(nextArcMm)
			// The segment covers half the local track pitch on both sides, so
			// adjacent turns tile the surface instead of leaving gaps.
			turnArcMm := arcLengthMm + 2*math.Pi*radiusMm
			if turnArcMm > trackLengthMm {
				turnArcMm = trackLengthMm
			}
			pitchMm := math.Abs(spiral.radiusAtArcLengthFast(turnArcMm) - radiusMm)
			pad := int(math.RoundToEven(pitchMm / 2 / millimetresPerPixel))
			var blue, green, red int
			polarAngle := startAngleRadians + direction*trackAngle
			cosine, sine := math.Cos(polarAngle), math.Sin(polarAngle)
			switch class {
			case sectorUsageData:
				blue, green, red = 255, 0, 255 // #FF00FF
			case sectorUsageFree:
				blue, green, red = 255, 255, 0 // #00FFFF
			default:
				sample := image.sampleDisc(radiusMm*cosine, radiusMm*sine, samplingOuter, true)
				luminance := sample.Luminance
				if sample.Alpha < options.AlphaThreshold {
					luminance = 255 // unconstrained reads as white
				}
				level := 0
				if grayBand != nil && luminance >= grayBand[0] && luminance <= grayBand[1] {
					level = 128 // the dispersed writer's gray data tone
				} else if dvdToneUseWhite(luminance, threshold, options.Tone, int64(globalChannelBit/16.0)) {
					level = 255
				}
				blue, green, red = level, level, level
			}
			pixelRadius := centre * radiusMm / options.OuterRadiusMm
			x0 := centre + pixelRadius*cosine
			y0 := centre - pixelRadius*sine
			nextPolarAngle := startAngleRadians + direction*nextTrackAngle
			nextPixelRadius := centre * nextRadiusMm / options.OuterRadiusMm
			x1 := centre + nextPixelRadius*math.Cos(nextPolarAngle)
			y1 := centre - nextPixelRadius*math.Sin(nextPolarAngle)
			minX := int(math.Floor(math.Min(x0, x1))) - pad
			maxX := int(math.Floor(math.Max(x0, x1))) + pad
			minY := int(math.Floor(math.Min(y0, y1))) - pad
			maxY := int(math.Floor(math.Max(y0, y1))) + pad
			for pixelY := minY; pixelY <= maxY; pixelY++ {
				if uint(pixelY) >= uint(size) {
					continue
				}
				for pixelX := minX; pixelX <= maxX; pixelX++ {
					if uint(pixelX) >= uint(size) {
						continue
					}
					pixelIndex := pixelY*size + pixelX
					sums[pixelIndex*3] += uint32(blue)
					sums[pixelIndex*3+1] += uint32(green)
					sums[pixelIndex*3+2] += uint32(red)
					counts[pixelIndex]++
				}
			}
		}
	}

	for pixelY := 0; pixelY < size; pixelY++ {
		yMm := (centre - float64(pixelY)) * millimetresPerPixel
		for pixelX := 0; pixelX < size; pixelX++ {
			xMm := (float64(pixelX) - centre) * millimetresPerPixel
			radiusMm := math.Sqrt(xMm*xMm + yMm*yMm)
			if radiusMm < options.InnerRadiusMm || radiusMm > options.OuterRadiusMm {
				continue // outside the disc surface: transparent
			}
			pixelIndex := pixelY*size + pixelX
			offset := pixelIndex * 4
			if count := counts[pixelIndex]; count == 0 {
				pixels[offset] = 255 // #00FFFF
				pixels[offset+1] = 255
				pixels[offset+2] = 0
			} else {
				for channel := 0; channel < 3; channel++ {
					pixels[offset+channel] = byte((sums[pixelIndex*3+channel] + count/2) / count)
				}
			}
			pixels[offset+3] = 255
		}
	}
	return raster.SavePng(outputPath, size, size, pixels, false)
}
