package bluray

// Pp17Result ports BluRay17PpResult.
type Pp17Result struct {
	ModulationBits  []byte
	UsedTermination bool
}

// LastModulationBit ports the C# property.
func (r *Pp17Result) LastModulationBit() byte {
	if len(r.ModulationBits) == 0 {
		return 0
	}
	return r.ModulationBits[len(r.ModulationBits)-1]
}

// Encode17PP ports BluRay17PpEncoder.Encode: 17PP variable-length RLL(1,7)
// conversion, ISO/IEC 30190:2021, 13.17.2, Figure 41. It encodes an even
// number of MSB-first data bits (each byte is 0 or 1). The previous
// modulation bit supplies the context for data pair 11. The caller supplies
// dc-control bits and frame-sync boundaries.
func Encode17PP(dataBits []byte, previousModulationBit byte) (*Pp17Result, error) {
	if len(dataBits)&1 != 0 {
		return nil, argErr("17PP consumes complete pairs of data bits.", "dataBits")
	}
	if previousModulationBit > 1 {
		return nil, argOutOfRange("previousModulationBit")
	}
	for _, bit := range dataBits {
		if bit > 1 {
			return nil, argErr("Data bits must be represented as 0 or 1.", "dataBits")
		}
	}

	result := make([]byte, len(dataBits)/2*3)
	source := 0
	output := 0
	terminated := false
	for source < len(dataBits) {
		var word pp17Codeword
		// Figure 41's substitution breaks long repetitions of minimum run
		// lengths. Its following 010 prefix is unchanged by the substituted
		// word's final bit.
		if len(dataBits)-source >= 8 && pp17ReadBits(dataBits[source:source+6]) == 0b110111 &&
			pp17BeginsWith010(pp17ConvertNext(dataBits[source+6:], 1)) {
			word = pp17Codeword{dataLength: 6, channelValue: 0b001000000, isTerminating: false}
		} else {
			word = pp17ConvertNext(dataBits[source:], previousModulationBit)
		}

		channelLength := word.dataLength / 2 * 3
		for bit := channelLength - 1; bit >= 0; bit-- {
			result[output] = byte((word.channelValue >> bit) & 1)
			output++
		}
		previousModulationBit = result[output-1]
		source += word.dataLength
		terminated = word.isTerminating
	}
	return &Pp17Result{ModulationBits: result, UsedTermination: terminated}, nil
}

// ToChannelLevels is the NRZI conversion; a modulation ONE toggles the
// recorded level.
func ToChannelLevels(modulationBits []byte, initialLevel byte) ([]byte, error) {
	if initialLevel > 1 {
		return nil, argOutOfRange("initialLevel")
	}
	result := make([]byte, len(modulationBits))
	level := initialLevel
	for i := range result {
		if modulationBits[i] > 1 {
			return nil, argErr("Modulation bits must be represented as 0 or 1.", "modulationBits")
		}
		level ^= modulationBits[i]
		result[i] = level
	}
	return result, nil
}

type pp17Codeword struct {
	dataLength    int
	channelValue  int
	isTerminating bool
}

func pp17ConvertNext(bits []byte, previous byte) pp17Codeword {
	firstPair := pp17ReadBits(bits[:2])
	switch firstPair {
	case 1:
		return pp17Codeword{2, 0b010, false}
	case 2:
		return pp17Codeword{2, 0b001, false}
	case 3:
		if previous == 1 {
			return pp17Codeword{2, 0b000, false}
		}
		return pp17Codeword{2, 0b101, false}
	}
	if len(bits) == 2 {
		return pp17Codeword{2, 0b000, true}
	}

	nextPair := pp17ReadBits(bits[2:4])
	switch nextPair {
	case 1:
		return pp17Codeword{4, 0b000100, false}
	case 2:
		return pp17Codeword{4, 0b010000, false}
	case 3:
		return pp17Codeword{4, 0b010100, false}
	}
	if len(bits) == 4 {
		return pp17Codeword{4, 0b010100, true}
	}

	thirdPair := pp17ReadBits(bits[4:6])
	if len(bits) >= 8 && pp17ReadBits(bits[6:8]) == 0 {
		if thirdPair == 0 {
			return pp17Codeword{8, 0b010100100100, false}
		}
		if thirdPair == 2 {
			return pp17Codeword{8, 0b000100100100, false}
		}
	}
	switch thirdPair {
	case 0:
		return pp17Codeword{6, 0b010100000, false}
	case 1:
		return pp17Codeword{6, 0b010100100, false}
	case 2:
		return pp17Codeword{6, 0b000100000, false}
	default:
		return pp17Codeword{6, 0b000100100, false}
	}
}

func pp17BeginsWith010(word pp17Codeword) bool {
	return (word.channelValue >> (word.dataLength/2*3 - 3)) == 0b010
}

func pp17ReadBits(bits []byte) int {
	value := 0
	for _, bit := range bits {
		value = (value << 1) | int(bit)
	}
	return value
}
