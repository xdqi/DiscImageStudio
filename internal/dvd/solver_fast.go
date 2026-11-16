package dvd

import (
	"io"
	"math"
	"runtime"
	"time"
)

// dvdCodewordSample ports DvdCodewordSample.
type dvdCodewordSample struct {
	Xmm, Ymm            float64
	GlobalCodewordIndex uint64
	TrackAngleRadians   float64
}

type fastDvdStreamProgress struct {
	CompletedBlocks int
	TotalBlocks     int
	BlockLba        uint32
	BytesWritten    int64
	TotalBytes      int64
	Elapsed         time.Duration
}

type fastDvdStreamSummary struct {
	CompletedBlocks      int
	SectorsWritten       uint32
	BytesWritten         int64
	ControlledWords      int64
	Elapsed              time.Duration
	CompatibilityChanges int64
}

type fastDispersionWriteResult struct {
	CompletedBlocks      int
	FilledSectors        uint32
	ControlledWords      int64
	Elapsed              time.Duration
	SparseIso            bool
	IsoSectors           uint32
	CompatibilityChanges int64
}

type fastDispersionProgress struct {
	CompletedBlocks int
	TotalBlocks     int
	BlockLba        uint32
	ControlledWords int64
	Elapsed         time.Duration
}

var payloadChannelCenters = buildPayloadChannelCenters()

func buildPayloadChannelCenters() []int {
	centers := make([]int, dvdPayloadBytesPerBlock)
	for payloadIndex := 0; payloadIndex < len(centers); payloadIndex++ {
		offset, err := payloadDirectChannelOffset(payloadIndex)
		if err != nil {
			panic(err)
		}
		centers[payloadIndex] = offset + 8
	}
	return centers
}

// fastDispersionWrite ports FastDispersionImageWriter.Write (sequential; the
// C# batch parallelism produces identical ordering and bytes).
func fastDispersionWrite(imagePath, isoOutputPath string, options multiBlockSolveOptions, progress func(fastDispersionProgress)) (fastDispersionWriteResult, error) {
	if err := fastDispersionValidate(options); err != nil {
		return fastDispersionWriteResult{}, err
	}
	image, err := rasterImageLoad(imagePath)
	if err != nil {
		return fastDispersionWriteResult{}, err
	}
	sampler, err := newPayloadImageSamplerFromImage(image, options.ImageMapping)
	if err != nil {
		return fastDispersionWriteResult{}, err
	}
	totalBlocks := int(options.FillSectors / dvdEccSectorCount)
	var isoSectors uint32
	if options.PreserveExistingIso {
		isoSectors = options.ImageMapping.outputSectors()
	} else {
		isoSectors, err = checkedAddU32(options.StartLba, options.FillSectors)
		if err != nil {
			return fastDispersionWriteResult{}, err
		}
	}
	var controlledWords int64
	var compatibilityChanges int64
	stopwatchStart := time.Now()
	iso, err := newRawIsoImageWriter(isoOutputPath, isoSectors, options.PreserveExistingIso)
	if err != nil {
		return fastDispersionWriteResult{}, err
	}
	payloads := make([]byte, dvdPayloadBytesPerBlock)
	for blockIndex := 0; blockIndex < totalBlocks; blockIndex++ {
		block, err := generateFastBlock(blockIndex, sampler, options, payloads)
		if err != nil {
			iso.close()
			return fastDispersionWriteResult{}, err
		}
		if err := iso.writeBlock(block.lba, block.payloads); err != nil {
			iso.close()
			return fastDispersionWriteResult{}, err
		}
		controlledWords += int64(block.controlledWords)
		compatibilityChanges += int64(block.compatibilityChanges)
		if progress != nil {
			progress(fastDispersionProgress{
				CompletedBlocks: blockIndex + 1,
				TotalBlocks:     totalBlocks,
				BlockLba:        block.lba,
				ControlledWords: controlledWords,
				Elapsed:         time.Since(stopwatchStart),
			})
		}
	}
	sparseIso := iso.sparseFile
	if err := iso.close(); err != nil {
		return fastDispersionWriteResult{}, err
	}
	return fastDispersionWriteResult{
		CompletedBlocks:      totalBlocks,
		FilledSectors:        options.FillSectors,
		ControlledWords:      controlledWords,
		Elapsed:              time.Since(stopwatchStart),
		SparseIso:            sparseIso,
		IsoSectors:           isoSectors,
		CompatibilityChanges: compatibilityChanges,
	}, nil
}

// fastDispersionWriteStream ports FastDispersionImageWriter.WriteStream.
func fastDispersionWriteStream(imagePath string, output io.Writer, options multiBlockSolveOptions, progress func(fastDvdStreamProgress)) (fastDvdStreamSummary, error) {
	if err := fastDispersionValidateStream(output, options); err != nil {
		return fastDvdStreamSummary{}, err
	}
	image, err := rasterImageLoad(imagePath)
	if err != nil {
		return fastDvdStreamSummary{}, err
	}
	sampler, err := newPayloadImageSamplerFromImage(image, options.ImageMapping)
	if err != nil {
		return fastDvdStreamSummary{}, err
	}
	return writeSampledStream(sampler, output, options, progress)
}

// fastDispersionWritePatternStream ports WritePatternStream.
func fastDispersionWritePatternStream(sampleGray func(x, y float64) byte, output io.Writer, options multiBlockSolveOptions, progress func(fastDvdStreamProgress)) (fastDvdStreamSummary, error) {
	if sampleGray == nil {
		return fastDvdStreamSummary{}, newArgumentErrorParam("Value cannot be null.", "sampleGray")
	}
	if err := fastDispersionValidateStream(output, options); err != nil {
		return fastDvdStreamSummary{}, err
	}
	sampler, err := newPayloadImageSamplerFromGray(sampleGray, options.ImageMapping)
	if err != nil {
		return fastDvdStreamSummary{}, err
	}
	return writeSampledStream(sampler, output, options, progress)
}

// fastDispersionWriteCodewordPatternStream ports WriteCodewordPatternStream.
func fastDispersionWriteCodewordPatternStream(sampleGray func(dvdCodewordSample) byte, output io.Writer, options multiBlockSolveOptions, progress func(fastDvdStreamProgress)) (fastDvdStreamSummary, error) {
	if sampleGray == nil {
		return fastDvdStreamSummary{}, newArgumentErrorParam("Value cannot be null.", "sampleGray")
	}
	if err := fastDispersionValidateStream(output, options); err != nil {
		return fastDvdStreamSummary{}, err
	}
	sampler, err := newPayloadImageSamplerFromCodeword(sampleGray, options.ImageMapping, false)
	if err != nil {
		return fastDvdStreamSummary{}, err
	}
	return writeSampledStream(sampler, output, options, progress)
}

// fastDispersionWriteScrambledCodewordPatternStream ports WriteScrambledCodewordPatternStream.
func fastDispersionWriteScrambledCodewordPatternStream(sampleScrambledByte func(dvdCodewordSample) byte, output io.Writer, options multiBlockSolveOptions, progress func(fastDvdStreamProgress)) (fastDvdStreamSummary, error) {
	if sampleScrambledByte == nil {
		return fastDvdStreamSummary{}, newArgumentErrorParam("Value cannot be null.", "sampleScrambledByte")
	}
	if err := fastDispersionValidateStream(output, options); err != nil {
		return fastDvdStreamSummary{}, err
	}
	if options.ReadCompatibility {
		return fastDvdStreamSummary{}, newArgumentErrorParam("Read compatibility cannot be used with an exact scrambled-byte pattern.", "options")
	}
	sampler, err := newPayloadImageSamplerFromCodeword(sampleScrambledByte, options.ImageMapping, true)
	if err != nil {
		return fastDvdStreamSummary{}, err
	}
	return writeSampledStream(sampler, output, options, progress)
}

// fastDispersionWriteSectorPatternStream ports WriteSectorPatternStream.
func fastDispersionWriteSectorPatternStream(sectorLuminance func(uint32) byte, output io.Writer, options multiBlockSolveOptions, progress func(fastDvdStreamProgress)) (fastDvdStreamSummary, error) {
	if sectorLuminance == nil {
		return fastDvdStreamSummary{}, newArgumentErrorParam("Value cannot be null.", "sectorLuminance")
	}
	if err := fastDispersionValidateStream(output, options); err != nil {
		return fastDvdStreamSummary{}, err
	}
	fill := func(index int, buffer []byte) (generatedBlock, error) {
		lba, err := checkedAddU32(options.StartLba, uint32(index)*dvdEccSectorCount)
		if err != nil {
			return generatedBlock{}, err
		}
		compatibilityChanges := 0
		var ownership []byte
		if options.ReadCompatibility {
			ownership = make([]byte, dvdPayloadBytesPerSector)
		}
		for i := range ownership {
			ownership[i] = 1
		}
		for offset := uint32(0); offset < dvdEccSectorCount; offset++ {
			sector := lba + offset
			payload := buffer[int(offset)*dvdPayloadBytesPerSector : int(offset)*dvdPayloadBytesPerSector+dvdPayloadBytesPerSector]
			fillByte := dvdGetScrambledByte(sectorLuminance(sector))
			for i := range payload {
				payload[i] = fillByte
			}
			psn, err := checkedAddU32(sector, options.PsnOffset)
			if err != nil {
				return generatedBlock{}, err
			}
			if err := transformPayloadScrambling(payload, psn); err != nil {
				return generatedBlock{}, err
			}
			if options.ReadCompatibility {
				changes, err := applyFastReadCompatibility(payload, payloadScramblerMask(psn), ownership, options)
				if err != nil {
					return generatedBlock{}, err
				}
				compatibilityChanges += changes
			}
		}
		return generatedBlock{lba: lba, payloads: buffer, controlledWords: len(buffer), compatibilityChanges: compatibilityChanges}, nil
	}
	return writeBlockStream(fill, output, options, progress)
}

func fastDispersionValidateStream(output io.Writer, options multiBlockSolveOptions) error {
	if output == nil {
		return newArgumentErrorParam("Value cannot be null.", "output")
	}
	if err := fastDispersionValidate(options); err != nil {
		return err
	}
	if options.PreserveExistingIso {
		return newArgumentErrorParam("Direct DVD streaming cannot preserve or seek within an existing ISO.", "options")
	}
	return nil
}

func writeSampledStream(sampler *payloadImageSampler, output io.Writer, options multiBlockSolveOptions, progress func(fastDvdStreamProgress)) (fastDvdStreamSummary, error) {
	generate := func(index int, buffer []byte) (generatedBlock, error) {
		return generateFastBlock(index, sampler, options, buffer)
	}
	return writeBlockStream(generate, output, options, progress)
}

type generatedBlock struct {
	lba                  uint32
	payloads             []byte
	controlledWords      int
	compatibilityChanges int
}

func writeBlockStream(generateBlock func(index int, buffer []byte) (generatedBlock, error), output io.Writer, options multiBlockSolveOptions, progress func(fastDvdStreamProgress)) (fastDvdStreamSummary, error) {
	totalBlocks := int(options.FillSectors / dvdEccSectorCount)
	totalBytes := int64(options.FillSectors) * dvdPayloadBytesPerSector
	var bytesWritten int64
	var controlledWords int64
	var compatibilityChanges int64
	stopwatchStart := time.Now()
	payloads := make([]byte, dvdPayloadBytesPerBlock)
	for blockIndex := 0; blockIndex < totalBlocks; blockIndex++ {
		block, err := generateBlock(blockIndex, payloads)
		if err != nil {
			return fastDvdStreamSummary{}, err
		}
		if _, err := output.Write(block.payloads); err != nil {
			return fastDvdStreamSummary{}, err
		}
		bytesWritten += int64(len(block.payloads))
		controlledWords += int64(block.controlledWords)
		compatibilityChanges += int64(block.compatibilityChanges)
		if progress != nil {
			progress(fastDvdStreamProgress{
				CompletedBlocks: blockIndex + 1,
				TotalBlocks:     totalBlocks,
				BlockLba:        block.lba,
				BytesWritten:    bytesWritten,
				TotalBytes:      totalBytes,
				Elapsed:         time.Since(stopwatchStart),
			})
		}
	}
	if flusher, ok := output.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			return fastDvdStreamSummary{}, err
		}
	}
	return fastDvdStreamSummary{
		CompletedBlocks:      totalBlocks,
		SectorsWritten:       options.FillSectors,
		BytesWritten:         bytesWritten,
		ControlledWords:      controlledWords,
		Elapsed:              time.Since(stopwatchStart),
		CompatibilityChanges: compatibilityChanges,
	}, nil
}

// generateFastBlock ports FastDispersionImageWriter.GenerateBlock with the
// SIMD path removed (the C# scalar fallback produces identical decisions).
func generateFastBlock(blockIndex int, sampler *payloadImageSampler, options multiBlockSolveOptions, payloads []byte) (generatedBlock, error) {
	lba, err := checkedAddU32(options.StartLba, uint32(blockIndex)*dvdEccSectorCount)
	if err != nil {
		return generatedBlock{}, err
	}
	if options.ZeroInitialPayload {
		// Alpha-excluded positions retain their initial payload, never a
		// prior block's data from this reusable slot.
		for i := range payloads {
			payloads[i] = 0
		}
	} else {
		blockSeed := options.RandomSeed + int32(blockIndex)*1_000_003
		newDotnetRandom(blockSeed).nextBytes(payloads)
	}

	controlledWords := 0
	compatibilityChanges := 0
	var ownership []byte
	if options.ReadCompatibility {
		ownership = make([]byte, dvdPayloadBytesPerSector)
	}
	blockFirstGlobalBit := uint64(lba) * dvdChannelBitsPerSector
	firstPsn, err := checkedAddU32(lba, options.PsnOffset)
	if err != nil {
		return generatedBlock{}, err
	}
	useReuse := options.UseSampleReuse && sampler.supportsSampleReuse()
	var cachedSample pixelSample
	reusableUntil := math.Inf(-1)
	reuseMisses := 0
	nextReusePayload := 0
	reuseCooldown := 256
	for sector := 0; sector < dvdEccSectorCount; sector++ {
		for i := range ownership {
			ownership[i] = 0
		}
		payloadOffset := sector * dvdPayloadBytesPerSector
		sectorPayload := payloads[payloadOffset : payloadOffset+dvdPayloadBytesPerSector]
		scrambler := payloadScramblerMask(firstPsn + uint32(sector))
		toneIndexBase := (int64(lba) + int64(sector)) * dvdPayloadBytesPerSector
		byteIndex := 0
		for byteIndex < len(sectorPayload) {
			if useReuse && payloadOffset+byteIndex >= nextReusePayload {
				for ; byteIndex < len(sectorPayload); byteIndex++ {
					payloadIndex := payloadOffset + byteIndex
					channel := blockFirstGlobalBit + uint64(payloadChannelCenters[payloadIndex])
					if float64(channel) >= reusableUntil {
						cachedSample, reusableUntil = sampler.sampleWithRegion(channel)
						var nextChannel float64
						if payloadIndex+1 < len(payloadChannelCenters) {
							nextChannel = float64(blockFirstGlobalBit + uint64(payloadChannelCenters[payloadIndex+1]))
						} else {
							nextChannel = float64(uint64(math.MaxUint64))
						}
						if nextChannel < reusableUntil {
							reuseMisses = 0
						} else {
							reuseMisses++
						}
					} else {
						reuseMisses = 0
					}
					if reuseMisses == 0 {
						reuseCooldown = 256
					}
					// Cache raw samples only: tone phase and scrambling retain
					// each byte's original absolute index and sector mask.
					sample := sampler.mapSample(cachedSample, toneIndexBase+int64(byteIndex))
					if sample.alpha >= options.ImageMapping.AlphaThreshold {
						sectorPayload[byteIndex] = sample.scrambledByte ^ scrambler[byteIndex]
						if options.ReadCompatibility {
							ownership[byteIndex] = 1
						}
						controlledWords++
					}
					if reuseMisses >= 8 {
						// A short detailed edge must not disable reuse for the
						// remaining block. Retry after bounded SIMD work; sparse
						// opportunities back off at most one logical sector.
						nextReusePayload = payloadIndex + 1 + reuseCooldown
						reuseCooldown = minInt(2048, reuseCooldown*2)
						reuseMisses = 0
						reusableUntil = math.Inf(-1)
						byteIndex++
						break
					}
				}
			}
			var fallbackEnd int
			if useReuse {
				fallbackEnd = minInt(len(sectorPayload), maxInt(byteIndex, nextReusePayload-payloadOffset))
			} else {
				fallbackEnd = len(sectorPayload)
			}
			for ; byteIndex < fallbackEnd; byteIndex++ {
				payloadIndex := payloadOffset + byteIndex
				sample := sampler.sampleGlobalChannelBit(
					blockFirstGlobalBit+uint64(payloadChannelCenters[payloadIndex]),
					toneIndexBase+int64(byteIndex))
				if sample.alpha < options.ImageMapping.AlphaThreshold {
					continue
				}
				sectorPayload[byteIndex] = sample.scrambledByte ^ scrambler[byteIndex]
				if options.ReadCompatibility {
					ownership[byteIndex] = 1
				}
				controlledWords++
			}
		}
		if options.ReadCompatibility {
			changes, err := applyFastReadCompatibility(sectorPayload, scrambler, ownership, options)
			if err != nil {
				return generatedBlock{}, err
			}
			compatibilityChanges += changes
		}
	}

	return generatedBlock{lba: lba, payloads: payloads, controlledWords: controlledWords, compatibilityChanges: compatibilityChanges}, nil
}

func applyFastReadCompatibility(payload, mask, ownership []byte, options multiBlockSolveOptions) (int, error) {
	if options.ReadCompatibilityMode == dvdReadCompatibilityLegacyBridge {
		return applyReadCompatibilityLegacySector(payload, mask, ownership)
	}
	return applyReadCompatibilitySector(payload, mask, ownership)
}

// fastDispersionValidate ports FastDispersionImageWriter.Validate.
func fastDispersionValidate(options multiBlockSolveOptions) error {
	if options.ReadCompatibility && !dvdReadCompatibilityModeIsSupported(options.ReadCompatibilityMode) {
		return newArgumentErrorParam("Unsupported DVD read compatibility recipe.", "options")
	}
	if options.Algorithm != solverAlgorithmDispersion {
		return newArgumentError("Fast output requires --algorithm dispersion.")
	}
	if options.FastParallelism < 0 || options.FastParallelism > 64 {
		return &argumentOutOfRangeError{param: "options", msg: "Fast parallelism must be 0 (automatic) or 1..64."}
	}
	if options.FillSectors < dvdEccSectorCount || options.FillSectors%dvdEccSectorCount != 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Fast output sector count must be a positive multiple of 16."}
	}
	samplingOuter := options.ImageMapping.samplingOuterRadiusMm()
	if options.ImageMapping.outputSectors() == 0 ||
		options.ImageMapping.outputSectors() > options.ImageMapping.TotalSectors ||
		math.IsNaN(samplingOuter) || math.IsInf(samplingOuter, 0) ||
		samplingOuter <= 0 {
		return &argumentOutOfRangeError{param: "options", msg: "Output capacity and image radius must lie inside the disc geometry."}
	}
	if uint64(options.StartLba)+uint64(options.FillSectors) > uint64(options.ImageMapping.outputSectors()) {
		return &argumentOutOfRangeError{param: "options", msg: "Filled range exceeds the disc size."}
	}
	firstPsn, err := checkedAddU32(options.StartLba, options.PsnOffset)
	if err != nil {
		return err
	}
	finalPsn, err := checkedAddU32(firstPsn, options.FillSectors-1)
	if err != nil {
		return err
	}
	if firstPsn&0xF != 0 {
		return newArgumentError("The first PSN must be aligned to a 16-sector ECC Block.")
	}
	if finalPsn > 0x00FFFFFF {
		return &argumentOutOfRangeError{param: "options", msg: "The output exceeds the 24-bit PSN range."}
	}
	return nil
}

// payloadTarget ports the PayloadTarget record struct.
type payloadTarget struct {
	scrambledByte byte
	alpha         byte
}

// payloadImageSampler ports the nested PayloadImageSampler (raster/analytic
// variants; the calibration-reference variant is out of CLI scope).
type payloadImageSampler struct {
	image                   *rasterImage
	sampleGray              func(x, y float64) byte
	sampleCodeword          func(dvdCodewordSample) byte
	sampleScrambledCodeword func(dvdCodewordSample) byte
	options                 imageMappingOptions
	spiral                  *archimedeanSpiral
	bitLengthMm             float64
	startAngleRadians       float64
	direction               float64
	supportsReuseGeometry   bool
	reusePositionMargin     float64
	reuseTangentMargin      float64
	reuseQuadraticBound     float64
}

func newPayloadImageSamplerFromImage(image *rasterImage, options imageMappingOptions) (*payloadImageSampler, error) {
	s, err := newPayloadImageSampler(options)
	if err != nil {
		return nil, err
	}
	s.image = image
	return s, nil
}

func newPayloadImageSamplerFromGray(sampleGray func(x, y float64) byte, options imageMappingOptions) (*payloadImageSampler, error) {
	s, err := newPayloadImageSampler(options)
	if err != nil {
		return nil, err
	}
	s.sampleGray = sampleGray
	return s, nil
}

func newPayloadImageSamplerFromCodeword(sampleCodeword func(dvdCodewordSample) byte, options imageMappingOptions, samplesScrambledBytes bool) (*payloadImageSampler, error) {
	s, err := newPayloadImageSampler(options)
	if err != nil {
		return nil, err
	}
	if samplesScrambledBytes {
		s.sampleScrambledCodeword = sampleCodeword
	} else {
		s.sampleCodeword = sampleCodeword
	}
	return s, nil
}

func newPayloadImageSampler(options imageMappingOptions) (*payloadImageSampler, error) {
	s := &payloadImageSampler{options: options}
	s.bitLengthMm = options.ChannelBitLengthNm * 1e-6
	trackLengthMm := float64(options.TotalSectors) * dvdChannelBitsPerSector * s.bitLengthMm
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
	s.spiral = spiral
	s.startAngleRadians = options.StartAngleDegrees * math.Pi / 180.0
	s.direction = 1.0
	if options.Clockwise {
		s.direction = -1.0
	}
	positionMargin, tangentError, accelerationBound, ok := spiral.tryGetSampleReuseBounds(s.startAngleRadians)
	s.supportsReuseGeometry = ok
	s.reusePositionMargin = positionMargin
	s.reuseTangentMargin = tangentError + 4*s.reusePositionMargin/s.spiral.innerRadiusMm() +
		128*2.2204460492503131e-16
	s.reuseQuadraticBound = accelerationBound / 2
	return s, nil
}

// The SIMD four-point path (SupportsSimdSampling) is hardware-gated in C#;
// this port always takes the scalar fallback, which the C# comments document
// as producing identical decisions.
func (s *payloadImageSampler) supportsSampleReuse() bool {
	return s.supportsReuseGeometry && s.image != nil
}

func (s *payloadImageSampler) sampleWithRegion(globalChannelBit uint64) (pixelSample, float64) {
	radius, angle := s.spiral.atArcLengthFast(float64(globalChannelBit) * s.bitLengthMm)
	polarAngle := s.startAngleRadians + s.direction*angle
	x := radius * math.Cos(polarAngle)
	y := radius * math.Sin(polarAngle)
	sample, region, stable := s.image.tryGetStableRegion(x, y, s.options.samplingOuterRadiusMm(), true)
	reusableUntil := float64(globalChannelBit)
	if !stable {
		return s.sampleRasterPoint(x, y), reusableUntil
	}

	tangentX := -s.direction * y / radius
	tangentY := s.direction * x / radius
	// Each signed edge velocity includes the circular-tangent error and
	// anchor rounding. The second-derivative bound encloses the complete
	// intervening arc, even when it curves back toward a boundary.
	travel := math.Min(
		math.Min(s.safeTravel(x-region.MinX, -tangentX), s.safeTravel(region.MaxX-x, tangentX)),
		math.Min(s.safeTravel(y-region.MinY, -tangentY), s.safeTravel(region.MaxY-y, tangentY)))
	if travel > 0 && !math.IsNaN(travel) && !math.IsInf(travel, 0) {
		reusableUntil = math.Nextafter(float64(globalChannelBit)+travel/s.bitLengthMm, math.Inf(-1))
	}
	return sample, reusableUntil
}

func (s *payloadImageSampler) safeTravel(distance, outwardTangent float64) float64 {
	distance -= s.reusePositionMargin
	if distance <= 0 {
		return 0
	}
	velocity := outwardTangent + s.reuseTangentMargin
	// Solve k*s² + v*s < distance, avoiding cancellation for either sign.
	root := math.Sqrt(velocity*velocity + 4*s.reuseQuadraticBound*distance)
	if velocity >= 0 {
		return 2 * distance / (root + velocity)
	}
	return (root - velocity) / (2 * s.reuseQuadraticBound)
}

func (s *payloadImageSampler) mapSample(sample pixelSample, toneIndex int64) payloadTarget {
	return payloadTarget{scrambledByte: s.scrambledForLuminance(sample.Luminance, toneIndex), alpha: sample.Alpha}
}

func (s *payloadImageSampler) sampleRasterPoint(x, y float64) pixelSample {
	return s.image.sampleDisc(x, y, s.options.samplingOuterRadiusMm(), true)
}

func (s *payloadImageSampler) sampleGlobalChannelBit(globalChannelBit uint64, toneIndex int64) payloadTarget {
	arcLengthMm := (float64(globalChannelBit) + 0.0) * s.bitLengthMm
	radiusMm, trackAngle := s.spiral.atArcLengthFast(arcLengthMm)
	polarAngle := s.startAngleRadians + s.direction*trackAngle
	return s.samplePoint(radiusMm, polarAngle, trackAngle, globalChannelBit, toneIndex)
}

func (s *payloadImageSampler) samplePoint(radiusMm, polarAngle, trackAngle float64, globalChannelBit uint64, toneIndex int64) payloadTarget {
	xMm := radiusMm * math.Cos(polarAngle)
	yMm := radiusMm * math.Sin(polarAngle)
	if s.sampleScrambledCodeword != nil {
		return payloadTarget{scrambledByte: s.sampleScrambledCodeword(dvdCodewordSample{
			Xmm: xMm, Ymm: -yMm, GlobalCodewordIndex: globalChannelBit / 16, TrackAngleRadians: trackAngle}), alpha: 255}
	}
	if s.sampleCodeword != nil {
		return payloadTarget{scrambledByte: dvdGetScrambledByte(s.sampleCodeword(dvdCodewordSample{
			Xmm: xMm, Ymm: -yMm, GlobalCodewordIndex: globalChannelBit / 16, TrackAngleRadians: trackAngle})), alpha: 255}
	}
	// RasterImage.SampleDisc flips mathematical y-up into image y-down.
	// Apply that same flip for analytic patterns, including the start angle.
	var sample pixelSample
	if s.sampleGray != nil {
		sample = pixelSample{Luminance: s.sampleGray(xMm, -yMm), Alpha: 255}
	} else {
		sample = s.sampleRasterPoint(xMm, yMm)
	}
	return payloadTarget{scrambledByte: s.scrambledForLuminance(sample.Luminance, toneIndex), alpha: sample.Alpha}
}

func (s *payloadImageSampler) scrambledForLuminance(luminance byte, toneIndex int64) byte {
	if tone := s.options.Tone; tone != nil && !tone.IsLegacyBinary() {
		if dvdToneUseWhite(luminance, s.options.LuminanceThreshold, tone, toneIndex) {
			return dvdWhiteScrambledByte
		}
		return dvdBlackScrambledByte
	}
	return dvdGetScrambledByte(luminance)
}

func processorCount() int {
	return runtime.NumCPU()
}
