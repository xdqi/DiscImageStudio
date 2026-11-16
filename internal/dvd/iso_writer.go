package dvd

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// isoWriteSummary ports IsoWriteSummary.
type isoWriteSummary struct {
	Mode               string
	VolumeSectors      uint32
	LogicalSectorBytes int
	PayloadLba         uint32
	PayloadSectorCount int
	VisibleFileLba     *uint32
	SparseFile         bool
	TemplatePath       *string
}

const (
	isoLogicalSectorBytes         = 2048
	isoPayloadSectorCount         = 16
	isoPrimaryVolumeDescriptorLba = 16
	isoTerminatorLba              = 17
)

func isoGetTemplateSectorCount(templatePath string) (uint32, error) {
	length, err := fileLength(templatePath)
	if err != nil {
		return 0, err
	}
	if length == 0 || length%isoLogicalSectorBytes != 0 {
		return 0, newArgumentError("ISO template size must be a non-zero multiple of 2048 bytes.")
	}
	sectors := length / isoLogicalSectorBytes
	if sectors > 0xFFFFFFFF {
		return 0, &argumentOutOfRangeError{param: "templatePath", msg: "ISO template has too many sectors."}
	}
	return uint32(sectors), nil
}

// isoPatchTemplate ports IsoImageWriter.PatchTemplate.
func isoPatchTemplate(templatePath, outputPath string, payloads []byte, payloadLba uint32) (*isoWriteSummary, error) {
	if err := isoValidatePayload(payloads); err != nil {
		return nil, err
	}
	templateFullPath := fullPath(templatePath)
	outputFullPath := fullPath(outputPath)
	if pathEquals(templateFullPath, outputFullPath) {
		return nil, newArgumentError("--iso-template and --iso-output must be different files.")
	}

	volumeSectors, err := isoGetTemplateSectorCount(templateFullPath)
	if err != nil {
		return nil, err
	}
	if err := isoValidatePayloadRange(payloadLba, volumeSectors, false); err != nil {
		return nil, err
	}
	temporaryPath, err := isoCreateTemporaryOutputPath(outputFullPath)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			isoTryDelete(temporaryPath)
		}
	}()
	if err := isoCopyFile(templateFullPath, temporaryPath); err != nil {
		return nil, err
	}
	{
		output, err := os.OpenFile(temporaryPath, os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		_, err = output.WriteAt(payloads, int64(payloadLba)*isoLogicalSectorBytes)
		if err == nil {
			err = output.Sync()
		}
		closeErr := output.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if err := os.Rename(temporaryPath, outputFullPath); err != nil {
		return nil, err
	}
	committed = true
	return &isoWriteSummary{
		Mode:               "template",
		VolumeSectors:      volumeSectors,
		LogicalSectorBytes: isoLogicalSectorBytes,
		PayloadLba:         payloadLba,
		PayloadSectorCount: isoPayloadSectorCount,
		VisibleFileLba:     nil,
		SparseFile:         false,
		TemplatePath:       &templateFullPath,
	}, nil
}

// isoCreateMinimal ports IsoImageWriter.CreateMinimal.
func isoCreateMinimal(outputPath string, payloads []byte, payloadLba, volumeSectors uint32, volumeLabel string) (*isoWriteSummary, error) {
	if err := isoValidatePayload(payloads); err != nil {
		return nil, err
	}
	if err := isoValidatePayloadRange(payloadLba, volumeSectors, true); err != nil {
		return nil, err
	}
	normalizedLabel := isoNormalizeIdentifier(volumeLabel, 32, "DVD_IMAGE")

	reserved := map[uint32]bool{isoPrimaryVolumeDescriptorLba: true, isoTerminatorLba: true}
	for sector := payloadLba; uint64(sector) < uint64(payloadLba)+isoPayloadSectorCount; sector++ {
		reserved[sector] = true
	}

	typeLPathTableLba, err := isoFindFreeSector(volumeSectors, reserved)
	if err != nil {
		return nil, err
	}
	reserved[typeLPathTableLba] = true
	typeMPathTableLba, err := isoFindFreeSector(volumeSectors, reserved)
	if err != nil {
		return nil, err
	}
	reserved[typeMPathTableLba] = true
	rootDirectoryLba, err := isoFindFreeSector(volumeSectors, reserved)
	if err != nil {
		return nil, err
	}
	reserved[rootDirectoryLba] = true

	var visibleFileLba uint32
	if payloadLba >= isoPrimaryVolumeDescriptorLba {
		visibleFileLba = payloadLba
	} else {
		visibleFileLba, err = isoFindFreeExtent(volumeSectors, isoPayloadSectorCount, reserved)
		if err != nil {
			return nil, err
		}
	}
	outputFullPath := fullPath(outputPath)
	temporaryPath, err := isoCreateTemporaryOutputPath(outputFullPath)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			isoTryDelete(temporaryPath)
		}
	}()
	sparse := false
	{
		output, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o666)
		if err != nil {
			return nil, err
		}
		sparse = isoTryMarkSparse()
		if err := output.Truncate(int64(volumeSectors) * isoLogicalSectorBytes); err != nil {
			output.Close()
			return nil, err
		}
		timestamp := time.Now().UTC()
		if err := isoWriteSector(output, isoPrimaryVolumeDescriptorLba, isoBuildPrimaryVolumeDescriptor(
			volumeSectors, normalizedLabel, typeLPathTableLba, typeMPathTableLba, rootDirectoryLba, timestamp)); err != nil {
			output.Close()
			return nil, err
		}
		if err := isoWriteSector(output, isoTerminatorLba, isoBuildTerminator()); err != nil {
			output.Close()
			return nil, err
		}
		if err := isoWriteSector(output, typeLPathTableLba, isoBuildPathTable(rootDirectoryLba, true)); err != nil {
			output.Close()
			return nil, err
		}
		if err := isoWriteSector(output, typeMPathTableLba, isoBuildPathTable(rootDirectoryLba, false)); err != nil {
			output.Close()
			return nil, err
		}
		if err := isoWriteSector(output, rootDirectoryLba, isoBuildRootDirectory(rootDirectoryLba, visibleFileLba, timestamp)); err != nil {
			output.Close()
			return nil, err
		}
		if err := isoWritePayload(output, payloadLba, payloads); err != nil {
			output.Close()
			return nil, err
		}
		if visibleFileLba != payloadLba {
			if err := isoWritePayload(output, visibleFileLba, payloads); err != nil {
				output.Close()
				return nil, err
			}
		}
		if err := output.Sync(); err != nil {
			output.Close()
			return nil, err
		}
		if err := output.Close(); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(temporaryPath, outputFullPath); err != nil {
		return nil, err
	}
	committed = true
	return &isoWriteSummary{
		Mode:               "minimal-iso9660",
		VolumeSectors:      volumeSectors,
		LogicalSectorBytes: isoLogicalSectorBytes,
		PayloadLba:         payloadLba,
		PayloadSectorCount: isoPayloadSectorCount,
		VisibleFileLba:     &visibleFileLba,
		SparseFile:         sparse,
		TemplatePath:       nil,
	}, nil
}

func isoBuildPrimaryVolumeDescriptor(volumeSectors uint32, volumeLabel string, typeLPathTableLba, typeMPathTableLba, rootDirectoryLba uint32, timestamp time.Time) []byte {
	sector := make([]byte, isoLogicalSectorBytes)
	sector[0] = 1
	copy(sector[1:], "CD001")
	sector[6] = 1
	isoWritePaddedAscii(sector, 8, 32, "DVDIMAGE")
	isoWritePaddedAscii(sector, 40, 32, volumeLabel)
	isoWriteBothEndian32(sector, 80, volumeSectors)
	isoWriteBothEndian16(sector, 120, 1)
	isoWriteBothEndian16(sector, 124, 1)
	isoWriteBothEndian16(sector, 128, isoLogicalSectorBytes)
	isoWriteBothEndian32(sector, 132, 10)
	binary.LittleEndian.PutUint32(sector[140:144], typeLPathTableLba)
	binary.BigEndian.PutUint32(sector[148:152], typeMPathTableLba)
	copy(sector[156:], isoBuildDirectoryRecord(rootDirectoryLba, isoLogicalSectorBytes, true, []byte{0}, timestamp))
	isoWritePaddedAscii(sector, 190, 128, volumeLabel)
	isoWritePaddedAscii(sector, 318, 128, "")
	isoWritePaddedAscii(sector, 446, 128, "DVD IMAGE SOLVER")
	isoWritePaddedAscii(sector, 574, 128, "DVD IMAGE SOLVER")
	isoWritePaddedAscii(sector, 702, 37, "")
	isoWritePaddedAscii(sector, 739, 37, "")
	isoWritePaddedAscii(sector, 776, 37, "")
	isoWriteVolumeDate(sector[813:830], timestamp)
	isoWriteVolumeDate(sector[830:847], timestamp)
	isoWriteUnspecifiedVolumeDate(sector[847:864])
	isoWriteUnspecifiedVolumeDate(sector[864:881])
	sector[881] = 1
	return sector
}

func isoBuildTerminator() []byte {
	sector := make([]byte, isoLogicalSectorBytes)
	sector[0] = 255
	copy(sector[1:], "CD001")
	sector[6] = 1
	return sector
}

func isoBuildPathTable(rootDirectoryLba uint32, littleEndian bool) []byte {
	sector := make([]byte, isoLogicalSectorBytes)
	sector[0] = 1
	if littleEndian {
		binary.LittleEndian.PutUint32(sector[2:6], rootDirectoryLba)
		binary.LittleEndian.PutUint16(sector[6:8], 1)
	} else {
		binary.BigEndian.PutUint32(sector[2:6], rootDirectoryLba)
		binary.BigEndian.PutUint16(sector[6:8], 1)
	}
	sector[8] = 0
	return sector
}

func isoBuildRootDirectory(rootDirectoryLba, visibleFileLba uint32, timestamp time.Time) []byte {
	sector := make([]byte, isoLogicalSectorBytes)
	offset := 0
	records := [][]byte{
		isoBuildDirectoryRecord(rootDirectoryLba, isoLogicalSectorBytes, true, []byte{0}, timestamp),
		isoBuildDirectoryRecord(rootDirectoryLba, isoLogicalSectorBytes, true, []byte{1}, timestamp),
		isoBuildDirectoryRecord(visibleFileLba, isoPayloadSectorCount*isoLogicalSectorBytes, false, []byte("SOLVED.BIN;1"), timestamp),
	}
	for _, record := range records {
		copy(sector[offset:], record)
		offset += len(record)
	}
	return sector
}

func isoBuildDirectoryRecord(extentLba uint32, dataLength int, isDirectory bool, identifier []byte, timestamp time.Time) []byte {
	padding := 0
	if len(identifier)%2 == 0 {
		padding = 1
	}
	length := 33 + len(identifier) + padding
	record := make([]byte, length)
	record[0] = byte(length)
	isoWriteBothEndian32(record, 2, extentLba)
	isoWriteBothEndian32(record, 10, uint32(dataLength))
	isoWriteDirectoryDate(record[18:25], timestamp)
	if isDirectory {
		record[25] = 0x02
	} else {
		record[25] = 0x00
	}
	isoWriteBothEndian16(record, 28, 1)
	record[32] = byte(len(identifier))
	copy(record[33:], identifier)
	return record
}

func isoWritePayload(output *os.File, lba uint32, payloads []byte) error {
	_, err := output.WriteAt(payloads, int64(lba)*isoLogicalSectorBytes)
	return err
}

func isoWriteSector(output *os.File, lba uint32, sector []byte) error {
	if len(sector) != isoLogicalSectorBytes {
		return newArgumentErrorParam("ISO logical sector must contain 2048 bytes.", "sector")
	}
	_, err := output.WriteAt(sector, int64(lba)*isoLogicalSectorBytes)
	return err
}

func isoWriteBothEndian16(destination []byte, offset int, value uint16) {
	binary.LittleEndian.PutUint16(destination[offset:offset+2], value)
	binary.BigEndian.PutUint16(destination[offset+2:offset+4], value)
}

func isoWriteBothEndian32(destination []byte, offset int, value uint32) {
	binary.LittleEndian.PutUint32(destination[offset:offset+4], value)
	binary.BigEndian.PutUint32(destination[offset+4:offset+8], value)
}

func isoWriteDirectoryDate(destination []byte, timestamp time.Time) {
	utc := timestamp.UTC()
	destination[0] = byte(utc.Year() - 1900)
	destination[1] = byte(utc.Month())
	destination[2] = byte(utc.Day())
	destination[3] = byte(utc.Hour())
	destination[4] = byte(utc.Minute())
	destination[5] = byte(utc.Second())
	destination[6] = 0
}

func isoWriteVolumeDate(destination []byte, timestamp time.Time) {
	utc := timestamp.UTC()
	digits := fmt.Sprintf("%04d%02d%02d%02d%02d%02d%02d",
		utc.Year(), int(utc.Month()), utc.Day(), utc.Hour(), utc.Minute(), utc.Second(), utc.Nanosecond()/10000000)
	copy(destination[:16], digits)
	destination[16] = 0
}

func isoWriteUnspecifiedVolumeDate(destination []byte) {
	for i := 0; i < 16; i++ {
		destination[i] = '0'
	}
	destination[16] = 0
}

func isoWritePaddedAscii(destination []byte, offset, length int, value string) {
	field := destination[offset : offset+length]
	for i := range field {
		field[i] = ' '
	}
	copy(field, value)
}

// isoNormalizeIdentifier ports NormalizeIdentifier (ASCII a-characters).
func isoNormalizeIdentifier(value string, maximumLength int, fallback string) string {
	var result []byte
	for _, character := range strings.ToUpper(value) {
		var normalized byte
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' {
			normalized = byte(character)
		} else {
			normalized = '_'
		}
		result = append(result, normalized)
		if len(result) == maximumLength {
			break
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return string(result)
}

func isoFindFreeSector(volumeSectors uint32, reserved map[uint32]bool) (uint32, error) {
	for sector := uint32(isoTerminatorLba + 1); sector < volumeSectors; sector++ {
		if !reserved[sector] {
			return sector, nil
		}
	}
	return 0, newArgumentError("ISO volume is too small for filesystem metadata.")
}

func isoFindFreeExtent(volumeSectors uint32, sectorCount int, reserved map[uint32]bool) (uint32, error) {
	for start := uint32(isoTerminatorLba + 1); uint64(start)+uint64(sectorCount) <= uint64(volumeSectors); start++ {
		free := true
		for sector := start; uint64(sector) < uint64(start)+uint64(sectorCount); sector++ {
			if reserved[sector] {
				free = false
				break
			}
		}
		if free {
			return start, nil
		}
	}
	return 0, newArgumentError("ISO volume is too small for the visible SOLVED.BIN extent.")
}

func isoValidatePayload(payloads []byte) error {
	if len(payloads) != isoPayloadSectorCount*isoLogicalSectorBytes {
		return newArgumentErrorParam("ISO output needs exactly 16 consecutive 2048-byte payloads.", "payloads")
	}
	return nil
}

func isoValidatePayloadRange(payloadLba, volumeSectors uint32, protectVolumeDescriptors bool) error {
	end := uint64(payloadLba) + isoPayloadSectorCount
	if end > uint64(volumeSectors) {
		return &argumentOutOfRangeError{param: "payloadLba", msg: "Solved ECC Block exceeds the ISO volume size."}
	}
	if protectVolumeDescriptors &&
		payloadLba <= isoTerminatorLba &&
		end > isoPrimaryVolumeDescriptorLba {
		return newArgumentError(
			"The solved ECC Block overlaps ISO9660 volume descriptors in sectors 16 and 17. " +
				"Use LBA 0, an LBA at or above 18, or an existing template whose structure you control.")
	}
	return nil
}

func isoCreateTemporaryOutputPath(outputFullPath string) (string, error) {
	directory := filepath.Dir(outputFullPath)
	if directory == "" {
		return "", newArgumentErrorParam("ISO output path has no parent directory.", "outputFullPath")
	}
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return "", err
	}
	return filepath.Join(directory, "."+filepath.Base(outputFullPath)+"."+newGuidN()+".tmp"), nil
}

func isoTryDelete(path string) {
	if _, err := os.Stat(path); err == nil {
		os.Remove(path)
	}
}

// isoTryMarkSparse mirrors TryMarkSparse: only supported on Windows.
func isoTryMarkSparse() bool { return false }

// isoCopyFile mirrors File.Copy(templateFullPath, temporaryPath, overwrite: true).
func isoCopyFile(source, destination string) error {
	data, err := dotnetReadAllBytes(source)
	if err != nil {
		return err
	}
	return dotnetWriteAllBytes(destination, data)
}
