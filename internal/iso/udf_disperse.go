// udf_disperse.go implements a UDF 2.01 writer variant whose FILE DATA
// extents are placed at explicitly assigned LBAs (possibly fragmented into
// several extents per file) instead of contiguously after the metadata.
// This supports a DVD "picture + data" hybrid where file data is dispersed
// into picture-selected sectors. The partition metadata layout (FSD, file
// entries, directory data) is byte-identical to planUdf's for the same
// tree; only the file extent placement differs.
package iso

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UdfMaxAllocationDescriptors is the short_ad capacity of one 2048-byte UDF
// file entry with no extended attributes: (2048-176)/8 = 234. The dispersed
// DVD planner uses it to keep every file under the one-file-entry limit.
const UdfMaxAllocationDescriptors = (LogicalSectorBytes - 176) / 8

// UdfExtraFile is a synthetic file not present on disk (e.g. a boot image or
// boot catalog). Name is the root-level file name (UDF CS0 identifier rules).
type UdfExtraFile struct {
	Name string
	Size int64
	Fill func() ([]byte, error) // content, called once at finalize time
}

// UdfFileExtent is one placed data extent of a file's content.
type UdfFileExtent struct {
	FileIndex int    // into the plan's file list (tree files in enumeration order, then extras)
	Offset    int64  // byte offset within the file's content
	Lba       uint32 // absolute disc LBA
	Sectors   uint32 // length in 2048-byte sectors; last extent of a file may cover the file tail (partial sector is zero-padded)
}

// UdfDispersedPlan is a planned UDF 2.01 volume with explicitly placed file data.
type UdfDispersedPlan struct {
	layout              *UdfLayoutPlan // descriptor builders consume this
	vrsStart            uint32
	metaEndLbaExclusive uint32
	files               []*udfNode // tree files in enumeration order, then extras
	extras              []UdfExtraFile

	assigned     bool
	fileExtents  [][]UdfFileExtent // per file index, in extent (offset) order
	rendered     map[uint32][]byte // pre-rendered descriptor sectors by LBA
	extraContent map[int][]byte    // cached Fill results for extras
}

// PrepareUdfDispersed scans the source tree exactly like planUdf (same
// ordering, identifiers, timestamps, uniqueIds starting at len(nodes)+15),
// then lays out the partition metadata (FSD at partitionStart, file entries,
// directory data) contiguously from udfFirstPartitionLba — IDENTICAL to
// planUdf's metadata layout (but no file data follows). An empty
// sourceDirectory means no tree files.
// extraFiles become additional root children (after the tree's root children)
// with the given names/sizes; their identifier encoding uses udfEncodeIdentifier.
// vrsStart is 16 normally, 19 when an ISO9660 layer occupies 16-18 (UDF bridge).
func PrepareUdfDispersed(sourceDirectory string, volumeSectors uint32, volumeLabel string,
	vrsStart uint32, extraFiles []UdfExtraFile) (*UdfDispersedPlan, error) {
	if volumeSectors < UdfMinimumVolumeSectors || volumeSectors%32 != 0 {
		return nil, fmt.Errorf("UDF drawing volumes need at least %d sectors and a multiple of 32 sectors. (Parameter 'volumeSectors')",
			UdfMinimumVolumeSectors)
	}
	recorded := nowFunc()
	sourcePath := ""
	if sourceDirectory != "" {
		var err error
		sourcePath, err = filepath.Abs(sourceDirectory)
		if err != nil {
			return nil, err
		}
		info, statErr := os.Stat(sourcePath)
		if statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("Data directory does not exist: %s", sourcePath)
		}
		if err := udfRejectReparsePoint(sourcePath); err != nil {
			return nil, err
		}
	}

	root := &udfNode{sourcePath: sourcePath, isDirectory: true, timestamp: recorded}
	if sourceDirectory != "" {
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return nil, err
		}
		root.timestamp = info.ModTime().UTC()
	}
	nodes := []*udfNode{root}
	// Iterative traversal, identical to planUdf.
	for index := 0; index < len(nodes); index++ {
		directory := nodes[index]
		if !directory.isDirectory || len(directory.sourcePath) == 0 {
			continue
		}
		entries, err := readDirSorted(directory.sourcePath, func(a, b string) bool { return a < b })
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			fullName := filepath.Join(directory.sourcePath, entry.name)
			isSymlink := entry.info.Mode()&os.ModeSymlink != 0
			isDirectory := entry.info.IsDir()
			identifier, err := udfEncodeIdentifier(entry.name)
			if err != nil {
				return nil, err
			}
			child := &udfNode{
				sourcePath:  fullName,
				name:        entry.name,
				isDirectory: isDirectory,
				parent:      directory,
				identifier:  identifier,
				timestamp:   entry.info.ModTime().UTC(),
				uniqueId:    uint64(len(nodes)) + 15,
				isSymlink:   isSymlink,
			}
			if isSymlink {
				target, err := os.Readlink(fullName)
				if err != nil {
					return nil, err
				}
				content, err := udfEncodeSymlinkTarget(target)
				if err != nil {
					return nil, err
				}
				child.linkTarget = target
				child.linkContent = content
				child.dataLength = int64(len(content))
			} else if !isDirectory {
				child.dataLength = entry.info.Size()
			}
			directory.children = append(directory.children, child)
			nodes = append(nodes, child)
		}
	}
	// Synthetic extra files become additional root children, after the tree's
	// root children; uniqueIds continue from len(nodes)+15.
	for _, extra := range extraFiles {
		identifier, err := udfEncodeIdentifier(extra.Name)
		if err != nil {
			return nil, err
		}
		if extra.Size < 0 {
			return nil, errors.New("Extra file size cannot be negative. (Parameter 'extraFiles')")
		}
		child := &udfNode{
			name:       extra.Name,
			parent:     root,
			identifier: identifier,
			timestamp:  recorded,
			uniqueId:   uint64(len(nodes)) + 15,
			dataLength: extra.Size,
		}
		root.children = append(root.children, child)
		nodes = append(nodes, child)
	}

	// Metadata layout identical to planUdf: file entries from partitionStart+2,
	// then directory data. File data is placed later via AssignExtents.
	cursor := uint64(udfFirstPartitionLba + 2)
	for _, node := range nodes {
		if cursor > math.MaxUint32 {
			return nil, errOverflow
		}
		node.entryLba = uint32(cursor)
		cursor++
		if node.isDirectory {
			node.dataLength = int64(udfFileIdentifierLength(0, 0))
			for _, child := range node.children {
				node.dataLength += int64(udfFileIdentifierLength(len(child.identifier), node.dataLength))
			}
		}
		if (node.dataLength+UdfMaximumExtentBytes-1)/UdfMaximumExtentBytes >
			UdfMaxAllocationDescriptors {
			return nil, fmt.Errorf("File needs more allocation descriptors than one UDF file entry supports: %s", udfDispersedFileLabel(node))
		}
		childDirectories := 0
		for _, child := range node.children {
			if child.isDirectory {
				childDirectories++
			}
		}
		if childDirectories >= math.MaxUint16 {
			return nil, fmt.Errorf("Too many child directories for a UDF link count: %s", node.sourcePath)
		}
	}
	for _, node := range nodes {
		if !node.isDirectory {
			continue
		}
		if cursor > math.MaxUint32 {
			return nil, errOverflow
		}
		node.dataLba = uint32(cursor)
		cursor += uint64(sectorCount(node.dataLength))
	}
	if cursor > uint64(volumeSectors)-257 {
		return nil, fmt.Errorf("UDF metadata needs %d sectors, reaching beyond the backup metadata at sector %d.", cursor, uint64(volumeSectors)-257)
	}

	label := volumeLabel
	if strings.TrimSpace(label) == "" {
		label = "BD_IMAGE"
	}
	label, err := udfFitDstring(label, 128)
	if err != nil {
		return nil, err
	}

	fileCount := 0
	var fileBytes int64
	var files []*udfNode
	for _, node := range nodes {
		if node.isDirectory {
			continue
		}
		fileCount++
		fileBytes += node.dataLength
		files = append(files, node)
	}
	layout := &UdfLayoutPlan{
		sourceDirectory:     sourcePath,
		volumeSectors:       volumeSectors,
		volumeLabel:         label,
		timestamp:           recorded,
		fileCount:           fileCount,
		directoryCount:      len(nodes) - fileCount,
		fileBytes:           fileBytes,
		dataEndLbaExclusive: uint32(cursor), // metadata-only until AssignExtents
		partitionStartLba:   udfFirstPartitionLba,
		nodes:               nodes,
	}
	return &UdfDispersedPlan{
		layout:              layout,
		vrsStart:            vrsStart,
		metaEndLbaExclusive: uint32(cursor),
		files:               files,
		extras:              append([]UdfExtraFile(nil), extraFiles...),
	}, nil
}

// udfDispersedFileLabel names a file in errors like the C# source path does,
// falling back to the volume-relative name for synthetic files.
func udfDispersedFileLabel(node *udfNode) string {
	if node.sourcePath != "" {
		return node.sourcePath
	}
	return node.name
}

// VolumeSectors is the total volume length in 2048-byte sectors.
func (p *UdfDispersedPlan) VolumeSectors() uint32 { return p.layout.volumeSectors }

// VolumeLabel is the fitted logical volume identifier.
func (p *UdfDispersedPlan) VolumeLabel() string { return p.layout.volumeLabel }

// FileCount includes extras.
func (p *UdfDispersedPlan) FileCount() int { return p.layout.fileCount }

// DirectoryCount includes the root directory.
func (p *UdfDispersedPlan) DirectoryCount() int { return p.layout.directoryCount }

// FileBytes is the total file content length in bytes.
func (p *UdfDispersedPlan) FileBytes() int64 { return p.layout.fileBytes }

// MetaEndLbaExclusive is the first LBA after the partition metadata block.
func (p *UdfDispersedPlan) MetaEndLbaExclusive() uint32 { return p.metaEndLbaExclusive }

// PartitionStartLba is the physical partition origin (288).
func (p *UdfDispersedPlan) PartitionStartLba() uint32 { return p.layout.partitionStartLba }

// FileSize returns the content length of file index.
func (p *UdfDispersedPlan) FileSize(index int) int64 { return p.files[index].dataLength }

// FileName returns the UDF file name of file index.
func (p *UdfDispersedPlan) FileName(index int) string { return p.files[index].name }

// FileSourcePath returns the source path of file index, "" for extras and
// symlinks (their content comes from FileContent instead).
func (p *UdfDispersedPlan) FileSourcePath(index int) string {
	if p.files[index].isSymlink {
		return ""
	}
	return p.files[index].sourcePath
}

// FileContent returns the in-memory content for an extra file, caching the
// Fill result. ok=false for tree files (read those from their source path).
func (p *UdfDispersedPlan) FileContent(index int) (content []byte, ok bool, err error) {
	treeCount := len(p.files) - len(p.extras)
	if index < 0 || index >= len(p.files) {
		return nil, false, fmt.Errorf("File index out of range. (Parameter 'index')")
	}
	if index < treeCount {
		node := p.files[index]
		if node.isSymlink {
			return node.linkContent, true, nil
		}
		return nil, false, nil
	}
	extra := p.extras[index-treeCount]
	if extra.Fill == nil {
		return nil, false, fmt.Errorf("Extra file %q has no content source. (Parameter 'extraFiles')", extra.Name)
	}
	if p.extraContent == nil {
		p.extraContent = make(map[int][]byte)
	}
	if cached, ok2 := p.extraContent[index]; ok2 {
		return cached, true, nil
	}
	data, err := extra.Fill()
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) != extra.Size {
		return nil, false, fmt.Errorf("Extra file %q content is %d bytes, not the planned %d. (Parameter 'extraFiles')",
			extra.Name, len(data), extra.Size)
	}
	p.extraContent[index] = data
	return data, true, nil
}

// AssignExtents records the placement. For every file index 0..FileCount-1 the
// extents must cover the file's full size in order (offsets contiguous from 0;
// each extent's byte length = Sectors*2048 except that the file's LAST extent
// may end mid-sector); extents must not overlap each other, the metadata block
// [288, MetaEndLbaExclusive) or the reserved tail [volumeSectors-257, ...);
// LBAs must be >= MetaEndLbaExclusive. Per-file extent count must not exceed
// (2048-176)/8 = 234. After assignment the plan computes each file entry's
// short_ad list and validates the partition span.
func (p *UdfDispersedPlan) AssignExtents(extents map[int][]UdfFileExtent) error {
	for index := range extents {
		if index < 0 || index >= len(p.files) {
			return fmt.Errorf("Extent file index %d is out of range. (Parameter 'extents')", index)
		}
	}
	type placedExtent struct{ start, end uint64 }
	var all []placedExtent
	reserveTail := uint64(p.layout.volumeSectors) - 257
	partitionEnd := uint64(p.metaEndLbaExclusive)
	perFile := make([][]UdfFileExtent, len(p.files))
	for index, node := range p.files {
		label := udfDispersedFileLabel(node)
		list := extents[index]
		if len(list) > UdfMaxAllocationDescriptors {
			return fmt.Errorf("File needs more allocation descriptors than one UDF file entry supports: %s", label)
		}
		descriptors := 0
		offset := int64(0)
		for _, extent := range list {
			if extent.FileIndex != index {
				return fmt.Errorf("Extent file index %d does not match the map key %d: %s", extent.FileIndex, index, label)
			}
			if extent.Sectors == 0 {
				return fmt.Errorf("UDF extent has zero sectors: %s", label)
			}
			if extent.Offset != offset {
				return fmt.Errorf("UDF extent for %s starts at offset %d, but %d was expected: extents must be contiguous from 0.", label, extent.Offset, offset)
			}
			remaining := node.dataLength - offset
			if remaining <= 0 {
				return fmt.Errorf("UDF extent lies beyond the end of the file: %s", label)
			}
			start := uint64(extent.Lba)
			end := start + uint64(extent.Sectors)
			if start < uint64(p.metaEndLbaExclusive) {
				return fmt.Errorf("UDF extent for %s starts at sector %d, inside the metadata block ending at sector %d.", label, extent.Lba, p.metaEndLbaExclusive)
			}
			if end > reserveTail {
				return fmt.Errorf("UDF extent for %s ends at sector %d, inside the reserve metadata tail starting at sector %d.", label, end, reserveTail)
			}
			cover := int64(extent.Sectors) * LogicalSectorBytes
			if cover >= remaining {
				// The file's last extent covers the tail; a partial sector is zero-padded.
				if uint64(extent.Sectors) != uint64(sectorCount(remaining)) {
					return fmt.Errorf("UDF tail extent of %s is %d sectors, but the remaining %d bytes need %d.", label, extent.Sectors, remaining, sectorCount(remaining))
				}
				offset = node.dataLength
			} else {
				offset += cover
			}
			descriptors += int((cover + UdfMaximumExtentBytes - 1) / UdfMaximumExtentBytes)
			all = append(all, placedExtent{start, end})
			if end > partitionEnd {
				partitionEnd = end
			}
		}
		if offset != node.dataLength {
			return fmt.Errorf("UDF extents cover %d of %d bytes of the file: %s", offset, node.dataLength, label)
		}
		if descriptors > UdfMaxAllocationDescriptors {
			return fmt.Errorf("File needs more allocation descriptors than one UDF file entry supports: %s", label)
		}
		perFile[index] = append([]UdfFileExtent(nil), list...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].start < all[j].start })
	for i := 1; i < len(all); i++ {
		if all[i].start < all[i-1].end {
			return fmt.Errorf("UDF file data extents overlap at sector %d.", all[i].start)
		}
	}

	// The partition span must cover every placed extent.
	p.layout.dataEndLbaExclusive = uint32(partitionEnd)
	rendered, err := p.renderDescriptors(perFile)
	if err != nil {
		return err
	}
	p.fileExtents = perFile
	p.rendered = rendered
	p.assigned = true
	return nil
}

// DescriptorAt returns the pre-rendered 2048-byte descriptor/metadata sector
// for an LBA, or ok=false when the LBA holds file data or picture. It is
// callable only after AssignExtents.
func (p *UdfDispersedPlan) DescriptorAt(lba uint32) (data []byte, ok bool) {
	if !p.assigned {
		return nil, false
	}
	data, ok = p.rendered[lba]
	return data, ok
}

// FileExtents returns all file-data extents sorted by LBA (for the writer).
func (p *UdfDispersedPlan) FileExtents() []UdfFileExtent {
	var all []UdfFileExtent
	for _, list := range p.fileExtents {
		all = append(all, list...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Lba < all[j].Lba })
	return all
}

// renderDescriptors pre-renders every descriptor/metadata sector of the
// volume, mirroring writeUdfSequentialPrefix/writeUdfSequentialSuffix but
// honoring vrsStart for the volume recognition sequence.
func (p *UdfDispersedPlan) renderDescriptors(perFile [][]UdfFileExtent) (map[uint32][]byte, error) {
	plan := p.layout
	rendered := make(map[uint32][]byte)
	rendered[p.vrsStart] = udfVolumeRecognition("BEA01")
	rendered[p.vrsStart+1] = udfVolumeRecognition("NSR03")
	rendered[p.vrsStart+2] = udfVolumeRecognition("TEA01")
	if err := udfDispersedRenderVds(plan, rendered, udfMainVdsLba); err != nil {
		return nil, err
	}
	rendered[udfIntegrityLba] = udfIntegrityDescriptor(plan)
	rendered[udfIntegrityLba+1] = udfDescriptor(8, udfIntegrityLba+1, 512)
	rendered[256] = udfAnchor(plan, 256)
	rendered[plan.volumeSectors-257] = udfAnchor(plan, plan.volumeSectors-257)
	if err := udfDispersedRenderVds(plan, rendered, plan.ReserveVdsLba()); err != nil {
		return nil, err
	}
	rendered[plan.volumeSectors-1] = udfAnchor(plan, plan.volumeSectors-1)
	fileSet, err := udfFileSetDescriptor(plan)
	if err != nil {
		return nil, err
	}
	rendered[plan.partitionStartLba] = fileSet
	rendered[plan.partitionStartLba+1] = udfDescriptor(8, 1, 512)
	fileIndex := 0
	for _, node := range plan.nodes {
		if node.isDirectory {
			rendered[node.entryLba] = udfFileEntry(plan, node)
			continue
		}
		rendered[node.entryLba] = udfDispersedFileEntry(plan, node, perFile[fileIndex])
		fileIndex++
	}
	udfDispersedRenderDirectoryData(plan, rendered)
	return rendered, nil
}

// udfDispersedRenderVds is udfWriteVolumeDescriptors rendered into a sector
// map instead of a sequential writer (including the zero-padded tail of the
// 16-sector descriptor sequence).
func udfDispersedRenderVds(plan *UdfLayoutPlan, rendered map[uint32][]byte, start uint32) error {
	primary, err := udfPrimaryDescriptor(plan, start)
	if err != nil {
		return err
	}
	rendered[start] = primary
	implementation, err := udfImplementationDescriptor(plan, start+1)
	if err != nil {
		return err
	}
	rendered[start+1] = implementation
	rendered[start+2] = udfPartitionDescriptor(plan, start+2)
	logical, err := udfLogicalVolumeDescriptor(plan, start+3)
	if err != nil {
		return err
	}
	rendered[start+3] = logical
	unallocated := make([]byte, LogicalSectorBytes)
	udfU32(unallocated, 16, 5)
	// Zero free extents: pattern bytes outside the partition are reserved volume space.
	udfSetTag(unallocated, 7, start+4, 24)
	rendered[start+4] = unallocated
	rendered[start+5] = udfDescriptor(8, start+5, 512)
	for lba := start + 6; lba < start+udfVdsSectors; lba++ {
		rendered[lba] = make([]byte, LogicalSectorBytes)
	}
	return nil
}

// udfDispersedRenderDirectoryData renders each directory's file identifier
// records with the same padding rules as udfWritePartition.
func udfDispersedRenderDirectoryData(plan *UdfLayoutPlan, rendered map[uint32][]byte) {
	for _, directory := range plan.nodes {
		if !directory.isDirectory {
			continue
		}
		sectors := sectorCount(directory.dataLength)
		data := make([]byte, int(sectors)*LogicalSectorBytes)
		directoryOffset := int64(0)
		writeIdentifier := func(target *udfNode, identifier []byte, parent bool) {
			location := directory.dataLba - plan.partitionStartLba +
				uint32(directoryOffset/LogicalSectorBytes)
			record := udfFileIdentifier(plan, target, identifier, parent, location, directoryOffset)
			copy(data[directoryOffset:], record)
			directoryOffset += int64(len(record))
		}
		parent := directory.parent
		if parent == nil {
			parent = directory
		}
		writeIdentifier(parent, nil, true)
		for _, child := range directory.children {
			writeIdentifier(child, child.identifier, false)
		}
		for sector := int64(0); sector < sectors; sector++ {
			rendered[directory.dataLba+uint32(sector)] = data[sector*LogicalSectorBytes : (sector+1)*LogicalSectorBytes]
		}
	}
}

// udfDispersedFileEntry is udfFileEntry with an explicit, possibly fragmented
// extent list instead of the single contiguous run starting at node.dataLba.
// Each caller extent emits one short_ad (8 bytes: length with high-2-bits
// extent type 0, position = LBN = Lba - partitionStart), in extent order;
// over-long extents split at UdfMaximumExtentBytes exactly like udfFileEntry.
// dataLength and the blocks-recorded field (U64 at offset 64) stay the file's
// full size/sector count. Empty files emit no allocation descriptors, exactly
// like udfFileEntry.
func udfDispersedFileEntry(plan *UdfLayoutPlan, node *udfNode, extents []UdfFileExtent) []byte {
	data := make([]byte, LogicalSectorBytes)
	udfU16(data, 20, 4) // ICB strategy type 4, short allocation descriptors.
	udfU16(data, 24, 1)
	if node.isDirectory {
		data[27] = 4
	} else if node.isSymlink {
		data[27] = 12 // symbolic link
	} else {
		data[27] = 5
	}
	parent := node.parent
	if parent == nil {
		parent = node
	}
	udfU32(data, 28, parent.entryLba-plan.partitionStartLba)
	udfU16(data, 34, 0) // AD type 0 (short_ad); no optional ICB flags.
	udfU32(data, 36, math.MaxUint32)
	udfU32(data, 40, math.MaxUint32)
	if node.isDirectory {
		udfU32(data, 44, 0x14a5) // Read (and directory traverse).
	} else {
		udfU32(data, 44, 0x1084)
	}
	linkCount := 1
	if node.isDirectory {
		for _, child := range node.children {
			if child.isDirectory {
				linkCount++
			}
		}
	}
	udfU16(data, 48, uint16(linkCount))
	udfU64(data, 56, uint64(node.dataLength))
	udfU64(data, 64, uint64(sectorCount(node.dataLength)))
	udfTimestamp(data[72:84], node.timestamp)
	udfTimestamp(data[84:96], node.timestamp)
	udfTimestamp(data[96:108], node.timestamp)
	udfU32(data, 108, 1)
	udfEntity(data[128:160], udfImplementationId, false, false)
	udfU64(data, 160, node.uniqueId)
	offset := 176
	for _, extent := range extents {
		length := int64(extent.Sectors) * LogicalSectorBytes
		if tail := node.dataLength - extent.Offset; length > tail {
			length = tail
		}
		lbn := extent.Lba - plan.partitionStartLba
		for length > 0 {
			chunk := length
			if chunk > UdfMaximumExtentBytes {
				chunk = UdfMaximumExtentBytes
			}
			udfU32(data, offset, uint32(chunk))
			udfU32(data, offset+4, lbn)
			length -= chunk
			lbn += uint32(sectorCount(chunk))
			offset += 8
		}
	}
	udfU32(data, 172, uint32(offset-176))
	udfSetTag(data, 261, node.entryLba-plan.partitionStartLba, offset)
	return data
}
