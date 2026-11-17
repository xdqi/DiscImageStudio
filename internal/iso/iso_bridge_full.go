// iso_bridge_full.go implements the "full tree" variant of the UDF bridge's
// ISO9660 layer: every directory, file and symlink of the data tree is listed
// in the ISO9660 (Rock Ridge) and Joliet trees, so a reader that only speaks
// ISO9660 — the Linux kernel mounting -t iso9660, e.g. debian-installer's
// cdrom-detect — sees the whole volume, not just the boot files.
//
// File data is NOT duplicated: every ISO9660 file points at the same extents
// as its UDF file entry. A file the planner fragmented becomes a multi-extent
// file (ECMA-119 file sections: consecutive directory records with the same
// identifier, flag 0x80 on all but the last), one section per fragment. The
// sections are generally NOT adjacent on the disc; ECMA-119 does not require
// adjacency, but readers differ (measured):
//   - Linux isofs follows each section's own extent, up to 100 sections
//     (fs/isofs/inode.c aborts beyond that) — IsoLinuxMaxSections;
//   - GRUB 2.14 reads past a section's end when a read starts inside it, and
//     syslinux/isolinux supports only single-extent files: files those boot
//     loaders read must stay contiguous (the caller's --expose list);
//   - libarchive (bsdtar) reads sections as if they were adjacent.
//
// The tree no longer fits in sectors 50..255, so it is laid out in a region
// the caller reserves right after the UDF partition metadata; only the
// volume descriptors stay at 16..19.
package iso

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// IsoMaxSectionBytes is the largest sector-aligned ISO9660 file section
	// (the 32-bit data length, rounded down to whole 2048-byte sectors).
	IsoMaxSectionBytes = 0xFFFFF800
	// IsoLinuxMaxSections is the most file sections per file the Linux
	// isofs driver follows; it aborts the read past 100.
	IsoLinuxMaxSections = 100

	rripModeDirectory = 0o040000
	rripModeRegular   = 0o100000
	rripModeSymlink   = 0o120000

	maximumDirectoryRecordBytes = 255
)

// BridgeTreeEntry is one node of the data tree for a full-tree bridge, in
// parents-before-children order (UdfDispersedPlan.BridgeTree).
type BridgeTreeEntry struct {
	Path        string // image-root-relative, '/'-separated; "" is the root
	IsDirectory bool
	IsSymlink   bool
	LinkTarget  string
	Size        int64 // regular files only
	Mode        uint32
	ModTime     time.Time
	FileIndex   int // dispersed UDF file index (regular files, symlinks); -1 for directories
}

// NewElToritoBridgeFull creates a full-tree bridge layer: every entry of tree
// is listed. The boot images are regular files of the tree, named by their
// image-root-relative paths (at least one is required, like
// NewElToritoBridge). Call FullTreeSectors to size the region to reserve,
// then SetFullPlacements once the planner has placed the files.
func NewElToritoBridgeFull(volumeSectors uint32, volumeLabel string, bootImagePath string, bootImageSize int64,
	legacyBootImagePath string, legacyBootImageSize int64, tree []BridgeTreeEntry) (*ElToritoBridge, error) {
	if err := validateBridgeBootImages(volumeSectors, bootImagePath, bootImageSize, legacyBootImagePath, legacyBootImageSize); err != nil {
		return nil, err
	}
	b := &ElToritoBridge{
		volumeSectors: volumeSectors,
		volumeLabel:   fullTreeVolumeLabel(volumeLabel),
		timestamp:     nowFunc(),
		bootImageName: bootImagePath,
		bootImageSize: bootImageSize,
		legacyPath:    legacyBootImagePath,
		legacySize:    legacyBootImageSize,
		fullTree:      true,
		fileNodes:     map[int]*isoNode{},
	}

	root := &isoNode{isDirectory: true, timestamp: b.timestamp, mode: 0o555, fileIndex: -1}
	b.root = root
	directoryPaths := map[string]*isoNode{"": root}
	filePaths := map[string]*isoNode{}
	for _, entry := range tree {
		clean := cleanImagePath(entry.Path)
		if clean == "" {
			if !entry.IsDirectory {
				return nil, errors.New("The tree root must be a directory. (Parameter 'tree')")
			}
			root.timestamp = entry.ModTime
			root.mode = entry.Mode
			continue
		}
		parentPath, name := "", clean
		if slash := strings.LastIndexByte(clean, '/'); slash >= 0 {
			parentPath, name = clean[:slash], clean[slash+1:]
		}
		if name == ".." {
			return nil, fmt.Errorf("The path must stay inside the image root: %s (Parameter 'tree')", entry.Path)
		}
		parent, ok := directoryPaths[parentPath]
		if !ok {
			return nil, fmt.Errorf("Tree entry %s precedes its parent directory. (Parameter 'tree')", entry.Path)
		}
		if _, dup := directoryPaths[clean]; dup {
			return nil, fmt.Errorf("Duplicate tree entry: %s (Parameter 'tree')", entry.Path)
		}
		if _, dup := filePaths[clean]; dup {
			return nil, fmt.Errorf("Duplicate tree entry: %s (Parameter 'tree')", entry.Path)
		}
		node := &isoNode{
			name:        name,
			isDirectory: entry.IsDirectory,
			parent:      parent,
			timestamp:   entry.ModTime,
			mode:        entry.Mode,
			isSymlink:   entry.IsSymlink,
			linkTarget:  entry.LinkTarget,
			fileIndex:   -1,
		}
		switch {
		case entry.IsDirectory:
			directoryPaths[clean] = node
		case entry.IsSymlink:
			node.fileIndex = entry.FileIndex
			filePaths[clean] = node
		default:
			if entry.Size < 0 {
				return nil, fmt.Errorf("The file size cannot be negative: %s (Parameter 'tree')", entry.Path)
			}
			if _, dup := b.fileNodes[entry.FileIndex]; dup || entry.FileIndex < 0 {
				return nil, fmt.Errorf("Tree entry %s has an invalid or duplicate file index %d. (Parameter 'tree')", entry.Path, entry.FileIndex)
			}
			node.dataLength = entry.Size
			node.fileIndex = entry.FileIndex
			b.fileNodes[entry.FileIndex] = node
			filePaths[clean] = node
		}
		parent.children = append(parent.children, node)
	}

	bootNode := func(path string, size int64, flag string) (*isoNode, error) {
		if path == "" {
			return nil, nil
		}
		node, ok := filePaths[cleanImagePath(path)]
		if !ok || node.isSymlink {
			return nil, fmt.Errorf("%s must name a regular file of the tree: %s", flag, path)
		}
		if node.dataLength != size {
			return nil, fmt.Errorf("%s %s is %d bytes in the tree, not %d.", flag, path, node.dataLength, size)
		}
		return node, nil
	}
	var err error
	if b.legacyNode, err = bootNode(legacyBootImagePath, legacyBootImageSize, "The legacy boot image"); err != nil {
		return nil, err
	}
	if b.bootNode, err = bootNode(bootImagePath, bootImageSize, "The boot image"); err != nil {
		return nil, err
	}

	// The boot catalog is listed at the root under a name the tree does not
	// use (normally "boot.cat").
	catalogName := "boot.cat"
	for k := 1; rootHasChild(root, catalogName); k++ {
		catalogName = fmt.Sprintf("boot%d.cat", k)
	}
	b.catalogNode = &isoNode{
		name: catalogName, dataLength: LogicalSectorBytes, timestamp: b.timestamp, parent: root,
		mode: 0o444, fileIndex: -1, sections: []isoSection{{length: LogicalSectorBytes}},
	}
	root.children = append(root.children, b.catalogNode)

	directories, err := isoAssignDirectoryNumbers(root)
	if err != nil {
		return nil, err
	}
	if err := isoAssignIdentifiers(directories); err != nil {
		return nil, err
	}
	// Joliet-long keeps the exact names (up to 103 UTF-16 units); symlinks
	// have no Joliet representation and are listed only in the Rock Ridge
	// tree.
	for _, directory := range directories {
		used := make(map[string]bool)
		for _, child := range directory.children {
			if child.isSymlink {
				continue
			}
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
	// ECMA-119 orders directory records (and, through the numbering, the
	// path table) by identifier.
	for _, directory := range directories {
		sort.SliceStable(directory.children, func(i, j int) bool {
			return directory.children[i].primaryIdentifier < directory.children[j].primaryIdentifier
		})
	}
	if directories, err = isoAssignDirectoryNumbers(root); err != nil {
		return nil, err
	}
	b.directories = directories

	b.assignRockRidgeSerials()

	// Validate every record (e.g. Rock Ridge names that overflow a record)
	// up front, with one section per file.
	if _, err := b.FullTreeSectors(nil); err != nil {
		return nil, err
	}
	return b, nil
}

// fullTreeVolumeLabel keeps the volume label verbatim when it is printable
// ASCII of at most 32 bytes — what xorriso records, and what label lookups
// such as dracut's root=live:CDLABEL=Fedora-WS-Live-45 compare against — and
// normalizes it to ISO9660 d-characters otherwise. (The Joliet volume
// identifier holds the first 16 characters.)
func fullTreeVolumeLabel(label string) string {
	if label == "" || len(label) > 32 {
		return normalizePrimaryIdentifier(label, 32, "DVD_IMAGE")
	}
	for i := 0; i < len(label); i++ {
		if label[i] < 0x20 || label[i] > 0x7E {
			return normalizePrimaryIdentifier(label, 32, "DVD_IMAGE")
		}
	}
	return label
}

// assignRockRidgeSerials numbers the Rock Ridge inodes: the root is 1, then
// every node in directory order.
func (b *ElToritoBridge) assignRockRidgeSerials() {
	b.rockRidgeSerials = map[*isoNode]uint32{b.root: 1}
	for _, directory := range b.directories {
		for _, child := range directory.children {
			b.rockRidgeSerials[child] = uint32(len(b.rockRidgeSerials) + 1)
		}
	}
}

// setSingleSections gives every file of the tree one section at its dataLba,
// as the exposed bridge stores each file contiguously (empty files: none).
func (b *ElToritoBridge) setSingleSections() {
	for _, directory := range b.directories {
		for _, child := range directory.children {
			if child.isDirectory || child.isSymlink {
				continue
			}
			child.sections = nil
			if child.dataLength > 0 {
				child.sections = []isoSection{{lba: child.dataLba, length: uint32(child.dataLength)}}
			}
		}
	}
}

func rootHasChild(root *isoNode, name string) bool {
	for _, child := range root.children {
		if child.name == name {
			return true
		}
	}
	return false
}

// FullTreeSectors returns the size of the region the full tree needs: path
// tables, the primary and Joliet directory extents and the boot catalog,
// given each file's section count (by dispersed UDF file index; files
// missing from the map count one section). Record sizes do not depend on
// LBAs, so the count is exact.
func (b *ElToritoBridge) FullTreeSectors(sectionCounts map[int]int) (uint32, error) {
	if !b.fullTree {
		return 0, errors.New("FullTreeSectors needs a full-tree bridge.")
	}
	if b.placementsSet {
		return 0, errors.New("The full tree is already placed.")
	}
	for index, node := range b.fileNodes {
		count := sectionCounts[index]
		if node.dataLength == 0 {
			count = 0
		} else if count < 1 {
			count = 1
		}
		node.sections = make([]isoSection, count)
	}
	end, err := b.layoutFullTree(0)
	if err != nil {
		return 0, err
	}
	return end, nil
}

// SetFullPlacements lays the tree out in [treeStartLba,
// treeStartLba+treeSectors), records the boot image LBAs and every file's
// sections from its dispersed UDF extents (the same map AssignExtents takes:
// offset-ordered, covering the file), and renders the layer's sectors. A
// fragment longer than IsoMaxSectionBytes becomes several sections. Boot
// images must be single, contiguous extents at their boot LBAs.
func (b *ElToritoBridge) SetFullPlacements(treeStartLba, treeSectors uint32, bootImageLba, legacyBootImageLba uint32,
	extents map[int][]UdfFileExtent) error {
	if !b.fullTree {
		return errors.New("SetFullPlacements needs a full-tree bridge; use SetPlacements.")
	}
	if treeStartLba <= bridgeTerminatorLba || uint64(treeStartLba)+uint64(treeSectors) > uint64(b.volumeSectors) {
		return fmt.Errorf("The ISO9660 tree region [%d, %d) must follow the volume descriptors and lie within the %d-sector volume.",
			treeStartLba, uint64(treeStartLba)+uint64(treeSectors), b.volumeSectors)
	}
	treeEnd := treeStartLba + treeSectors
	for index, node := range b.fileNodes {
		sections, err := isoSectionsFromExtents(node, extents[index], treeEnd, b.volumeSectors)
		if err != nil {
			return err
		}
		node.sections = sections
	}
	checkBoot := func(node *isoNode, lba uint32, what string) error {
		if node == nil {
			return nil
		}
		if len(node.sections) != 1 || node.sections[0].lba != lba {
			return fmt.Errorf("The %s %s must be one contiguous extent at its boot LBA %d.", what, node.name, lba)
		}
		return nil
	}
	if err := checkBoot(b.bootNode, bootImageLba, "boot image"); err != nil {
		return err
	}
	if err := checkBoot(b.legacyNode, legacyBootImageLba, "legacy boot image"); err != nil {
		return err
	}
	end, err := b.layoutFullTree(treeStartLba)
	if err != nil {
		return err
	}
	if end > treeEnd {
		return fmt.Errorf("The ISO9660 tree needs %d sectors, but only %d are reserved.", end-treeStartLba, treeSectors)
	}
	b.treeStartLba, b.treeEndLba = treeStartLba, treeEnd
	b.bootImageLba, b.legacyBootLba = bootImageLba, legacyBootImageLba
	b.placementsSet = true
	b.sectors = b.buildFullSectors()
	return nil
}

// isoSectionsFromExtents converts one file's offset-ordered extents into
// ISO9660 sections: all but the last a whole number of sectors, the last
// ending at the file size.
func isoSectionsFromExtents(node *isoNode, extents []UdfFileExtent, dataStart, volumeSectors uint32) ([]isoSection, error) {
	var sections []isoSection
	offset := int64(0)
	for _, extent := range extents {
		if extent.Offset != offset || extent.Sectors == 0 {
			return nil, fmt.Errorf("The extents of %s must be contiguous in content from offset 0 (got offset %d, expected %d).", node.name, extent.Offset, offset)
		}
		if extent.Lba < dataStart || uint64(extent.Lba)+uint64(extent.Sectors) > uint64(volumeSectors) {
			return nil, fmt.Errorf("The extent of %s at LBA %d must follow the ISO9660 tree (ending at %d) and lie within the volume.", node.name, extent.Lba, dataStart)
		}
		remaining := node.dataLength - offset
		cover := int64(extent.Sectors) * LogicalSectorBytes
		if cover > remaining {
			if sectorCount(remaining) != int64(extent.Sectors) {
				return nil, fmt.Errorf("The tail extent of %s is %d sectors, but the remaining %d bytes need %d.", node.name, extent.Sectors, remaining, sectorCount(remaining))
			}
			cover = remaining
		}
		lba := extent.Lba
		for left := cover; left > 0; {
			take := min(left, int64(IsoMaxSectionBytes))
			sections = append(sections, isoSection{lba: lba, length: uint32(take)})
			lba += uint32(take / LogicalSectorBytes)
			left -= take
		}
		offset += cover
	}
	if offset != node.dataLength {
		return nil, fmt.Errorf("The extents of %s cover %d of its %d bytes.", node.name, offset, node.dataLength)
	}
	return sections, nil
}

// layoutFullTree assigns the path tables, directory extents and the catalog
// consecutive LBAs from start and returns the end LBA (exclusive).
func (b *ElToritoBridge) layoutFullTree(start uint32) (uint32, error) {
	cursor := uint64(start)
	advance := func(sectors int64) uint32 {
		lba := uint32(cursor)
		cursor += uint64(sectors)
		return lba
	}
	b.pathTableBytes = isoPathTableLength(b.directories, false)
	b.pathTableSectors = uint32(sectorCount(int64(b.pathTableBytes)))
	b.pathTableLba = advance(int64(b.pathTableSectors))
	b.bigPathTableLba = advance(int64(b.pathTableSectors))
	for _, directory := range b.directories {
		bytes, err := b.fullDirectoryBytes(directory, false)
		if err != nil {
			return 0, err
		}
		directory.primaryDirectoryBytes = bytes
		directory.primaryDirectoryLba = advance(sectorCount(int64(bytes)))
	}
	b.jolietPathTableBytes = isoPathTableLength(b.directories, true)
	b.jolietPathTableSectors = uint32(sectorCount(int64(b.jolietPathTableBytes)))
	b.jolietPathTableLba = advance(int64(b.jolietPathTableSectors))
	b.jolietBigPathTableLba = advance(int64(b.jolietPathTableSectors))
	for _, directory := range b.directories {
		bytes, err := b.fullDirectoryBytes(directory, true)
		if err != nil {
			return 0, err
		}
		directory.jolietDirectoryBytes = bytes
		directory.jolietDirectoryLba = advance(sectorCount(int64(bytes)))
	}
	b.catalogLba = advance(1)
	b.catalogNode.dataLba = b.catalogLba
	b.catalogNode.sections = []isoSection{{lba: b.catalogLba, length: LogicalSectorBytes}}
	if cursor > uint64(b.volumeSectors) {
		return 0, fmt.Errorf("The ISO9660 tree needs %d sectors; the volume has %d.", cursor-uint64(start), b.volumeSectors)
	}
	return uint32(cursor), nil
}

func (b *ElToritoBridge) fullDirectoryBytes(directory *isoNode, joliet bool) (int, error) {
	records, err := b.fullDirectoryRecords(directory, joliet)
	if err != nil {
		return 0, err
	}
	lengths := make([]int, len(records))
	for i, record := range records {
		lengths[i] = len(record)
	}
	return isoPackedRecordLength(lengths), nil
}

// fullDirectoryRecords builds a directory's records in order: ".", "..",
// then the children, a file with several sections contributing one record
// per section. The primary tree carries Rock Ridge; Joliet omits symlinks.
func (b *ElToritoBridge) fullDirectoryRecords(directory *isoNode, joliet bool) ([][]byte, error) {
	extentOf := func(node *isoNode) (uint32, int64) {
		if joliet {
			return node.jolietDirectoryLba, int64(node.jolietDirectoryBytes)
		}
		return node.primaryDirectoryLba, int64(node.primaryDirectoryBytes)
	}
	parent := directory.parent
	if parent == nil {
		parent = directory
	}
	var records [][]byte
	add := func(lba uint32, length int64, isDirectory bool, identifier []byte, timestamp time.Time, systemUse []byte, label string) error {
		if len(systemUse)%2 != 0 {
			systemUse = append(systemUse, 0) // keep the record length even
		}
		if isoDirectoryRecordLength(len(identifier))+len(systemUse) > maximumDirectoryRecordBytes {
			return fmt.Errorf("The ISO9660 directory record of %q exceeds %d bytes (its name is too long for Rock Ridge without continuation areas).",
				label, maximumDirectoryRecordBytes)
		}
		records = append(records, isoBuildDirectoryRecordSusp(lba, length, isDirectory, identifier, timestamp, systemUse))
		return nil
	}
	var dotUse, dotDotUse []byte
	if !joliet {
		dotUse = b.rockRidgePX(directory)
		if directory.parent == nil {
			// SUSP: SP first in the root's "." record, then the RRIP ER.
			dotUse = append(append(append([]byte(nil), isoSuspSP...), dotUse...), isoRripER()...)
		}
		dotDotUse = b.rockRidgePX(parent)
	}
	lba, length := extentOf(directory)
	if err := add(lba, length, true, []byte{0}, directory.timestamp, dotUse, "."); err != nil {
		return nil, err
	}
	lba, length = extentOf(parent)
	if err := add(lba, length, true, []byte{1}, parent.timestamp, dotDotUse, ".."); err != nil {
		return nil, err
	}
	for _, child := range directory.children {
		if joliet && child.isSymlink {
			continue
		}
		identifier := isoIdentifierBytes(child, joliet)
		var systemUse []byte
		if !joliet {
			systemUse = append(b.rockRidgePX(child), rripNM(child.name)...)
			if child.isSymlink {
				systemUse = append(systemUse, isoSymlinkSystemUse(child.linkTarget)...)
			}
		}
		switch {
		case child.isDirectory:
			lba, length := extentOf(child)
			if err := add(lba, length, true, identifier, child.timestamp, systemUse, child.name); err != nil {
				return nil, err
			}
		case child.isSymlink || len(child.sections) == 0:
			// Symlinks and empty files have no extent (LBA 0, length 0).
			if err := add(0, 0, false, identifier, child.timestamp, systemUse, child.name); err != nil {
				return nil, err
			}
		default:
			for i, section := range child.sections {
				if err := add(section.lba, int64(section.length), false, identifier, child.timestamp, systemUse, child.name); err != nil {
					return nil, err
				}
				if i < len(child.sections)-1 {
					records[len(records)-1][25] |= 0x80 // multi-extent: more sections follow
				}
			}
		}
	}
	return records, nil
}

// directoryExtent packs a directory's records (Rock Ridge in the primary
// tree) into its extent of size bytes. The records were validated when the
// layout sized the extent.
func (b *ElToritoBridge) directoryExtent(directory *isoNode, joliet bool, size int) []byte {
	records, err := b.fullDirectoryRecords(directory, joliet)
	if err != nil {
		panic(err)
	}
	extent := make([]byte, size)
	offset := 0
	for _, record := range records {
		offset = isoAddDirectoryRecord(extent, offset, record)
	}
	return extent
}

// rockRidgePX is the RRIP PX entry of a node: POSIX mode (type bits plus the
// source permission bits), link count, uid/gid 0 and the inode serial.
func (b *ElToritoBridge) rockRidgePX(node *isoNode) []byte {
	var mode, links uint32
	switch {
	case node.isDirectory:
		mode = rripModeDirectory | node.mode&0o7777
		links = 2
		for _, child := range node.children {
			if child.isDirectory {
				links++
			}
		}
	case node.isSymlink:
		mode, links = rripModeSymlink|0o777, 1
	default:
		mode, links = rripModeRegular|node.mode&0o7777, 1
	}
	entry := make([]byte, 44)
	entry[0], entry[1], entry[2], entry[3] = 'P', 'X', 44, 1
	for i, value := range []uint32{mode, links, 0, 0, b.rockRidgeSerials[node]} {
		binary.LittleEndian.PutUint32(entry[4+8*i:], value)
		binary.BigEndian.PutUint32(entry[8+8*i:], value)
	}
	return entry
}

// rripNM is the RRIP alternate name: NM entries of at most 250 name bytes,
// all but the last flagged CONTINUE.
func rripNM(name string) []byte {
	var out []byte
	rest := []byte(name)
	for {
		take := min(len(rest), 250)
		flags := byte(0)
		if take < len(rest) {
			flags = 1
		}
		out = append(out, 'N', 'M', byte(5+take), 1, flags)
		out = append(out, rest[:take]...)
		rest = rest[take:]
		if len(rest) == 0 {
			return out
		}
	}
}

// buildFullSectors renders the volume descriptors, both path table pairs,
// all directory extents and the catalog.
func (b *ElToritoBridge) buildFullSectors() map[uint32][]byte {
	sectors := map[uint32][]byte{
		primaryVolumeDescriptorLba: isoBuildVolumeDescriptor(1, b.volumeSectors, b.volumeLabel, b.pathTableBytes,
			b.pathTableLba, b.bigPathTableLba, b.root.primaryDirectoryLba, b.root.primaryDirectoryBytes, false, b.timestamp),
		bridgeBootRecordLba: buildBootRecordDescriptor(b.catalogLba),
		bridgeSupplementaryLba: isoBuildVolumeDescriptor(2, b.volumeSectors, b.volumeLabel, b.jolietPathTableBytes,
			b.jolietPathTableLba, b.jolietBigPathTableLba, b.root.jolietDirectoryLba, b.root.jolietDirectoryBytes, true, b.timestamp),
		bridgeTerminatorLba: isoBuildTerminator(),
		b.catalogLba:        b.buildBootCatalog(),
	}
	putTable := func(table []byte, lba uint32, count uint32) {
		for i := uint32(0); i < count; i++ {
			sector := make([]byte, LogicalSectorBytes)
			if start := int(i) * LogicalSectorBytes; start < len(table) {
				copy(sector, table[start:])
			}
			sectors[lba+i] = sector
		}
	}
	putTable(isoBuildPathTable(b.directories, true, false), b.pathTableLba, b.pathTableSectors)
	putTable(isoBuildPathTable(b.directories, false, false), b.bigPathTableLba, b.pathTableSectors)
	putTable(isoBuildPathTable(b.directories, true, true), b.jolietPathTableLba, b.jolietPathTableSectors)
	putTable(isoBuildPathTable(b.directories, false, true), b.jolietBigPathTableLba, b.jolietPathTableSectors)
	for _, joliet := range []bool{false, true} {
		for _, directory := range b.directories {
			lba, size := directory.primaryDirectoryLba, directory.primaryDirectoryBytes
			if joliet {
				lba, size = directory.jolietDirectoryLba, directory.jolietDirectoryBytes
			}
			extent := b.directoryExtent(directory, joliet, size)
			for k := 0; k < size/LogicalSectorBytes; k++ {
				sectors[lba+uint32(k)] = extent[k*LogicalSectorBytes : (k+1)*LogicalSectorBytes]
			}
		}
	}
	return sectors
}
