package bluray

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"disc-image-studio/internal/iso"
)

// IsoPlan ports BluRayIsoPlan: a full-disc drawing plan, optionally containing
// a fixed UDF filesystem snapshot.
type IsoPlan struct {
	disc       *DiscParameters
	filesystem *iso.UdfLayoutPlan

	drawingStartSector        uint32
	drawingStartRadiusMm      float64
	drawingEndSectorExclusive uint32
	drawingEndRadiusMm        float64
}

func newIsoPlanFilesystem(disc *DiscParameters, filesystem *iso.UdfLayoutPlan, drawingStartSector uint32) (*IsoPlan, error) {
	geometry, err := NewTrackGeometry(disc)
	if err != nil {
		return nil, err
	}
	start, err := geometry.AtSector(float64(drawingStartSector))
	if err != nil {
		return nil, err
	}
	end, err := geometry.AtSector(float64(filesystem.DrawingEndLbaExclusive()))
	if err != nil {
		return nil, err
	}
	return &IsoPlan{
		disc:                      disc,
		filesystem:                filesystem,
		drawingStartSector:        drawingStartSector,
		drawingStartRadiusMm:      start.RadiusMm,
		drawingEndSectorExclusive: filesystem.DrawingEndLbaExclusive(),
		drawingEndRadiusMm:        end.RadiusMm,
	}, nil
}

func newIsoPlanNoFilesystem(disc *DiscParameters) *IsoPlan {
	return &IsoPlan{
		disc:                      disc,
		drawingStartSector:        0,
		drawingEndSectorExclusive: disc.OutputSectors(),
		drawingStartRadiusMm:      disc.InnerRadiusMm,
		drawingEndRadiusMm:        disc.OutputOuterRadiusMm(),
	}
}

// NewIsoPlanRaw ports the saved-drawing-range constructor used by calibration
// records and sidecar-driven previews.
func NewIsoPlanRaw(disc *DiscParameters, drawingStartSector, drawingEndSectorExclusive uint32) (*IsoPlan, error) {
	if err := disc.Validate(); err != nil {
		return nil, err
	}
	if drawingStartSector >= drawingEndSectorExclusive || drawingEndSectorExclusive > disc.OutputSectors() {
		return nil, argOutOfRangeMsg("drawingEndSectorExclusive", "The drawing range must lie inside the disc.")
	}
	geometry, err := NewTrackGeometry(disc)
	if err != nil {
		return nil, err
	}
	start, err := geometry.AtSector(float64(drawingStartSector))
	if err != nil {
		return nil, err
	}
	end, err := geometry.AtSector(float64(drawingEndSectorExclusive))
	if err != nil {
		return nil, err
	}
	return &IsoPlan{
		disc:                      disc,
		drawingStartSector:        drawingStartSector,
		drawingStartRadiusMm:      start.RadiusMm,
		drawingEndSectorExclusive: drawingEndSectorExclusive,
		drawingEndRadiusMm:        end.RadiusMm,
	}, nil
}

// Filesystem returns the UDF layout plan, or an error when absent.
func (p *IsoPlan) Filesystem() (*iso.UdfLayoutPlan, error) {
	if p.filesystem == nil {
		return nil, errors.New("This drawing plan has no filesystem.")
	}
	return p.filesystem, nil
}

// HasFileSystem ports the C# property.
func (p *IsoPlan) HasFileSystem() bool { return p.filesystem != nil }

// FilesAtOuterRing ports the C# property.
func (p *IsoPlan) FilesAtOuterRing() bool {
	return p.filesystem != nil && p.filesystem.FilesAtOuterRing()
}

// Disc ports the C# property.
func (p *IsoPlan) Disc() *DiscParameters { return p.disc }

// TotalSectors is Disc.OutputSectors.
func (p *IsoPlan) TotalSectors() uint32 { return p.disc.OutputSectors() }

// ContentLength is Disc.ContentLength.
func (p *IsoPlan) ContentLength() int64 { return p.disc.ContentLength() }

// DataDirectory returns the source directory, "" when absent.
func (p *IsoPlan) DataDirectory() string {
	if p.filesystem != nil && len(p.filesystem.SourceDirectory()) > 0 {
		return p.filesystem.SourceDirectory()
	}
	return ""
}

// VolumeLabel returns "" when there is no filesystem.
func (p *IsoPlan) VolumeLabel() string {
	if p.filesystem == nil {
		return ""
	}
	return p.filesystem.VolumeLabel()
}

// UdfTimestamp returns nil when there is no filesystem.
func (p *IsoPlan) UdfTimestamp() *time.Time {
	if p.filesystem == nil {
		return nil
	}
	t := p.filesystem.Timestamp()
	return &t
}

// FileCount ports the C# property.
func (p *IsoPlan) FileCount() int {
	if p.filesystem == nil {
		return 0
	}
	return p.filesystem.FileCount()
}

// DirectoryCount ports the C# property.
func (p *IsoPlan) DirectoryCount() int {
	if p.filesystem == nil {
		return 0
	}
	return p.filesystem.DirectoryCount()
}

// FileBytes ports the C# property.
func (p *IsoPlan) FileBytes() int64 {
	if p.filesystem == nil {
		return 0
	}
	return p.filesystem.FileBytes()
}

// DrawingStartSector ports the C# property.
func (p *IsoPlan) DrawingStartSector() uint32 { return p.drawingStartSector }

// DrawingStartRadiusMm ports the C# property.
func (p *IsoPlan) DrawingStartRadiusMm() float64 { return p.drawingStartRadiusMm }

// DrawingEndSectorExclusive ports the C# property.
func (p *IsoPlan) DrawingEndSectorExclusive() uint32 { return p.drawingEndSectorExclusive }

// DrawingEndRadiusMm ports the C# property.
func (p *IsoPlan) DrawingEndRadiusMm() float64 { return p.drawingEndRadiusMm }

// PartitionStartSector ports the C# property.
func (p *IsoPlan) PartitionStartSector() uint32 {
	if p.filesystem == nil {
		return 0
	}
	return p.filesystem.PartitionStartLba()
}

// DataEndSectorExclusive ports the C# property.
func (p *IsoPlan) DataEndSectorExclusive() uint32 {
	if p.filesystem == nil {
		return p.drawingEndSectorExclusive
	}
	return p.filesystem.DataEndLbaExclusive()
}

// PrepareRawDrawing ports BluRayIsoGenerator.PrepareRaw.
func PrepareRawDrawing(disc *DiscParameters, drawingStartSector, drawingEndSectorExclusive uint32) (*IsoPlan, error) {
	return NewIsoPlanRaw(disc, drawingStartSector, drawingEndSectorExclusive)
}

// PrepareDrawing ports BluRayIsoGenerator.PrepareDrawing: draws every
// user-data sector when no directory is selected. Selecting a directory keeps
// a mountable UDF volume, including for an empty directory.
func PrepareDrawing(disc *DiscParameters, dataDirectory, volumeLabel string,
	filesAtOuterRing bool) (*IsoPlan, error) {
	if disc == nil {
		return nil, argNull("disc")
	}
	if err := disc.Validate(); err != nil {
		return nil, err
	}
	if filesAtOuterRing && isNullOrWhiteSpace(dataDirectory) {
		return nil, argErr("Outer-ring files require a data directory.", "dataDirectory")
	}
	if isNullOrWhiteSpace(dataDirectory) {
		return newIsoPlanNoFilesystem(disc), nil
	}
	return PrepareUdfDrawing(disc, dataDirectory, volumeLabel, filesAtOuterRing)
}

// PrepareUdfDrawing ports BluRayIsoGenerator.Prepare (explicit UDF
// preparation with the plan-time timestamp).
func PrepareUdfDrawing(disc *DiscParameters, dataDirectory, volumeLabel string,
	filesAtOuterRing bool) (*IsoPlan, error) {
	if disc == nil {
		return nil, argNull("disc")
	}
	if err := disc.Validate(); err != nil {
		return nil, err
	}
	// C#: if (dataDirectory is not null) ThrowIfNullOrWhiteSpace(dataDirectory).
	// The Go port represents null and "" identically, so validate on whitespace.
	if isNullOrWhiteSpace(dataDirectory) {
		return nil, argNullOrWhiteSpace("dataDirectory")
	}
	filesystem, err := iso.PlanUdf(dataDirectory, disc.OutputSectors(), volumeLabel, filesAtOuterRing)
	if err != nil {
		return nil, err
	}
	drawingStart := filesystem.DrawingStartLba()
	if drawingStart >= filesystem.DrawingEndLbaExclusive() {
		return nil, argErr("UDF metadata and files must leave at least 32 sectors for drawing.", "dataDirectory")
	}
	return newIsoPlanFilesystem(disc, filesystem, drawingStart)
}

// GenerateIsoToStream ports BluRayIsoGenerator.GenerateToStream. The same
// sequential byte producer is used by ISO files and live burns.
func GenerateIsoToStream(output io.Writer, plan *IsoPlan, mapping PayloadMapping,
	progress StreamingProgressFunc, ctx context.Context) (StreamingSummary, error) {
	if output == nil {
		return StreamingSummary{}, argNull("output")
	}
	if plan == nil {
		return StreamingSummary{}, argNull("plan")
	}
	if mapping == nil {
		return StreamingSummary{}, argNull("mapping")
	}
	if err := ctx.Err(); err != nil {
		return StreamingSummary{}, err
	}
	if !plan.HasFileSystem() {
		return GenerateStreamingToStream(output,
			&StreamingOptions{Disc: plan.disc, StartSector: 0, SectorCount: plan.TotalSectors()},
			mapping, progress, ctx)
	}
	start := time.Now()
	if progress != nil {
		progress(StreamingProgress{CompletedBytes: 0, TotalBytes: plan.ContentLength(), Elapsed: time.Since(start)})
	}
	destination := &progressStream{
		output: output, totalBytes: plan.ContentLength(), start: start,
		progress: progress, ctx: ctx,
	}
	if err := iso.WriteUdfSequentialPrefix(plan.filesystem, destination, plan.DrawingStartSector(), ctx); err != nil {
		return StreamingSummary{}, err
	}
	if _, err := GenerateStreamingToStream(destination,
		&StreamingOptions{Disc: plan.disc, StartSector: plan.DrawingStartSector(),
			SectorCount: plan.DrawingEndSectorExclusive() - plan.DrawingStartSector()},
		mapping, nil, ctx); err != nil {
		return StreamingSummary{}, err
	}
	if err := iso.WriteUdfSequentialSuffix(plan.filesystem, destination, plan.DrawingEndSectorExclusive(), ctx); err != nil {
		return StreamingSummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return StreamingSummary{}, err
	}
	if destination.bytesWritten != plan.ContentLength() {
		return StreamingSummary{}, errors.New("Blu-ray ISO generation did not produce the planned byte length.")
	}
	return StreamingSummary{StartSector: 0, SectorCount: plan.TotalSectors(),
		BytesWritten: destination.bytesWritten, Elapsed: time.Since(start)}, nil
}

// GenerateIsoFile ports BluRayIsoGenerator.GenerateFile: publishes only a
// complete ISO; cancellation or failure preserves an existing file.
func GenerateIsoFile(outputPath string, plan *IsoPlan, mapping PayloadMapping,
	progress StreamingProgressFunc, ctx context.Context) (StreamingSummary, error) {
	if isNullOrWhiteSpace(outputPath) {
		return StreamingSummary{}, argNullOrWhiteSpace("outputPath")
	}
	if plan == nil {
		return StreamingSummary{}, argNull("plan")
	}
	if mapping == nil {
		return StreamingSummary{}, argNull("mapping")
	}
	if err := ValidateIsoOutputPath(outputPath, plan); err != nil {
		return StreamingSummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return StreamingSummary{}, err
	}
	var summary StreamingSummary
	err := outputPublisherWrite(outputPath, func(stream io.Writer) error {
		var err error
		summary, err = GenerateIsoToStream(stream, plan, mapping, progress, ctx)
		return err
	}, ctx)
	if err != nil {
		return StreamingSummary{}, err
	}
	return summary, nil
}

// ValidateIsoOutputPath ports BluRayIsoGenerator.ValidateOutputPath.
func ValidateIsoOutputPath(outputPath string, plan *IsoPlan) error {
	if isNullOrWhiteSpace(outputPath) {
		return argNullOrWhiteSpace("outputPath")
	}
	if plan == nil {
		return argNull("plan")
	}
	source := plan.DataDirectory()
	if source == "" {
		return nil
	}
	destination, err := filepath.Abs(outputPath)
	if err != nil {
		return err
	}
	if iso.IsWithinDirectory(destination, source) ||
		iso.IsWithinDirectory(resolveDirectoryLinks(destination), resolveDirectoryLinks(source)) {
		return argErr("ISO output must be outside the source directory to preserve source files.", "outputPath")
	}
	return nil
}

// resolveDirectoryLinks ports the private BluRayIsoGenerator.ResolveDirectoryLinks.
func resolveDirectoryLinks(path string) string {
	full, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(full); volume != "" {
		root = volume + string(filepath.Separator)
	}
	current := root
	rest := strings.TrimPrefix(full, root)
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Stat(current) // follows links, like Directory.Exists
		linfo, lstatErr := os.Lstat(current)
		if statErr == nil && info.IsDir() && lstatErr == nil && linfo.Mode()&os.ModeSymlink != 0 {
			if resolved, resolveErr := filepath.EvalSymlinks(current); resolveErr == nil {
				current = resolved
			}
		}
	}
	return current
}

// progressStream ports the private BluRayIsoGenerator.ProgressStream.
type progressStream struct {
	output       io.Writer
	totalBytes   int64
	start        time.Time
	progress     StreamingProgressFunc
	ctx          context.Context
	bytesWritten int64
}

func (s *progressStream) Write(buffer []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := s.output.Write(buffer)
	s.bytesWritten += int64(n)
	if s.progress != nil {
		s.progress(StreamingProgress{CompletedBytes: s.bytesWritten, TotalBytes: s.totalBytes, Elapsed: time.Since(s.start)})
	}
	return n, err
}
