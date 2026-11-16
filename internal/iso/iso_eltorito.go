// iso_eltorito.go implements the ISO9660 + El Torito layer of a UDF bridge
// disc: a minimal ISO9660 volume at the inner edge (sectors 16..24) that
// carries an El Torito boot catalog while UDF owns the rest of the volume.
// Sectors 19..21 belong to the UDF volume recognition sequence and are not
// emitted by this layer.
package iso

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// Fixed bridge layout (2048-byte sectors). Sectors 16 and 18 reuse the
// primaryVolumeDescriptorLba/terminatorLba constants from iso9660.go.
const (
	bridgeBootRecordLba    uint32 = 17
	bridgePathTableLba     uint32 = 22
	bridgeRootDirectoryLba uint32 = 23
	bridgeBootCatalogLba   uint32 = 24
	bridgeLastMetadataLba  uint32 = 24
	bridgePathTableBytes          = 10 // root-only little-endian path table
	maxBootImageBytes             = int64(2048) * 2048 * 2048

	elToritoIdentifier = "EL TORITO SPECIFICATION"
	bootCatalogIsoName = "BOOT.CAT;1"
	bootCatalogUdfName = "boot.catalog"
)

// ElToritoBridge is the ISO9660 + El Torito layer of a UDF bridge disc.
type ElToritoBridge struct {
	volumeSectors      uint32
	volumeLabel        string
	bootImageName      string
	bootImagePrimaryId string
	bootImageSize      int64
	timestamp          time.Time
	bootImageLba       uint32
	bootImageLbaSet    bool
	sectors            map[uint32][]byte
}

// NewElToritoBridge creates the layer. volumeSectors is the total image size
// in 2048-byte sectors. bootImageName is the file name shown in ISO9660/UDF.
// bootImageSize is its size in bytes. The timestamp follows the iso package's
// nowFunc convention (time.Now().UTC()).
func NewElToritoBridge(volumeSectors uint32, volumeLabel string, bootImageName string, bootImageSize int64) (*ElToritoBridge, error) {
	if volumeSectors <= 25 {
		return nil, fmt.Errorf("UDF bridge volume of %d sectors is too small for the ISO9660/El Torito layer (sectors through %d). (Parameter 'volumeSectors')",
			volumeSectors, bridgeLastMetadataLba)
	}
	if bootImageSize <= 0 || bootImageSize > maxBootImageBytes {
		return nil, fmt.Errorf("Boot image size must be between 1 byte and %d. (Parameter 'bootImageSize')",
			maxBootImageBytes)
	}
	primaryBase := normalizePrimaryIdentifier(bootImageName, maximumPrimaryIdentifierCharacters-2, "")
	if primaryBase == "" {
		return nil, fmt.Errorf("Boot image name does not normalize to an ISO9660 primary identifier: %s (Parameter 'bootImageName')",
			bootImageName)
	}
	return &ElToritoBridge{
		volumeSectors:      volumeSectors,
		volumeLabel:        normalizePrimaryIdentifier(volumeLabel, 32, "DVD_IMAGE"),
		bootImageName:      bootImageName,
		bootImagePrimaryId: primaryBase + ";1",
		bootImageSize:      bootImageSize,
		timestamp:          nowFunc(),
	}, nil
}

// SetBootImageLba records the planner-assigned boot image LBA. The catalog is
// fixed at sector 24; the boot image LBA must be set before content
// generation.
func (b *ElToritoBridge) SetBootImageLba(lba uint32) error {
	if lba <= bridgeLastMetadataLba || lba >= b.volumeSectors {
		return fmt.Errorf("Boot image LBA %d must be after sector %d and within the %d-sector volume. (Parameter 'lba')",
			lba, bridgeLastMetadataLba, b.volumeSectors)
	}
	b.bootImageLba = lba
	b.bootImageLbaSet = true
	b.sectors = b.buildSectors()
	return nil
}

// BootImageName echoes the constructor value.
func (b *ElToritoBridge) BootImageName() string { return b.bootImageName }

// BootImageSize echoes the constructor value.
func (b *ElToritoBridge) BootImageSize() int64 { return b.bootImageSize }

// SectorContent returns the 2048 bytes to write at lba, or ok=false for LBAs
// not owned by this layer. Valid after SetBootImageLba. Covers sectors
// 16, 17, 18, 22, 23, 24.
func (b *ElToritoBridge) SectorContent(lba uint32) ([]byte, bool, error) {
	if !b.bootImageLbaSet {
		return nil, false, errors.New("El Torito bridge content is available only after SetBootImageLba.")
	}
	sector, ok := b.sectors[lba]
	if !ok {
		return nil, false, nil
	}
	out := make([]byte, len(sector))
	copy(out, sector)
	return out, true, nil
}

// UdfExtraFiles exposes the boot image and boot catalog as UDF root files so
// the UDF planner can include them. The boot image bytes are supplied by the
// dvd writer (Fill unused); the catalog Fill returns the content built by
// SetBootImageLba.
func (b *ElToritoBridge) UdfExtraFiles() []UdfExtraFile {
	return []UdfExtraFile{
		{Name: b.bootImageName, Size: b.bootImageSize},
		{
			Name: bootCatalogUdfName,
			Size: LogicalSectorBytes,
			Fill: func() ([]byte, error) {
				if !b.bootImageLbaSet {
					return nil, errors.New("El Torito boot catalog is available only after SetBootImageLba.")
				}
				out := make([]byte, LogicalSectorBytes)
				copy(out, b.sectors[bridgeBootCatalogLba])
				return out, nil
			},
		},
	}
}

func (b *ElToritoBridge) buildSectors() map[uint32][]byte {
	return map[uint32][]byte{
		primaryVolumeDescriptorLba: b.buildPrimaryVolumeDescriptor(),
		bridgeBootRecordLba:        buildBootRecordDescriptor(),
		terminatorLba:              isoBuildTerminator(),
		bridgePathTableLba:         buildBridgePathTable(),
		bridgeRootDirectoryLba:     b.buildRootDirectoryExtent(),
		bridgeBootCatalogLba:       b.buildBootCatalog(),
	}
}

// buildPrimaryVolumeDescriptor is iso9660.go's descriptor with the fixed
// bridge layout: little-endian path table at 22, no big-endian table, root
// directory extent at 23.
func (b *ElToritoBridge) buildPrimaryVolumeDescriptor() []byte {
	return isoBuildVolumeDescriptor(
		1,
		b.volumeSectors,
		b.volumeLabel,
		bridgePathTableBytes,
		bridgePathTableLba,
		0,
		bridgeRootDirectoryLba,
		LogicalSectorBytes,
		false,
		b.timestamp)
}

// buildBootRecordDescriptor is the El Torito boot record volume descriptor
// (type 0): the specification identifier at offset 7 and the little-endian
// boot catalog LBA at offset 71.
func buildBootRecordDescriptor() []byte {
	sector := make([]byte, LogicalSectorBytes)
	// sector[0] = 0: boot record volume descriptor.
	copy(sector[1:], "CD001")
	sector[6] = 1
	isoWritePaddedAscii(sector, 7, 32, elToritoIdentifier)
	binary.LittleEndian.PutUint32(sector[71:], bridgeBootCatalogLba)
	return sector
}

// buildBridgePathTable is the root-only little-endian path table.
func buildBridgePathTable() []byte {
	root := &isoNode{isDirectory: true, primaryDirectoryLba: bridgeRootDirectoryLba}
	table := isoBuildPathTable([]*isoNode{root}, true, false)
	sector := make([]byte, LogicalSectorBytes)
	copy(sector, table)
	return sector
}

// buildRootDirectoryExtent holds ".", "..", the boot catalog and the boot
// image; all four records fit one sector.
func (b *ElToritoBridge) buildRootDirectoryExtent() []byte {
	extent := make([]byte, LogicalSectorBytes)
	offset := isoAddDirectoryRecord(extent, 0, isoBuildDirectoryRecord(
		bridgeRootDirectoryLba, LogicalSectorBytes, true, []byte{0}, b.timestamp))
	offset = isoAddDirectoryRecord(extent, offset, isoBuildDirectoryRecord(
		bridgeRootDirectoryLba, LogicalSectorBytes, true, []byte{1}, b.timestamp))
	offset = isoAddDirectoryRecord(extent, offset, isoBuildDirectoryRecord(
		bridgeBootCatalogLba, LogicalSectorBytes, false, []byte(bootCatalogIsoName), b.timestamp))
	isoAddDirectoryRecord(extent, offset, isoBuildDirectoryRecord(
		b.bootImageLba, b.bootImageSize, false, []byte(b.bootImagePrimaryId), b.timestamp))
	return extent
}

// buildBootCatalog is the one-sector El Torito boot catalog: a validation
// entry followed by the initial/default (no-emulation) entry.
func (b *ElToritoBridge) buildBootCatalog() []byte {
	sector := make([]byte, LogicalSectorBytes)

	// Validation entry (El Torito spec Figure 7): bytes 2..27 are the
	// reserved word and the empty ID string, all zero.
	sector[0] = 1 // header ID
	sector[1] = 0 // platform ID: 80x86
	sector[30] = 0x55
	sector[31] = 0xAA
	// Checksum word at bytes 28..29: the 16 little-endian words of the
	// 32-byte entry must sum to zero.
	var sum uint16
	for i := 0; i < 32; i += 2 {
		sum += binary.LittleEndian.Uint16(sector[i:])
	}
	binary.LittleEndian.PutUint16(sector[28:], -sum)

	// Initial/Default entry at bytes 32..63.
	sector[32] = 0x88 // bootable
	sector[33] = 0    // boot media type: no emulation
	// Bytes 34..35: load segment 0 (BIOS default 0x7C0).
	// Byte 36: system type, a copy of the PVD's (0); byte 37: unused.
	sectorCount := uint16((b.bootImageSize + 511) / 512) // in 512-byte units
	binary.LittleEndian.PutUint16(sector[38:], sectorCount)
	binary.LittleEndian.PutUint32(sector[40:], b.bootImageLba)
	return sector
}
