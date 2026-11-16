package bluray

// Recording frame constants (ISO/IEC 30190:2021, 13.15..13.18).
const (
	DcControlBitsPerFrame     = 28
	ModulatedDataBitsPerFrame = 1268
	ChannelBitsPerFrame       = 1932
)

var syncSequence = [31]byte{0, 1, 2, 3, 3, 1, 4, 1, 5, 5, 4, 3, 4, 6, 6, 3, 5, 3, 2, 2, 5, 6, 5, 1, 1, 6, 2, 6, 4, 4, 2}
var syncSignatures = [7]byte{0b000001, 0b010010, 0b101000, 0b100001, 0b000100, 0b001001, 0b010000}

// RecordingFrameResult ports BluRayRecordingFrameResult.
type RecordingFrameResult struct {
	ModulationBits  []byte
	ChannelLevels   []byte
	UsedTermination bool
}

// FinalChannelLevel ports the C# property.
func (r *RecordingFrameResult) FinalChannelLevel() byte {
	return r.ChannelLevels[len(r.ChannelLevels)-1]
}

// DigitalSum ports the C# property.
func (r *RecordingFrameResult) DigitalSum() int {
	sum := 0
	for _, level := range r.ChannelLevels {
		if level == 0 {
			sum--
		} else {
			sum++
		}
	}
	return sum
}

// EncodeRecordingFrame ports BluRayRecordingFrameEncoder.Encode: recording-
// frame assembly and sync/NRZI conversion. It accepts explicit dc-control
// bits; it does not select them or construct RUB run-in/run-out.
func EncodeRecordingFrame(eccFrame []byte, frameWithinAddressUnit int,
	dcControlBits []byte, precedingFrameUsedTermination bool, initialChannelLevel byte) (*RecordingFrameResult, error) {
	sync, err := CreateFrameSync(frameWithinAddressUnit, precedingFrameUsedTermination)
	if err != nil {
		return nil, err
	}
	data, err := CreateDataBits(eccFrame, dcControlBits)
	if err != nil {
		return nil, err
	}
	encoded, err := Encode17PP(data, sync[len(sync)-1])
	if err != nil {
		return nil, err
	}
	modulation := make([]byte, ChannelBitsPerFrame)
	copy(modulation, sync)
	copy(modulation[len(sync):], encoded.ModulationBits)
	levels, err := ToChannelLevels(modulation, initialChannelLevel)
	if err != nil {
		return nil, err
	}
	return &RecordingFrameResult{
		ModulationBits:  modulation,
		ChannelLevels:   levels,
		UsedTermination: encoded.UsedTermination,
	}, nil
}

// CreateDataBits inserts 28 caller-selected dc-control bits after the initial
// 25 and subsequent 45 data bits.
func CreateDataBits(eccFrame []byte, dcControlBits []byte) ([]byte, error) {
	if len(eccFrame) != EccBytesPerFrame {
		return nil, argErr("A recording frame starts with 155 ECC bytes.", "eccFrame")
	}
	if len(dcControlBits) != DcControlBitsPerFrame {
		return nil, argErr("Supply exactly 28 dc-control bits.", "dcControlBits")
	}
	for _, bit := range dcControlBits {
		if bit > 1 {
			return nil, argErr("dc-control bits must be 0 or 1.", "dcControlBits")
		}
	}
	result := make([]byte, ModulatedDataBitsPerFrame)
	input := 0
	output := 0
	for block := 0; block < DcControlBitsPerFrame; block++ {
		count := 45
		if block == 0 {
			count = 25
		}
		for bit := 0; bit < count; bit++ {
			result[output] = (eccFrame[input/8] >> (7 - input%8)) & 1
			output++
			input++
		}
		result[output] = dcControlBits[block]
		output++
	}
	return result, nil
}

// CreateFrameSync creates the 30-bit sync at frame index 0..30, including the
// previous-frame termination flag.
func CreateFrameSync(frameWithinAddressUnit int, precedingFrameUsedTermination bool) ([]byte, error) {
	if frameWithinAddressUnit < 0 || frameWithinAddressUnit > 30 {
		return nil, argOutOfRange("frameWithinAddressUnit")
	}
	const body uint32 = 0b001010000000010000000010
	var termBit uint32
	if precedingFrameUsedTermination {
		termBit = 1 << 23
	}
	value := ((body | termBit) << 6) | uint32(syncSignatures[syncSequence[frameWithinAddressUnit]])
	result := make([]byte, 30)
	for i := range result {
		result[i] = byte((value >> (29 - i)) & 1)
	}
	return result, nil
}
