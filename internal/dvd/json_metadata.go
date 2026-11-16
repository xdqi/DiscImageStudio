package dvd

import (
	"fmt"

	"disc-image-studio/internal/core"
	"disc-image-studio/internal/trackgeom"
)

// JSON metadata emitters, byte-compatible with the C# System.Text.Json
// (WriteIndented, camelCase) anonymous objects and records.

func writeJsonFile(path string, write func(w *stjWriter)) error {
	if err := ensureParentDirectory(path); err != nil {
		return err
	}
	writer := &stjWriter{}
	writer.beginObject()
	write(writer)
	writer.endObject()
	return dotnetWriteAllText(path, writer.buf.String())
}

func writeBoundaryObject(w *stjWriter, name string, boundary modulationBoundary) {
	w.objectField(name, func() {
		w.intField("state", int(boundary.State))
		w.intField("dsv", boundary.Dsv)
		w.stringField("level", boundary.LevelName())
	})
}

func writeToneProfile(w *stjWriter, name string, tone *core.ToneProfile) {
	if tone == nil {
		w.nullField(name)
		return
	}
	w.objectField(name, func() {
		w.doubleField("ditherStrength", tone.DitherStrength)
		w.intField("levels", tone.Levels)
		w.doubleField("brightness", tone.Brightness)
		w.boolField("srgbLinearization", tone.SrgbLinearization)
	})
}

func writeImageMappingSummary(w *stjWriter, name string, mapping *imageMappingSummary) {
	if mapping == nil {
		w.nullField(name)
		return
	}
	w.objectField(name, func() {
		w.stringFieldOrNull("sourceImage", mapping.SourceImage)
		w.intField("imageWidth", mapping.ImageWidth)
		w.intField("imageHeight", mapping.ImageHeight)
		w.uintField("totalSectors", mapping.TotalSectors)
		w.doubleField("innerRadiusMm", mapping.InnerRadiusMm)
		w.doubleField("outerRadiusMm", mapping.OuterRadiusMm)
		w.doubleField("channelBitLengthNm", mapping.ChannelBitLengthNm)
		w.doubleField("startAngleDegrees", mapping.StartAngleDegrees)
		w.stringField("spiralDirection", mapping.SpiralDirection)
		w.intField("luminanceThreshold", int(mapping.LuminanceThreshold))
		w.intField("alphaThreshold", int(mapping.AlphaThreshold))
		w.intField("sampleEveryChannelBits", mapping.SampleEveryChannelBits)
		w.doubleField("trackLengthMetres", mapping.TrackLengthMetres)
		w.doubleField("trackPitchMicrometres", mapping.TrackPitchMicrometres)
		w.doubleField("totalTurns", mapping.TotalTurns)
		w.doubleField("blockStartRadiusMm", mapping.BlockStartRadiusMm)
		w.doubleField("blockEndRadiusMm", mapping.BlockEndRadiusMm)
		w.doubleField("blockStartTrackTurns", mapping.BlockStartTrackTurns)
		w.doubleField("blockEndTrackTurns", mapping.BlockEndTrackTurns)
		w.doubleField("pitchLinear", mapping.PitchLinear)
		w.doubleField("pitchQuadratic", mapping.PitchQuadratic)
		w.doubleField("pitchCubic", mapping.PitchCubic)
		writeToneProfile(w, "tone", mapping.Tone)
		w.doubleField("pitchLinearMicrometres", mapping.pitchLinearMicrometres())
		w.doubleField("pitchQuadraticMicrometres", mapping.pitchQuadraticMicrometres())
		w.doubleField("pitchCubicMicrometres", mapping.pitchCubicMicrometres())
	})
}

func writeImageMappingOptions(w *stjWriter, name string, options imageMappingOptions) {
	w.objectField(name, func() {
		w.uintField("totalSectors", options.TotalSectors)
		w.doubleField("innerRadiusMm", options.InnerRadiusMm)
		w.doubleField("outerRadiusMm", options.OuterRadiusMm)
		w.doubleField("channelBitLengthNm", options.ChannelBitLengthNm)
		w.doubleField("startAngleDegrees", options.StartAngleDegrees)
		w.boolField("clockwise", options.Clockwise)
		w.intField("luminanceThreshold", int(options.LuminanceThreshold))
		w.intField("alphaThreshold", int(options.AlphaThreshold))
		w.intField("sampleEveryChannelBits", options.SampleEveryChannelBits)
		w.doubleField("pitchLinear", options.PitchLinear)
		w.doubleField("pitchQuadratic", options.PitchQuadratic)
		w.doubleField("pitchCubic", options.PitchCubic)
		writeToneProfile(w, "tone", options.Tone)
		w.uintFieldOrNull("usableSectors", options.UsableSectors)
		w.doubleFieldOrNull("imageOuterRadiusMm", options.ImageOuterRadiusMm)
		w.uintField("outputSectors", options.outputSectors())
		w.doubleField("samplingOuterRadiusMm", options.samplingOuterRadiusMm())
	})
}

func writeIsoWriteSummary(w *stjWriter, name string, summary *isoWriteSummary) {
	if summary == nil {
		w.nullField(name)
		return
	}
	w.objectField(name, func() {
		w.stringField("mode", summary.Mode)
		w.uintField("volumeSectors", summary.VolumeSectors)
		w.intField("logicalSectorBytes", summary.LogicalSectorBytes)
		w.uintField("payloadLba", summary.PayloadLba)
		w.intField("payloadSectorCount", summary.PayloadSectorCount)
		w.uintFieldOrNull("visibleFileLba", summary.VisibleFileLba)
		w.boolField("sparseFile", summary.SparseFile)
		w.stringFieldOrNull("templatePath", summary.TemplatePath)
	})
}

func writeHybridIsoWriteSummary(w *stjWriter, name string, summary *hybridIsoWriteSummary) {
	if summary == nil {
		w.nullField(name)
		return
	}
	w.objectField(name, func() {
		w.stringField("mode", summary.Mode)
		w.uintField("volumeSectors", summary.VolumeSectors)
		w.intField("logicalSectorBytes", summary.LogicalSectorBytes)
		w.stringField("sourceDirectory", summary.SourceDirectory)
		w.intField("fileCount", summary.FileCount)
		w.intField("directoryCount", summary.DirectoryCount)
		w.longField("fileBytes", summary.FileBytes)
		w.uintField("dataEndLbaExclusive", summary.DataEndLbaExclusive)
		w.boolField("sparseFile", summary.SparseFile)
		w.boolField("iso9660", summary.Iso9660)
		w.boolField("joliet", summary.Joliet)
		w.boolField("filesAtOuterRing", summary.FilesAtOuterRing)
		w.uintField("metadataEndLbaExclusive", summary.MetadataEndLbaExclusive)
		w.uintField("fileDataStartLba", summary.FileDataStartLba)
	})
}

// dispersionPaletteMetadata ports FastDvdImageFileWriter.DispersionPaletteMetadata.
func writeDispersionPaletteMetadata(w *stjWriter, name string) {
	w.objectField(name, func() {
		w.stringField("domain", "scrambled-main-data-byte-at-efmplus-input")
		w.stringField("blackByte", "0x"+formatHexUpper(uint64(dvdBlackScrambledByte), 2))
		w.stringField("whiteByte", "0x"+formatHexUpper(uint64(dvdWhiteScrambledByte), 2))
		w.intField("blackTransitionsPerCodeword", 5)
		w.intField("whiteTransitionsPerCodeword", 2)
		w.stringField("luminanceCalibration", "nominal-black-white-not-photometric-measurement")
		w.stringField("rasterSampling", "nearest-for-opaque-binary-grayscale-otherwise-alpha-weighted-bilinear")
		w.arrayField("levels", func() {
			for level := 0; level < 2; level++ {
				lvl := level
				w.arrayObject(func() {
					inputMinimum := 0
					for value := 0; value < 256; value++ {
						if core.BinaryLevelIndex(byte(value)) == lvl {
							inputMinimum = value
							break
						}
					}
					inputMaximum := 0
					for value := 255; value >= 0; value-- {
						if core.BinaryLevelIndex(byte(value)) == lvl {
							inputMaximum = value
							break
						}
					}
					previewLuminance, _ := core.BinaryLuminance(lvl)
					transitions := 2
					if lvl == 0 {
						transitions = 5
					}
					w.intField("inputMinimum", inputMinimum)
					w.intField("inputMaximum", inputMaximum)
					w.intField("previewLuminance", int(previewLuminance))
					w.stringField("scrambledByte", "0x"+formatHexUpper(uint64(dvdGetScrambledByteForLevel(lvl)), 2))
					w.intField("transitionsPerCodeword", transitions)
				})
			}
		})
	})
}

// binaryBytePairMetadata ports Program.BinaryBytePairMetadata.
func writeBinaryBytePairMetadata(w *stjWriter, name string) {
	w.arrayField(name, func() {
		for _, entry := range efmPlusBinaryBytePairAtZeroDsv() {
			entry := entry
			w.arrayObject(func() {
				inputLevel := "pit"
				if entry.InitialIsLand {
					inputLevel = "land"
				}
				w.intField("state", int(entry.State))
				w.stringField("inputLevel", inputLevel)
				w.stringField("blackByte", "0x"+formatHexUpper(uint64(entry.Pair.BlackByte), 2))
				w.stringField("whiteByte", "0x"+formatHexUpper(uint64(entry.Pair.WhiteByte), 2))
				w.intField("blackWorstLandCount", int(entry.Pair.BlackWorstLandCount))
				w.intField("whiteWorstLandCount", int(entry.Pair.WhiteWorstLandCount))
				w.intField("referenceDsv", entry.Pair.ReferenceDsv)
				w.longField("blackWorstAbsoluteOutputDsv", entry.Pair.BlackWorstAbsoluteOutputDsv)
				w.longField("whiteWorstAbsoluteOutputDsv", entry.Pair.WhiteWorstAbsoluteOutputDsv)
			})
		}
	})
}

// describePitchPolynomial ports FastDvdImageFileWriter.DescribePitchPolynomial.
func writePitchPolynomial(w *stjWriter, name string, options imageMappingOptions) error {
	length := float64(options.TotalSectors) * dvdChannelBitsPerSector *
		options.ChannelBitLengthNm * 1e-6
	geometry, err := trackgeom.New(options.InnerRadiusMm, options.OuterRadiusMm,
		length, options.PitchLinear, options.PitchQuadratic, options.PitchCubic)
	if err != nil {
		return fmt.Errorf("pitch polynomial: %w", err)
	}
	scale := geometry.TrackPitchMm() * 1000
	w.objectField(name, func() {
		w.stringField("variable", "x=(radiusMm-innerRadiusMm)/(outerRadiusMm-innerRadiusMm)")
		w.stringField("unit", "micrometres")
		w.doubleField("constant", scale)
		w.doubleField("linear", scale*options.PitchLinear)
		w.doubleField("quadratic", scale*options.PitchQuadratic)
		w.doubleField("cubic", scale*options.PitchCubic)
	})
	return nil
}

// writeMetadata ports Program.WriteMetadata (single-block solve/encode sidecar).
func writeMetadata(
	path string,
	lba, psnOffset uint32,
	initial, final modulationBoundary,
	score, scoreMaximum *int, scoreMetric *string,
	grayScore, grayScoreMaximum *int, grayScoreMetric *string,
	controlledScore, controlledScoreMaximum *int, controlledScoreMetric *string,
	controlledGrayScore, controlledGrayScoreMaximum *int, controlledGrayScoreMetric *string,
	iterations *int,
	elapsedSeconds *float64,
	randomSeed *int32,
	options *solverOptions,
	imageMapping *imageMappingSummary,
	iso *isoWriteSummary) error {
	firstPsn, err := checkedAddU32(lba, psnOffset)
	if err != nil {
		return err
	}
	return writeJsonFile(path, func(w *stjWriter) {
		w.stringField("format", "dvd-nrzi-image-solver-v2")
		w.uintField("initialLba", lba)
		w.stringField("psnOffset", "0x"+formatHexUpper(uint64(psnOffset), 0))
		w.stringField("firstPsn", "0x"+formatHexUpper(uint64(firstPsn), 6))
		w.intField("sectorCount", dvdEccSectorCount)
		writeBoundaryObject(w, "initialBoundary", initial)
		writeBoundaryObject(w, "finalBoundary", final)
		w.intFieldOrNull("score", score)
		w.intFieldOrNull("scoreMaximum", scoreMaximum)
		w.stringFieldOrNull("scoreMetric", scoreMetric)
		writeNormalized(w, "normalizedScore", score, scoreMaximum)
		w.intFieldOrNull("grayScore", grayScore)
		w.intFieldOrNull("grayScoreMaximum", grayScoreMaximum)
		w.stringFieldOrNull("grayScoreMetric", grayScoreMetric)
		writeNormalized(w, "normalizedGrayScore", grayScore, grayScoreMaximum)
		w.intFieldOrNull("controlledScore", controlledScore)
		w.intFieldOrNull("controlledScoreMaximum", controlledScoreMaximum)
		w.stringFieldOrNull("controlledScoreMetric", controlledScoreMetric)
		writeNormalized(w, "normalizedControlledScore", controlledScore, controlledScoreMaximum)
		w.intFieldOrNull("controlledGrayScore", controlledGrayScore)
		w.intFieldOrNull("controlledGrayScoreMaximum", controlledGrayScoreMaximum)
		w.stringFieldOrNull("controlledGrayScoreMetric", controlledGrayScoreMetric)
		writeNormalized(w, "normalizedControlledGrayScore", controlledGrayScore, controlledGrayScoreMaximum)
		w.intFieldOrNull("iterations", iterations)
		w.doubleFieldOrNull("elapsedSeconds", elapsedSeconds)
		if randomSeed == nil {
			w.nullField("randomSeed")
		} else {
			w.intField("randomSeed", int(*randomSeed))
		}
		if options == nil {
			w.nullField("algorithm")
		} else {
			w.stringField("algorithm", algorithmName(options.Algorithm))
		}
		if options != nil && options.Algorithm == solverAlgorithmFull {
			w.intField("mutationBytes", options.MutationBytes)
		} else {
			w.nullField("mutationBytes")
		}
		if options != nil && options.Algorithm == solverAlgorithmStateControl {
			w.intField("bridgeBytes", options.BridgeBytes)
			w.intField("beamWidth", options.BeamWidth)
			w.intField("retryBeamWidth", options.RetryBeamWidth)
			w.doubleField("retryFrameFraction", options.RetryFrameFraction)
			w.boolField("adaptiveRetryEnabled", options.adaptiveRetryEnabled())
			w.intField("batchFrames", options.BatchFrames)
			w.intField("batchCandidates", options.BatchCandidates)
			w.intField("searchLanes", options.SearchLanes)
			w.stringField("binaryBytePairMode", "efm-state-input-level-and-current-dsv")
			writeBinaryBytePairMetadata(w, "binaryBytePairs")
		} else {
			w.nullField("bridgeBytes")
			w.nullField("beamWidth")
			w.nullField("retryBeamWidth")
			w.nullField("retryFrameFraction")
			w.nullField("adaptiveRetryEnabled")
			w.nullField("batchFrames")
			w.nullField("batchCandidates")
			w.nullField("searchLanes")
			w.nullField("binaryBytePairMode")
			w.nullField("binaryBytePairs")
		}
		if options != nil && options.Algorithm == solverAlgorithmDispersion {
			writeDispersionPaletteMetadata(w, "dispersionPool")
		} else {
			w.nullField("dispersionPool")
		}
		w.stringField("channelPacking", "MSB-first, 1=land/white, 0=pit/black")
		writeImageMappingSummary(w, "imageMapping", imageMapping)
		writeIsoWriteSummary(w, "iso", iso)
	})
}

func writeNormalized(w *stjWriter, name string, score, scoreMaximum *int) {
	if score == nil || scoreMaximum == nil || *scoreMaximum == 0 {
		w.nullField(name)
		return
	}
	w.doubleField(name, float64(*score)/float64(*scoreMaximum))
}

func writeNormalizedLong(w *stjWriter, name string, score, scoreMaximum *int64) {
	if score == nil || scoreMaximum == nil || *scoreMaximum == 0 {
		w.nullField(name)
		return
	}
	w.doubleField(name, float64(*score)/float64(*scoreMaximum))
}

// writeMultiBlockMetadata ports the SolveMultipleBlocks anonymous metadata object.
func writeMultiBlockMetadata(
	path string,
	imagePathFull, isoOutputFull string,
	lba uint32,
	hybridIso *hybridIsoWriteSummary,
	fillSectors uint32,
	imageMapping imageMappingOptions,
	psnOffset uint32,
	result multiBlockSolveResult,
	algorithm solverAlgorithm,
	iterationsPerBlock int,
	options multiBlockSolveOptions,
	drawingInitialBoundary modulationBoundary,
	discInitialBoundary modulationBoundary) error {
	return writeJsonFile(path, func(w *stjWriter) {
		w.stringField("format", "dvd-nrzi-disc-fill-v2")
		w.stringField("sourceImage", imagePathFull)
		w.stringField("isoOutput", isoOutputFull)
		w.uintField("startLba", lba)
		filesAtOuterRing := false
		if hybridIso != nil {
			filesAtOuterRing = hybridIso.FilesAtOuterRing
		}
		w.boolField("filesAtOuterRing", filesAtOuterRing)
		w.uintField("fillSectors", fillSectors)
		w.uintField("totalSectors", imageMapping.TotalSectors)
		w.stringField("psnOffset", "0x"+formatHexUpper(uint64(psnOffset), 0))
		w.intField("blockCount", result.CompletedBlocks)
		w.stringField("algorithm", algorithmName(algorithm))
		w.intField("iterationsPerBlock", iterationsPerBlock)
		if algorithm == solverAlgorithmFull {
			w.intField("mutationBytes", options.MutationBytes)
		} else {
			w.nullField("mutationBytes")
		}
		if algorithm == solverAlgorithmStateControl {
			w.intField("bridgeBytes", options.BridgeBytes)
			w.intField("beamWidth", options.BeamWidth)
			w.intField("retryBeamWidth", options.RetryBeamWidth)
			w.doubleField("retryFrameFraction", options.RetryFrameFraction)
			w.boolField("adaptiveRetryEnabled", options.RetryBeamWidth > options.BeamWidth)
			w.intField("batchFrames", options.BatchFrames)
			w.intField("batchCandidates", options.BatchCandidates)
			w.intField("searchLanes", options.SearchLanes)
			w.stringField("binaryBytePairMode", "efm-state-input-level-and-current-dsv")
			writeBinaryBytePairMetadata(w, "binaryBytePairs")
		} else {
			w.nullField("bridgeBytes")
			w.nullField("beamWidth")
			w.nullField("retryBeamWidth")
			w.nullField("retryFrameFraction")
			w.nullField("adaptiveRetryEnabled")
			w.nullField("batchFrames")
			w.nullField("batchCandidates")
			w.nullField("searchLanes")
			w.nullField("binaryBytePairMode")
			w.nullField("binaryBytePairs")
		}
		if algorithm == solverAlgorithmDispersion {
			writeDispersionPaletteMetadata(w, "dispersionPool")
		} else {
			w.nullField("dispersionPool")
		}
		w.intField("constraintStep", imageMapping.SampleEveryChannelBits)
		writeBoundaryObject(w, "initialBoundary", drawingInitialBoundary)
		if hybridIso == nil {
			w.nullField("discInitialBoundary")
		} else {
			writeBoundaryObject(w, "discInitialBoundary", discInitialBoundary)
		}
		writeBoundaryObject(w, "finalBoundary", result.FinalBoundary)
		w.longField("totalScore", result.TotalScore)
		w.longField("totalScoreMaximum", result.TotalScoreMaximum)
		w.stringField("scoreMetric", result.ScoreMetric)
		w.longFieldOrNull("totalGrayScore", result.TotalGrayScore)
		w.longFieldOrNull("totalGrayScoreMaximum", result.TotalGrayScoreMaximum)
		w.stringFieldOrNull("grayScoreMetric", result.GrayScoreMetric)
		w.longFieldOrNull("totalControlledScore", result.TotalControlledScore)
		w.longFieldOrNull("totalControlledScoreMaximum", result.TotalControlledScoreMaximum)
		w.longFieldOrNull("totalControlledGrayScore", result.TotalControlledGrayScore)
		w.longFieldOrNull("totalControlledGrayScoreMaximum", result.TotalControlledGrayScoreMaximum)
		if result.TotalScoreMaximum == 0 {
			w.doubleField("normalizedScore", 0)
		} else {
			w.doubleField("normalizedScore", float64(result.TotalScore)/float64(result.TotalScoreMaximum))
		}
		writeNormalizedLong(w, "normalizedGrayScore", result.TotalGrayScore, result.TotalGrayScoreMaximum)
		writeNormalizedLong(w, "normalizedControlledScore", result.TotalControlledScore, result.TotalControlledScoreMaximum)
		writeNormalizedLong(w, "normalizedControlledGrayScore", result.TotalControlledGrayScore, result.TotalControlledGrayScoreMaximum)
		w.doubleField("elapsedSeconds", result.Elapsed.Seconds())
		writeImageMappingOptions(w, "imageMapping", imageMapping)
		if err := writePitchPolynomial(w, "pitchPolynomial", imageMapping); err != nil {
			panic(err)
		}
		w.objectField("iso", func() {
			mode := "raw-sector-image"
			if hybridIso != nil {
				mode = hybridIso.Mode
			}
			w.stringField("mode", mode)
			w.intField("logicalSectorBytes", isoLogicalSectorBytes)
			w.uintField("imageSectors", result.IsoSectors)
			w.boolField("sparseFile", result.SparseIso)
			w.boolField("mountableFilesystem", hybridIso != nil)
			writeHybridIsoWriteSummary(w, "hybrid", hybridIso)
		})
	})
}

// writeCalibrateJson ports WriteJson for CalibrationPreviewSummary.
func writeCalibrateJson(path string, s *calibrationPreviewSummary) error {
	return writeJsonFile(path, func(w *stjWriter) {
		w.stringField("sourceImage", s.SourceImage)
		w.stringField("outputImage", s.OutputImage)
		w.intField("sourceWidth", s.SourceWidth)
		w.intField("sourceHeight", s.SourceHeight)
		w.intField("previewSize", s.PreviewSize)
		w.uintField("totalSectors", s.TotalSectors)
		w.uintField("startLba", s.StartLba)
		w.uintField("fillSectors", s.FillSectors)
		w.doubleField("channelBitLengthNm", s.ChannelBitLengthNm)
		w.doubleField("trackLengthMetres", s.TrackLengthMetres)
		w.doubleField("generatedInnerRadiusMm", s.GeneratedInnerRadiusMm)
		w.doubleField("generatedOuterRadiusMm", s.GeneratedOuterRadiusMm)
		w.doubleField("generatedTrackPitchMicrometres", s.GeneratedTrackPitchMicrometres)
		w.doubleField("generatedTurns", s.GeneratedTurns)
		w.doubleField("actualInnerRadiusMm", s.ActualInnerRadiusMm)
		w.doubleField("actualOuterRadiusMm", s.ActualOuterRadiusMm)
		w.doubleField("actualTrackPitchMicrometres", s.ActualTrackPitchMicrometres)
		w.doubleField("actualTurns", s.ActualTurns)
		w.stringField("spiralDirection", s.SpiralDirection)
		w.intField("luminanceThreshold", int(s.LuminanceThreshold))
		w.intField("alphaThreshold", int(s.AlphaThreshold))
		w.intField("samplesPerSector", s.SamplesPerSector)
		w.stringField("mappingMode", s.MappingMode)
		w.doubleField("generatedPitchLinear", s.GeneratedPitchLinear)
		w.doubleField("generatedPitchQuadratic", s.GeneratedPitchQuadratic)
		w.doubleField("generatedPitchCubic", s.GeneratedPitchCubic)
		w.doubleField("actualPitchLinear", s.ActualPitchLinear)
		w.doubleField("actualPitchQuadratic", s.ActualPitchQuadratic)
		w.doubleField("actualPitchCubic", s.ActualPitchCubic)
		writeToneProfile(w, "tone", s.Tone)
		w.doubleField("generatedPitchLinearMicrometres", s.generatedPitchLinearMicrometres())
		w.doubleField("generatedPitchQuadraticMicrometres", s.generatedPitchQuadraticMicrometres())
		w.doubleField("generatedPitchCubicMicrometres", s.generatedPitchCubicMicrometres())
		w.doubleField("actualPitchLinearMicrometres", s.actualPitchLinearMicrometres())
		w.doubleField("actualPitchQuadraticMicrometres", s.actualPitchQuadraticMicrometres())
		w.doubleField("actualPitchCubicMicrometres", s.actualPitchCubicMicrometres())
	})
}

// writeReadbackJson ports WriteJson for IsoReadbackSummary.
func writeReadbackJson(path string, s *isoReadbackSummary) error {
	return writeJsonFile(path, func(w *stjWriter) {
		w.stringField("sourceIso", s.SourceIso)
		w.stringField("outputImage", s.OutputImage)
		w.intField("previewSize", s.PreviewSize)
		w.uintField("totalSectors", s.TotalSectors)
		w.uintField("startLba", s.StartLba)
		w.uintField("fillSectors", s.FillSectors)
		w.doubleField("channelBitLengthNm", s.ChannelBitLengthNm)
		w.doubleField("trackLengthMetres", s.TrackLengthMetres)
		w.doubleField("trackPitchMicrometres", s.TrackPitchMicrometres)
		w.doubleField("totalTurns", s.TotalTurns)
		w.doubleField("innerRadiusMm", s.InnerRadiusMm)
		w.doubleField("outerRadiusMm", s.OuterRadiusMm)
		w.stringField("spiralDirection", s.SpiralDirection)
		w.intField("samplesPerSector", s.SamplesPerSector)
		w.longField("totalSamples", s.TotalSamples)
		w.longField("classifiedSamples", s.ClassifiedSamples)
		w.stringField("mappingMode", s.MappingMode)
		w.doubleField("pitchLinear", s.PitchLinear)
		w.doubleField("pitchQuadratic", s.PitchQuadratic)
		w.doubleField("pitchCubic", s.PitchCubic)
		w.doubleField("pitchLinearMicrometres", s.pitchLinearMicrometres())
		w.doubleField("pitchQuadraticMicrometres", s.pitchQuadraticMicrometres())
		w.doubleField("pitchCubicMicrometres", s.pitchCubicMicrometres())
	})
}
