package dvd

import (
	"math"

	"disc-image-studio/internal/core"
)

// imageMappingOptions ports ImageMappingOptions.
type imageMappingOptions struct {
	TotalSectors           uint32
	InnerRadiusMm          float64
	OuterRadiusMm          float64
	ChannelBitLengthNm     float64
	StartAngleDegrees      float64
	Clockwise              bool
	LuminanceThreshold     byte
	AlphaThreshold         byte
	SampleEveryChannelBits int
	PitchLinear            float64
	PitchQuadratic         float64
	PitchCubic             float64
	Tone                   *core.ToneProfile
	UsableSectors          *uint32
	ImageOuterRadiusMm     *float64
}

func (o imageMappingOptions) outputSectors() uint32 {
	if o.UsableSectors != nil {
		return *o.UsableSectors
	}
	return o.TotalSectors
}

func (o imageMappingOptions) samplingOuterRadiusMm() float64 {
	if o.ImageOuterRadiusMm != nil {
		return *o.ImageOuterRadiusMm
	}
	return o.OuterRadiusMm
}

// imageMappingSummary ports ImageMappingSummary.
type imageMappingSummary struct {
	SourceImage            *string
	ImageWidth             int
	ImageHeight            int
	TotalSectors           uint32
	InnerRadiusMm          float64
	OuterRadiusMm          float64
	ChannelBitLengthNm     float64
	StartAngleDegrees      float64
	SpiralDirection        string
	LuminanceThreshold     byte
	AlphaThreshold         byte
	SampleEveryChannelBits int
	TrackLengthMetres      float64
	TrackPitchMicrometres  float64
	TotalTurns             float64
	BlockStartRadiusMm     float64
	BlockEndRadiusMm       float64
	BlockStartTrackTurns   float64
	BlockEndTrackTurns     float64
	PitchLinear            float64
	PitchQuadratic         float64
	PitchCubic             float64
	Tone                   *core.ToneProfile
}

func (s *imageMappingSummary) pitchLinearMicrometres() float64 {
	return s.TrackPitchMicrometres * s.PitchLinear
}
func (s *imageMappingSummary) pitchQuadraticMicrometres() float64 {
	return s.TrackPitchMicrometres * s.PitchQuadratic
}
func (s *imageMappingSummary) pitchCubicMicrometres() float64 {
	return s.TrackPitchMicrometres * s.PitchCubic
}

// imageTargetMapperMapPath ports ImageTargetMapper.Map(path, ...).
func imageTargetMapperMapPath(imagePath string, initialLba uint32, options imageMappingOptions) (*imageConstraint, error) {
	image, err := rasterImageLoad(imagePath)
	if err != nil {
		return nil, err
	}
	full := fullPath(imagePath)
	return imageTargetMapperMap(image, initialLba, options, &full)
}

// imageTargetMapperMapInMemory ports ImageTargetMapper.Map(RasterImage, ...) (no source path).
func imageTargetMapperMapInMemory(image *rasterImage, initialLba uint32, options imageMappingOptions) (*imageConstraint, error) {
	return imageTargetMapperMap(image, initialLba, options, nil)
}

func imageTargetMapperMap(image *rasterImage, initialLba uint32, options imageMappingOptions, sourceImage *string) (*imageConstraint, error) {
	if err := imageMappingValidate(initialLba, options); err != nil {
		return nil, err
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

	startAngleRadians := options.StartAngleDegrees * math.Pi / 180.0
	direction := 1.0
	if options.Clockwise {
		direction = -1.0
	}
	targets := make([]int8, dvdChannelBitsPerBlock)
	for i := range targets {
		targets[i] = constraintUnconstrained
	}
	firstGlobalBit := uint64(initialLba) * dvdChannelBitsPerSector
	sampleAt := func(relativeChannelBit float64, preserveDiscreteGrayscale bool) (pixelSample, error) {
		arcLengthMm := (float64(firstGlobalBit) + relativeChannelBit) * bitLengthMm
		radiusMm, err := spiral.radiusAtArcLength(arcLengthMm)
		if err != nil {
			return pixelSample{}, err
		}
		trackAngle := spiral.angleAtRadius(radiusMm)
		polarAngle := startAngleRadians + direction*trackAngle
		return image.sampleDisc(
			radiusMm*math.Cos(polarAngle),
			radiusMm*math.Sin(polarAngle),
			options.samplingOuterRadiusMm(),
			preserveDiscreteGrayscale), nil
	}

	for bitIndex := 0; bitIndex < len(targets); bitIndex += options.SampleEveryChannelBits {
		sample, err := sampleAt(float64(bitIndex)+0.5, false)
		if err != nil {
			return nil, err
		}
		if sample.Alpha >= options.AlphaThreshold {
			if dvdToneUseWhite(sample.Luminance, options.LuminanceThreshold, options.Tone,
				int64(firstGlobalBit+uint64(bitIndex))) {
				targets[bitIndex] = 1
			} else {
				targets[bitIndex] = 0
			}
		}
	}

	channelWordTargets := make([]int8, dvdChannelBitsPerBlock/16)
	grayscaleTargets := make([]int8, len(channelWordTargets))
	for i := range channelWordTargets {
		channelWordTargets[i] = constraintUnconstrained
		grayscaleTargets[i] = constraintUnconstrained
	}
	for wordIndex := 0; wordIndex < len(channelWordTargets); wordIndex++ {
		wordCenter := float64(wordIndex*16) + 8.0
		sample, err := sampleAt(wordCenter, false)
		if err != nil {
			return nil, err
		}
		if sample.Alpha >= options.AlphaThreshold {
			white := dvdToneUseWhite(
				sample.Luminance,
				options.LuminanceThreshold,
				options.Tone,
				int64(firstGlobalBit+uint64(wordIndex*16+8)))
			if white {
				channelWordTargets[wordIndex] = 16
			} else {
				channelWordTargets[wordIndex] = 0
			}
			grayscaleSample := sample
			if image.hasBinaryGrayscalePalette {
				grayscaleSample, err = sampleAt(wordCenter, true)
				if err != nil {
					return nil, err
				}
			}
			if options.Tone != nil && !options.Tone.IsLegacyBinary() {
				if white {
					grayscaleTargets[wordIndex] = 1
				} else {
					grayscaleTargets[wordIndex] = 0
				}
			} else {
				grayscaleTargets[wordIndex] = int8(core.BinaryLevelIndex(grayscaleSample.Luminance))
			}
		}
	}

	blockStartArc := float64(firstGlobalBit) * bitLengthMm
	blockEndArc := float64(firstGlobalBit+uint64(len(targets))) * bitLengthMm
	blockStartRadius, err := spiral.radiusAtArcLength(blockStartArc)
	if err != nil {
		return nil, err
	}
	blockEndRadius, err := spiral.radiusAtArcLength(blockEndArc)
	if err != nil {
		return nil, err
	}
	summary := &imageMappingSummary{
		SourceImage:            sourceImage,
		ImageWidth:             image.width,
		ImageHeight:            image.height,
		TotalSectors:           options.TotalSectors,
		InnerRadiusMm:          options.InnerRadiusMm,
		OuterRadiusMm:          options.OuterRadiusMm,
		ChannelBitLengthNm:     options.ChannelBitLengthNm,
		StartAngleDegrees:      options.StartAngleDegrees,
		SpiralDirection:        directionString(options.Clockwise),
		LuminanceThreshold:     options.LuminanceThreshold,
		AlphaThreshold:         options.AlphaThreshold,
		SampleEveryChannelBits: options.SampleEveryChannelBits,
		TrackLengthMetres:      trackLengthMm / 1000.0,
		TrackPitchMicrometres:  spiral.trackPitchMm() * 1000.0,
		TotalTurns:             spiral.totalAngleRadians() / (2 * math.Pi),
		BlockStartRadiusMm:     blockStartRadius,
		BlockEndRadiusMm:       blockEndRadius,
		BlockStartTrackTurns:   spiral.angleAtRadius(blockStartRadius) / (2 * math.Pi),
		BlockEndTrackTurns:     spiral.angleAtRadius(blockEndRadius) / (2 * math.Pi),
		PitchLinear:            options.PitchLinear,
		PitchQuadratic:         options.PitchQuadratic,
		PitchCubic:             options.PitchCubic,
		Tone:                   options.Tone,
	}
	return imageConstraintCreate(targets, summary, channelWordTargets, grayscaleTargets)
}

func directionString(clockwise bool) string {
	if clockwise {
		return "cw"
	}
	return "ccw"
}

func imageMappingValidate(initialLba uint32, options imageMappingOptions) error {
	if options.TotalSectors < dvdEccSectorCount ||
		uint64(initialLba)+dvdEccSectorCount > uint64(options.TotalSectors) {
		return &argumentOutOfRangeError{param: "initialLba", msg: "The selected 16-sector ECC Block must fit inside the reported total sector count."}
	}
	if math.IsNaN(options.ChannelBitLengthNm) || math.IsInf(options.ChannelBitLengthNm, 0) || options.ChannelBitLengthNm <= 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Channel-bit length must be positive."}
	}
	if math.IsNaN(options.StartAngleDegrees) || math.IsInf(options.StartAngleDegrees, 0) {
		return &argumentOutOfRangeError{param: "options", msg: "Start angle must be finite."}
	}
	if options.AlphaThreshold == 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Alpha threshold must be between 1 and 255."}
	}
	if options.SampleEveryChannelBits < 1 {
		return &argumentOutOfRangeError{param: "options", msg: "Image constraint step must be positive."}
	}
	return nil
}
