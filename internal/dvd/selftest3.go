package dvd

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf16"
)

// testIsoOutput ports TestIsoOutput.
func testIsoOutput() error {
	directory := filepath.Join(os.TempDir(), "dvd-image-solver-"+newGuidN())
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return err
	}
	defer os.RemoveAll(directory)

	payload := make([]byte, dvdPayloadBytesPerBlock)
	for index := range payload {
		payload[index] = byte(index*37 + 11)
	}

	minimalPath := filepath.Join(directory, "minimal.iso")
	minimal, err := isoCreateMinimal(minimalPath, payload, 0, 64, "TEST_DISC")
	if err != nil {
		return err
	}
	selfTestEqual(uint32(64), minimal.VolumeSectors, "minimal ISO sectors")
	minimalLength, err := fileLength(minimalPath)
	if err != nil {
		return err
	}
	selfTestEqual(int64(64)*isoLogicalSectorBytes, minimalLength, "minimal ISO size")
	selfTestTrue(minimal.VisibleFileLba != nil && *minimal.VisibleFileLba >= 18,
		"system-area payload has visible file copy")

	{
		iso, err := os.Open(minimalPath)
		if err != nil {
			return err
		}
		defer iso.Close()
		pvd, err := readIsoSector(iso, 16)
		if err != nil {
			return err
		}
		selfTestEqual(byte(1), pvd[0], "PVD type")
		selfTestSequenceEqual([]byte("CD001"), pvd[1:6], "PVD identifier")
		selfTestEqual(uint32(64), binary.LittleEndian.Uint32(pvd[80:84]), "PVD LE volume size")
		selfTestEqual(uint32(64), binary.BigEndian.Uint32(pvd[84:88]), "PVD BE volume size")

		terminator, err := readIsoSector(iso, 17)
		if err != nil {
			return err
		}
		selfTestEqual(byte(255), terminator[0], "descriptor terminator type")
		selfTestSequenceEqual([]byte("CD001"), terminator[1:6], "terminator identifier")

		solvedAtLba := make([]byte, len(payload))
		if _, err := iso.ReadAt(solvedAtLba, 0); err != nil {
			return err
		}
		selfTestSequenceEqual(payload, solvedAtLba, "minimal ISO solved sectors")

		if _, err := iso.ReadAt(solvedAtLba, int64(*minimal.VisibleFileLba)*isoLogicalSectorBytes); err != nil {
			return err
		}
		selfTestSequenceEqual(payload, solvedAtLba, "visible SOLVED.BIN copy")
	}

	replacement := make([]byte, len(payload))
	for i, value := range payload {
		replacement[i] = value ^ 0xA5
	}
	patchedPath := filepath.Join(directory, "patched.iso")
	patched, err := isoPatchTemplate(minimalPath, patchedPath, replacement, 32)
	if err != nil {
		return err
	}
	selfTestEqual("template", patched.Mode, "ISO template mode")
	{
		patchedIso, err := os.Open(patchedPath)
		if err != nil {
			return err
		}
		defer patchedIso.Close()
		actualReplacement := make([]byte, len(replacement))
		if _, err := patchedIso.ReadAt(actualReplacement, 32*isoLogicalSectorBytes); err != nil {
			return err
		}
		selfTestSequenceEqual(replacement, actualReplacement, "template replacement sectors")
	}

	dataDirectory := filepath.Join(directory, "data-source")
	nestedDirectory := filepath.Join(dataDirectory, "子目录")
	if err := os.MkdirAll(nestedDirectory, 0o777); err != nil {
		return err
	}
	rootFile := []byte("hybrid root file")
	nestedFile := make([]byte, 4097)
	for index := range nestedFile {
		nestedFile[index] = byte(index * 29)
	}
	if err := dotnetWriteAllBytes(filepath.Join(dataDirectory, "资料.txt"), rootFile); err != nil {
		return err
	}
	if err := dotnetWriteAllBytes(filepath.Join(nestedDirectory, "hello.bin"), nestedFile); err != nil {
		return err
	}

	hybridPath := filepath.Join(directory, "hybrid.iso")
	plan, err := hybridIsoPlan(dataDirectory, 128, "HYBRID_TEST", false)
	if err != nil {
		return err
	}
	hybrid, err := hybridIsoCreate(plan, hybridPath, context.Background())
	if err != nil {
		return err
	}
	selfTestEqual("hybrid-iso9660-joliet", hybrid.Mode, "hybrid ISO mode")
	selfTestEqual(2, hybrid.FileCount, "hybrid ISO file count")
	selfTestEqual(2, hybrid.DirectoryCount, "hybrid ISO directory count")
	selfTestTrue(hybrid.DataEndLbaExclusive < 128, "hybrid ISO leaves drawing sectors")
	hybridLength, err := fileLength(hybridPath)
	if err != nil {
		return err
	}
	selfTestEqual(int64(128)*isoLogicalSectorBytes, hybridLength, "hybrid ISO size")

	var rootEntry, nestedEntry isoDirectoryEntry
	{
		hybridIso, err := os.Open(hybridPath)
		if err != nil {
			return err
		}
		svd, err := readIsoSector(hybridIso, 17)
		if err != nil {
			hybridIso.Close()
			return err
		}
		selfTestEqual(byte(2), svd[0], "Joliet descriptor type")
		selfTestSequenceEqual([]byte("%/E"), svd[88:91], "Joliet level 3 escape sequence")
		jolietRootLba := binary.LittleEndian.Uint32(svd[158:162])
		jolietRootBytes := binary.LittleEndian.Uint32(svd[166:170])
		jolietRoot, err := readIsoExtent(hybridIso, jolietRootLba, jolietRootBytes)
		if err != nil {
			hybridIso.Close()
			return err
		}
		rootEntry, err = findIsoDirectoryEntry(jolietRoot, "资料.txt;1", true)
		if err != nil {
			hybridIso.Close()
			return err
		}
		nestedEntry, err = findIsoDirectoryEntry(jolietRoot, "子目录", true)
		if err != nil {
			hybridIso.Close()
			return err
		}
		selfTestTrue(!rootEntry.IsDirectory, "hybrid Joliet file record flag")
		selfTestTrue(nestedEntry.IsDirectory, "hybrid Joliet directory record flag")

		actualRootFile, err := readIsoExtent(hybridIso, rootEntry.ExtentLba, rootEntry.DataLength)
		if err != nil {
			hybridIso.Close()
			return err
		}
		selfTestSequenceEqual(rootFile, actualRootFile, "hybrid Joliet root file data")

		nestedRecords, err := readIsoExtent(hybridIso, nestedEntry.ExtentLba, nestedEntry.DataLength)
		if err != nil {
			hybridIso.Close()
			return err
		}
		nestedFileEntry, err := findIsoDirectoryEntry(nestedRecords, "hello.bin;1", true)
		if err != nil {
			hybridIso.Close()
			return err
		}
		actualNestedFile, err := readIsoExtent(hybridIso, nestedFileEntry.ExtentLba, nestedFileEntry.DataLength)
		if err != nil {
			hybridIso.Close()
			return err
		}
		selfTestSequenceEqual(nestedFile, actualNestedFile, "hybrid Joliet nested file data")
		hybridIso.Close()
	}

	drawingSector := make([]byte, isoLogicalSectorBytes)
	for i := range drawingSector {
		drawingSector[i] = 0xA7
	}
	{
		drawingWriter, err := newRawIsoImageWriter(hybridPath, 128, true)
		if err != nil {
			return err
		}
		if err := drawingWriter.writeBlock(hybrid.DataEndLbaExclusive, drawingSector); err != nil {
			return err
		}
		if err := drawingWriter.close(); err != nil {
			return err
		}
	}

	{
		hybridIso, err := os.Open(hybridPath)
		if err != nil {
			return err
		}
		defer hybridIso.Close()
		actualRootFile, err := readIsoExtent(hybridIso, rootEntry.ExtentLba, rootEntry.DataLength)
		if err != nil {
			return err
		}
		selfTestSequenceEqual(rootFile, actualRootFile, "outer write preserves hybrid file data")
		actualDrawing, err := readIsoSector(hybridIso, hybrid.DataEndLbaExclusive)
		if err != nil {
			return err
		}
		selfTestSequenceEqual(drawingSector, actualDrawing, "hybrid outer drawing sector")
	}
	return nil
}

type isoDirectoryEntry struct {
	ExtentLba   uint32
	DataLength  uint32
	IsDirectory bool
}

func readIsoSector(file *os.File, lba uint32) ([]byte, error) {
	sector := make([]byte, isoLogicalSectorBytes)
	_, err := file.ReadAt(sector, int64(lba)*isoLogicalSectorBytes)
	return sector, err
}

func readIsoExtent(file *os.File, lba uint32, byteCount uint32) ([]byte, error) {
	extent := make([]byte, int(byteCount))
	_, err := file.ReadAt(extent, int64(lba)*isoLogicalSectorBytes)
	return extent, err
}

func findIsoDirectoryEntry(directory []byte, expectedIdentifier string, joliet bool) (isoDirectoryEntry, error) {
	offset := 0
	for offset < len(directory) {
		recordLength := int(directory[offset])
		if recordLength == 0 {
			offset = (offset/isoLogicalSectorBytes + 1) * isoLogicalSectorBytes
			continue
		}

		record := directory[offset : offset+recordLength]
		identifierLength := int(record[32])
		identifierBytes := record[33 : 33+identifierLength]
		var identifier string
		if joliet {
			identifier = decodeBigEndianUnicode(identifierBytes)
		} else {
			identifier = string(identifierBytes)
		}
		if identifier == expectedIdentifier {
			return isoDirectoryEntry{
				ExtentLba:   binary.LittleEndian.Uint32(record[2:6]),
				DataLength:  binary.LittleEndian.Uint32(record[10:14]),
				IsDirectory: record[25]&0x02 != 0,
			}, nil
		}
		offset += recordLength
	}
	return isoDirectoryEntry{}, newInvalidOperationError(fmt.Sprintf("ISO directory entry '%s' was not found.", expectedIdentifier))
}

func decodeBigEndianUnicode(data []byte) string {
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.BigEndian.Uint16(data[i*2:])
	}
	return string(utf16.Decode(units))
}

// testCalibrationAndRange ports TestCalibrationAndRange.
func testCalibrationAndRange() error {
	directory := filepath.Join(os.TempDir(), "dvd-range-solver-"+newGuidN())
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return err
	}
	defer os.RemoveAll(directory)

	imagePath := filepath.Join(directory, "source.png")
	writeTestPng(imagePath, false, false)
	previewPath := filepath.Join(directory, "preview.png")
	previewOptions := calibrationPreviewOptions{
		TotalSectors:           2_297_888,
		StartLba:               0,
		FillSectors:            2_297_888,
		GeneratedInnerRadiusMm: 24,
		GeneratedOuterRadiusMm: 58,
		ActualInnerRadiusMm:    24,
		ActualOuterRadiusMm:    58,
		ChannelBitLengthNm:     133.33,
		Clockwise:              false,
		LuminanceThreshold:     128,
		AlphaThreshold:         1,
		PreviewSize:            64,
		SamplesPerSector:       16,
	}
	preview, err := calibrationPreviewRender(imagePath, previewPath, previewOptions)
	if err != nil {
		return err
	}
	if _, err := os.Stat(previewPath); err != nil {
		return err
	}
	selfTestNear(preview.GeneratedTrackPitchMicrometres, preview.ActualTrackPitchMicrometres, 1e-9,
		"identical-radius calibration pitch")
	selfTestEqual(16, preview.SamplesPerSector, "calibration samples per sector")
	selfTestEqual("forward-channel-splat-average", preview.MappingMode, "calibration forward mapping mode")

	crossImagePath := filepath.Join(directory, "cross.png")
	writeCrossPng(crossImagePath)
	radialPreviewPath := filepath.Join(directory, "physical-preview.png")
	radialOptions := previewOptions
	radialOptions.ActualOuterRadiusMm = 58.5
	radialOptions.PreviewSize = 256
	if _, err := calibrationPreviewRender(crossImagePath, radialPreviewPath, radialOptions); err != nil {
		return err
	}
	intermediatePixels, err := countIntermediateOpaquePixels(radialPreviewPath)
	if err != nil {
		return err
	}
	selfTestTrue(intermediatePixels > 100,
		"physical calibration averages sub-pixel tracks instead of binary moire")

	mapping := imageMappingOptions{
		TotalSectors:           64,
		InnerRadiusMm:          24,
		OuterRadiusMm:          24.1,
		ChannelBitLengthNm:     133.33,
		StartAngleDegrees:      0,
		Clockwise:              false,
		LuminanceThreshold:     128,
		AlphaThreshold:         1,
		SampleEveryChannelBits: 64,
	}
	rangeOptions := multiBlockSolveOptions{
		StartLba:           0,
		FillSectors:        32,
		PsnOffset:          0x30000,
		IterationsPerBlock: 1,
		MutationBytes:      4,
		RandomSeed:         9,
		InitialTemperature: 64,
		FinalTemperature:   0.25,
		ZeroInitialPayload: false,
		ImageMapping:       mapping,
		Algorithm:          solverAlgorithmStateControl,
		BridgeBytes:        1,
		BeamWidth:          8,
		RetryBeamWidth:     0,
		RetryFrameFraction: 0.1,
		BatchFrames:        8,
		BatchCandidates:    1,
		// C# record defaults: SearchLanes = 1, UseSampleReuse = true.
		SearchLanes:    1,
		UseSampleReuse: true,
	}
	rawIsoPath := filepath.Join(directory, "range.iso")
	rangeResult, err := multiBlockImageSolverSolve(
		imagePath,
		rawIsoPath,
		modulationBoundary{State: 1, Dsv: 0, IsLand: true},
		rangeOptions,
		nil)
	if err != nil {
		return err
	}
	selfTestEqual(2, rangeResult.CompletedBlocks, "range ECC block count")
	selfTestEqual(uint32(32), rangeResult.IsoSectors, "range ISO sector count")
	selfTestEqual("all-channel-word-majority-error", rangeResult.ScoreMetric, "range state-control metric")
	grayMetric := ""
	if rangeResult.GrayScoreMetric != nil {
		grayMetric = *rangeResult.GrayScoreMetric
	}
	selfTestEqual("all-channel-word-gray-error", grayMetric, "range gray metric")
	rangeLength, err := fileLength(rawIsoPath)
	if err != nil {
		return err
	}
	selfTestEqual(int64(32)*isoLogicalSectorBytes, rangeLength, "range ISO size")
	rawIsoBytes, err := dotnetReadAllBytes(rawIsoPath)
	if err != nil {
		return err
	}
	selfTestTrue(!allZeroBytes(rawIsoBytes), "range ISO payload data")

	dispersionMapping := mapping
	dispersionMapping.SampleEveryChannelBits = 256
	zeroPayload := make([]byte, dvdPayloadBytesPerBlock)
	dispersionImage, err := rasterImageLoad(imagePath)
	if err != nil {
		return err
	}
	dispersionConstraint, err := imageTargetMapperMapInMemory(dispersionImage, 0, dispersionMapping)
	if err != nil {
		return err
	}
	exactDispersion, err := solverEngineSolve(
		dispersionConstraint,
		zeroPayload,
		0,
		0x30000,
		modulationBoundary{State: 1, Dsv: 0, IsLand: true},
		solverOptions{
			Iterations: 1, MutationBytes: 4, RandomSeed: 9,
			InitialTemperature: 64, FinalTemperature: 0.25, Algorithm: solverAlgorithmDispersion,
		}, nil)
	if err != nil {
		return err
	}
	fastOptions := rangeOptions
	fastOptions.FillSectors = 16
	fastOptions.ZeroInitialPayload = true
	fastOptions.ImageMapping = dispersionMapping
	fastOptions.Algorithm = solverAlgorithmDispersion
	fastIsoPath := filepath.Join(directory, "fast-dispersion.iso")
	fast, err := fastDispersionWrite(imagePath, fastIsoPath, fastOptions, nil)
	if err != nil {
		return err
	}
	selfTestEqual(1, fast.CompletedBlocks, "fast dispersion block count")
	selfTestEqual(int64(dvdPayloadBytesPerBlock), fast.ControlledWords, "fast dispersion controlled words")
	fastIsoBytes, err := dotnetReadAllBytes(fastIsoPath)
	if err != nil {
		return err
	}
	selfTestSequenceEqual(exactDispersion.Payloads, fastIsoBytes,
		"fast dispersion matches exact-replay payload bytes")

	// The read-back simulation must classify every dispersion payload byte of the
	// generated ISO and reproduce a non-trivial two-level scan image from it.
	readbackPath := filepath.Join(directory, "readback.png")
	readback, err := isoReadbackRender(
		fastIsoPath,
		readbackPath,
		isoReadbackOptions{
			TotalSectors:       64,
			StartLba:           0,
			FillSectors:        16,
			PsnOffset:          0x30000,
			InnerRadiusMm:      24,
			OuterRadiusMm:      24.1,
			ChannelBitLengthNm: 133.33,
			Clockwise:          false,
			PreviewSize:        256,
			SamplesPerSector:   64,
		})
	if err != nil {
		return err
	}
	if _, err := os.Stat(readbackPath); err != nil {
		return err
	}
	selfTestEqual("payload-scramble-classify-splat", readback.MappingMode, "ISO readback mapping mode")
	selfTestEqual(int64(16*64), readback.TotalSamples, "ISO readback sample count")
	selfTestEqual(readback.TotalSamples, readback.ClassifiedSamples, "ISO readback classification")
	darkPixels, lightPixels, err := countDarkAndLightPixels(readbackPath)
	if err != nil {
		return err
	}
	selfTestTrue(darkPixels > 0, "ISO readback renders dark code words")
	selfTestTrue(lightPixels > 0, "ISO readback renders light code words")
	return nil
}

var _ = bytes.Equal
