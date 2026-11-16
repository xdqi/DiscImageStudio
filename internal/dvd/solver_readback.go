package dvd

import (
	"bytes"
	"encoding/json"
	"math"
	"os"

	"disc-image-studio/internal/raster"
)

// isoReadbackOptions ports IsoReadbackOptions.
type isoReadbackOptions struct {
	TotalSectors         uint32
	StartLba             uint32
	FillSectors          uint32
	PsnOffset            uint32
	InnerRadiusMm        float64
	OuterRadiusMm        float64
	ChannelBitLengthNm   float64
	Clockwise            bool
	PreviewSize          int
	SamplesPerSector     int
	PitchLinear          float64
	PitchQuadratic       float64
	PitchCubic           float64
	PreviewOuterRadiusMm *float64
}

// isoReadbackSummary ports IsoReadbackSummary.
type isoReadbackSummary struct {
	SourceIso             string
	OutputImage           string
	PreviewSize           int
	TotalSectors          uint32
	StartLba              uint32
	FillSectors           uint32
	ChannelBitLengthNm    float64
	TrackLengthMetres     float64
	TrackPitchMicrometres float64
	TotalTurns            float64
	InnerRadiusMm         float64
	OuterRadiusMm         float64
	SpiralDirection       string
	SamplesPerSector      int
	TotalSamples          int64
	ClassifiedSamples     int64
	MappingMode           string
	PitchLinear           float64
	PitchQuadratic        float64
	PitchCubic            float64
}

func (s *isoReadbackSummary) pitchLinearMicrometres() float64 {
	return s.TrackPitchMicrometres * s.PitchLinear
}
func (s *isoReadbackSummary) pitchQuadraticMicrometres() float64 {
	return s.TrackPitchMicrometres * s.PitchQuadratic
}
func (s *isoReadbackSummary) pitchCubicMicrometres() float64 {
	return s.TrackPitchMicrometres * s.PitchCubic
}

const readbackCodeWordCenterOffset = 8

// isoReadbackRender ports IsoReadbackRenderer.Render (sequential; the C#
// worker parallelism only partitions independent block reads).
func isoReadbackRender(isoPath, outputPath string, options isoReadbackOptions) (*isoReadbackSummary, error) {
	if err := readbackValidate(options); err != nil {
		return nil, err
	}
	isoFullPath := fullPath(isoPath)
	readCompatibilityMode := isoReadCompatibilityMode(isoFullPath)
	readCompatibility := readCompatibilityMode != nil
	isoSectors, err := fileLength(isoFullPath)
	if err != nil {
		return nil, err
	}
	isoSectors /= dvdPayloadBytesPerSector
	if uint64(options.StartLba)+uint64(options.FillSectors) > uint64(isoSectors) {
		return nil, &argumentOutOfRangeError{param: "options", msg: "The visible sector range exceeds the ISO image contents."}
	}

	bitLengthMm := options.ChannelBitLengthNm * 1e-6
	trackLengthMm := float64(options.TotalSectors) *
		dvdChannelBitsPerSector *
		bitLengthMm
	spiral, err := archimedeanSpiralCreate(
		options.InnerRadiusMm,
		options.OuterRadiusMm,
		trackLengthMm,
		options.PitchLinear,
		options.PitchQuadratic,
		options.PitchCubic)
	if err != nil {
		return nil, err
	}

	size := options.PreviewSize
	stride := size * 4
	pixels := make([]byte, stride*size)
	for i := range pixels {
		pixels[i] = 255
	}
	levelSums := make([]int, size*size)
	sampleCounts := make([]int, size*size)
	centre := float64(size-1) / 2.0
	direction := 1.0
	if options.Clockwise {
		direction = -1.0
	}
	samplesPerSector := options.SamplesPerSector
	byteStep := float64(dvdPayloadBytesPerSector) / float64(samplesPerSector)
	totalSamples := int64(options.FillSectors) * int64(samplesPerSector)
	var classifiedSamples int64

	totalBlocks := int(options.FillSectors / dvdEccSectorCount)
	iso, err := os.Open(isoFullPath)
	if err != nil {
		return nil, translateReadError(err, isoFullPath)
	}
	defer iso.Close()
	block := make([]byte, dvdPayloadBytesPerBlock)
	for blockIndex := 0; blockIndex < totalBlocks; blockIndex++ {
		blockStartLba, err := checkedAddU32(options.StartLba, uint32(blockIndex)*dvdEccSectorCount)
		if err != nil {
			return nil, err
		}
		if _, err := iso.ReadAt(block, int64(blockStartLba)*dvdPayloadBytesPerSector); err != nil {
			return nil, translateReadError(err, isoFullPath)
		}
		for sector := 0; sector < dvdEccSectorCount; sector++ {
			psn := blockStartLba + options.PsnOffset + uint32(sector)
			mask := payloadScramblerMask(psn)
			payload := block[sector*dvdPayloadBytesPerSector : sector*dvdPayloadBytesPerSector+dvdPayloadBytesPerSector]
			for sample := 0; sample < samplesPerSector; sample++ {
				byteIndex := int((float64(sample) + 0.5) * byteStep)
				if byteIndex >= len(payload) {
					byteIndex = len(payload) - 1
				}

				scrambled := payload[byteIndex] ^ mask[byteIndex]
				// Compatibility guards replace white texture bytes, so show their intended
				// tone only for a sidecar identifying that algorithm.
				var level byte
				if readCompatibility && scrambled == 0x9B {
					level = 255
				} else if l, ok := dvdTryGetLuminance(scrambled); ok {
					level = l
				} else {
					continue
				}

				payloadIndex := sector*dvdPayloadBytesPerSector + byteIndex
				channelOffset, err := payloadDirectChannelOffset(payloadIndex)
				if err != nil {
					return nil, err
				}
				globalBit := int64(blockStartLba)*dvdChannelBitsPerSector +
					int64(channelOffset) +
					readbackCodeWordCenterOffset
				arcLengthMm := float64(globalBit) * bitLengthMm
				radiusMm, trackAngle := spiral.atArcLengthFast(arcLengthMm)
				polarAngle := direction * trackAngle
				previewOuter := options.OuterRadiusMm
				if options.PreviewOuterRadiusMm != nil {
					previewOuter = *options.PreviewOuterRadiusMm
				}
				pixelRadius := centre * radiusMm / previewOuter
				pixelX := int(math.RoundToEven(centre + pixelRadius*math.Cos(polarAngle)))
				pixelY := int(math.RoundToEven(centre - pixelRadius*math.Sin(polarAngle)))
				if uint(pixelX) >= uint(size) || uint(pixelY) >= uint(size) {
					continue
				}

				pixelIndex := pixelY*size + pixelX
				levelSums[pixelIndex] += int(level)
				sampleCounts[pixelIndex]++
				classifiedSamples++
			}
		}
	}

	for pixelIndex := 0; pixelIndex < len(sampleCounts); pixelIndex++ {
		count := sampleCounts[pixelIndex]
		if count == 0 {
			continue
		}
		level := byte((levelSums[pixelIndex] + count/2) / count)
		offset := pixelIndex * 4
		pixels[offset] = level
		pixels[offset+1] = level
		pixels[offset+2] = level
	}

	if err := readbackWritePng(fullPath(outputPath), size, pixels); err != nil {
		return nil, err
	}
	mappingMode := "payload-scramble-classify-splat"
	if readCompatibility {
		mappingMode = "payload-scramble-classify-splat-" + *readCompatibilityMode
	}
	return &isoReadbackSummary{
		SourceIso:             isoFullPath,
		OutputImage:           fullPath(outputPath),
		PreviewSize:           size,
		TotalSectors:          options.TotalSectors,
		StartLba:              options.StartLba,
		FillSectors:           options.FillSectors,
		ChannelBitLengthNm:    options.ChannelBitLengthNm,
		TrackLengthMetres:     trackLengthMm / 1000.0,
		TrackPitchMicrometres: spiral.trackPitchMm() * 1000.0,
		TotalTurns:            spiral.totalAngleRadians() / (2 * math.Pi),
		InnerRadiusMm:         options.InnerRadiusMm,
		OuterRadiusMm:         options.OuterRadiusMm,
		SpiralDirection:       directionString(options.Clockwise),
		SamplesPerSector:      samplesPerSector,
		TotalSamples:          totalSamples,
		ClassifiedSamples:     classifiedSamples,
		MappingMode:           mappingMode,
		PitchLinear:           options.PitchLinear,
		PitchQuadratic:        options.PitchQuadratic,
		PitchCubic:            options.PitchCubic,
	}, nil
}

// isoReadbackReadSolveSummary ports IsoReadbackRenderer.ReadSolveSummary: a
// missing or malformed JSON sidecar falls back to zeros; I/O errors propagate.
func isoReadbackReadSolveSummary(sidecarPath string) (startLba, fillSectors, totalSectors uint32, err error) {
	if _, statErr := os.Stat(sidecarPath); statErr != nil {
		return 0, 0, 0, nil // File.Exists false in C#
	}
	data, err := dotnetReadAllBytes(sidecarPath)
	if err != nil {
		return 0, 0, 0, err
	}
	start, fill, total, ok := parseSolveSummaryJson(data)
	if !ok {
		return 0, 0, 0, nil
	}
	return start, fill, total, nil
}

func parseSolveSummaryJson(data []byte) (uint32, uint32, uint32, bool) {
	var root any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return 0, 0, 0, false
	}
	object, ok := root.(map[string]any)
	if !ok {
		return 0, 0, 0, true
	}
	return jsonGetUInt32(object, "startLba"), jsonGetUInt32(object, "fillSectors"), jsonGetUInt32(object, "totalSectors"), true
}

func jsonGetUInt32(object map[string]any, name string) uint32 {
	value, ok := object[name]
	if !ok {
		return 0
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0
	}
	text := number.String()
	// C# TryGetUInt32 only accepts plain non-negative integer literals.
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0
		}
	}
	if len(text) == 0 {
		return 0
	}
	parsed, err := strconvParseUint32(text)
	if err != nil {
		return 0
	}
	return parsed
}

func strconvParseUint32(text string) (uint32, error) {
	var value uint64
	for i := 0; i < len(text); i++ {
		value = value*10 + uint64(text[i]-'0')
		if value > 0xFFFFFFFF {
			return 0, newOverflowError("uint32 overflow")
		}
	}
	return uint32(value), nil
}

// isoReadCompatibilityMode ports IsoReadbackRenderer.ReadCompatibilityMode.
func isoReadCompatibilityMode(isoPath string) *string {
	sidecarPath := isoPath + ".json"
	if _, statErr := os.Stat(sidecarPath); statErr != nil {
		return nil
	}
	data, err := dotnetReadAllBytes(sidecarPath)
	if err != nil {
		return nil
	}
	var root any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil
	}
	object, ok := root.(map[string]any)
	if !ok {
		return nil
	}
	value, ok := object["readCompatibility"]
	if !ok {
		return nil
	}
	mode, ok := value.(string)
	if !ok || !dvdReadCompatibilityModeIsSupported(mode) {
		return nil
	}
	return &mode
}

func readbackWritePng(outputPath string, size int, pixels []byte) error {
	return raster.SavePng(outputPath, size, size, pixels, false)
}

func readbackValidate(options isoReadbackOptions) error {
	if options.TotalSectors < dvdEccSectorCount {
		return &argumentOutOfRangeError{param: "options", msg: "Total sector count must cover at least one ECC Block."}
	}
	if options.FillSectors == 0 ||
		options.FillSectors%dvdEccSectorCount != 0 ||
		options.StartLba%dvdEccSectorCount != 0 ||
		uint64(options.StartLba)+uint64(options.FillSectors) > uint64(options.TotalSectors) {
		return &argumentOutOfRangeError{param: "options", msg: "The visible range must be ECC-Block aligned and fit inside the disc."}
	}
	if math.IsNaN(options.ChannelBitLengthNm) || math.IsInf(options.ChannelBitLengthNm, 0) || options.ChannelBitLengthNm <= 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Channel-bit length must be positive."}
	}
	if options.PreviewSize < 64 || options.PreviewSize > 8192 {
		return &argumentOutOfRangeError{param: "options", msg: "Preview size must be 64..8192 pixels."}
	}
	if options.SamplesPerSector < 1 || options.SamplesPerSector > dvdPayloadBytesPerSector {
		return &argumentOutOfRangeError{param: "options", msg: "Preview samples per sector must be 1..2048."}
	}
	return nil
}
