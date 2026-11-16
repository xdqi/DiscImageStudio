package dvd

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"disc-image-studio/internal/core"
	"disc-image-studio/internal/raster"
)

// testBinaryDispersion ports TestBinaryDispersion.
func testBinaryDispersion() error {
	inputLevels := []byte{0, 127, 128, 255}
	expectedBytes := []byte{0xA5, 0xA5, 0x92, 0x92}
	displayLevels := []byte{0, 255}
	transitionCounts := []int{5, 2}
	for index := 0; index < len(inputLevels); index++ {
		selfTestEqual(expectedBytes[index], dvdGetScrambledByte(inputLevels[index]),
			fmt.Sprintf("DVD grayscale threshold %d", inputLevels[index]))
	}

	for level := 0; level < len(displayLevels); level++ {
		value := expectedBytes[level*2]
		luminance, ok := dvdTryGetLuminance(value)
		selfTestTrue(ok, "DVD palette readback")
		selfTestEqual(displayLevels[level], luminance, "DVD nominal readback level")
		for state := byte(1); state <= 4; state++ {
			word, _, err := efmPlusLookupMain(value, state)
			selfTestTrue(err == nil, "lookupMain")
			selfTestEqual(transitionCounts[level], popCount16(word),
				fmt.Sprintf("DVD tone 0x%02X has stable transitions in state %d", value, state))
		}
	}

	_, ok := dvdTryGetLuminance(0)
	selfTestTrue(!ok, "unknown DVD byte is not classified")
	legacyLuminance, ok := dvdTryGetLuminance(0x66)
	selfTestTrue(ok, "legacy Q66 is recognized")
	selfTestEqual(byte(0), legacyLuminance, "legacy Q66 readback matches observed black appearance")
	legacyLuminance, ok = dvdTryGetLuminance(0xB9)
	selfTestTrue(ok, "legacy QB9 is recognized")
	selfTestEqual(byte(85), legacyLuminance, "legacy QB9 readback remains nominal dark gray")
	selfTestSequenceEqual([]byte{0xA5, 0x92}, dvdPaletteScrambledBytes,
		"DVD generation palette excludes rejected candidates")
	directory := filepath.Join(os.TempDir(), "dvd-grayscale-"+newGuidN())
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return err
	}
	defer os.RemoveAll(directory)

	sourcePath := filepath.Join(directory, "gray-source.png")
	writeTestPng(sourcePath, true, false)
	mapping := imageMappingOptions{
		TotalSectors:           64,
		InnerRadiusMm:          24,
		OuterRadiusMm:          24.1,
		ChannelBitLengthNm:     133.33,
		StartAngleDegrees:      17,
		Clockwise:              true,
		LuminanceThreshold:     20,
		AlphaThreshold:         128,
		SampleEveryChannelBits: 256,
	}
	fastOptions := multiBlockSolveOptions{
		StartLba:           16,
		FillSectors:        16,
		PsnOffset:          0x30000,
		IterationsPerBlock: 1,
		MutationBytes:      4,
		RandomSeed:         804,
		InitialTemperature: 64,
		FinalTemperature:   0.25,
		ZeroInitialPayload: false,
		ImageMapping:       mapping,
		Algorithm:          solverAlgorithmDispersion,
		FastParallelism:    2,
		// C# record defaults: SearchLanes = 1, UseSampleReuse = true,
		// ReadCompatibilityMode = current.
		SearchLanes:           1,
		UseSampleReuse:        true,
		ReadCompatibilityMode: dvdReadCompatibilityCurrent,
	}
	constraint, err := imageTargetMapperMapPath(sourcePath, fastOptions.StartLba, mapping)
	if err != nil {
		return err
	}
	initialPayload := make([]byte, dvdPayloadBytesPerBlock)
	newDotnetRandom(fastOptions.RandomSeed).nextBytes(initialPayload)
	initialBoundary := modulationBoundary{State: 1, Dsv: 0, IsLand: true}
	exact, err := solverEngineSolve(constraint, initialPayload, fastOptions.StartLba, fastOptions.PsnOffset, initialBoundary,
		solverOptions{
			Iterations:         1,
			MutationBytes:      4,
			RandomSeed:         804,
			InitialTemperature: 64,
			FinalTemperature:   0.25,
			Algorithm:          solverAlgorithmDispersion,
		}, nil)
	if err != nil {
		return err
	}
	isoPath := filepath.Join(directory, "gray.iso")
	if _, err := fastDispersionWrite(sourcePath, isoPath, fastOptions, nil); err != nil {
		return err
	}
	isoBytes, err := dotnetReadAllBytes(isoPath)
	if err != nil {
		return err
	}
	selfTestSequenceEqual(exact.Payloads, isoBytes[len(initialPayload):],
		"binary fast file matches exact payload at nonzero LBA")
	var imageStream bytes.Buffer
	if _, err := fastDispersionWriteStream(sourcePath, &imageStream, fastOptions, nil); err != nil {
		return err
	}
	selfTestSequenceEqual(exact.Payloads, imageStream.Bytes(), "binary streaming matches exact payload")

	observedBytes := map[byte]struct{}{}
	preserved := 0
	for payloadIndex := 0; payloadIndex < len(initialPayload); payloadIndex++ {
		channelOffset, err := payloadDirectChannelOffset(payloadIndex)
		if err != nil {
			return err
		}
		wordIndex := channelOffset / 16
		target := constraint.channelWordGrayscaleTargets[wordIndex]
		wordTarget := constraint.channelWordTargets[wordIndex]
		selfTestTrue(wordTarget == -1 || wordTarget == 0 || wordTarget == 16,
			"dispersion targets leave full/state-control solver targets unchanged")
		if target < 0 {
			selfTestEqual(initialPayload[payloadIndex], exact.Payloads[payloadIndex],
				"transparent payload remains unchanged")
			preserved++
			continue
		}

		sector := payloadIndex / dvdPayloadBytesPerSector
		byteIndex := payloadIndex % dvdPayloadBytesPerSector
		mask := payloadScramblerMask(fastOptions.StartLba + fastOptions.PsnOffset + uint32(sector))
		q := exact.Payloads[payloadIndex] ^ mask[byteIndex]
		selfTestEqual(expectedBytes[target*2], q, "binary payload reverse scrambling")
		observedBytes[q] = struct{}{}
	}

	selfTestEqual(2, len(observedBytes), "both DVD tones generated from image")
	_, has66 := observedBytes[0x66]
	_, hasB9 := observedBytes[0xB9]
	selfTestTrue(!has66 && !hasB9, "new constrained payload never uses Q66 or QB9")
	selfTestTrue(preserved > 0, "binary image includes unconstrained alpha samples")

	var codewordStream bytes.Buffer
	if _, err := fastDispersionWriteCodewordPatternStream(
		func(sample dvdCodewordSample) byte {
			return inputLevels[sample.GlobalCodewordIndex%uint64(len(inputLevels))]
		}, &codewordStream, fastOptions, nil); err != nil {
		return err
	}
	codewordPayload := codewordStream.Bytes()
	encoded, err := newDvdEccBlockEncoder().encode(codewordPayload, fastOptions.StartLba, fastOptions.PsnOffset, initialBoundary)
	if err != nil {
		return err
	}
	validateDataRll(encoded.ChannelLevels, initialBoundary.IsLand)
	for payloadIndex := 0; payloadIndex < len(codewordPayload); payloadIndex++ {
		channelOffset, err := payloadDirectChannelOffset(payloadIndex)
		if err != nil {
			return err
		}
		globalWord := (uint64(fastOptions.StartLba)*dvdChannelBitsPerSector + uint64(channelOffset)) / 16
		expectedIndex := int(globalWord % uint64(len(inputLevels)))
		sector := payloadIndex / dvdPayloadBytesPerSector
		byteIndex := payloadIndex % dvdPayloadBytesPerSector
		mask := payloadScramblerMask(fastOptions.StartLba + fastOptions.PsnOffset + uint32(sector))
		q := codewordPayload[payloadIndex] ^ mask[byteIndex]
		selfTestEqual(expectedBytes[expectedIndex], q, "codeword stream quantizes boundary values")
		transitions := 0
		for bit := channelOffset; bit < channelOffset+16; bit++ {
			if encoded.ChannelLevels[bit] != encoded.ChannelLevels[bit-1] {
				transitions++
			}
		}
		selfTestEqual(transitionCounts[expectedIndex/2], transitions, "binary ECC/SYNC replay transition count")
	}

	analyticOptions := fastOptions
	analyticOptions.StartLba = 0
	for level := 0; level < len(displayLevels); level++ {
		input := displayLevels[level]
		var analyticStream bytes.Buffer
		if _, err := fastDispersionWritePatternStream(func(x, y float64) byte { return input }, &analyticStream, analyticOptions, nil); err != nil {
			return err
		}
		payload := append([]byte(nil), analyticStream.Bytes()...)
		for sector := 0; sector < dvdEccSectorCount; sector++ {
			scrambled := payload[sector*dvdPayloadBytesPerSector : sector*dvdPayloadBytesPerSector+dvdPayloadBytesPerSector]
			if err := transformPayloadScrambling(scrambled, 0x30000+uint32(sector)); err != nil {
				return err
			}
			for _, b := range scrambled {
				selfTestTrue(b == expectedBytes[level*2], "analytic stream generates the selected DVD tone")
			}
		}

		constantIsoPath := filepath.Join(directory, fmt.Sprintf("tone-%d.iso", level))
		if err := dotnetWriteAllBytes(constantIsoPath, append([]byte(nil), analyticStream.Bytes()...)); err != nil {
			return err
		}
		readbackPath := filepath.Join(directory, fmt.Sprintf("tone-%d.png", level))
		readback, err := isoReadbackRender(
			constantIsoPath, readbackPath,
			isoReadbackOptions{
				TotalSectors: 64, StartLba: 0, FillSectors: 16, PsnOffset: 0x30000,
				InnerRadiusMm: 24, OuterRadiusMm: 24.1, ChannelBitLengthNm: 133.33,
				Clockwise: true, PreviewSize: 128, SamplesPerSector: 64,
			})
		if err != nil {
			return err
		}
		selfTestEqual(readback.TotalSamples, readback.ClassifiedSamples,
			"ISO readback recognizes every sample of each DVD tone")
		contains, err := containsOpaqueGray(readbackPath, displayLevels[level])
		if err != nil {
			return err
		}
		selfTestTrue(contains, "ISO readback renders each nominal DVD tone")
	}

	ditheredPath := filepath.Join(directory, "dithered-source.png")
	writeTestPng(ditheredPath, false, true)
	ditheredConstraint, err := imageTargetMapperMapPath(ditheredPath, fastOptions.StartLba, mapping)
	if err != nil {
		return err
	}
	ditheredExact, err := solverEngineSolve(ditheredConstraint, initialPayload, fastOptions.StartLba, fastOptions.PsnOffset, initialBoundary,
		solverOptions{
			Iterations: 1, MutationBytes: 4, RandomSeed: 804,
			InitialTemperature: 64, FinalTemperature: 0.25, Algorithm: solverAlgorithmDispersion,
		}, nil)
	if err != nil {
		return err
	}
	var ditheredStream bytes.Buffer
	if _, err := fastDispersionWriteStream(ditheredPath, &ditheredStream, fastOptions, nil); err != nil {
		return err
	}
	selfTestSequenceEqual(ditheredExact.Payloads, ditheredStream.Bytes(),
		"discrete dither exact and streaming paths select identical source pixels")
	ditheredIsoPath := filepath.Join(directory, "dithered.iso")
	if _, err := fastDispersionWrite(ditheredPath, ditheredIsoPath, fastOptions, nil); err != nil {
		return err
	}
	ditheredIsoBytes, err := dotnetReadAllBytes(ditheredIsoPath)
	if err != nil {
		return err
	}
	selfTestSequenceEqual(ditheredExact.Payloads, ditheredIsoBytes[len(initialPayload):],
		"discrete dither fast file and exact payload agree")
	return nil
}

func popCount16(value uint16) int {
	count := 0
	for value != 0 {
		count += int(value & 1)
		value >>= 1
	}
	return count
}

// testDiscreteGrayscaleSampling ports TestDiscreteGrayscaleSampling.
func testDiscreteGrayscaleSampling() error {
	directory := filepath.Join(os.TempDir(), "dvd-discrete-sampling-"+newGuidN())
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return err
	}
	defer os.RemoveAll(directory)

	discretePath := filepath.Join(directory, "discrete.png")
	writePixelColumns(discretePath, []byte{0, 255, 0}, 255)
	discrete, err := rasterImageLoad(discretePath)
	if err != nil {
		return err
	}
	selfTestTrue(discrete.hasBinaryGrayscalePalette, "loaded opaque black/white PNG is detected")
	selfTestEqual(pixelSample{Luminance: 0, Alpha: 255}, discrete.sampleDisc(-0.6, 0, 1, true),
		"dither black pixel is preserved without interpolation")
	selfTestEqual(pixelSample{Luminance: 255, Alpha: 255}, discrete.sampleDisc(-0.4, 0, 1, true),
		"dither white pixel is preserved without interpolation")
	selfTestEqual(pixelSample{Luminance: 153, Alpha: 255}, discrete.sampleDisc(-0.4, 0, 1, false),
		"binary solver retains existing bilinear sampling")
	selfTestEqual(byte(255), core.BinaryQuantize(153),
		"continuous gray is thresholded to one of the two production tones")

	continuousPath := filepath.Join(directory, "continuous.png")
	writePixelColumns(continuousPath, []byte{0, 128, 255}, 255)
	continuous, err := rasterImageLoad(continuousPath)
	if err != nil {
		return err
	}
	selfTestTrue(!continuous.hasBinaryGrayscalePalette, "continuous grayscale is not mistaken for dither")
	selfTestEqual(pixelSample{Luminance: 77, Alpha: 255}, continuous.sampleDisc(-0.4, 0, 1, true),
		"continuous image retains bilinear sampling")

	alphaPath := filepath.Join(directory, "alpha.png")
	writePixelColumns(alphaPath, []byte{0, 255, 0}, 128)
	alpha, err := rasterImageLoad(alphaPath)
	if err != nil {
		return err
	}
	selfTestTrue(!alpha.hasBinaryGrayscalePalette, "partial alpha disables discrete sampling")
	selfTestEqual(pixelSample{Luminance: 145, Alpha: 179}, alpha.sampleDisc(-0.6, 0, 1, true),
		"transparent image retains alpha-weighted bilinear sampling")
	color := rasterImageCreateForTest(3, 3, 0, 255, 0, 255)
	selfTestTrue(!color.hasBinaryGrayscalePalette, "color images are not treated as grayscale dither")
	return nil
}

func writePixelColumns(path string, values []byte, firstAlpha byte) {
	pixels := make([]byte, 3*3*4)
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			offset := (y*3 + x) * 4
			pixels[offset] = values[x]
			pixels[offset+1] = values[x]
			pixels[offset+2] = values[x]
			if x == 0 {
				pixels[offset+3] = firstAlpha
			} else {
				pixels[offset+3] = 255
			}
		}
	}
	if err := raster.SavePng(path, 3, 3, pixels, false); err != nil {
		panic(err)
	}
}

// testImageMapping ports TestImageMapping.
func testImageMapping() {
	const innerRadius = 24.0
	const outerRadius = 58.0
	const trackLength = 12_000_000.0
	spiral, err := archimedeanSpiralCreate(innerRadius, outerRadius, trackLength, 0, 0, 0)
	selfTestTrue(err == nil, "create spiral")
	start, err := spiral.radiusAtArcLength(0)
	selfTestTrue(err == nil, "radius 0")
	selfTestNear(innerRadius, start, 1e-9, "spiral start radius")
	end, err := spiral.radiusAtArcLength(trackLength)
	selfTestTrue(err == nil, "radius end")
	selfTestNear(outerRadius, end, 1e-9, "spiral end radius")
	selfTestTrue(spiral.trackPitchMm() > 0, "spiral pitch")

	black := rasterImageCreateForTest(16, 16, 0, 0, 0, 255)
	options := imageMappingOptions{
		TotalSectors:       16,
		InnerRadiusMm:      24.0,
		OuterRadiusMm:      24.1,
		ChannelBitLengthNm: 133.33,
		StartAngleDegrees:  0,
		Clockwise:          false,
		LuminanceThreshold: 128,
		AlphaThreshold:     1,
		// C# record default: SampleEveryChannelBits = 1.
		SampleEveryChannelBits: 1,
	}
	constraint, err := imageTargetMapperMapInMemory(black, 0, options)
	selfTestTrue(err == nil, "map black image")
	selfTestEqual(dvdChannelBitsPerBlock, constraint.constrainedBits, "opaque image constraint count")
	selfTestEqual(int8(0), constraint.targetAt(0), "black maps to pit")
	selfTestEqual(int8(0), constraint.targetAt(dvdChannelBitsPerBlock-1), "black final target")
}

func writeTestPng(path string, grayscale, binaryDither bool) {
	const size = 64
	const stride = size * 4
	pixels := make([]byte, stride*size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			offset := y*stride + x*4
			var value byte
			if binaryDither {
				value, _ = core.BinaryLuminance((x + y) % 2)
			} else if grayscale {
				value = byte((x / 16) * 85)
			} else if ((x/8)+(y/8))%2 == 0 {
				value = 0
			} else {
				value = 255
			}
			pixels[offset] = value
			pixels[offset+1] = value
			pixels[offset+2] = value
			if grayscale && x < 8 && y < 32 {
				pixels[offset+3] = 0
			} else {
				pixels[offset+3] = 255
			}
		}
	}
	if err := raster.SavePng(path, size, size, pixels, false); err != nil {
		panic(err)
	}
}

func writeCrossPng(path string) {
	const size = 128
	const stride = size * 4
	pixels := make([]byte, stride*size)
	centre := float64(size-1) / 2.0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			offset := y*stride + x*4
			value := byte(255)
			if math.Abs(float64(x)-centre) <= 4 || math.Abs(float64(y)-centre) <= 4 {
				value = 0
			}
			pixels[offset] = value
			pixels[offset+1] = value
			pixels[offset+2] = value
			pixels[offset+3] = 255
		}
	}
	if err := raster.SavePng(path, size, size, pixels, false); err != nil {
		panic(err)
	}
}

func countDarkAndLightPixels(path string) (int, int, error) {
	loaded, err := raster.LoadBgra32(path, math.MaxInt64, math.MaxInt32/4)
	if err != nil {
		return 0, 0, err
	}
	pixels := loaded.Pixels
	dark := 0
	light := 0
	for offset := 0; offset < len(pixels); offset += 4 {
		if pixels[offset+3] != 255 {
			continue
		}
		if pixels[offset] < 128 {
			dark++
		} else if pixels[offset] > 200 {
			light++
		}
	}
	return dark, light, nil
}

func containsOpaqueGray(path string, gray byte) (bool, error) {
	loaded, err := raster.LoadBgra32(path, math.MaxInt64, math.MaxInt32/4)
	if err != nil {
		return false, err
	}
	pixels := loaded.Pixels
	for offset := 0; offset < len(pixels); offset += 4 {
		if pixels[offset] == gray && pixels[offset+1] == gray &&
			pixels[offset+2] == gray && pixels[offset+3] == 255 {
			return true, nil
		}
	}
	return false, nil
}

func countIntermediateOpaquePixels(path string) (int, error) {
	loaded, err := raster.LoadBgra32(path, math.MaxInt64, math.MaxInt32/4)
	if err != nil {
		return 0, err
	}
	pixels := loaded.Pixels
	count := 0
	for offset := 0; offset < len(pixels); offset += 4 {
		level := pixels[offset]
		if pixels[offset+3] == 255 && level > 0 && level < 255 {
			count++
		}
	}
	return count, nil
}

var _ = binary.LittleEndian
