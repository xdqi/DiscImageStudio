package dvd

import (
	"context"
)

// fastDvdImageFileWriterWritePrepared ports FastDvdImageFileWriter.WritePrepared:
// writes the dispersion ISO, then the sidecar metadata JSON.
func fastDvdImageFileWriterWritePrepared(imagePath, outputPath string, options multiBlockSolveOptions, hybrid *hybridIsoWriteSummary, progress func(fastDispersionProgress), ctx context.Context) (fastDispersionWriteResult, error) {
	result, err := fastDispersionWrite(imagePath, outputPath, options, progress)
	if err != nil {
		return fastDispersionWriteResult{}, err
	}
	imageMapping := options.ImageMapping
	err = writeJsonFile(outputPath+".json", func(w *stjWriter) {
		w.stringField("format", "dvd-nrzi-disc-fill-v2")
		w.stringField("sourceImage", fullPath(imagePath))
		w.stringField("isoOutput", fullPath(outputPath))
		w.uintField("startLba", options.StartLba)
		filesAtOuterRing := false
		if hybrid != nil {
			filesAtOuterRing = hybrid.FilesAtOuterRing
		}
		w.boolField("filesAtOuterRing", filesAtOuterRing)
		w.uintField("fillSectors", options.FillSectors)
		w.uintField("totalSectors", imageMapping.TotalSectors)
		w.stringField("psnOffset", "0x"+formatHexUpper(uint64(options.PsnOffset), 0))
		w.intField("blockCount", result.CompletedBlocks)
		w.stringField("algorithm", "dispersion")
		w.boolField("fastOutput", true)
		readCompatibility := dvdReadCompatibilityOff
		if options.ReadCompatibility {
			readCompatibility = options.ReadCompatibilityMode
		}
		w.stringField("readCompatibility", readCompatibility)
		w.longField("compatibilityChanges", result.CompatibilityChanges)
		fastParallelism := options.FastParallelism
		if fastParallelism == 0 {
			fastParallelism = processorCount()
		}
		w.intField("fastParallelism", fastParallelism)
		w.boolField("exactEccEfmReplay", false)
		w.nullField("finalBoundary")
		writeDispersionPaletteMetadata(w, "dispersionPool")
		w.intField("constraintStep", imageMapping.SampleEveryChannelBits)
		w.boolField("constraintStepUsed", false)
		if options.ReadCompatibility {
			w.nullField("totalScore")
		} else {
			w.longField("totalScore", 0)
		}
		w.longField("totalScoreMaximum", result.ControlledWords)
		w.stringField("scoreMetric", "payload-transition-class-error")
		if options.ReadCompatibility {
			w.nullField("normalizedScore")
		} else {
			w.doubleField("normalizedScore", 0.0)
		}
		w.doubleField("elapsedSeconds", result.Elapsed.Seconds())
		writeImageMappingOptions(w, "imageMapping", imageMapping)
		if err := writePitchPolynomial(w, "pitchPolynomial", imageMapping); err != nil {
			panic(err)
		}
		w.objectField("iso", func() {
			mode := "raw-sector-image"
			if hybrid != nil {
				mode = hybrid.Mode
			}
			w.stringField("mode", mode)
			w.intField("logicalSectorBytes", isoLogicalSectorBytes)
			w.uintField("imageSectors", result.IsoSectors)
			w.boolField("sparseFile", result.SparseIso)
			w.boolField("mountableFilesystem", hybrid != nil)
			writeHybridIsoWriteSummary(w, "hybrid", hybrid)
		})
	})
	if err != nil {
		return fastDispersionWriteResult{}, err
	}
	return result, nil
}
