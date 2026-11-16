package cd

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"disc-image-studio/internal/core"
	"disc-image-studio/internal/raster"
)

// Progress is one CD progress report.
type Progress struct {
	CompletedBytes int64
	TotalBytes     int64
	Elapsed        time.Duration
}

// Fraction is the completion fraction.
func (p Progress) Fraction() float64 {
	if p.TotalBytes == 0 {
		return 0
	}
	return float64(p.CompletedBytes) / float64(p.TotalBytes)
}

// GenerationSummary is the result of a CD generation.
type GenerationSummary struct {
	SourceImage  string
	OutputTrack  string
	Sectors      int64
	BytesWritten int64
	Interleaved  bool
	Elapsed      time.Duration
	CueSheetPath string
}

// GenerateTrack ports CdTrackGenerator.Generate.
func GenerateTrack(imagePath, outputPath string, parameters *DiscParameters,
	interleave, writeCue bool, progress func(Progress), ctx context.Context,
	tone core.ToneProfile) (GenerationSummary, error) {
	if err := parameters.Validate(); err != nil {
		return GenerationSummary{}, err
	}
	effectiveTone := tone.Normalized()
	sourceFullPath, err := filepath.Abs(imagePath)
	if err != nil {
		return GenerationSummary{}, err
	}
	outputFullPath, err := filepath.Abs(outputPath)
	if err != nil {
		return GenerationSummary{}, err
	}
	extension := filepath.Ext(outputFullPath)
	wave := strings.EqualFold(extension, ".wav")
	if !wave && !strings.EqualFold(extension, ".raw") {
		return GenerationSummary{}, fmt.Errorf("CD output must be .wav (PCM audio + CUE) or .raw (big-endian cdrecord audio). (Parameter 'outputPath')")
	}
	if wave {
		if err := ValidateWaveLength(parameters.TotalBytes()); err != nil {
			return GenerationSummary{}, err
		}
	}
	if strings.EqualFold(sourceFullPath, outputFullPath) {
		return GenerationSummary{}, fmt.Errorf("The output must not overwrite the source image. (Parameter 'outputPath')")
	}
	if dir := filepath.Dir(outputFullPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return GenerationSummary{}, err
		}
	}
	file, err := os.Create(outputFullPath)
	if err != nil {
		return GenerationSummary{}, err
	}
	if wave {
		if err := WriteWaveHeader(file, parameters.TotalBytes()); err != nil {
			file.Close()
			return GenerationSummary{}, err
		}
	}

	byteOrder := AudioBigEndian
	if wave {
		byteOrder = AudioLittleEndian
	}
	summary, genErr := generateTrackToStream(ctx, sourceFullPath, file, parameters, interleave,
		progress, outputFullPath, byteOrder, effectiveTone)
	closeErr := file.Close()
	if genErr != nil {
		return GenerationSummary{}, genErr
	}
	if closeErr != nil {
		return GenerationSummary{}, closeErr
	}
	if err := SaveTrackMetadata(outputFullPath, TrackMetadata{
		SourceImage:               sourceFullPath,
		OutputTrack:               outputFullPath,
		InnerRadiusMm:             parameters.InnerRadiusMm,
		OuterRadiusMm:             parameters.OuterRadiusMm,
		Sectors:                   parameters.Sectors,
		LinearVelocityMmPerSecond: parameters.LinearVelocityMmPerSecond,
		StartAngleDegrees:         parameters.StartAngleRadians * 180.0 / math.Pi,
		ImageOuterRadiusMm:        parameters.ImageOuterRadiusMm,
		Interleaved:               interleave,
		ToneDitherStrength:        effectiveTone.DitherStrength,
		ToneLevels:                effectiveTone.Levels,
		ToneBrightness:            effectiveTone.Brightness,
		ToneSrgbLinearization:     effectiveTone.SrgbLinearization,
		PitchLinear:               parameters.PitchLinear,
		PitchQuadratic:            parameters.PitchQuadratic,
		PitchCubic:                parameters.PitchCubic,
		UsableSectors:             parameters.UsableSectors,
		HasUsableSectors:          parameters.HasUsableSectors,
		SourceImageOuterRadiusMm:  parameters.SourceImageOuterRadiusMm,
		HasSourceImageOuterRadius: parameters.HasSourceImageOuterRadius,
	}); err != nil {
		return GenerationSummary{}, err
	}
	if wave && writeCue {
		cuePath, err := WriteCue(outputFullPath)
		if err != nil {
			return GenerationSummary{}, err
		}
		summary.CueSheetPath = cuePath
	}
	return summary, nil
}

// generateTrackToStream ports CdTrackGenerator.GenerateToStream.
func generateTrackToStream(ctx context.Context, imagePath string, output *os.File,
	parameters *DiscParameters, interleave bool, progress func(Progress),
	outputDescription string, byteOrder AudioByteOrder, tone core.ToneProfile) (GenerationSummary, error) {
	sourceFullPath, err := filepath.Abs(imagePath)
	if err != nil {
		return GenerationSummary{}, err
	}
	if err := parameters.Validate(); err != nil {
		return GenerationSummary{}, err
	}
	source, err := loadRasterImage(sourceFullPath)
	if err != nil {
		return GenerationSummary{}, err
	}
	sampler := newImageSampler(source, parameters)
	return generateSamplesToStream(ctx, sampler.Sample, sourceFullPath, output, parameters,
		interleave, progress, outputDescription, byteOrder, tone.Normalized())
}

// generateSamplesToStream ports the parallel sampling path of
// CdTrackGenerator.GenerateSamplesToStream (useParallelSampling: true). The
// output is identical to the sequential path: sampling is pure.
func generateSamplesToStream(ctx context.Context, sampleGray func(int64) byte, summarySource string,
	output *os.File, parameters *DiscParameters, interleave bool, progress func(Progress),
	outputDescription string, byteOrder AudioByteOrder, tone core.ToneProfile) (GenerationSummary, error) {
	totalBytes := parameters.TotalBytes()
	var interleaver *cddaInterleaver
	var plainSector []byte
	if interleave {
		interleaver = newCddaInterleaver(byteOrder)
	} else {
		plainSector = make([]byte, BytesPerSector)
	}
	plainPosition := 0
	stopwatch := time.Now()
	progressInterval := max(int64(BytesPerSector), totalBytes/200)
	nextProgress := progressInterval

	fill := func(firstSector int64, buffer []byte, sectors int) error {
		firstByte := firstSector * BytesPerSector
		length := sectors * BytesPerSector
		for offset := 0; offset < length; offset++ {
			if (offset & 0xFFF) == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			position := firstByte + int64(offset)
			buffer[offset] = ToneMap(sampleGray(position), tone, position)
		}
		return nil
	}

	consume := func(firstSector int64, buffer []byte, sectors int) error {
		position := firstSector * BytesPerSector
		length := sectors * BytesPerSector
		offset := 0
		for offset < length {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Preserve the original progress positions, even inside sectors.
			count := int64(length - offset)
			if d := nextProgress - position; d < count {
				count = d
			}
			if d := int64(4096) - (position & 0xFFF); d < count {
				count = d
			}
			values := buffer[offset : offset+int(count)]
			if interleaver != nil {
				if err := interleaver.AddSpan(values, output); err != nil {
					return err
				}
			} else {
				for len(values) > 0 {
					copied := min(len(values), len(plainSector)-plainPosition)
					copy(plainSector[plainPosition:], values[:copied])
					plainPosition += copied
					values = values[copied:]
					if plainPosition == len(plainSector) {
						if err := WriteAudioSector(output, plainSector, byteOrder); err != nil {
							return err
						}
						plainPosition = 0
					}
				}
			}
			offset += int(count)
			position += count
			if position >= nextProgress {
				if progress != nil {
					progress(Progress{position, totalBytes, time.Since(stopwatch)})
				}
				nextProgress = position + progressInterval
			}
		}
		return nil
	}

	if err := runBlockPipeline(ctx, parameters.OutputSectors(), BytesPerSector, fill, consume); err != nil {
		return GenerationSummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return GenerationSummary{}, err
	}
	if interleaver != nil {
		if err := interleaver.Flush(output); err != nil {
			return GenerationSummary{}, err
		}
	}
	elapsed := time.Since(stopwatch)
	if progress != nil {
		progress(Progress{totalBytes, totalBytes, elapsed})
	}
	return GenerationSummary{
		SourceImage:  summarySource,
		OutputTrack:  outputDescription,
		Sectors:      parameters.OutputSectors(),
		BytesWritten: totalBytes,
		Interleaved:  interleave,
		Elapsed:      elapsed,
	}, nil
}

// grayPreview averages every sample that lands in one pixel.
type grayPreview struct {
	size         int
	levelSums    []int
	sampleCounts []int
	actual       *DiscParameters
}

func newGrayPreview(size int, actual *DiscParameters) *grayPreview {
	return &grayPreview{
		size:         size,
		levelSums:    make([]int, size*size),
		sampleCounts: make([]int, size*size),
		actual:       actual,
	}
}

func (p *grayPreview) Add(x, y int, level byte) {
	if x < 0 || y < 0 || x >= p.size || y >= p.size {
		return
	}
	pixelIndex := y*p.size + x
	p.levelSums[pixelIndex] += int(level)
	p.sampleCounts[pixelIndex]++
}

func (p *grayPreview) CreatePixels() []byte {
	pixels := make([]byte, p.size*p.size*4)
	for i := range pixels {
		pixels[i] = 255
	}
	for pixelIndex := 0; pixelIndex < len(p.sampleCounts); pixelIndex++ {
		count := p.sampleCounts[pixelIndex]
		if count == 0 {
			continue
		}
		level := byte((p.levelSums[pixelIndex] + count/2) / count)
		offset := pixelIndex * 4
		pixels[offset] = level
		pixels[offset+1] = level
		pixels[offset+2] = level
	}
	applyPreviewMask(pixels, p.size, p.actual)
	return pixels
}

// PreviewWarp ports CdTrackGenerator.PreviewWarp.
func PreviewWarp(ctx context.Context, imagePath, outputPath string, generated, actual *DiscParameters,
	outputSize, samplesPerSector int, tone core.ToneProfile) error {
	if err := generated.Validate(); err != nil {
		return err
	}
	if err := actual.Validate(); err != nil {
		return err
	}
	if err := validatePreview(outputSize, samplesPerSector); err != nil {
		return err
	}
	tone = tone.Normalized()
	sourceFullPath, err := filepath.Abs(imagePath)
	if err != nil {
		return err
	}
	source, err := loadRasterImage(sourceFullPath)
	if err != nil {
		return err
	}
	sampler := newImageSampler(source, generated)
	preview := newGrayPreview(outputSize, actual)
	step := max(int64(BytesPerSector)/int64(samplesPerSector), 1)
	totalBytes := generated.TotalBytes()
	sampleNumber := int64(0)
	for globalByte := int64(0); globalByte < totalBytes; globalByte += step {
		if (sampleNumber & 0x3FFF) == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		gray := sampler.Sample(globalByte)
		splatPreview(actual, outputSize, preview, globalByte, ToneMap(gray, tone, globalByte))
		sampleNumber++
	}
	outputFullPath, err := filepath.Abs(outputPath)
	if err != nil {
		return err
	}
	return writePreviewPng(outputFullPath, outputSize, preview.CreatePixels())
}

// PreviewTrack ports CdTrackGenerator.PreviewTrack.
func PreviewTrack(ctx context.Context, trackPath, outputPath string, actual *DiscParameters,
	outputSize, byteStep int, deinterleave bool) error {
	if err := actual.Validate(); err != nil {
		return err
	}
	if outputSize < 64 || outputSize > 8192 {
		return fmt.Errorf("Preview size must be 64..8192. (Parameter 'outputSize')")
	}
	if byteStep <= 0 {
		return fmt.Errorf("Byte step must be positive. (Parameter 'byteStep')")
	}
	trackFullPath, err := filepath.Abs(trackPath)
	if err != nil {
		return err
	}
	input, err := os.Open(trackFullPath)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	fileLength := info.Size()

	signature := make([]byte, 12)
	signatureLength, _ := io.ReadFull(input, signature)
	wave := hasWaveExtension(trackFullPath) ||
		(signatureLength == 12 && string(signature[:4]) == "RIFF" && string(signature[8:12]) == "WAVE")
	audioOffset := int64(0)
	audioLength := fileLength
	if wave {
		audioOffset, audioLength, err = ReadWaveAudioRange(input, fileLength)
		if err != nil {
			return err
		}
	}

	maximumBytes := min(actual.TotalBytes(), audioLength)
	preview := newGrayPreview(outputSize, actual)
	if deinterleave {
		err = splatDeinterleaved(ctx, input, audioOffset, audioLength, maximumBytes, wave, actual, outputSize, byteStep, preview)
	} else {
		err = splatFileOrder(ctx, input, audioOffset, maximumBytes, wave, actual, outputSize, byteStep, preview)
	}
	if err != nil {
		return err
	}
	outputFullPath, err := filepath.Abs(outputPath)
	if err != nil {
		return err
	}
	return writePreviewPng(outputFullPath, outputSize, preview.CreatePixels())
}

func splatFileOrder(ctx context.Context, input *os.File, audioOffset, maximumBytes int64,
	wave bool, actual *DiscParameters, outputSize, byteStep int, preview *grayPreview) error {
	buffer := make([]byte, 1024*1024)
	globalOffset := int64(0)
	for globalOffset < maximumBytes {
		requested := min(int64(len(buffer)), maximumBytes-globalOffset)
		if _, err := input.Seek(audioOffset+globalOffset, io.SeekStart); err != nil {
			return err
		}
		bytesRead, err := io.ReadFull(input, buffer[:requested])
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			// partial read; proceed with what we have
		} else if err != nil {
			return err
		}
		if bytesRead == 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if wave {
			if err := SwapAudioByteOrder(buffer[:bytesRead]); err != nil {
				return err
			}
		}
		remainder := globalOffset % int64(byteStep)
		first := int64(0)
		if remainder != 0 {
			first = int64(byteStep) - remainder
		}
		for index := first; index < int64(bytesRead); index += int64(byteStep) {
			globalByte := globalOffset + index
			splatPreview(actual, outputSize, preview, globalByte, buffer[index])
		}
		globalOffset += int64(bytesRead)
	}
	return nil
}

func splatDeinterleaved(ctx context.Context, input *os.File, audioOffset, audioLength, maximumBytes int64,
	wave bool, actual *DiscParameters, outputSize, byteStep int, preview *grayPreview) error {
	const windowBytes = 1024 * 1024
	lookAhead := InterleaveMaximumFileOffset
	buffer := make([]byte, windowBytes+lookAhead)
	for windowStart := int64(0); windowStart < maximumBytes; windowStart += windowBytes {
		if err := ctx.Err(); err != nil {
			return err
		}
		logicalCount := min(int64(windowBytes), maximumBytes-windowStart)
		remaining := audioLength - windowStart
		wanted := min(int64(len(buffer)), max(int64(0), remaining))
		if wanted == 0 {
			break
		}
		if _, err := input.Seek(audioOffset+windowStart, io.SeekStart); err != nil {
			return err
		}
		bytesRead, err := io.ReadFull(input, buffer[:wanted])
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			// partial read
		} else if err != nil {
			return err
		}
		bytesRead &= ^1
		if wave {
			if err := SwapAudioByteOrder(buffer[:bytesRead]); err != nil {
				return err
			}
		}
		first := ((windowStart + int64(byteStep) - 1) / int64(byteStep)) * int64(byteStep)
		for logical := first; logical < windowStart+logicalCount; logical += int64(byteStep) {
			local := logical + int64(InterleaveFileOffsetFor(int(logical%InterleaveBytesFrame))) - windowStart
			if local >= int64(bytesRead) {
				// The delay line loses the last frames of the track.
				continue
			}
			splatPreview(actual, outputSize, preview, logical, buffer[local])
		}
	}
	return nil
}

func splatPreview(actual *DiscParameters, outputSize int, preview *grayPreview, globalByte int64, paletteByte byte) {
	pixelX, pixelY := actual.ImagePointFromByte(globalByte, outputSize)
	preview.Add(int(math.RoundToEven(pixelX)), int(math.RoundToEven(pixelY)), ToneDisplayGray(paletteByte))
}

func writePreviewPng(outputPath string, size int, pixels []byte) error {
	if dir := filepath.Dir(outputPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return raster.SavePng(outputPath, size, size, pixels, false)
}

func validatePreview(outputSize, samplesPerSector int) error {
	if outputSize < 64 || outputSize > 8192 {
		return fmt.Errorf("Preview size must be 64..8192. (Parameter 'outputSize')")
	}
	if samplesPerSector < 1 || samplesPerSector > BytesPerSector {
		return fmt.Errorf("Samples per sector must be 1..%d. (Parameter 'samplesPerSector')", BytesPerSector)
	}
	return nil
}
