// iso_eltorito.go implements the ISO9660 + El Torito layer of a UDF bridge
// disc: a minimal ISO9660 volume at the inner edge that carries an El Torito
// boot catalog and, optionally, exposes selected data files (each stored
// contiguously) at the same paths they have in the image. Sectors 20..22
// belong to the UDF volume recognition sequence and are not emitted by this
// layer.
package iso

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Fixed bridge layout (2048-byte sectors): ISO descriptors at 16-19, UDF
// volume recognition at 20-22, UDF descriptors at 32-49, and both ISO9660
// and Joliet trees from 50 through the boot catalog, before the anchor at 256.
const (
	bridgeBootRecordLba    uint32 = 17
	bridgeSupplementaryLba uint32 = 18
	bridgeTerminatorLba    uint32 = 19
	// BridgeVrsLba is where the UDF volume recognition sequence starts on a
	// bridge disc, right after the ISO9660/El Torito descriptors.
	BridgeVrsLba       uint32 = 20
	bridgePathTableLba uint32 = 50 // 32..49 are UDF descriptors

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

	bootImageName string // the EFI boot image's image-root-relative path
	bootImageSize int64
	bootNode      *isoNode
	legacyPath    string // the x86 legacy BIOS boot image's path
	legacySize    int64
	legacyNode    *isoNode
	catalogNode   *isoNode
	exposed       []ExposedFile
	exposedNodes  []*isoNode

	root                   *isoNode
	directories            []*isoNode
	pathTableBytes         int
	pathTableSectors       uint32
	jolietPathTableLba     uint32
	jolietPathTableBytes   int
	jolietPathTableSectors uint32
	catalogLba             uint32

	bootImageLba  uint32 // EFI image extent
	legacyBootLba uint32 // x86 legacy BIOS image extent
	placementsSet bool
	sectors       map[uint32][]byte
}

// NewElToritoBridge creates the layer. volumeSectors is the total image size
// in 2048-byte sectors. Boot images are image-root-relative paths:
// bootImageName is the EFI boot entry, legacyBootImagePath the x86 legacy
// BIOS entry (no-emulation, BootInfoTable patched into its on-disc content);
// at least one is required, and when both exist the legacy entry appears
// before the EFI section in the El Torito catalog. exposed lists data files
// to list in the ISO9660 tree at their image paths; extraDirs lists
// directories to create even when they would otherwise be empty (directory
// expansion of --expose). The timestamp follows the iso package's nowFunc
// convention (time.Now().UTC()).
func NewElToritoBridge(volumeSectors uint32, volumeLabel string, bootImageName string, bootImageSize int64,
	legacyBootImagePath string, legacyBootImageSize int64, exposed []ExposedFile, extraDirs []string) (*ElToritoBridge, error) {
	if volumeSectors <= 25 {
		return nil, fmt.Errorf("UDF bridge volume of %d sectors is too small for the ISO9660/El Torito layer (sectors through %d). (Parameter 'volumeSectors')",
			volumeSectors, 25)
	}
	checkBootImage := func(path string, size int64, param string) error {
		if path == "" {
			return nil
		}
		if size <= 0 || size > maxBootImageBytes {
			return fmt.Errorf("Boot image size must be between 1 byte and %d. (Parameter '%s')",
				maxBootImageBytes, param)
		}
		if normalizePrimaryIdentifier(path, maximumPrimaryIdentifierCharacters-2, "") == "" {
			return fmt.Errorf("Boot image path does not normalize to an ISO9660 primary identifier: %s (Parameter '%s')",
				path, param)
		}
		return nil
	}
	if err := checkBootImage(bootImageName, bootImageSize, "bootImageName"); err != nil {
		return nil, err
	}
	if err := checkBootImage(legacyBootImagePath, legacyBootImageSize, "legacyBootImagePath"); err != nil {
		return nil, err
	}
	if bootImageName == "" && legacyBootImagePath == "" {
		return nil, errors.New("The bridge needs at least one boot image (EFI or x86 legacy BIOS).")
	}
	b := &ElToritoBridge{
		volumeSectors: volumeSectors,
		volumeLabel:   normalizePrimaryIdentifier(volumeLabel, 32, "DVD_IMAGE"),
		timestamp:     nowFunc(),
		bootImageName: bootImageName,
		bootImageSize: bootImageSize,
		legacyPath:    legacyBootImagePath,
		legacySize:    legacyBootImageSize,
		exposed:       append([]ExposedFile(nil), exposed...),
	}

	root := &isoNode{isDirectory: true, timestamp: b.timestamp}
	b.root = root
	// Named so the identifier machinery produces exactly "BOOT.CAT;1".
	b.catalogNode = &isoNode{name: "boot.cat", dataLength: LogicalSectorBytes, timestamp: b.timestamp, parent: root}
	root.children = []*isoNode{b.catalogNode}

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
	// File leaves are shared by clean path: exposing the boot image's path
	// (or giving both boot flags the same path) must not duplicate nodes.
	fileNodes := map[string]*isoNode{}
	addFile := func(path string, size int64) (*isoNode, error) {
		clean := cleanImagePath(path)
		if node, ok := fileNodes[clean]; ok {
			if node.dataLength != size {
				return nil, fmt.Errorf("The file has conflicting sizes: %s (Parameter 'exposed')", path)
			}
			return node, nil
		}
		components := splitImagePath(clean)
		if len(components) == 0 || components[len(components)-1] == ".." {
			return nil, fmt.Errorf("Invalid file path: %s (Parameter 'exposed')", path)
		}
		parent := root
		var err error
		if len(components) > 1 {
			parent, err = mkdir(strings.Join(components[:len(components)-1], "/"))
			if err != nil {
				return nil, err
			}
		}
		leaf := &isoNode{name: components[len(components)-1], dataLength: size, timestamp: b.timestamp, parent: parent}
		parent.children = append(parent.children, leaf)
		fileNodes[clean] = leaf
		return leaf, nil
	}

	for _, dir := range extraDirs {
		if _, err := mkdir(dir); err != nil {
			return nil, err
		}
	}
	if legacyBootImagePath != "" {
		node, err := addFile(legacyBootImagePath, legacyBootImageSize)
		if err != nil {
			return nil, err
		}
		b.legacyNode = node
	}
	if bootImageName != "" {
		node, err := addFile(bootImageName, bootImageSize)
		if err != nil {
			return nil, err
		}
		b.bootNode = node
	}
	for i := range b.exposed {
		if b.exposed[i].Size < 0 {
			return nil, fmt.Errorf("The exposed file size cannot be negative: %s (Parameter 'exposed')", b.exposed[i].Path)
		}
		node, err := addFile(b.exposed[i].Path, b.exposed[i].Size)
		if err != nil {
			return nil, err
		}
		b.exposedNodes = append(b.exposedNodes, node)
	}

	directories, err := isoAssignDirectoryNumbers(root)
	if err != nil {
		return nil, err
	}
	if err := isoAssignIdentifiers(directories); err != nil {
		return nil, err
	}
	// Joliet-long supports up to 103 UTF-16 code units per name. Do not
	// silently normalize, truncate or rename paths in the Joliet tree.
	for _, directory := range directories {
		used := make(map[string]bool)
		for _, child := range directory.children {
			if !utf8.ValidString(child.name) || utf16Length(child.name) > 103 || strings.ContainsAny(child.name, "\x00/\\:;*?") {
				return nil, fmt.Errorf("Name cannot be represented in Joliet-long: %q", child.name)
			}
			identifier := child.name
			if !child.isDirectory {
				identifier += ";1"
			}
			if used[identifier] {
				return nil, fmt.Errorf("Duplicate Joliet identifier: %q", child.name)
			}
			used[identifier] = true
			child.jolietIdentifier = identifier
		}
	}
	b.directories = directories

	// Layout: primary path table and directories, Joliet path table and
	// directories, then the catalog, all before the anchor at 256.
	b.pathTableBytes = isoPathTableLength(directories, false)
	b.pathTableSectors = uint32(sectorCount(int64(b.pathTableBytes)))
	cursor := bridgePathTableLba + b.pathTableSectors
	for _, directory := range directories {
		directoryBytes := isoDirectoryExtentLength(directory, false, false)
		directory.primaryDirectoryLba = cursor
		directory.primaryDirectoryBytes = directoryBytes
		cursor += uint32(sectorCount(int64(directoryBytes)))
	}
	b.jolietPathTableBytes = isoPathTableLength(directories, true)
	b.jolietPathTableSectors = uint32(sectorCount(int64(b.jolietPathTableBytes)))
	b.jolietPathTableLba = cursor
	cursor += b.jolietPathTableSectors
	for _, directory := range directories {
		directory.jolietDirectoryBytes = isoDirectoryExtentLength(directory, true, false)
		directory.jolietDirectoryLba = cursor
		cursor += uint32(sectorCount(int64(directory.jolietDirectoryBytes)))
	}
	b.catalogLba = cursor
	cursor++
	if cursor > UdfAnchorLba {
		return nil, fmt.Errorf("The ISO9660/El Torito bridge tree needs %d sectors but must end before sector %d; expose fewer files. (Parameter 'exposed')",
			cursor-bridgePathTableLba, UdfAnchorLba)
	}
	b.catalogNode.dataLba = b.catalogLba
	return b, nil
}

// SetPlacements records the planner-assigned LBAs of the boot images and the
// exposed files (in the constructor's order) and renders the layer's
// sectors. Every file's extent must be contiguous. Absent boot images take
// LBA 0.
func (b *ElToritoBridge) SetPlacements(bootImageLba uint32, legacyBootImageLba uint32, exposedLbas []uint32) error {
	if len(exposedLbas) != len(b.exposedNodes) {
		return fmt.Errorf("Got %d exposed-file LBAs for %d exposed files. (Parameter 'exposedLbas')",
			len(exposedLbas), len(b.exposedNodes))
	}
	if b.bootNode != nil {
		if err := b.checkPlacement(bootImageLba, "bootImageLba"); err != nil {
			return err
		}
	}
	if b.legacyNode != nil {
		if err := b.checkPlacement(legacyBootImageLba, "legacyBootImageLba"); err != nil {
			return err
		}
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
		if b.exposedNodes[i] == b.bootNode && lba != bootImageLba {
			return fmt.Errorf("Exposed boot image must share its boot LBA")
		}
		if b.exposedNodes[i] == b.legacyNode && lba != legacyBootImageLba {
			return fmt.Errorf("Exposed legacy boot image must share its boot LBA")
		}
		b.exposedNodes[i].dataLba = lba
	}
	b.bootImageLba = bootImageLba
	if b.bootNode != nil {
		b.bootNode.dataLba = bootImageLba
	}
	b.legacyBootLba = legacyBootImageLba
	if b.legacyNode != nil {
		b.legacyNode.dataLba = legacyBootImageLba
	}
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
		{Role: "joliet-svd", Lba: bridgeSupplementaryLba},
		{Role: "iso9660-terminator", Lba: bridgeTerminatorLba},
	}
	for i := uint32(0); i < b.jolietPathTableSectors; i++ {
		out = append(out, BridgeMetadataExtent{Role: "joliet-pathtable", Lba: b.jolietPathTableLba + i})
	}
	for _, directory := range b.directories {
		for k := uint32(0); k < uint32(sectorCount(int64(directory.jolietDirectoryBytes))); k++ {
			out = append(out, BridgeMetadataExtent{Role: "joliet-directory", Lba: directory.jolietDirectoryLba + k})
		}
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
		bridgeSupplementaryLba:     isoBuildVolumeDescriptor(2, b.volumeSectors, b.volumeLabel, b.jolietPathTableBytes, b.jolietPathTableLba, 0, b.root.jolietDirectoryLba, b.root.jolietDirectoryBytes, true, b.timestamp),
		bridgeTerminatorLba:        isoBuildTerminator(),
		b.catalogLba:               b.buildBootCatalog(),
	}
	jolietTable := isoBuildPathTable(b.directories, true, true)
	for i := uint32(0); i < b.jolietPathTableSectors; i++ {
		sector := make([]byte, LogicalSectorBytes)
		copy(sector, jolietTable[int(i)*LogicalSectorBytes:])
		sectors[b.jolietPathTableLba+i] = sector
	}
	for _, directory := range b.directories {
		extent := isoBuildDirectoryExtent(directory, true, false)
		for k := uint32(0); k < uint32(len(extent))/LogicalSectorBytes; k++ {
			sectors[directory.jolietDirectoryLba+k] = extent[k*LogicalSectorBytes : (k+1)*LogicalSectorBytes]
		}
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
// layout: little-endian path table, no big-endian table, and the
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
	// The identifier field must be NUL-padded, NOT space-padded: SeaBIOS
	// strcmp()s it against "CD001\001EL TORITO SPECIFICATION".
	copy(sector[7:39], elToritoIdentifier)
	binary.LittleEndian.PutUint32(sector[71:], catalogLba)
	return sector
}

// buildBootCatalog is the one-sector El Torito boot catalog: a validation
// entry, then the x86 legacy BIOS default entry when present, then the EFI
// section (the legacy entry always appears before the EFI one). With only an
// EFI image the entry takes the default position and the validation entry's
// platform is 0xEF (xorriso's EFI-only layout).
func (b *ElToritoBridge) buildBootCatalog() []byte {
	sector := make([]byte, LogicalSectorBytes)

	// Validation entry (El Torito spec Figure 7): bytes 2..27 are the
	// reserved word and the empty ID string, all zero.
	sector[0] = 1 // header ID
	sector[1] = 0 // platform ID: 80x86
	if b.legacyNode == nil {
		sector[1] = 0xEF // platform ID: EFI (EFI-only catalog)
	}
	sector[30] = 0x55
	sector[31] = 0xAA
	// Checksum word at bytes 28..29: the 16 little-endian words of the
	// 32-byte entry must sum to zero.
	var sum uint16
	for i := 0; i < 32; i += 2 {
		sum += binary.LittleEndian.Uint16(sector[i:])
	}
	binary.LittleEndian.PutUint16(sector[28:], -sum)

	offset := 32
	writeEntry := func(lba uint32, loadSectors uint16) {
		sector[offset] = 0x88 // bootable
		sector[offset+1] = 0  // boot media type: no emulation
		// Load segment 0 (BIOS default 0x7C0); system type 0.
		binary.LittleEndian.PutUint16(sector[offset+6:], loadSectors)
		binary.LittleEndian.PutUint32(sector[offset+8:], lba)
	}
	if b.legacyNode != nil {
		// x86 legacy BIOS default entry: no-emulation, BootInfoTable, and
		// the load size rounded up to 2 KiB (4 sectors of 512 bytes each).
		writeEntry(b.legacyBootLba, uint16((b.legacySize+LogicalSectorBytes-1)/LogicalSectorBytes)*4)
		offset += 32
	}
	if b.bootNode != nil {
		if b.legacyNode != nil {
			// Section header: final (0x91), platform 0xEF (EFI), one entry.
			sector[offset] = 0x91
			sector[offset+1] = 0xEF
			binary.LittleEndian.PutUint16(sector[offset+2:], 1)
			offset += 32
		}
		writeEntry(b.bootImageLba, efiLoadSectors(b.bootImageSize))
		offset += 32
	}
	return sector
}

// efiLoadSectors is the EFI catalog entry's sector count: the image size in
// 512-byte units, or 0 when that overflows the 16-bit field (images over
// 32 MiB, e.g. archiso's ESP with the kernel and initramfs). 0 is what
// xorriso records; UEFI firmware then takes the image to extend from its LBA
// to the end of the volume and the FAT inside bounds itself (EDK2
// ElTorito.c).
func efiLoadSectors(size int64) uint16 {
	if sectors := (size + 511) / 512; sectors <= 0xFFFF {
		return uint16(sectors)
	}
	return 0
}

// BootInfoTableChecksum computes the El Torito Boot Information Table's
// bi_csum of a boot file: the sum of all little-endian uint32 words from
// byte 64 to the end of the file (bi_length), the last partial word
// zero-padded (verified byte-equal to xorriso/libisofs).
func BootInfoTableChecksum(content []byte) uint32 {
	var sum uint32
	for i := 64; i < len(content); i += 4 {
		word := content[i:]
		if len(word) > 4 {
			word = word[:4]
		}
		var v uint32
		for j := 0; j < len(word); j++ {
			v |= uint32(word[j]) << (8 * j)
		}
		sum += v
	}
	return sum
}

// PatchBootInfoTable writes the El Torito Boot Information Table of a legacy
// BIOS no-emulation boot image into bytes 8..55 of its first 2048-byte
// sector: bi_pvd (LBA of the Primary Volume Descriptor), bi_file (the boot
// image's LBA), bi_length (image size in bytes), and bi_csum (see
// BootInfoTableChecksum — it covers the WHOLE file, not just this sector).
// The reserved bytes 24..55 are zeroed.
func PatchBootInfoTable(firstSector []byte, fileLba uint32, imageSize int64, checksum uint32) {
	for i := 8; i < 56; i++ {
		firstSector[i] = 0
	}
	binary.LittleEndian.PutUint32(firstSector[8:], primaryVolumeDescriptorLba)
	binary.LittleEndian.PutUint32(firstSector[12:], fileLba)
	binary.LittleEndian.PutUint32(firstSector[16:], uint32(imageSize))
	binary.LittleEndian.PutUint32(firstSector[20:], checksum)
}
