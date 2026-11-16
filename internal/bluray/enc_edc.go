package bluray

import (
	"encoding/binary"
)

// EDC sizes (BD-R data-frame EDC, ISO/IEC 30190:2021, 13.2 and 13.3).
const (
	EdcUserDataBytes  = 2048
	EdcDataFrameBytes = 2052
)

const edcPolynomial uint32 = 0x80000011

var edcTable = buildEdcTable()

// EdcCompute is the MSB-first polynomial remainder; initial value and final
// xor are zero.
func EdcCompute(data []byte) uint32 {
	var remainder uint32
	for _, value := range data {
		remainder = (remainder << 8) ^ edcTable[byte(remainder>>24)^value]
	}
	return remainder
}

// WriteDataFrame ports BluRayEdc.WriteDataFrame.
func WriteDataFrame(userData, destination []byte) error {
	if len(userData) != EdcUserDataBytes {
		return argErr("A BD user-data frame contains exactly 2048 bytes.", "userData")
	}
	if len(destination) != EdcDataFrameBytes {
		return argErr("A BD data frame contains exactly 2052 bytes.", "destination")
	}
	edc := EdcCompute(userData)
	copy(destination, userData)
	binary.BigEndian.PutUint32(destination[EdcUserDataBytes:], edc)
	return nil
}

func buildEdcTable() [256]uint32 {
	var table [256]uint32
	for i := 0; i < 256; i++ {
		value := uint32(i) << 24
		for bit := 0; bit < 8; bit++ {
			top := value & 0x80000000
			value <<= 1
			if top != 0 {
				value ^= edcPolynomial
			}
		}
		table[i] = value
	}
	return table
}
