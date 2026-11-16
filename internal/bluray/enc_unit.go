package bluray

import (
	"context"
	"errors"
)

// Recording-unit channel-bit counts (ISO/IEC 30190:2021, Clauses 13 and 14).
const (
	RunInChannelBits           = 2760
	PhysicalClusterChannelBits = 496 * ChannelBitsPerFrame
	RunOutChannelBits          = 1104
	RecordingUnitChannelBits   = RunInChannelBits + PhysicalClusterChannelBits + RunOutChannelBits
	Guard3ChannelBits          = 540
)

var guardPattern = [20]byte{0, 1, 0, 0, 1, 0, 0, 1, 0, 1, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0}

// RecordingUnitResult ports BluRayRecordingUnitResult.
type RecordingUnitResult struct {
	FirstPhysicalSectorNumber    uint32
	ModulationBits               []byte
	ChannelLevels                []byte
	DcControlBits                []byte
	IncludesGuard3               bool
	FinalDigitalSum              int64
	SuboptimalDcControlDecisions int
}

// FinalChannelLevel ports the C# property.
func (r *RecordingUnitResult) FinalChannelLevel() byte {
	return r.ChannelLevels[len(r.ChannelLevels)-1]
}

// EncodeRecordingUnit ports BluRayRecordingUnitEncoder.Encode: a diagnostic
// BD-R recording-unit simulation including nominal guard/preamble/postamble
// patterns and the deterministic greedy dc-control policy. The result is one
// byte per channel bit, not a drive command, sector image or optical
// reflectance model.
func EncodeRecordingUnit(userData []byte, firstPhysicalSectorNumber uint32,
	addressFlags, userControlData []byte, appendGuard3 bool, initialChannelLevel byte,
	initialDigitalSum int64, ctx context.Context) (*RecordingUnitResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ecc, err := EccEncode(userData, firstPhysicalSectorNumber, addressFlags, userControlData)
	if err != nil {
		return nil, err
	}
	return EncodeRecordingUnitEccCluster(ecc, firstPhysicalSectorNumber, appendGuard3,
		initialChannelLevel, initialDigitalSum, ctx)
}

// EncodeRecordingUnitEccCluster ports BluRayRecordingUnitEncoder.EncodeEccCluster.
func EncodeRecordingUnitEccCluster(eccCluster []byte, firstPhysicalSectorNumber uint32,
	appendGuard3 bool, initialChannelLevel byte, initialDigitalSum int64,
	ctx context.Context) (*RecordingUnitResult, error) {
	if len(eccCluster) != EccClusterBytes {
		return nil, argErr("An ECC cluster contains 496 rows of 155 bytes.", "eccCluster")
	}
	if (firstPhysicalSectorNumber&31) != 0 || firstPhysicalSectorNumber > 0x07ffffe0 {
		return nil, argOutOfRange("firstPhysicalSectorNumber")
	}
	if initialChannelLevel > 1 {
		return nil, argOutOfRange("initialChannelLevel")
	}
	if err := validateInitialDigitalSum(initialDigitalSum); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	length := RecordingUnitChannelBits
	if appendGuard3 {
		length += Guard3ChannelBits
	}
	modulation := make([]byte, length)
	levels := make([]byte, length)
	controls := make([]byte, 496*28)
	cursor := 0
	level := initialChannelLevel
	sum := initialDigitalSum

	appendBits := func(bits []byte) {
		for _, transition := range bits {
			modulation[cursor] = transition
			level ^= transition
			levels[cursor] = level
			cursor++
			if level == 0 {
				sum--
			} else {
				sum++
			}
		}
	}
	appendGuard := func(repeats int) {
		for i := 0; i < repeats; i++ {
			appendBits(guardPattern[:])
		}
	}

	appendGuard(55) // Guard_1
	appendGuard(77) // PrA, first 1540 channel bits
	sync4, err := CreateFrameSync(6, false)
	if err != nil {
		return nil, err
	}
	appendBits(sync4) // FS4
	appendGuard(2)
	sync6, err := CreateFrameSync(13, false)
	if err != nil {
		return nil, err
	}
	appendBits(sync6) // FS6
	appendGuard(1)
	previousTermination := false
	terminationFlags := make([]bool, 496)
	startingDigitalSums := make([]int64, 496)
	for frame := 0; frame < 496; frame++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var next []byte
		if frame != 495 {
			next = eccCluster[(frame+1)*155 : (frame+1)*155+155]
		}
		startingDigitalSums[frame] = sum
		terminationFlags[frame] = previousTermination
		encoded, err := GreedyDcControlEncode(eccCluster[frame*155:frame*155+155],
			frame%31, next, previousTermination, level, sum)
		if err != nil {
			return nil, err
		}
		copy(controls[frame*28:], encoded.DcControlBits)
		appendBits(encoded.Frame.ModulationBits)
		previousTermination = encoded.Frame.UsedTermination
	}
	finalSync, err := CreateFrameSync(0, previousTermination)
	if err != nil {
		return nil, err
	}
	appendBits(finalSync)
	stopPattern := make([]byte, 54)
	for i := 1; i < len(stopPattern); i += 9 {
		stopPattern[i] = 1
	}
	appendBits(stopPattern)
	appendGuard(24) // PoA remainder
	appendGuard(27) // Guard_2
	if appendGuard3 {
		appendGuard(27)
	}
	if cursor != len(modulation) {
		return nil, errors.New("Incorrect recording-unit length.")
	}
	suboptimalDecisions := 0
	for frame := 0; frame < 496; frame++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var next, nextControls []byte
		if frame != 495 {
			next = eccCluster[(frame+1)*155 : (frame+1)*155+155]
			nextControls = controls[(frame+1)*28 : (frame+1)*28+28]
		}
		count, err := CountSuboptimalDecisions(eccCluster[frame*155:frame*155+155],
			frame%31, controls[frame*28:frame*28+28], next, nextControls, terminationFlags[frame],
			levels[RunInChannelBits+frame*1932-1], startingDigitalSums[frame])
		if err != nil {
			return nil, err
		}
		suboptimalDecisions += count
	}
	return &RecordingUnitResult{
		FirstPhysicalSectorNumber:    firstPhysicalSectorNumber,
		ModulationBits:               modulation,
		ChannelLevels:                levels,
		DcControlBits:                controls,
		IncludesGuard3:               appendGuard3,
		FinalDigitalSum:              sum,
		SuboptimalDcControlDecisions: suboptimalDecisions,
	}, nil
}
