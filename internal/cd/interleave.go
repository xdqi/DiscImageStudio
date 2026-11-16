// Port of CddaInterleaveTable.cs / CddaInterleaver.cs / CdAudioByteOrder.cs,
// adapted from CDImage's converter.cpp (GPL-3.0-or-later, Copyright (C)
// 2008-2022 arduinocelentano; Copyright (c) 2026 Disc Image Studio contributors).
package cd

import (
	"fmt"
	"io"
)

// CIRC delay table constants.
const (
	InterleaveDepth       = 4
	InterleaveFramesCycle = 28 * InterleaveDepth
	InterleaveBytesFrame  = 24
)

var interleaveDelayTable = buildInterleaveDelays()
var interleaveFileOffsetTable = buildInterleaveFileOffsets()

// InterleaveMaximumFileOffset is the largest file offset in the table.
var InterleaveMaximumFileOffset = func() int {
	max := 0
	for _, v := range interleaveFileOffsetTable {
		if v > max {
			max = v
		}
	}
	return max
}()

func buildInterleaveDelays() []int {
	k0Even := []int{0, 8, 16, 2, 10, 18}
	k0Odd := []int{4, 12, 20, 6, 14, 22}
	delays := make([]int, InterleaveBytesFrame)
	for index := 0; index < 12; index++ {
		odd := (index & 1) != 0
		oddAdd := 0
		if odd {
			oddAdd = 1
		}
		frameBias := 3
		if odd {
			frameBias = 2
		}
		delays[index] = oddAdd - InterleaveBytesFrame*(index*InterleaveDepth+frameBias) + k0Even[index/2]
	}
	for index := 12; index < InterleaveBytesFrame; index++ {
		odd := ((index - 12) & 1) != 0
		oddAdd := 0
		if odd {
			oddAdd = 1
		}
		frameBias := 1
		if odd {
			frameBias = 0
		}
		delays[index] = oddAdd - InterleaveBytesFrame*((index+4)*InterleaveDepth+frameBias) + k0Odd[(index-12)/2]
	}
	return delays
}

func buildInterleaveFileOffsets() []int {
	offsets := make([]int, InterleaveBytesFrame)
	for position := 0; position < InterleaveBytesFrame; position++ {
		delay := interleaveDelayTable[position]
		column := ((delay % InterleaveBytesFrame) + InterleaveBytesFrame) % InterleaveBytesFrame
		frameDelay := (column - delay) / InterleaveBytesFrame
		offsets[position] = InterleaveBytesFrame*(InterleaveFramesCycle-1) - InterleaveBytesFrame*frameDelay + column - position
	}
	return offsets
}

// InterleaveDelayFor returns the delay for a byte position within a frame.
func InterleaveDelayFor(position int) int { return interleaveDelayTable[position] }

// InterleaveFileOffsetFor returns the file offset for a byte position.
func InterleaveFileOffsetFor(position int) int { return interleaveFileOffsetTable[position] }

// AudioByteOrder selects the output byte order of 16-bit audio samples.
type AudioByteOrder int

const (
	AudioBigEndian AudioByteOrder = iota
	AudioLittleEndian
)

// SwapAudioByteOrder swaps adjacent bytes in place; length must be even.
func SwapAudioByteOrder(samples []byte) error {
	if len(samples)&1 != 0 {
		return fmt.Errorf("CD audio must contain complete 16-bit samples")
	}
	for index := 0; index < len(samples); index += 2 {
		samples[index], samples[index+1] = samples[index+1], samples[index]
	}
	return nil
}

// WriteAudioSector writes one sector, converting byte order when needed.
func WriteAudioSector(output io.Writer, sector []byte, byteOrder AudioByteOrder) error {
	if byteOrder == AudioLittleEndian {
		if err := SwapAudioByteOrder(sector); err != nil {
			return err
		}
	}
	_, err := output.Write(sector)
	return err
}

// cddaInterleaver is the streaming CIRC delay interleaver.
type cddaInterleaver struct {
	sequence       []byte
	sector         []byte
	head           int
	inputPosition  int
	sectorPosition int
	byteOrder      AudioByteOrder
}

func newCddaInterleaver(byteOrder AudioByteOrder) *cddaInterleaver {
	return &cddaInterleaver{
		sequence:  make([]byte, InterleaveFramesCycle*InterleaveBytesFrame),
		sector:    make([]byte, BytesPerSector),
		head:      InterleaveFramesCycle - 1,
		byteOrder: byteOrder,
	}
}

func (i *cddaInterleaver) mapIndex(position int) int {
	value := i.head*InterleaveBytesFrame + position
	modulus := len(i.sequence)
	if value >= modulus {
		return value - modulus
	}
	if value < 0 {
		return value + modulus
	}
	return value
}

func (i *cddaInterleaver) Add(value byte, output io.Writer) error {
	i.sequence[i.mapIndex(InterleaveDelayFor(i.inputPosition))] = value
	i.inputPosition++
	if i.inputPosition < InterleaveBytesFrame {
		return nil
	}
	return i.completeFrame(output)
}

// AddSpan adds a span of values.
func (i *cddaInterleaver) AddSpan(values []byte, output io.Writer) error {
	for len(values) > 0 {
		count := min(len(values), InterleaveBytesFrame-i.inputPosition)
		for index := 0; index < count; index++ {
			i.sequence[i.mapIndex(InterleaveDelayFor(i.inputPosition+index))] = values[index]
		}
		i.inputPosition += count
		values = values[count:]
		if i.inputPosition == InterleaveBytesFrame {
			if err := i.completeFrame(output); err != nil {
				return err
			}
		}
	}
	return nil
}

func (i *cddaInterleaver) completeFrame(output io.Writer) error {
	i.inputPosition = 0
	i.head++
	if i.head >= InterleaveFramesCycle {
		i.head = 0
	}
	copy(i.sector[i.sectorPosition:], i.sequence[i.head*InterleaveBytesFrame:i.head*InterleaveBytesFrame+InterleaveBytesFrame])
	i.sectorPosition += InterleaveBytesFrame
	if i.sectorPosition == len(i.sector) {
		if err := WriteAudioSector(output, i.sector, i.byteOrder); err != nil {
			return err
		}
		i.sectorPosition = 0
	}
	return nil
}

// Flush pads the final partial sector with zeros and writes it.
func (i *cddaInterleaver) Flush(output io.Writer) error {
	if i.sectorPosition == 0 {
		return nil
	}
	for index := i.sectorPosition; index < len(i.sector); index++ {
		i.sector[index] = 0
	}
	if err := WriteAudioSector(output, i.sector, i.byteOrder); err != nil {
		return err
	}
	i.sectorPosition = 0
	return nil
}
