package dvd

import (
	"math"

	"disc-image-studio/internal/core"
	"disc-image-studio/internal/raster"
)

// calibrationPreviewOptions ports CalibrationPreviewOptions.
type calibrationPreviewOptions struct {
	TotalSectors             uint32
	StartLba                 uint32
	FillSectors              uint32
	GeneratedInnerRadiusMm   float64
	GeneratedOuterRadiusMm   float64
	ActualInnerRadiusMm      float64
	ActualOuterRadiusMm      float64
	ChannelBitLengthNm       float64
	Clockwise                bool
	LuminanceThreshold       byte
	AlphaThreshold           byte
	PreviewSize              int
	SamplesPerSector         int
	GeneratedPitchLinear     float64
	GeneratedPitchQuadratic  float64
	GeneratedPitchCubic      float64
	ActualPitchLinear        float64
	ActualPitchQuadratic     float64
	ActualPitchCubic         float64
	Tone                     *core.ToneProfile
	SourceImageOuterRadiusMm *float64
	PreviewOuterRadiusMm     *float64
}

// calibrationPreviewSummary ports CalibrationPreviewSummary.
type calibrationPreviewSummary struct {
	SourceImage                    string
	OutputImage                    string
	SourceWidth                    int
	SourceHeight                   int
	PreviewSize                    int
	TotalSectors                   uint32
	StartLba                       uint32
	FillSectors                    uint32
	ChannelBitLengthNm             float64
	TrackLengthMetres              float64
	GeneratedInnerRadiusMm         float64
	GeneratedOuterRadiusMm         float64
	GeneratedTrackPitchMicrometres float64
	GeneratedTurns                 float64
	ActualInnerRadiusMm            float64
	ActualOuterRadiusMm            float64
	ActualTrackPitchMicrometres    float64
	ActualTurns                    float64
	SpiralDirection                string
	LuminanceThreshold             byte
	AlphaThreshold                 byte
	SamplesPerSector               int
	MappingMode                    string
	GeneratedPitchLinear           float64
	GeneratedPitchQuadratic        float64
	GeneratedPitchCubic            float64
	ActualPitchLinear              float64
	ActualPitchQuadratic           float64
	ActualPitchCubic               float64
	Tone                           *core.ToneProfile
}

func (s *calibrationPreviewSummary) generatedPitchLinearMicrometres() float64 {
	return s.GeneratedTrackPitchMicrometres * s.GeneratedPitchLinear
}
func (s *calibrationPreviewSummary) generatedPitchQuadraticMicrometres() float64 {
	return s.GeneratedTrackPitchMicrometres * s.GeneratedPitchQuadratic
}
func (s *calibrationPreviewSummary) generatedPitchCubicMicrometres() float64 {
	return s.GeneratedTrackPitchMicrometres * s.GeneratedPitchCubic
}
func (s *calibrationPreviewSummary) actualPitchLinearMicrometres() float64 {
	return s.ActualTrackPitchMicrometres * s.ActualPitchLinear
}
func (s *calibrationPreviewSummary) actualPitchQuadraticMicrometres() float64 {
	return s.ActualTrackPitchMicrometres * s.ActualPitchQuadratic
}
func (s *calibrationPreviewSummary) actualPitchCubicMicrometres() float64 {
	return s.ActualTrackPitchMicrometres * s.ActualPitchCubic
}

// calibrationPreviewRender ports CalibrationPreviewRenderer.Render.
func calibrationPreviewRender(imagePath, outputPath string, options calibrationPreviewOptions) (*calibrationPreviewSummary, error) {
	if err := calibrateValidate(options); err != nil {
		return nil, err
	}
	sourceFullPath := fullPath(imagePath)
	outputFullPath := fullPath(outputPath)
	source, err := rasterImageLoad(sourceFullPath)
	if err != nil {
		return nil, err
	}
	trackLengthMm := float64(options.TotalSectors) *
		dvdChannelBitsPerSector *
		options.ChannelBitLengthNm *
		1e-6
	generated, err := archimedeanSpiralCreate(
		options.GeneratedInnerRadiusMm,
		options.GeneratedOuterRadiusMm,
		trackLengthMm,
		options.GeneratedPitchLinear,
		options.GeneratedPitchQuadratic,
		options.GeneratedPitchCubic)
	if err != nil {
		return nil, err
	}
	actual, err := archimedeanSpiralCreate(
		options.ActualInnerRadiusMm,
		options.ActualOuterRadiusMm,
		trackLengthMm,
		options.ActualPitchLinear,
		options.ActualPitchQuadratic,
		options.ActualPitchCubic)
	if err != nil {
		return nil, err
	}

	size := options.PreviewSize
	stride := size * 4
	pixels := make([]byte, stride*size)
	weightedLevelSums := make([]int, size*size)
	alphaSums := make([]int, size*size)
	centre := float64(size-1) / 2.0
	previewOuterRadius := options.ActualOuterRadiusMm
	if options.PreviewOuterRadiusMm != nil {
		previewOuterRadius = *options.PreviewOuterRadiusMm
	}
	millimetresPerPixel := (2 * previewOuterRadius) / float64(size-1)
	direction := 1.0
	if options.Clockwise {
		direction = -1.0
	}
	const fullTurn = 2 * math.Pi
	visibleStartMm := float64(options.StartLba) *
		dvdChannelBitsPerSector *
		options.ChannelBitLengthNm *
		1e-6
	visibleEndMm := (float64(options.StartLba) + float64(options.FillSectors)) *
		dvdChannelBitsPerSector *
		options.ChannelBitLengthNm *
		1e-6
	visibleStartRadiusMm := actual.radiusAtArcLengthFast(visibleStartMm)
	visibleEndRadiusMm := actual.radiusAtArcLengthFast(visibleEndMm)

	// As in the CD preview, walk forward through the logical track: sample the
	// source at the generated position, then splat that same channel position at
	// the guessed actual geometry. Multiple track samples landing in one preview
	// pixel are averaged instead of allowing the last writer to win.
	totalSamples := int64(options.FillSectors) * int64(options.SamplesPerSector)
	channelBitsPerSample := float64(dvdChannelBitsPerSector) / float64(options.SamplesPerSector)
	firstGlobalChannelBit := float64(options.StartLba) * dvdChannelBitsPerSector
	for sampleIndex := int64(0); sampleIndex < totalSamples; sampleIndex++ {
		globalChannelBit := firstGlobalChannelBit + (float64(sampleIndex)+0.5)*channelBitsPerSample
		arcLengthMm := globalChannelBit * options.ChannelBitLengthNm * 1e-6
		generatedRadiusMm, generatedTrackAngle := generated.atArcLengthFast(arcLengthMm)
		generatedPolarAngle := direction * generatedTrackAngle
		sourceOuterRadius := generated.outerRadiusMm()
		if options.SourceImageOuterRadiusMm != nil {
			sourceOuterRadius = *options.SourceImageOuterRadiusMm
		}
		sample := source.sampleDisc(
			generatedRadiusMm*math.Cos(generatedPolarAngle),
			generatedRadiusMm*math.Sin(generatedPolarAngle),
			sourceOuterRadius,
			true)
		if sample.Alpha < options.AlphaThreshold {
			continue
		}

		actualRadiusMm, actualTrackAngle := actual.atArcLengthFast(arcLengthMm)
		actualPolarAngle := direction * actualTrackAngle
		pixelRadius := centre * actualRadiusMm / previewOuterRadius
		pixelX := int(math.RoundToEven(centre + pixelRadius*math.Cos(actualPolarAngle)))
		pixelY := int(math.RoundToEven(centre - pixelRadius*math.Sin(actualPolarAngle)))
		if uint(pixelX) >= uint(size) || uint(pixelY) >= uint(size) {
			continue
		}

		pixelIndex := pixelY*size + pixelX
		binaryLevel := 0
		if dvdToneUseWhite(sample.Luminance, options.LuminanceThreshold, options.Tone, int64(globalChannelBit/16.0)) {
			binaryLevel = 255
		}
		weightedLevelSums[pixelIndex] += binaryLevel * int(sample.Alpha)
		alphaSums[pixelIndex] += int(sample.Alpha)
	}

	for pixelY := 0; pixelY < size; pixelY++ {
		yMm := (centre - float64(pixelY)) * millimetresPerPixel
		rowOffset := pixelY * stride
		for pixelX := 0; pixelX < size; pixelX++ {
			xMm := (float64(pixelX) - centre) * millimetresPerPixel
			radiusMm := math.Sqrt(xMm*xMm + yMm*yMm)
			if radiusMm < visibleStartRadiusMm || radiusMm > visibleEndRadiusMm {
				continue
			}

			pixelIndex := pixelY*size + pixelX
			alphaSum := alphaSums[pixelIndex]
			var level byte
			if alphaSum == 0 {
				level = 255
			} else {
				level = byte(clampInt((weightedLevelSums[pixelIndex]+alphaSum/2)/alphaSum, 0, 255))
			}
			offset := rowOffset + pixelX*4
			pixels[offset] = level
			pixels[offset+1] = level
			pixels[offset+2] = level
			pixels[offset+3] = 255
		}
	}

	if err := calibrateWritePng(outputFullPath, size, pixels); err != nil {
		return nil, err
	}
	return &calibrationPreviewSummary{
		SourceImage:                    sourceFullPath,
		OutputImage:                    outputFullPath,
		SourceWidth:                    source.width,
		SourceHeight:                   source.height,
		PreviewSize:                    size,
		TotalSectors:                   options.TotalSectors,
		StartLba:                       options.StartLba,
		FillSectors:                    options.FillSectors,
		ChannelBitLengthNm:             options.ChannelBitLengthNm,
		TrackLengthMetres:              trackLengthMm / 1000.0,
		GeneratedInnerRadiusMm:         generated.innerRadiusMm(),
		GeneratedOuterRadiusMm:         generated.outerRadiusMm(),
		GeneratedTrackPitchMicrometres: generated.trackPitchMm() * 1000.0,
		GeneratedTurns:                 generated.totalAngleRadians() / fullTurn,
		ActualInnerRadiusMm:            actual.innerRadiusMm(),
		ActualOuterRadiusMm:            actual.outerRadiusMm(),
		ActualTrackPitchMicrometres:    actual.trackPitchMm() * 1000.0,
		ActualTurns:                    actual.totalAngleRadians() / fullTurn,
		SpiralDirection:                directionString(options.Clockwise),
		LuminanceThreshold:             options.LuminanceThreshold,
		AlphaThreshold:                 options.AlphaThreshold,
		SamplesPerSector:               options.SamplesPerSector,
		MappingMode:                    "forward-channel-splat-average",
		GeneratedPitchLinear:           options.GeneratedPitchLinear,
		GeneratedPitchQuadratic:        options.GeneratedPitchQuadratic,
		GeneratedPitchCubic:            options.GeneratedPitchCubic,
		ActualPitchLinear:              options.ActualPitchLinear,
		ActualPitchQuadratic:           options.ActualPitchQuadratic,
		ActualPitchCubic:               options.ActualPitchCubic,
		Tone:                           options.Tone,
	}, nil
}

func calibrateWritePng(outputPath string, size int, pixels []byte) error {
	return raster.SavePng(outputPath, size, size, pixels, false)
}

func calibrateValidate(options calibrationPreviewOptions) error {
	if options.TotalSectors == 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Total sector count must be positive."}
	}
	if options.FillSectors == 0 ||
		uint64(options.StartLba)+uint64(options.FillSectors) > uint64(options.TotalSectors) {
		return &argumentOutOfRangeError{param: "options", msg: "Preview sector range exceeds the disc size."}
	}
	if math.IsNaN(options.ChannelBitLengthNm) || math.IsInf(options.ChannelBitLengthNm, 0) || options.ChannelBitLengthNm <= 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Channel-bit length must be positive."}
	}
	if options.PreviewSize < 64 || options.PreviewSize > 8192 {
		return &argumentOutOfRangeError{param: "options", msg: "Preview size must be 64..8192 pixels."}
	}
	if options.SamplesPerSector < 1 || options.SamplesPerSector > 4096 {
		return &argumentOutOfRangeError{param: "options", msg: "Preview samples per sector must be 1..4096."}
	}
	if options.AlphaThreshold == 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Alpha threshold must be 1..255."}
	}
	return nil
}
