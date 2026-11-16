package bluray

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// SectorContext ports BluRaySectorContext.
type SectorContext struct {
	Sector               uint32
	PhysicalSectorNumber uint32
	Disc                 *DiscParameters
}

// PayloadMapping fills all 2048 user-byte positions in the scrambled domain.
// An image mapping may use any strategy; the stream writer does not choose an
// image mapping.
type PayloadMapping interface {
	FillScrambledPayload(context SectorContext, destination []byte) error
}

// batchPayloadMapping mirrors the internal IBluRayBatchPayloadMapping.
type batchPayloadMapping interface {
	PayloadMapping
	CanGenerateInParallel(disc *DiscParameters) bool
	tryFillScrambledCluster(disc *DiscParameters, firstSector uint32, destination []byte, ctx context.Context) (bool, error)
}

// ConstantByteMapping is an explicit numeric probe, not an optical luminance
// calibration.
type ConstantByteMapping struct {
	Value byte
}

// FillScrambledPayload fills the destination with the constant value.
func (m *ConstantByteMapping) FillScrambledPayload(_ SectorContext, destination []byte) error {
	for i := range destination {
		destination[i] = m.Value
	}
	return nil
}

// StreamingOptions ports BluRayStreamingOptions.
type StreamingOptions struct {
	Disc        *DiscParameters
	StartSector uint32
	SectorCount uint32
}

// ContentLength is (long)SectorCount * BytesPerSector.
func (o *StreamingOptions) ContentLength() int64 {
	return int64(o.SectorCount) * BytesPerSector
}

// Validate ports BluRayStreamingOptions.Validate.
func (o *StreamingOptions) Validate() error {
	if o.Disc == nil {
		return argNull("Disc")
	}
	if err := o.Disc.Validate(); err != nil {
		return err
	}
	if o.StartSector%32 != 0 || o.SectorCount == 0 || o.SectorCount%32 != 0 ||
		o.StartSector >= o.Disc.OutputSectors() || o.SectorCount > o.Disc.OutputSectors()-o.StartSector {
		return argOutOfRangeMsg("SectorCount",
			"BD range must be inside the disc, start on a 32-sector boundary, and contain a positive multiple of 32 sectors.")
	}
	return nil
}

// StreamingProgress ports BluRayStreamingProgress.
type StreamingProgress struct {
	CompletedBytes int64
	TotalBytes     int64
	Elapsed        time.Duration
}

// Fraction ports the C# property.
func (p StreamingProgress) Fraction() float64 {
	if p.TotalBytes == 0 {
		return 0
	}
	return float64(p.CompletedBytes) / float64(p.TotalBytes)
}

// StreamingSummary ports BluRayStreamingSummary.
type StreamingSummary struct {
	StartSector  uint32
	SectorCount  uint32
	BytesWritten int64
	Elapsed      time.Duration
}

// StreamingProgressFunc receives BluRayStreamingProgress reports.
type StreamingProgressFunc func(StreamingProgress)

// PendingMappingMessage ports BluRayStreamingGenerator.PendingMappingMessage.
const PendingMappingMessage = "请提供蓝光数据映射；图片可使用 BluRayRunLengthMapping 的 3T / 6T 数量偏向策略。"

// GenerateStreamingToStream ports BluRayStreamingGenerator.GenerateToStream.
// The C# parallel batches are executed sequentially; block independence makes
// the byte stream identical.
func GenerateStreamingToStream(output io.Writer, options *StreamingOptions, mapping PayloadMapping,
	progress StreamingProgressFunc, ctx context.Context) (StreamingSummary, error) {
	if output == nil {
		return StreamingSummary{}, argNull("output")
	}
	if options == nil {
		return StreamingSummary{}, argNull("options")
	}
	if err := options.Validate(); err != nil {
		return StreamingSummary{}, err
	}
	if mapping == nil {
		return StreamingSummary{}, errors.New(PendingMappingMessage)
	}
	if err := ctx.Err(); err != nil {
		return StreamingSummary{}, err
	}
	start := time.Now()
	var completed int64
	if progress != nil {
		progress(StreamingProgress{CompletedBytes: 0, TotalBytes: options.ContentLength(), Elapsed: time.Since(start)})
	}
	cluster := make([]byte, PayloadBytesPerCluster)
	for offset := uint32(0); offset < options.SectorCount; offset += SectorsPerCluster {
		if err := ctx.Err(); err != nil {
			return StreamingSummary{}, err
		}
		if err := fillCluster(cluster, options.Disc, options.StartSector+offset, mapping, ctx); err != nil {
			return StreamingSummary{}, err
		}
		if err := ctx.Err(); err != nil {
			return StreamingSummary{}, err
		}
		if _, err := output.Write(cluster); err != nil {
			return StreamingSummary{}, err
		}
		completed += int64(len(cluster))
		if progress != nil {
			progress(StreamingProgress{CompletedBytes: completed, TotalBytes: options.ContentLength(), Elapsed: time.Since(start)})
		}
	}
	if err := ctx.Err(); err != nil {
		return StreamingSummary{}, err
	}
	return StreamingSummary{StartSector: options.StartSector, SectorCount: options.SectorCount,
		BytesWritten: completed, Elapsed: time.Since(start)}, nil
}

func fillCluster(cluster []byte, disc *DiscParameters, firstSector uint32,
	mapping PayloadMapping, ctx context.Context) error {
	psn, err := disc.GetPhysicalSectorNumber(firstSector)
	if err != nil {
		return err
	}
	// §13.4 resets to the SAME seed for every sector in a cluster.
	mask := make([]byte, BytesPerSector)
	if err := FillScrambleMask(psn, mask); err != nil {
		return err
	}
	filledCluster := false
	if batch, ok := mapping.(batchPayloadMapping); ok {
		filled, err := batch.tryFillScrambledCluster(disc, firstSector, cluster, ctx)
		if err != nil {
			return err
		}
		filledCluster = filled
	}
	for sector := 0; sector < 32; sector++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		payload := cluster[sector*2048 : sector*2048+2048]
		if !filledCluster {
			clear(payload)
			if err := mapping.FillScrambledPayload(SectorContext{
				Sector:               firstSector + uint32(sector),
				PhysicalSectorNumber: psn + uint32(sector),
				Disc:                 disc,
			}, payload); err != nil {
				return err
			}
		}
		for index := range payload {
			payload[index] ^= mask[index]
		}
	}
	return nil
}

// GenerateStreamingFile ports BluRayStreamingGenerator.GenerateFile.
func GenerateStreamingFile(outputPath string, options *StreamingOptions, mapping PayloadMapping,
	progress StreamingProgressFunc, ctx context.Context) (StreamingSummary, error) {
	if isNullOrWhiteSpace(outputPath) {
		return StreamingSummary{}, argNullOrWhiteSpace("outputPath")
	}
	if options == nil {
		return StreamingSummary{}, argNull("options")
	}
	if err := options.Validate(); err != nil {
		return StreamingSummary{}, err
	}
	if mapping == nil {
		return StreamingSummary{}, errors.New(PendingMappingMessage)
	}
	if err := ctx.Err(); err != nil {
		return StreamingSummary{}, err
	}
	var summary StreamingSummary
	err := outputPublisherWrite(outputPath, func(stream io.Writer) error {
		var err error
		summary, err = GenerateStreamingToStream(stream, options, mapping, progress, ctx)
		return err
	}, ctx)
	if err != nil {
		return StreamingSummary{}, err
	}
	return summary, nil
}

// outputPublisherWrite ports BluRayOutputPublisher.Write: publishes only a
// complete output; cancellation/failure preserves existing files.
func outputPublisherWrite(outputPath string, write func(io.Writer) error, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	finalPath, err := filepath.Abs(outputPath)
	if err != nil {
		return err
	}
	directory := filepath.Dir(finalPath)
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return err
	}
	tempPath := filepath.Join(directory,
		fmt.Sprintf(".%s.%s.partial", filepath.Base(finalPath), newGuidN()))
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	// Port of the C# try/finally: the partial file is always removed when the
	// publish did not move it away.
	published := false
	defer func() {
		if !published {
			_ = os.Remove(tempPath)
		}
	}()
	if err := write(file); err != nil {
		_ = file.Close()
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil { // Flush(flushToDisk: true)
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, finalPath); err != nil { // File.Move(overwrite: true)
		return err
	}
	published = true
	return nil
}

// newGuidN returns 32 lowercase hex characters like Guid.NewGuid():N.
func newGuidN() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
