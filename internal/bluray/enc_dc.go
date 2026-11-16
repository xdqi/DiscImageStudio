package bluray

import (
	"errors"
	"math"
)

// BalancedFrameResult ports BluRayBalancedFrameResult.
type BalancedFrameResult struct {
	Frame           *RecordingFrameResult
	DcControlBits   []byte
	FinalDigitalSum int64
}

// One full RUB and frame lookahead fit comfortably in this headroom,
// including Math.Abs.
const maximumInitialDigitalSumMagnitude int64 = math.MaxInt64 - 2_000_000

func validateInitialDigitalSum(initialDigitalSum int64) error {
	if initialDigitalSum < -maximumInitialDigitalSumMagnitude || initialDigitalSum > maximumInitialDigitalSumMagnitude {
		return argOutOfRangeMsg("initialDigitalSum", "Initial digital sum is too close to Int64 limits.")
	}
	return nil
}

// GreedyDcControlEncode ports BluRayGreedyDcControlEncoder.Encode: the
// deterministic look-ahead selection of the dc-control positions in 13.17.3.
// This is a greedy diagnostic policy; it does not predict a recorder's
// proprietary balancing or write strategy.
func GreedyDcControlEncode(eccFrame []byte, frameWithinAddressUnit int,
	nextEccFrame []byte, precedingFrameUsedTermination bool, initialChannelLevel byte,
	initialDigitalSum int64) (*BalancedFrameResult, error) {
	if err := validateInitialDigitalSum(initialDigitalSum); err != nil {
		return nil, err
	}
	if len(nextEccFrame) != 0 && len(nextEccFrame) != EccBytesPerFrame {
		return nil, argErr("Next-frame lookahead requires 155 ECC bytes.", "nextEccFrame")
	}
	controls := make([]byte, DcControlBitsPerFrame)
	var selected *RecordingFrameResult
	for control := 0; control < len(controls); control++ {
		controls[control] = 0
		zero, err := EncodeRecordingFrame(eccFrame, frameWithinAddressUnit,
			controls, precedingFrameUsedTermination, initialChannelLevel)
		if err != nil {
			return nil, err
		}
		controls[control] = 1
		one, err := EncodeRecordingFrame(eccFrame, frameWithinAddressUnit,
			controls, precedingFrameUsedTermination, initialChannelLevel)
		if err != nil {
			return nil, err
		}
		scoreZero, err := scoreFrame(zero, control, nextEccFrame, nil, frameWithinAddressUnit, initialDigitalSum)
		if err != nil {
			return nil, err
		}
		scoreOne, err := scoreFrame(one, control, nextEccFrame, nil, frameWithinAddressUnit, initialDigitalSum)
		if err != nil {
			return nil, err
		}
		chooseOne := scoreOne < scoreZero
		if chooseOne {
			controls[control] = 1
			selected = one
		} else {
			controls[control] = 0
			selected = zero
		}
	}
	return &BalancedFrameResult{
		Frame:           selected,
		DcControlBits:   controls,
		FinalDigitalSum: initialDigitalSum + int64(selected.DigitalSum()),
	}, nil
}

// CountSuboptimalDecisions counts final decisions whose following-block |DSV|
// would improve by flipping just that control. This diagnoses the greedy
// policy's provisional-future limitation; zero is not a disc-conformance
// certificate.
func CountSuboptimalDecisions(eccFrame []byte, frameWithinAddressUnit int,
	dcControlBits, nextEccFrame, nextDcControlBits []byte,
	precedingFrameUsedTermination bool, initialChannelLevel byte, initialDigitalSum int64) (int, error) {
	if err := validateInitialDigitalSum(initialDigitalSum); err != nil {
		return 0, err
	}
	if (len(nextEccFrame) == 0) != (len(nextDcControlBits) == 0) ||
		(len(nextEccFrame) != 0 && (len(nextEccFrame) != 155 || len(nextDcControlBits) != 28)) {
		return 0, errors.New("Supply the next frame and all its final control bits together.")
	}
	controls := append([]byte(nil), dcControlBits...)
	actual, err := EncodeRecordingFrame(eccFrame, frameWithinAddressUnit,
		controls, precedingFrameUsedTermination, initialChannelLevel)
	if err != nil {
		return 0, err
	}
	count := 0
	for control := 0; control < len(controls); control++ {
		controls[control] ^= 1
		alternate, err := EncodeRecordingFrame(eccFrame, frameWithinAddressUnit,
			controls, precedingFrameUsedTermination, initialChannelLevel)
		if err != nil {
			return 0, err
		}
		controls[control] ^= 1
		scoreAlternate, err := scoreFrame(alternate, control, nextEccFrame, nextDcControlBits,
			frameWithinAddressUnit, initialDigitalSum)
		if err != nil {
			return 0, err
		}
		scoreActual, err := scoreFrame(actual, control, nextEccFrame, nextDcControlBits,
			frameWithinAddressUnit, initialDigitalSum)
		if err != nil {
			return 0, err
		}
		if scoreAlternate < scoreActual {
			count++
		}
	}
	return count, nil
}

func scoreFrame(frame *RecordingFrameResult, control int, nextEccFrame, nextDcControlBits []byte,
	frameWithinAddressUnit int, initialDigitalSum int64) (int64, error) {
	endpoint := (control + 2) * 69
	if endpoint > ChannelBitsPerFrame {
		endpoint = ChannelBitsPerFrame
	}
	sum := initialDigitalSum
	for _, level := range frame.ChannelLevels[:endpoint] {
		if level == 0 {
			sum--
		} else {
			sum++
		}
	}
	if control == 27 {
		var nextModulation []byte
		if len(nextEccFrame) != 0 {
			nextControls := nextDcControlBits
			if len(nextControls) == 0 {
				nextControls = make([]byte, 28)
			}
			nextData, err := CreateDataBits(nextEccFrame, nextControls)
			if err != nil {
				return 0, err
			}
			nextSync, err := CreateFrameSync((frameWithinAddressUnit+1)%31, frame.UsedTermination)
			if err != nil {
				return 0, err
			}
			encoded, err := Encode17PP(nextData, nextSync[len(nextSync)-1])
			if err != nil {
				return 0, err
			}
			nextModulation = make([]byte, 69)
			copy(nextModulation, nextSync)
			copy(nextModulation[30:], encoded.ModulationBits[:39])
		} else {
			// The last frame is followed by PoA: FS0 plus the 9T
			// stop-of-user-data pattern.
			nextModulation = make([]byte, 69)
			sync, err := CreateFrameSync(0, frame.UsedTermination)
			if err != nil {
				return 0, err
			}
			copy(nextModulation, sync)
			for i := 31; i < len(nextModulation); i += 9 {
				nextModulation[i] = 1
			}
		}
		level := frame.FinalChannelLevel()
		for _, transition := range nextModulation {
			level ^= transition
			if level == 0 {
				sum--
			} else {
				sum++
			}
		}
	}
	if sum < 0 {
		return -sum, nil
	}
	return sum, nil
}
