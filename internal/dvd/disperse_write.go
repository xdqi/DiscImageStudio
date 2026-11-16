package dvd

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"disc-image-studio/internal/iso"
)

// sectorByteSource resolves the content of non-picture sectors.
type sectorByteSource interface {
	// Content returns the 2048 bytes to write at lba, or ok=false when the
	// sector is a picture sector.
	Content(lba uint32) (data []byte, ok bool, err error)
	Close() error
}

// dispersedSectorSource serves descriptor bytes, file bytes and gray padding.
type dispersedSectorSource struct {
	descriptorAt func(lba uint32) ([]byte, bool)
	fileExtents  []fileExtent // sorted by LBA
	gray         []bool       // per-sector: render gray (zeros) not picture
	grayBefore   uint32       // [0, grayBefore) without a descriptor: zeros
	grayFrom     uint32       // [grayFrom, N) without a descriptor: zeros
	files        map[int]*os.File
	// readFileContent serves bytes for synthetic (path-less) files; may be nil.
	readFileContent func(fileIndex int, offset int64, buf []byte) error
	// bootInfo, when non-nil, patches a Boot Information Table into bytes
	// 8..55 of that file's first sector (the x86 legacy BIOS boot image).
	bootInfo *bootInfoTablePatch
}

type bootInfoTablePatch struct {
	fileIndex int
	lba       uint32
	size      int64
}

func (s *dispersedSectorSource) Content(lba uint32) ([]byte, bool, error) {
	if s.descriptorAt != nil {
		if data, ok := s.descriptorAt(lba); ok {
			return data, true, nil
		}
	}
	if lba < s.grayBefore || lba >= s.grayFrom {
		return make([]byte, 2048), true, nil
	}
	// Binary search the file extents for lba.
	lo, hi := 0, len(s.fileExtents)
	for lo < hi {
		mid := (lo + hi) / 2
		if s.fileExtents[mid].lba <= lba {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	// lo is the first extent with lba > target; the candidate is lo-1.
	if lo > 0 {
		ext := s.fileExtents[lo-1]
		if lba < ext.lba+ext.sectors {
			sectorInExtent := lba - ext.lba
			data := make([]byte, 2048)
			offset := ext.offset + int64(sectorInExtent)*2048
			if ext.filePath == "" {
				// Synthetic content (e.g. the boot image): from the provider.
				if s.readFileContent == nil {
					return data, true, nil
				}
				if err := s.readFileContent(ext.fileIndex, offset, data); err != nil {
					return nil, false, err
				}
			} else {
				file, err := s.openFile(ext)
				if err != nil {
					return nil, false, err
				}
				n, err := file.ReadAt(data, offset)
				if err != nil && err != io.EOF {
					return nil, false, err
				}
				if n < 2048 && err != io.EOF {
					return nil, false, fmt.Errorf("short read on %s", ext.filePath)
				}
			}
			if s.bootInfo != nil && ext.fileIndex == s.bootInfo.fileIndex && offset == 0 {
				iso.PatchBootInfoTable(data, s.bootInfo.lba, s.bootInfo.size)
			}
			return data, true, nil
		}
	}
	if s.gray != nil && lba < uint32(len(s.gray)) && s.gray[lba] {
		return make([]byte, 2048), true, nil
	}
	return nil, false, nil
}

func (s *dispersedSectorSource) openFile(ext fileExtent) (*os.File, error) {
	if ext.fileIndex < 0 {
		return nil, fmt.Errorf("extent without a file")
	}
	if f, ok := s.files[ext.fileIndex]; ok {
		return f, nil
	}
	f, err := os.Open(ext.filePath)
	if err != nil {
		return nil, err
	}
	if s.files == nil {
		s.files = make(map[int]*os.File)
	}
	s.files[ext.fileIndex] = f
	return f, nil
}

func (s *dispersedSectorSource) Close() error {
	var firstErr error
	for _, f := range s.files {
		if err := f.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// writeDispersedIso writes the ISO: picture everywhere, except data sectors
// (descriptors, files, surplus gray) which carry their content.
func writeDispersedIso(sampler *payloadImageSampler, output io.Writer, options multiBlockSolveOptions,
	source sectorByteSource, progress func(fastDvdStreamProgress), ctx context.Context) (fastDvdStreamSummary, error) {
	if err := fastDispersionValidateStream(output, options); err != nil {
		return fastDvdStreamSummary{}, err
	}
	totalBlocks := int(options.FillSectors / dvdEccSectorCount)
	totalBytes := int64(options.FillSectors) * dvdPayloadBytesPerSector
	var bytesWritten int64
	var controlledWords int64
	var compatibilityChanges int64
	stopwatchStart := time.Now()
	payloads := make([]byte, dvdPayloadBytesPerBlock)
	for blockIndex := 0; blockIndex < totalBlocks; blockIndex++ {
		if err := ctx.Err(); err != nil {
			return fastDvdStreamSummary{}, err
		}
		lba := options.StartLba + uint32(blockIndex)*dvdEccSectorCount
		// Any picture sector in this block?
		hasPicture := false
		for k := uint32(0); k < dvdEccSectorCount; k++ {
			if _, ok, err := source.Content(lba + k); err != nil {
				return fastDvdStreamSummary{}, err
			} else if !ok {
				hasPicture = true
				break
			}
		}
		if hasPicture {
			block, err := generateFastBlock(blockIndex, sampler, options, payloads)
			if err != nil {
				return fastDvdStreamSummary{}, err
			}
			// Overwrite data sectors with their content.
			for k := uint32(0); k < dvdEccSectorCount; k++ {
				data, ok, err := source.Content(lba + k)
				if err != nil {
					return fastDvdStreamSummary{}, err
				}
				if ok {
					copy(block.payloads[k*dvdPayloadBytesPerSector:(k+1)*dvdPayloadBytesPerSector], data)
				}
			}
			controlledWords += int64(block.controlledWords)
			compatibilityChanges += int64(block.compatibilityChanges)
			if _, err := output.Write(block.payloads); err != nil {
				return fastDvdStreamSummary{}, err
			}
			bytesWritten += int64(len(block.payloads))
		} else {
			// Whole block is data: write the 16 sectors' content directly.
			for k := uint32(0); k < dvdEccSectorCount; k++ {
				data, ok, err := source.Content(lba + k)
				if err != nil {
					return fastDvdStreamSummary{}, err
				}
				if !ok {
					return fastDvdStreamSummary{}, fmt.Errorf("internal: expected data at LBA %d", lba+k)
				}
				if _, err := output.Write(data); err != nil {
					return fastDvdStreamSummary{}, err
				}
			}
			bytesWritten += dvdPayloadBytesPerBlock
		}
		if progress != nil {
			progress(fastDvdStreamProgress{
				CompletedBlocks: blockIndex + 1,
				TotalBlocks:     totalBlocks,
				BlockLba:        lba,
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
