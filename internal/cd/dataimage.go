package cd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"disc-image-studio/internal/core"
	"disc-image-studio/internal/iso"
	"disc-image-studio/internal/raster"
)

// DataBytesPerSector is the Mode 1 payload size.
const DataBytesPerSector = 2048

const dataPayloadOffset = 16

var dataScrambleMask = buildScrambleMask()
var dataPhysicalOffsets = buildPhysicalOffsets()

// DataLayoutPlan ports CdDataLayoutPlan.
type DataLayoutPlan struct {
	IsoLayout *iso.IsoLayoutPlan

	DrawingStartLba        uint32
	DrawingEndLbaExclusive uint32
}

// TotalSectors is the volume sector count.
func (p *DataLayoutPlan) TotalSectors() int64 { return int64(p.IsoLayout.VolumeSectors()) }

// ContentLength is the output byte count.
func (p *DataLayoutPlan) ContentLength() int64 { return p.TotalSectors() * DataBytesPerSector }

// SourceDirectory is the planned data directory.
func (p *DataLayoutPlan) SourceDirectory() string { return p.IsoLayout.SourceDirectory() }

// DrawingSectors is the drawing range length.
func (p *DataLayoutPlan) DrawingSectors() uint32 { return p.DrawingEndLbaExclusive - p.DrawingStartLba }

// FileCount is the number of planned files.
func (p *DataLayoutPlan) FileCount() int { return p.IsoLayout.FileCount() }

// PrepareDataLayout ports CdDataImageGenerator.PrepareLayout.
func PrepareDataLayout(dataDirectory string, parameters *DiscParameters,
	volumeLabel string, filesAtOuterRing bool) (*DataLayoutPlan, error) {
	if strings.TrimSpace(dataDirectory) == "" {
		return nil, fmt.Errorf("Value cannot be an empty string, or consist only of white-space characters. (Parameter 'dataDirectory')")
	}
	if err := parameters.Validate(); err != nil {
		return nil, err
	}
	layout, err := iso.PlanIso(dataDirectory, uint32(parameters.OutputSectors()), volumeLabel, filesAtOuterRing)
	if err != nil {
		return nil, err
	}
	plan := &DataLayoutPlan{IsoLayout: layout}
	if layout.FilesAtOuterRing() {
		plan.DrawingStartLba = layout.MetadataEndLbaExclusive()
		plan.DrawingEndLbaExclusive = layout.FileDataStartLba()
	} else {
		plan.DrawingStartLba = layout.DataEndLbaExclusive()
		plan.DrawingEndLbaExclusive = layout.VolumeSectors()
	}
	if plan.DrawingStartLba >= plan.DrawingEndLbaExclusive {
		return nil, fmt.Errorf("The filesystem leaves no CD sectors for drawing. (Parameter 'dataDirectory')")
	}
	return plan, nil
}

// ECMA-130 Annex B: x^15 + x + 1, seed 1 at byte 12, LSB first.
func buildScrambleMask() []byte {
	mask := make([]byte, BytesPerSector-12)
	state := 1
	for index := range mask {
		value := 0
		for bit := 0; bit < 8; bit++ {
			value |= (state & 1) << uint(bit)
			state = (state >> 1) | (((state ^ (state >> 1)) & 1) << 14)
		}
		mask[index] = byte(value)
	}
	return mask
}

func buildPhysicalOffsets() []int {
	inverse := make([]int, InterleaveBytesFrame)
	for logical := 0; logical < len(inverse); logical++ {
		displacement := InterleaveFileOffsetFor(logical)
		inverse[(logical+displacement)%len(inverse)] = -displacement
	}
	return inverse
}

// PhysicalByteForPayload locates a payload byte in the CD drawing coordinate
// convention (ECMA-130 clauses 14–17).
func PhysicalByteForPayload(lba int64, payloadOffset int) (int64, error) {
	if lba < 0 || payloadOffset < 0 || payloadOffset >= DataBytesPerSector {
		return 0, fmt.Errorf("payload offset out of range")
	}
	frameByte := (lba*BytesPerSector + int64(dataPayloadOffset) + int64(payloadOffset)) ^ 1
	return frameByte + int64(dataPhysicalOffsets[frameByte%InterleaveBytesFrame]), nil
}

func validateDataParameters(parameters *DiscParameters, plan *DataLayoutPlan) error {
	if parameters == nil || plan == nil {
		return fmt.Errorf("Value cannot be null. (Parameter 'plan')")
	}
	if err := parameters.Validate(); err != nil {
		return err
	}
	if parameters.OutputSectors() != plan.TotalSectors() {
		return fmt.Errorf("CD layout and geometry must have the same sector count. (Parameter 'plan')")
	}
	return nil
}

// progressWriter ports the private ProgressStream of CdDataImageGenerator.
type progressWriter struct {
	output         io.Writer
	total          int64
	stopwatch      time.Time
	progress       func(Progress)
	ctx            context.Context
	completedBytes int64
	nextProgress   int64
}

func (w *progressWriter) Write(buffer []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.output.Write(buffer)
	w.completedBytes += int64(n)
	if err == nil && w.completedBytes >= w.nextProgress {
		if w.progress != nil {
			w.progress(Progress{w.completedBytes, w.total, time.Since(w.stopwatch)})
		}
		w.nextProgress = w.completedBytes + max(int64(DataBytesPerSector), w.total/200)
	}
	return n, err
}

// GenerateDataImage ports CdDataImageGenerator.Generate.
func GenerateDataImage(ctx context.Context, imagePath, outputPath string, parameters *DiscParameters,
	plan *DataLayoutPlan, progress func(Progress), tone core.ToneProfile) (GenerationSummary, error) {
	if err := validateDataParameters(parameters, plan); err != nil {
		return GenerationSummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return GenerationSummary{}, err
	}
	source, err := filepath.Abs(imagePath)
	if err != nil {
		return GenerationSummary{}, err
	}
	target, err := filepath.Abs(outputPath)
	if err != nil {
		return GenerationSummary{}, err
	}
	if !strings.EqualFold(filepath.Ext(target), ".iso") {
		return GenerationSummary{}, fmt.Errorf("CD data output must be a Mode 1 .iso image. (Parameter 'outputPath')")
	}
	sourceDir := strings.TrimRight(plan.SourceDirectory(), string(filepath.Separator))
	if strings.EqualFold(target, source) ||
		(sourceDir != "" && strings.HasPrefix(strings.ToLower(target), strings.ToLower(sourceDir+string(filepath.Separator)))) {
		return GenerationSummary{}, fmt.Errorf("The output must be outside the source data folder and must not overwrite the source image. (Parameter 'outputPath')")
	}
	image, err := loadRasterImage(source)
	if err != nil {
		return GenerationSummary{}, err
	}
	effectiveTone := tone.Normalized()
	if dir := filepath.Dir(target); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return GenerationSummary{}, err
		}
	}
	file, err := os.Create(target)
	if err != nil {
		return GenerationSummary{}, err
	}
	summary, genErr := generateDataImageToStream(ctx, source, image, file, parameters, plan, progress, effectiveTone)
	closeErr := file.Close()
	if genErr != nil {
		return GenerationSummary{}, genErr
	}
	if closeErr != nil {
		return GenerationSummary{}, closeErr
	}
	summary.OutputTrack = target
	if err := SaveDataImageMetadata(target, DataImageMetadata{
		Parameters:             parameters,
		DrawingStartLba:        plan.DrawingStartLba,
		DrawingEndLbaExclusive: plan.DrawingEndLbaExclusive,
		FilesAtOuterRing:       plan.IsoLayout.FilesAtOuterRing(),
		Tone:                   effectiveTone,
	}); err != nil {
		return GenerationSummary{}, err
	}
	return summary, nil
}

func generateDataImageToStream(ctx context.Context, source string, image *rasterImage, output io.Writer,
	parameters *DiscParameters, plan *DataLayoutPlan, progress func(Progress),
	tone core.ToneProfile) (GenerationSummary, error) {
	sampler := newImageSampler(image, parameters)
	stopwatch := time.Now()
	writer := &progressWriter{output: output, total: plan.ContentLength(), stopwatch: stopwatch, progress: progress, ctx: ctx}

	if plan.IsoLayout.FilesAtOuterRing() {
		if err := iso.WriteIsoOuterRingPrefix(plan.IsoLayout, writer, plan.DrawingStartLba, ctx); err != nil {
			return GenerationSummary{}, err
		}
	} else {
		if err := iso.WriteIsoSequentialPrefix(plan.IsoLayout, writer, plan.DrawingStartLba, ctx); err != nil {
			return GenerationSummary{}, err
		}
	}

	fill := func(firstSector int64, buffer []byte, sectorCount int) error {
		for index := 0; index < sectorCount; index++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			fillDrawingSector(sampler, tone, int64(plan.DrawingStartLba)+firstSector+int64(index),
				buffer[index*DataBytesPerSector:(index+1)*DataBytesPerSector])
		}
		return nil
	}
	consume := func(firstSector int64, buffer []byte, sectorCount int) error {
		for index := 0; index < sectorCount; index++ {
			if _, err := writer.Write(buffer[index*DataBytesPerSector : (index+1)*DataBytesPerSector]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := runBlockPipeline(ctx, int64(plan.DrawingSectors()), DataBytesPerSector, fill, consume); err != nil {
		return GenerationSummary{}, err
	}

	if plan.IsoLayout.FilesAtOuterRing() {
		if err := iso.WriteIsoOuterRingSuffix(plan.IsoLayout, writer, plan.DrawingEndLbaExclusive, ctx); err != nil {
			return GenerationSummary{}, err
		}
	}
	elapsed := time.Since(stopwatch)
	if progress != nil {
		progress(Progress{writer.completedBytes, plan.ContentLength(), elapsed})
	}
	return GenerationSummary{
		SourceImage:  source,
		OutputTrack:  "direct-burn-stream",
		Sectors:      parameters.OutputSectors(),
		BytesWritten: writer.completedBytes,
		Interleaved:  false,
		Elapsed:      elapsed,
	}, nil
}

func fillDrawingSector(sampler *imageSampler, tone core.ToneProfile, lba int64, sector []byte) {
	for offset := 0; offset < DataBytesPerSector; offset++ {
		physicalByte, err := PhysicalByteForPayload(lba, offset)
		if err != nil {
			continue // cannot happen: lba >= 0, offset in range
		}
		sector[offset] = ToneMap(sampler.Sample(physicalByte), tone, physicalByte) ^ dataScrambleMask[offset+dataPayloadOffset-12]
	}
}

// PreviewDataWarp ports CdDataImageGenerator.PreviewWarp.
func PreviewDataWarp(ctx context.Context, imagePath, outputPath string, generated, actual *DiscParameters,
	outputSize, samplesPerSector int, tone core.ToneProfile, plan *DataLayoutPlan) error {
	if err := validateDataPreview(actual, outputSize, samplesPerSector); err != nil {
		return err
	}
	if err := generated.Validate(); err != nil {
		return err
	}
	if plan != nil {
		if err := validateDataParameters(generated, plan); err != nil {
			return err
		}
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
	preview := newDataPreview(outputSize, actual)
	start := int64(0)
	end := generated.OutputSectors()
	if plan != nil {
		start = int64(plan.DrawingStartLba)
		end = int64(plan.DrawingEndLbaExclusive)
	}
	step := max(DataBytesPerSector/samplesPerSector, 1)
	for lba := start; lba < end; lba++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for offset := 0; offset < DataBytesPerSector; offset += step {
			physicalByte, err := PhysicalByteForPayload(lba, offset)
			if err != nil {
				return err
			}
			if physicalByte < 0 {
				continue
			}
			preview.Splat(actual, physicalByte, ToneMap(sampler.Sample(physicalByte), tone, physicalByte))
		}
	}
	return preview.Save(outputPath)
}

// PreviewDataTrack ports CdDataImageGenerator.PreviewTrack.
func PreviewDataTrack(ctx context.Context, isoPath, outputPath string, actual *DiscParameters,
	outputSize, byteStep int) error {
	if err := validateDataPreview(actual, outputSize, byteStep); err != nil {
		return err
	}
	input, err := os.Open(isoPath)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if info.Size()%DataBytesPerSector != 0 || info.Size() != actual.OutputSectors()*DataBytesPerSector {
		return fmt.Errorf("CD data ISO length must match the selected 2048-byte sector count. (Parameter 'isoPath')")
	}
	metadata := TryLoadDataImageMetadata(isoPath)
	if metadata != nil && metadata.Parameters.OutputSectors() != actual.OutputSectors() {
		metadata = nil
	}
	start := int64(0)
	end := actual.OutputSectors()
	if metadata != nil {
		start = int64(metadata.DrawingStartLba)
		end = int64(metadata.DrawingEndLbaExclusive)
	}
	if _, err := input.Seek(start*DataBytesPerSector, io.SeekStart); err != nil {
		return err
	}
	sector := make([]byte, DataBytesPerSector)
	preview := newDataPreview(outputSize, actual)
	for lba := start; lba < end; lba++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.ReadFull(input, sector); err != nil {
			return err
		}
		for offset := 0; offset < DataBytesPerSector; offset += byteStep {
			physicalByte, err := PhysicalByteForPayload(lba, offset)
			if err != nil {
				return err
			}
			if physicalByte < 0 {
				continue
			}
			preview.Splat(actual, physicalByte, sector[offset]^dataScrambleMask[offset+dataPayloadOffset-12])
		}
	}
	return preview.Save(outputPath)
}

func validateDataPreview(parameters *DiscParameters, size, interval int) error {
	if err := parameters.Validate(); err != nil {
		return err
	}
	if size < 64 || size > 8192 {
		return fmt.Errorf("Preview size must be 64..8192. (Parameter 'size')")
	}
	if interval <= 0 {
		return fmt.Errorf("Specified argument was out of the range of valid values. (Parameter 'interval')")
	}
	return nil
}

// dataPreview ports the private DataPreview accumulator.
type dataPreview struct {
	size   int
	sum    []int64
	count  []int
	actual *DiscParameters
}

func newDataPreview(size int, actual *DiscParameters) *dataPreview {
	return &dataPreview{size: size, sum: make([]int64, size*size), count: make([]int, size*size), actual: actual}
}

func (p *dataPreview) Splat(parameters *DiscParameters, physicalByte int64, palette byte) {
	x, y := parameters.ImagePointFromByte(physicalByte, p.size)
	px := int(math.RoundToEven(x))
	py := int(math.RoundToEven(y))
	if px < 0 || py < 0 || px >= p.size || py >= p.size {
		return
	}
	index := py*p.size + px
	p.sum[index] += int64(ToneDisplayGray(palette))
	p.count[index]++
}

func (p *dataPreview) Save(path string) error {
	pixels := make([]byte, p.size*p.size*4)
	for i := range pixels {
		pixels[i] = 255
	}
	for index := 0; index < len(p.count); index++ {
		if p.count[index] == 0 {
			continue
		}
		gray := byte((p.sum[index] + int64(p.count[index])/2) / int64(p.count[index]))
		pixels[index*4] = gray
		pixels[index*4+1] = gray
		pixels[index*4+2] = gray
	}
	applyPreviewMask(pixels, p.size, p.actual)
	fullPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(fullPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return raster.SavePng(fullPath, p.size, p.size, pixels, false)
}

// DataImageMetadata ports CdDataImageMetadata.
type DataImageMetadata struct {
	Parameters             *DiscParameters
	DrawingStartLba        uint32
	DrawingEndLbaExclusive uint32
	FilesAtOuterRing       bool
	Tone                   core.ToneProfile
}

// DataImageMetadataFormat is the serialized marker.
const DataImageMetadataFormat = "cd-rom-mode1-v1"

type dataParametersJSON struct {
	InnerRadiusMm             float64  `json:"innerRadiusMm"`
	OuterRadiusMm             float64  `json:"outerRadiusMm"`
	Sectors                   int64    `json:"sectors"`
	LinearVelocityMmPerSecond float64  `json:"linearVelocityMmPerSecond"`
	StartAngleRadians         float64  `json:"startAngleRadians"`
	ImageOuterRadiusMm        float64  `json:"imageOuterRadiusMm"`
	PitchLinear               float64  `json:"pitchLinear"`
	PitchQuadratic            float64  `json:"pitchQuadratic"`
	PitchCubic                float64  `json:"pitchCubic"`
	UsableSectors             *int64   `json:"usableSectors"`
	SourceImageOuterRadiusMm  *float64 `json:"sourceImageOuterRadiusMm"`
	// Computed get-only properties the C# record also serializes.
	OutputSectors              int64   `json:"outputSectors"`
	TotalBytes                 int64   `json:"totalBytes"`
	GeometryBytes              int64   `json:"geometryBytes"`
	OutputOuterRadiusMm        float64 `json:"outputOuterRadiusMm"`
	PreviewImageOuterRadiusMm  float64 `json:"previewImageOuterRadiusMm"`
	SamplingImageOuterRadiusMm float64 `json:"samplingImageOuterRadiusMm"`
	TotalTimeSeconds           float64 `json:"totalTimeSeconds"`
}

type dataToneJSON struct {
	DitherStrength    float64 `json:"ditherStrength"`
	Levels            int     `json:"levels"`
	Brightness        float64 `json:"brightness"`
	SrgbLinearization bool    `json:"srgbLinearization"`
}

type dataImageMetadataJSON struct {
	Parameters             dataParametersJSON `json:"parameters"`
	DrawingStartLba        uint32             `json:"drawingStartLba"`
	DrawingEndLbaExclusive uint32             `json:"drawingEndLbaExclusive"`
	FilesAtOuterRing       bool               `json:"filesAtOuterRing"`
	Tone                   dataToneJSON       `json:"tone"`
	Format                 string             `json:"format"`
}

// TryLoadDataImageMetadata reads a sidecar, returning nil when unusable.
func TryLoadDataImageMetadata(isoPath string) *DataImageMetadata {
	data, err := os.ReadFile(isoPath + ".json")
	if err != nil {
		return nil
	}
	var raw dataImageMetadataJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if raw.Format != DataImageMetadataFormat {
		return nil
	}
	parameters, err := NewDiscParameters(
		raw.Parameters.InnerRadiusMm, raw.Parameters.OuterRadiusMm, raw.Parameters.Sectors,
		raw.Parameters.LinearVelocityMmPerSecond, raw.Parameters.StartAngleRadians,
		raw.Parameters.ImageOuterRadiusMm, raw.Parameters.PitchLinear, raw.Parameters.PitchQuadratic,
		raw.Parameters.PitchCubic,
		valueOrZero(raw.Parameters.UsableSectors), raw.Parameters.UsableSectors != nil,
		valueOrZeroF(raw.Parameters.SourceImageOuterRadiusMm), raw.Parameters.SourceImageOuterRadiusMm != nil)
	if err != nil {
		return nil
	}
	if err := parameters.Validate(); err != nil {
		return nil
	}
	tone := core.ToneProfile{
		DitherStrength:    raw.Tone.DitherStrength,
		Levels:            raw.Tone.Levels,
		Brightness:        raw.Tone.Brightness,
		SrgbLinearization: raw.Tone.SrgbLinearization,
	}
	if err := tone.Validate("tone"); err != nil {
		return nil
	}
	if raw.DrawingStartLba < raw.DrawingEndLbaExclusive &&
		int64(raw.DrawingEndLbaExclusive) <= parameters.OutputSectors() {
		return &DataImageMetadata{
			Parameters:             parameters,
			DrawingStartLba:        raw.DrawingStartLba,
			DrawingEndLbaExclusive: raw.DrawingEndLbaExclusive,
			FilesAtOuterRing:       raw.FilesAtOuterRing,
			Tone:                   tone,
		}
	}
	return nil
}

// SaveDataImageMetadata writes the sidecar next to the ISO.
func SaveDataImageMetadata(path string, metadata DataImageMetadata) error {
	raw := dataImageMetadataJSON{
		DrawingStartLba:        metadata.DrawingStartLba,
		DrawingEndLbaExclusive: metadata.DrawingEndLbaExclusive,
		FilesAtOuterRing:       metadata.FilesAtOuterRing,
		Format:                 DataImageMetadataFormat,
		Tone: dataToneJSON{
			DitherStrength:    metadata.Tone.DitherStrength,
			Levels:            metadata.Tone.Levels,
			Brightness:        metadata.Tone.Brightness,
			SrgbLinearization: metadata.Tone.SrgbLinearization,
		},
	}
	p := metadata.Parameters
	raw.Parameters = dataParametersJSON{
		InnerRadiusMm:              p.InnerRadiusMm,
		OuterRadiusMm:              p.OuterRadiusMm,
		Sectors:                    p.Sectors,
		LinearVelocityMmPerSecond:  p.LinearVelocityMmPerSecond,
		StartAngleRadians:          p.StartAngleRadians,
		ImageOuterRadiusMm:         p.ImageOuterRadiusMm,
		PitchLinear:                p.PitchLinear,
		PitchQuadratic:             p.PitchQuadratic,
		PitchCubic:                 p.PitchCubic,
		OutputSectors:              p.OutputSectors(),
		TotalBytes:                 p.TotalBytes(),
		GeometryBytes:              p.GeometryBytes(),
		OutputOuterRadiusMm:        p.OutputOuterRadiusMm(),
		PreviewImageOuterRadiusMm:  p.PreviewImageOuterRadiusMm(),
		SamplingImageOuterRadiusMm: p.SamplingImageOuterRadiusMm(),
		TotalTimeSeconds:           p.TotalTimeSeconds(),
	}
	if p.HasUsableSectors {
		raw.Parameters.UsableSectors = &p.UsableSectors
	}
	if p.HasSourceImageOuterRadius {
		raw.Parameters.SourceImageOuterRadiusMm = &p.SourceImageOuterRadiusMm
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(raw); err != nil {
		return err
	}
	return os.WriteFile(path+".json", buffer.Bytes(), 0o644)
}

func valueOrZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func valueOrZeroF(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
