package dvd

import "encoding/binary"

// DvdDataFrameBuilder port.
const (
	dvdDataFramePayloadBytes = 2048
	dvdDataFrameFrameBytes   = 2064
	dvdDataFrameRows         = 12
	dvdDataFrameDataColumns  = 172
)

var dvdScramblerPresets = []uint16{
	0x0001, 0x5500, 0x0002, 0x2A00,
	0x0004, 0x5400, 0x0008, 0x2800,
	0x0010, 0x5000, 0x0020, 0x2001,
	0x0040, 0x4002, 0x0080, 0x0005,
}

var dvdPayloadScramblerMasks = buildPayloadScramblerMasks()

func buildPayloadScramblerMasks() [][]byte {
	masks := make([][]byte, len(dvdScramblerPresets))
	for presetIndex := 0; presetIndex < len(dvdScramblerPresets); presetIndex++ {
		mask := make([]byte, dvdDataFramePayloadBytes)
		scrambler := dvdScramblerPresets[presetIndex]
		for index := 0; index < len(mask); index++ {
			mask[index] = byte(scrambler)
			for shift := 0; shift < 8; shift++ {
				feedback := ((scrambler >> 14) ^ (scrambler >> 10)) & 1
				scrambler = uint16(((scrambler << 1) | feedback) & 0x7FFF)
			}
		}
		masks[presetIndex] = mask
	}
	return masks
}

func buildScrambledFrame(payload []byte, physicalSectorNumber uint32) ([]byte, error) {
	frame, err := buildDataFrame(payload, physicalSectorNumber)
	if err != nil {
		return nil, err
	}
	if err := scrambleMainData(frame, physicalSectorNumber); err != nil {
		return nil, err
	}
	return frame, nil
}

func buildDataFrame(payload []byte, physicalSectorNumber uint32) ([]byte, error) {
	if len(payload) != dvdDataFramePayloadBytes {
		return nil, newArgumentErrorParam("Payload must contain exactly 2048 bytes.", "payload")
	}
	if physicalSectorNumber > 0x00FFFFFF {
		return nil, &argumentOutOfRangeError{param: "physicalSectorNumber", msg: "DVD PSN is a 24-bit value."}
	}
	frame := make([]byte, dvdDataFrameFrameBytes)
	frame[0] = 0x00 // Single-layer Data Zone Sector Information.
	frame[1] = byte(physicalSectorNumber >> 16)
	frame[2] = byte(physicalSectorNumber >> 8)
	frame[3] = byte(physicalSectorNumber)
	rsAppendIed(frame[0:4], frame[4:6])
	// frame[6:12] stays zero: DVD-R RSV default.
	copy(frame[12:12+dvdDataFramePayloadBytes], payload)
	edc := computeEdc(frame[0:2060])
	binary.BigEndian.PutUint32(frame[2060:2064], edc)
	return frame, nil
}

func scrambleMainData(frame []byte, physicalSectorNumber uint32) error {
	if len(frame) != dvdDataFrameFrameBytes {
		return newArgumentErrorParam("Frame must contain exactly 2064 bytes.", "frame")
	}
	return transformPayloadScrambling(frame[12:12+dvdDataFramePayloadBytes], physicalSectorNumber)
}

func transformPayloadScrambling(payload []byte, physicalSectorNumber uint32) error {
	if len(payload) != dvdDataFramePayloadBytes {
		return newArgumentErrorParam("Payload must contain exactly 2048 bytes.", "payload")
	}
	mask := payloadScramblerMask(physicalSectorNumber)
	for index := range payload {
		payload[index] ^= mask[index]
	}
	return nil
}

func payloadScramblerMask(physicalSectorNumber uint32) []byte {
	return dvdPayloadScramblerMasks[(physicalSectorNumber>>4)&0xF]
}

// computeEdc ports DvdDataFrameBuilder.ComputeEdc (ECMA-267 16.4).
func computeEdc(data []byte) uint32 {
	const polynomialWithoutTopBit = 0x80000011
	var remainder uint32
	for _, value := range data {
		for bit := 7; bit >= 0; bit-- {
			input := uint32((value >> uint(bit)) & 1)
			feedback := (remainder >> 31) ^ input
			remainder <<= 1
			if feedback != 0 {
				remainder ^= polynomialWithoutTopBit
			}
		}
	}
	return remainder
}

func hasValidEdc(frame []byte) bool {
	if len(frame) != dvdDataFrameFrameBytes {
		return false
	}
	expected := binary.BigEndian.Uint32(frame[len(frame)-4:])
	return expected == computeEdc(frame[:len(frame)-4])
}
