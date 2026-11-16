package cd

import (
	"context"
	"runtime"
	"sync"
)

// pipelineSectorsPerBlock matches the C# block size.
const pipelineSectorsPerBlock = 32

// runBlockPipeline ports CdBlockPipeline.Run: bounded parallel block fills
// with ordered, caller-goroutine consumption.
func runBlockPipeline(ctx context.Context, sectorCount int64, bytesPerSector int,
	fillBlock func(firstSector int64, buffer []byte, sectors int) error,
	consumeBlock func(firstSector int64, buffer []byte, sectors int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sectorCount == 0 {
		return nil
	}
	workers := runtime.NumCPU() - 1
	if workers < 1 {
		workers = 1
	}
	if workers > 16 {
		workers = 16
	}
	blockCount := int64(1) + (sectorCount-1)/pipelineSectorsPerBlock
	capacity := int64(workers) * 2
	if capacity > blockCount {
		capacity = blockCount
	}
	bufferLength := int64(pipelineSectorsPerBlock) * int64(bytesPerSector)
	if int64(sectorCount) < pipelineSectorsPerBlock {
		bufferLength = sectorCount * int64(bytesPerSector)
	}
	buffers := make([][]byte, capacity)
	for i := range buffers {
		buffers[i] = make([]byte, bufferLength)
	}

	for firstSector := int64(0); firstSector < sectorCount; {
		if err := ctx.Err(); err != nil {
			return err
		}
		batchStart := firstSector
		count := int64(1) + (sectorCount-firstSector-1)/pipelineSectorsPerBlock
		if count > capacity {
			count = capacity
		}

		var wg sync.WaitGroup
		errs := make([]error, count)
		for index := int64(0); index < count; index++ {
			wg.Add(1)
			go func(index int64) {
				defer wg.Done()
				if ctx.Err() != nil {
					errs[index] = ctx.Err()
					return
				}
				start := batchStart + index*pipelineSectorsPerBlock
				sectors := int64(pipelineSectorsPerBlock)
				if remaining := sectorCount - start; remaining < sectors {
					sectors = remaining
				}
				errs[index] = fillBlock(start, buffers[index], int(sectors))
			}(index)
		}
		wg.Wait()
		for index := int64(0); index < count; index++ {
			if errs[index] != nil {
				return errs[index]
			}
		}

		for index := int64(0); index < count; index++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			start := batchStart + index*pipelineSectorsPerBlock
			sectors := int64(pipelineSectorsPerBlock)
			if remaining := sectorCount - start; remaining < sectors {
				sectors = remaining
			}
			if err := consumeBlock(start, buffers[index], int(sectors)); err != nil {
				return err
			}
		}
		firstSector += count * pipelineSectorsPerBlock
	}
	return nil
}
