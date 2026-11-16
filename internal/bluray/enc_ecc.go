package bluray

import (
	"encoding/binary"
)

// ECC cluster constants (ISO/IEC 30190:2021, 13.2..13.14).
const (
	EccSectorsPerCluster = 32
	EccPayloadBytes      = EccSectorsPerCluster * EdcUserDataBytes
	EccRecordingFrames   = 496
	EccBytesPerFrame     = 155
	EccClusterBytes      = EccRecordingFrames * EccBytesPerFrame
	LdcClusterBytes      = EccRecordingFrames * 152
	BisClusterBytes      = EccRecordingFrames * 3
	EccUserControlBytes  = EccSectorsPerCluster * 18
)

// EccEncode ports BluRayEccEncoder.Encode: a BD-R user-data cluster through
// ECC multiplexing. The returned bytes precede dc-control, 17PP and RUB
// framing; they are not a burnable disc image. Nil addressFlags /
// userControlData mean all-zero flags/control.
func EccEncode(userData []byte, firstPhysicalSectorNumber uint32,
	addressFlags, userControlData []byte) ([]byte, error) {
	ldc, err := EncodeLdcCluster(userData, firstPhysicalSectorNumber)
	if err != nil {
		return nil, err
	}
	bis, err := EncodeBisCluster(firstPhysicalSectorNumber, addressFlags, userControlData)
	if err != nil {
		return nil, err
	}
	ecc := make([]byte, EccClusterBytes)
	for row := 0; row < EccRecordingFrames; row++ {
		for group := 0; group < 4; group++ {
			copy(ecc[row*155+group*39:], ldc[row*152+group*38:row*152+group*38+38])
			if group < 3 {
				ecc[row*155+group*39+38] = bis[row*3+group]
			}
		}
	}
	return ecc, nil
}

// EncodeLdcCluster returns 496 rows of 152 LDC bytes in recording order.
func EncodeLdcCluster(userData []byte, firstPhysicalSectorNumber uint32) ([]byte, error) {
	if err := validateClusterPsn(firstPhysicalSectorNumber); err != nil {
		return nil, err
	}
	if len(userData) != EccPayloadBytes {
		return nil, argErr("A BD cluster contains 65536 user-data bytes.", "userData")
	}
	dataBlock := make([]byte, 32*EdcDataFrameBytes)
	mask, err := ScramblerMask(firstPhysicalSectorNumber, EdcDataFrameBytes)
	if err != nil {
		return nil, err
	}
	for sector := 0; sector < EccSectorsPerCluster; sector++ {
		frame := dataBlock[sector*EdcDataFrameBytes : (sector+1)*EdcDataFrameBytes]
		if err := WriteDataFrame(userData[sector*EdcUserDataBytes:(sector+1)*EdcUserDataBytes], frame); err != nil {
			return nil, err
		}
		for i := range frame {
			frame[i] ^= mask[i]
		}
	}

	cluster := make([]byte, LdcClusterBytes)
	var codeword [248]byte
	for column := 0; column < 304; column++ {
		copy(codeword[:], dataBlock[column*216:column*216+216])
		if err := RsComputeParity(codeword[:216], codeword[216:]); err != nil {
			return nil, err
		}
		for row := 0; row < 248; row++ {
			outputRow := row*2 + column%2
			outputColumn := (column/2 + 152 - (row*3)%152) % 152
			cluster[outputRow*152+outputColumn] = codeword[row]
		}
	}
	return cluster, nil
}

// EncodeBisCluster returns 496 rows of 3 BIS bytes. Nil optional slices mean
// all-zero flags/control.
func EncodeBisCluster(firstPhysicalSectorNumber uint32,
	addressFlags, userControlData []byte) ([]byte, error) {
	if err := validateClusterPsn(firstPhysicalSectorNumber); err != nil {
		return nil, err
	}
	if len(addressFlags) != 0 && len(addressFlags) != 16 {
		return nil, argErr("Supply one flag byte for each of the 16 address units.", "addressFlags")
	}
	if len(userControlData) != 0 && len(userControlData) != EccUserControlBytes {
		return nil, argErr("Supply 18 control bytes for each of the 32 sectors.", "userControlData")
	}

	// Keep the access block column-major so each shortened RS word is contiguous.
	access := make([]byte, 24*30)
	var address [9]byte
	for unit := 0; unit < 16; unit++ {
		binary.BigEndian.PutUint32(address[:], firstPhysicalSectorNumber+uint32(unit*2))
		if len(addressFlags) == 0 {
			address[4] = 0
		} else {
			address[4] = addressFlags[unit]
		}
		if err := RsComputeParity(address[:5], address[5:]); err != nil {
			return nil, err
		}
		for x := 0; x < 9; x++ {
			row := 2*(x/3) + unit/8
			column := 3*((x/3+16-unit)%8) + (x-x/3)%3
			access[column*30+row] = address[x]
		}
	}
	for i := 0; i < len(userControlData); i++ {
		access[(i/24)*30+6+i%24] = userControlData[i]
	}

	cluster := make([]byte, BisClusterBytes)
	var codeword [62]byte
	for column := 0; column < 24; column++ {
		copy(codeword[:], access[column*30:column*30+30])
		if err := RsComputeParity(codeword[:30], codeword[30:]); err != nil {
			return nil, err
		}
		for row := 0; row < 62; row++ {
			unit := (row/2+8-column/3)%8 + 8*(row%2)
			unitRow := row / 2
			outputColumn := (column + unitRow) % 3
			cluster[(unit*31+unitRow)*3+outputColumn] = codeword[row]
		}
	}
	return cluster, nil
}

func validateClusterPsn(firstPhysicalSectorNumber uint32) error {
	if (firstPhysicalSectorNumber&31) != 0 || firstPhysicalSectorNumber > 0x07ffffe0 {
		return argOutOfRangeMsg("firstPhysicalSectorNumber",
			"A cluster PSN must be a multiple of 32 with reserved bits 31..27 clear.")
	}
	return nil
}
