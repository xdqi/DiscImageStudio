// iso_eltorito.go implements the ISO9660 + El Torito layer of a UDF bridge
// disc: a minimal ISO9660 volume at the inner edge that carries an El Torito
// boot catalog and, optionally, exposes selected data files (each stored
// contiguously) at the same paths they have in the image. Sectors 19..21
// belong to the UDF volume recognition sequence and are not emitted by this
// layer.
package iso

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Fixed bridge layout (2048-byte sectors): descriptors at 16-18, the UDF
// volume recognition sequence owns 19-21, and the ISO9660 tree (path table,
// directory extents, boot catalog) starts at 22. It must end before the
// anchor volume descriptor at 256.
const (
	bridgeBootRecordLba uint32 = 17
	bridgePathTableLba  uint32 = 22
	bridgeAnchorLba     uint32 = 256

	maxBootImageBytes = int64(2048) * 2048 * 2048

	elToritoIdentifier = "EL TORITO SPECIFICATION"
)

// ExposedFile is one regular file from the data directory that is
// additionally listed in the bridge's ISO9660 tree, at the same
// image-root-relative path it has in the UDF tree. The dvd planner places it
// contiguously and reports the extent through SetPlacements.
type ExposedFile struct {
	Path string // image-root-relative, '/'-separated (e.g. "a/b/barz")
	Size int64
}

// BridgeMetadataExtent is one reserved bridge metadata sector, for the
// planner's fixed-constraint list and the sidecar report.
type BridgeMetadataExtent struct {
	Role string
	Lba  uint32
}

// ElToritoBridge is the ISO9660 + El Torito layer of a UDF bridge disc.
type ElToritoBridge struct {
	volumeSectors uint32
	volumeLabel   string
	timestamp     time.Time

	bootImageName string
	bootImageSize int64
	bootNode      *isoNode
	catalogNode   *isoNode
	exposed       []ExposedFile
	exposedNodes  []*isoNode

	root             *isoNode
	directories      []*isoNode
	pathTableBytes   int
	pathTableSectors uint32
	catalogLba       uint32

	bootImageLba  uint32
	placementsSet bool
	sectors       map[uint32][]byte
}

// NewElToritoBridge creates the layer. volumeSectors is the total image size
// in 2048-byte sectors. bootImageName is the file name shown at the ISO9660
// root. bootImageSize is its size in bytes. exposed lists data files to list
// in the ISO9660 tree at their image paths; extraDirs lists directories to
// create even when they would otherwise be empty (directory expansion of
// --expose). The timestamp follows the iso package's nowFunc convention
// (time.Now().UTC()).
func NewElToritoBridge(volumeSectors uint32, volumeLabel string, bootImageName string, bootImageSize int64,
	exposed []ExposedFile, extraDirs []string) (*ElToritoBridge, error) {
	if volumeSectors <= 25 {
		return nil, fmt.Errorf("UDF bridge volume of %d sectors is too small for the ISO9660/El Torito layer (sectors through %d). (Parameter 'volumeSectors')",
			volumeSectors, 25)
	}
	if bootImageSize <= 0 || bootImageSize > maxBootImageBytes {
		return nil, fmt.Errorf("Boot image size must be between 1 byte and %d. (Parameter 'bootImageSize')",
			maxBootImageBytes)
	}
	if normalizePrimaryIdentifier(bootImageName, maximumPrimaryIdentifierCharacters-2, "") == "" {
		return nil, fmt.Errorf("Boot image name does not normalize to an ISO9660 primary identifier: %s (Parameter 'bootImageName')",
			bootImageName)
	}
	b := &ElToritoBridge{
		volumeSectors: volumeSectors,
		volumeLabel:   normalizePrimaryIdentifier(volumeLabel, 32, "DVD_IMAGE"),
		timestamp:     nowFunc(),
		bootImageName: bootImageName,
		bootImageSize: bootImageSize,
		exposed:       append([]ExposedFile(nil), exposed...),
	}

	root := &isoNode{isDirectory: true, timestamp: b.timestamp}
	b.root = root
	// Named so the identifier machinery produces exactly "BOOT.CAT;1".
	b.catalogNode = &isoNode{name: "boot.cat", dataLength: LogicalSectorBytes, timestamp: b.timestamp, parent: root}
	b.bootNode = &isoNode{name: bootImageName, dataLength: bootImageSize, timestamp: b.timestamp, parent: root}
	root.children = []*isoNode{b.catalogNode, b.bootNode}

	// The exposed files' parent directories are synthesized on demand;
	// directoryPaths maps each created directory's image path to its node.
	directoryPaths := map[string]*isoNode{"": root}
	mkdir := func(path string) (*isoNode, error) {
		components := splitImagePath(path)
		current := root
		prefix := ""
		for _, component := range components {
			if component == ".." {
				return nil, fmt.Errorf("The path must stay inside the image root: %s (Parameter 'exposed')", path)
			}
			if prefix == "" {
				prefix = component
			} else {
				prefix += "/" + component
			}
			next, ok := directoryPaths[prefix]
			if !ok {
				next = &isoNode{name: component, isDirectory: true, timestamp: b.timestamp, parent: current}
				current.children = append(current.children, next)
				directoryPaths[prefix] = next
			}
			current = next
		}
		return current, nil
	}
	for _, dir := range extraDirs {
		if _, err := mkdir(dir); err != nil {
			return nil, err
		}
	}
	for i := range b.exposed {
		components := splitImagePath(b.exposed[i].Path)
		if len(components) == 0 {
			return nil, fmt.Errorf("The exposed path is empty. (Parameter 'exposed')")
		}
		if b.exposed[i].Size < 0 {
			return nil, fmt.Errorf("The exposed file size cannot be negative: %s (Parameter 'exposed')", b.exposed[i].Path)
		}
		var parent *isoNode
		var err error
		if len(components) > 1 {
			parent, err = mkdir(strings.Join(components[:len(components)-1], "/"))
			if err != nil {
				return nil, err
			}
		} else {
			parent = root
		}
		leaf := &isoNode{name: components[len(components)-1], dataLength: b.exposed[i].Size, timestamp: b.timestamp, parent: parent}
		parent.children = append(parent.children, leaf)
		b.exposedNodes = append(b.exposedNodes, leaf)
	}

	directories, err := isoAssignDirectoryNumbers(root)
	if err != nil {
		return nil, err
	}
	if err := isoAssignIdentifiers(directories); err != nil {
		return nil, err
	}
	b.directories = directories

	// Layout: path table at 22, directory extents in path-table order, then
	// the boot catalog. Everything must fit before the anchor at 256.
	b.pathTableBytes = isoPathTableLength(directories, false)
	b.pathTableSectors = uint32(sectorCount(int64(b.pathTableBytes)))
	cursor := bridgePathTableLba + b.pathTableSectors
	for _, directory := range directories {
		directoryBytes := isoDirectoryExtentLength(directory, false, false)
		directory.primaryDirectoryLba = cursor
		directory.primaryDirectoryBytes = directoryBytes
		cursor += uint32(sectorCount(int64(directoryBytes)))
	}
	b.catalogLba = cursor
	cursor++
	if cursor > bridgeAnchorLba {
		return nil, fmt.Errorf("The ISO9660/El Torito bridge tree needs %d sectors but must end before sector %d; expose fewer files. (Parameter 'exposed')",
			cursor-bridgePathTableLba, bridgeAnchorLba)
	}
	b.catalogNode.dataLba = b.catalogLba
	return b, nil
}

// SetPlacements records the planner-assigned LBAs of the boot image and the
// exposed files (in the constructor's order) and renders the layer's
// sectors. Every file's extent must be contiguous.
func (b *ElToritoBridge) SetPlacements(bootImageLba uint32, exposedLbas []uint32) error {
	if len(exposedLbas) != len(b.exposedNodes) {
		return fmt.Errorf("Got %d exposed-file LBAs for %d exposed files. (Parameter 'exposedLbas')",
			len(exposedLbas), len(b.exposedNodes))
	}
	if err := b.checkPlacement(bootImageLba, "bootImageLba"); err != nil {
		return err
	}
	for i, lba := range exposedLbas {
		if b.exposed[i].Size == 0 {
			if lba != 0 {
				return fmt.Errorf("The empty exposed file %s has no extent; its LBA must be 0. (Parameter 'exposedLbas')", b.exposed[i].Path)
			}
			continue
		}
		if err := b.checkPlacement(lba, "exposedLbas"); err != nil {
			return err
		}
		b.exposedNodes[i].dataLba = lba
	}
	b.bootImageLba = bootImageLba
	b.bootNode.dataLba = bootImageLba
	b.placementsSet = true
	b.sectors = b.buildSectors()
	return nil
}

func (b *ElToritoBridge) checkPlacement(lba uint32, param string) error {
	if lba <= b.catalogLba || lba >= b.volumeSectors {
		return fmt.Errorf("File data LBA %d must be after the bridge metadata ending at sector %d and within the %d-sector volume. (Parameter '%s')",
			lba, b.catalogLba, b.volumeSectors, param)
	}
	return nil
}

// MetadataExtents lists every sector this layer owns (for the planner's
// fixed constraints and the sidecar report).
func (b *ElToritoBridge) MetadataExtents() []BridgeMetadataExtent {
	out := []BridgeMetadataExtent{
		{Role: "iso9660-pvd", Lba: primaryVolumeDescriptorLba},
		{Role: "el-torito-record", Lba: bridgeBootRecordLba},
		{Role: "iso9660-terminator", Lba: terminatorLba},
	}
	for i := uint32(0); i < b.pathTableSectors; i++ {
		out = append(out, BridgeMetadataExtent{Role: "iso9660-pathtable", Lba: bridgePathTableLba + i})
	}
	for _, directory := range b.directories {
		for k := uint32(0); k < uint32(sectorCount(int64(directory.primaryDirectoryBytes))); k++ {
			out = append(out, BridgeMetadataExtent{Role: "iso9660-directory", Lba: directory.primaryDirectoryLba + k})
		}
	}
	out = append(out, BridgeMetadataExtent{Role: "boot-catalog", Lba: b.catalogLba})
	return out
}

// BootImageName echoes the constructor value.
func (b *ElToritoBridge) BootImageName() string { return b.bootImageName }

// BootImageSize echoes the constructor value.
func (b *ElToritoBridge) BootImageSize() int64 { return b.bootImageSize }

// CatalogLba is the boot catalog sector (dynamic: it follows the tree).
func (b *ElToritoBridge) CatalogLba() uint32 { return b.catalogLba }

// SectorContent returns the 2048 bytes to write at lba, or ok=false for LBAs
// not owned by this layer. Valid after SetPlacements.
func (b *ElToritoBridge) SectorContent(lba uint32) ([]byte, bool, error) {
	if !b.placementsSet {
		return nil, false, errors.New("El Torito bridge content is available only after SetPlacements.")
	}
	sector, ok := b.sectors[lba]
	if !ok {
		return nil, false, nil
	}
	out := make([]byte, len(sector))
	copy(out, sector)
	return out, true, nil
}

func (b *ElToritoBridge) buildSectors() map[uint32][]byte {
	sectors := map[uint32][]byte{
		primaryVolumeDescriptorLba: b.buildPrimaryVolumeDescriptor(),
		bridgeBootRecordLba:        buildBootRecordDescriptor(b.catalogLba),
		terminatorLba:              isoBuildTerminator(),
		b.catalogLba:               b.buildBootCatalog(),
	}
	table := isoBuildPathTable(b.directories, true, false)
	for i := uint32(0); i < b.pathTableSectors; i++ {
		sector := make([]byte, LogicalSectorBytes)
		copy(sector, table[int(i)*LogicalSectorBytes:])
		sectors[bridgePathTableLba+i] = sector
	}
	for _, directory := range b.directories {
		extent := isoBuildDirectoryExtent(directory, false, false)
		for k := uint32(0); k < uint32(len(extent))/LogicalSectorBytes; k++ {
			sectors[directory.primaryDirectoryLba+k] = extent[k*LogicalSectorBytes : (k+1)*LogicalSectorBytes]
		}
	}
	return sectors
}

// buildPrimaryVolumeDescriptor is iso9660.go's descriptor with the bridge
// layout: little-endian path table at 22, no big-endian table, and the
// dynamically placed root directory extent.
func (b *ElToritoBridge) buildPrimaryVolumeDescriptor() []byte {
	return isoBuildVolumeDescriptor(
		1,
		b.volumeSectors,
		b.volumeLabel,
		b.pathTableBytes,
		bridgePathTableLba,
		0,
		b.root.primaryDirectoryLba,
		b.root.primaryDirectoryBytes,
		false,
		b.timestamp)
}

// buildBootRecordDescriptor is the El Torito boot record volume descriptor
// (type 0): the specification identifier at offset 7 and the little-endian
// boot catalog LBA at offset 71.
func buildBootRecordDescriptor(catalogLba uint32) []byte {
	sector := make([]byte, LogicalSectorBytes)
	// sector[0] = 0: boot record volume descriptor.
	copy(sector[1:], "CD001")
	sector[6] = 1
	isoWritePaddedAscii(sector, 7, 32, elToritoIdentifier)
	binary.LittleEndian.PutUint32(sector[71:], catalogLba)
	return sector
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
