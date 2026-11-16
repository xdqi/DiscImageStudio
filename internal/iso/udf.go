// udf.go ports DiscImageStudio.Iso/UdfImageWriter.cs: a closed, read-only
// UDF 2.01 volume writer that plans a volume and writes it without seeking.
// ECMA-167 (3rd edition), ECMA TR/112-4 (UDF 2.01).
package iso

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Volume layout (2048-byte sectors), per ECMA-167 (3rd edition) and UDF 2.01
// (ECMA TR/112-4): the Volume Recognition Sequence follows the 32 KiB system
// area; the Anchor Volume Descriptor Pointer sits at the fixed sector 256 and
// again at N-257 (reserve) and N-1.
const (
	// UdfVrsLba is the first sector of the ECMA-167 Volume Recognition
	// Sequence (BEA01, NSR03, TEA01), right after the 32 KiB system area.
	UdfVrsLba = 16
	// UdfVrsSectors is the Volume Recognition Sequence length in sectors.
	UdfVrsSectors = 3
	// UdfMainVdsLba is the fixed main Volume Descriptor Sequence location
	// (the anchor VDP points here).
	UdfMainVdsLba = 32
	// UdfVdsSectors is the Volume Descriptor Sequence length in sectors.
	UdfVdsSectors = 16
	// UdfIntegrityLba is the Logical Volume Integrity Sequence location.
	UdfIntegrityLba = 48
	// UdfIntegritySectors is the integrity sequence length in sectors.
	UdfIntegritySectors = 2
	// UdfAnchorLba is the fixed Anchor Volume Descriptor Pointer sector.
	UdfAnchorLba = 256
	// UdfFirstPartitionLba is the first sector of the physical partition;
	// the range between the anchor and it holds the partition metadata.
	UdfFirstPartitionLba = 288
	// UdfReserveTailSectors is the reserved tail length: the reserve anchor
	// at N-257, then the reserve VDS and the final anchor at N-1.
	UdfReserveTailSectors = 257

	// UdfMaximumExtentBytes is the largest byte length one short_ad covers
	// (the top 2 bits of the length word are the extent type).
	UdfMaximumExtentBytes = 0x3ffff800
	udfImplementationId   = "*DiscImageStudio"

	udfCopyBufferBytes = 128 * 1024
)

// udfNode ports UdfImageWriter.Node.
type udfNode struct {
	sourcePath  string
	name        string
	isDirectory bool
	parent      *udfNode
	children    []*udfNode
	identifier  []byte
	timestamp   time.Time
	uniqueId    uint64
	entryLba    uint32
	dataLba     uint32
	dataLength  int64
	// Symlinks (UDF file type 12) store the target as CS0 path components at
	// their data extent, like a regular file's content.
	isSymlink   bool
	linkTarget  string
	linkContent []byte
	// mode carries the source permission bits (Unix rwxrwxrwx). Used by the
	// dispersed writer; the contiguous writer keeps the C# fixed defaults.
	mode uint32
}

// planUdf ports UdfImageWriter.Plan. An empty sourceDirectory is the C#
// null (a volume without files). The timestamp is always UtcNow.
func planUdf(sourceDirectory string, volumeSectors uint32, volumeLabel string, filesAtOuterRing bool) (*UdfLayoutPlan, error) {
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

	// Ownership defaults to uid:gid 0:0 (root) and each node's mode is copied
	// from the source. This deliberately differs from the original .NET CLI
	// (uint.MaxValue uid/gid, fixed permissions); golden/CLI comparisons mask
	// those bytes.
	root := &udfNode{sourcePath: sourcePath, isDirectory: true, timestamp: recorded, mode: 0o555}
	if sourceDirectory != "" {
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return nil, err
		}
		root.timestamp = info.ModTime().UTC()
		root.mode = uint32(info.Mode().Perm())
	}
	nodes := []*udfNode{root}
	// Iterative traversal avoids a call-stack dependency on the depth of the source tree.
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
				mode:        uint32(entry.info.Mode().Perm()),
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

	cursor := uint64(UdfFirstPartitionLba + 2)
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
		// A single-layer Blu-ray needs at most 24 descriptors. Fail explicitly if this
		// generic writer is ever used beyond one FE's capacity instead of truncating it.
		if (node.dataLength+UdfMaximumExtentBytes-1)/UdfMaximumExtentBytes >
			(LogicalSectorBytes-176)/8 {
			return nil, fmt.Errorf("File needs more allocation descriptors than one UDF file entry supports: %s", node.sourcePath)
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
	for _, node := range nodes {
		if node.isDirectory {
			continue
		}
		if cursor > math.MaxUint32 {
			return nil, errOverflow
		}
		node.dataLba = uint32(cursor)
		cursor += uint64(sectorCount(node.dataLength))
	}
	drawingEnd := uint64(volumeSectors-UdfReserveTailSectors) / 32 * 32
	drawingStart := (cursor + 31) / 32 * 32
	if drawingStart >= drawingEnd {
		return nil, fmt.Errorf("Files and UDF metadata need %d sectors, leaving no complete drawing cluster before the backup metadata at sector %d.", cursor, drawingEnd)
	}
	partitionStart := uint64(UdfFirstPartitionLba)
	if filesAtOuterRing {
		// Relocate the entire physical partition, keeping picture bytes outside it.
		// Its relative addresses stay identical; only the descriptor's partition
		// origin and the absolute sequential write positions change.
		partitionSectors := cursor - UdfFirstPartitionLba
		partitionStart = (drawingEnd - partitionSectors) / 32 * 32
		shift := partitionStart - UdfFirstPartitionLba
		for _, node := range nodes {
			node.entryLba += uint32(shift)
			node.dataLba += uint32(shift)
		}
		cursor += shift
		drawingStart = UdfFirstPartitionLba
		drawingEnd = partitionStart
	}
	label := volumeLabel
	if strings.TrimSpace(label) == "" {
		label = "BD_IMAGE"
	}
	// All descriptors use precisely the same logical volume identifier.
	label, err := udfFitDstring(label, 128)
	if err != nil {
		return nil, err
	}

	fileCount := 0
	var fileBytes int64
	for _, node := range nodes {
		if !node.isDirectory {
			fileCount++
			fileBytes += node.dataLength
		}
	}
	return &UdfLayoutPlan{
		sourceDirectory:        sourcePath,
		volumeSectors:          volumeSectors,
		volumeLabel:            label,
		timestamp:              recorded,
		fileCount:              fileCount,
		directoryCount:         len(nodes) - fileCount,
		fileBytes:              fileBytes,
		dataEndLbaExclusive:    uint32(cursor),
		drawingStartLba:        uint32(drawingStart),
		drawingEndLbaExclusive: uint32(drawingEnd),
		filesAtOuterRing:       filesAtOuterRing,
		partitionStartLba:      uint32(partitionStart),
		nodes:                  nodes,
	}, nil
}

// writeUdfSequentialPrefix ports UdfImageWriter.WriteSequentialPrefix.
func writeUdfSequentialPrefix(plan *UdfLayoutPlan, output io.Writer, endLbaExclusive uint32, ctx context.Context) error {
	if err := udfValidateOutput(plan, output); err != nil {
		return err
	}
	minimumEnd := plan.dataEndLbaExclusive
	if plan.filesAtOuterRing {
		minimumEnd = plan.drawingStartLba
	}
	if endLbaExclusive < minimumEnd || endLbaExclusive > plan.drawingEndLbaExclusive {
		return errors.New("Specified argument was out of the range of valid values. (Parameter 'endLbaExclusive')")
	}
	if err := udfValidateSources(plan, ctx); err != nil {
		return err
	}
	writer := newUdfSequentialWriter(output, 0, ctx)
	if err := writer.writeAtLba(UdfVrsLba, udfVolumeRecognition("BEA01")); err != nil {
		return err
	}
	if err := writer.writeAtLba(UdfVrsLba+1, udfVolumeRecognition("NSR03")); err != nil {
		return err
	}
	if err := writer.writeAtLba(UdfVrsLba+2, udfVolumeRecognition("TEA01")); err != nil {
		return err
	}
	if err := udfWriteVolumeDescriptors(plan, writer, UdfMainVdsLba); err != nil {
		return err
	}
	if err := writer.writeAtLba(UdfIntegrityLba, udfIntegrityDescriptor(plan)); err != nil {
		return err
	}
	if err := writer.writeAtLba(UdfIntegrityLba+1, udfDescriptor(8, UdfIntegrityLba+1, 512)); err != nil {
		return err
	}
	if err := writer.writeAtLba(UdfAnchorLba, udfAnchor(plan, UdfAnchorLba)); err != nil {
		return err
	}
	if !plan.filesAtOuterRing {
		if err := udfWritePartition(plan, writer); err != nil {
			return err
		}
	}
	if err := writer.padToLba(endLbaExclusive); err != nil {
		return err
	}
	return udfValidateSources(plan, ctx)
}

func udfWritePartition(plan *UdfLayoutPlan, writer *udfSequentialWriter) error {
	fileSet, err := udfFileSetDescriptor(plan)
	if err != nil {
		return err
	}
	if err := writer.writeAtLba(plan.partitionStartLba, fileSet); err != nil {
		return err
	}
	if err := writer.writeAtLba(plan.partitionStartLba+1, udfDescriptor(8, 1, 512)); err != nil {
		return err
	}
	for _, node := range plan.nodes {
		if err := writer.writeAtLba(node.entryLba, udfFileEntry(plan, node)); err != nil {
			return err
		}
	}
	for _, directory := range plan.nodes {
		if !directory.isDirectory {
			continue
		}
		if err := writer.padToLba(directory.dataLba); err != nil {
			return err
		}
		directoryOffset := int64(0)
		writeIdentifier := func(target *udfNode, identifier []byte, parent bool) error {
			location := directory.dataLba - plan.partitionStartLba +
				uint32(directoryOffset/LogicalSectorBytes)
			record := udfFileIdentifier(plan, target, identifier, parent, location, directoryOffset)
			if err := writer.write(record); err != nil {
				return err
			}
			directoryOffset += int64(len(record))
			return nil
		}
		parent := directory.parent
		if parent == nil {
			parent = directory
		}
		if err := writeIdentifier(parent, nil, true); err != nil {
			return err
		}
		for _, child := range directory.children {
			if err := writeIdentifier(child, child.identifier, false); err != nil {
				return err
			}
		}
	}
	for _, file := range plan.nodes {
		if file.isDirectory {
			continue
		}
		if err := writer.writeFile(file); err != nil {
			return err
		}
	}
	return nil
}

// writeUdfSequentialSuffix ports UdfImageWriter.WriteSequentialSuffix.
func writeUdfSequentialSuffix(plan *UdfLayoutPlan, output io.Writer, startLba uint32, ctx context.Context) error {
	if err := udfValidateOutput(plan, output); err != nil {
		return err
	}
	if startLba != plan.drawingEndLbaExclusive {
		return errors.New("The UDF suffix must start at DrawingEndLbaExclusive. (Parameter 'startLba')")
	}
	if err := checkCancelled(ctx); err != nil {
		return err
	}
	writer := newUdfSequentialWriter(output, startLba, ctx)
	if plan.filesAtOuterRing {
		if err := udfValidateSources(plan, ctx); err != nil {
			return err
		}
		if err := udfWritePartition(plan, writer); err != nil {
			return err
		}
		if err := udfValidateSources(plan, ctx); err != nil {
			return err
		}
	}
	if err := writer.writeAtLba(plan.volumeSectors-UdfReserveTailSectors, udfAnchor(plan, plan.volumeSectors-UdfReserveTailSectors)); err != nil {
		return err
	}
	if err := udfWriteVolumeDescriptors(plan, writer, plan.ReserveVdsLba()); err != nil {
		return err
	}
	if err := writer.writeAtLba(plan.volumeSectors-1, udfAnchor(plan, plan.volumeSectors-1)); err != nil {
		return err
	}
	return writer.padToLba(plan.volumeSectors)
}

func udfWriteVolumeDescriptors(plan *UdfLayoutPlan, writer *udfSequentialWriter, start uint32) error {
	primary, err := udfPrimaryDescriptor(plan, start)
	if err != nil {
		return err
	}
	if err := writer.writeAtLba(start, primary); err != nil {
		return err
	}
	implementation, err := udfImplementationDescriptor(plan, start+1)
	if err != nil {
		return err
	}
	if err := writer.writeAtLba(start+1, implementation); err != nil {
		return err
	}
	if err := writer.writeAtLba(start+2, udfPartitionDescriptor(plan, start+2)); err != nil {
		return err
	}
	logical, err := udfLogicalVolumeDescriptor(plan, start+3)
	if err != nil {
		return err
	}
	if err := writer.writeAtLba(start+3, logical); err != nil {
		return err
	}
	unallocated := make([]byte, LogicalSectorBytes)
	udfU32(unallocated, 16, 5)
	// Zero free extents: pattern bytes outside the partition are reserved volume space.
	udfSetTag(unallocated, 7, start+4, 24)
	if err := writer.writeAtLba(start+4, unallocated); err != nil {
		return err
	}
	if err := writer.writeAtLba(start+5, udfDescriptor(8, start+5, 512)); err != nil {
		return err
	}
	return writer.padToLba(start + UdfVdsSectors)
}

func udfVolumeRecognition(identifier string) []byte {
	sector := make([]byte, LogicalSectorBytes)
	copy(sector[1:6], identifier)
	sector[6] = 1
	return sector
}

func udfPrimaryDescriptor(plan *UdfLayoutPlan, location uint32) ([]byte, error) {
	data := make([]byte, LogicalSectorBytes)
	udfU32(data, 16, 1)
	label32, err := udfFitDstring(plan.volumeLabel, 32)
	if err != nil {
		return nil, err
	}
	if err := udfDstring(data[24:56], label32); err != nil {
		return nil, err
	}
	udfU16(data, 56, 1)
	udfU16(data, 58, 1)
	udfU16(data, 60, 2)
	udfU16(data, 62, 3)
	udfU32(data, 64, 1)
	udfU32(data, 68, 1)
	volumeSet := fmt.Sprintf("%08X", uint32(plan.timestamp.Unix())) + "00000000"
	if err := udfDstring(data[72:200], volumeSet); err != nil {
		return nil, err
	}
	udfCharspec(data[200:264])
	udfCharspec(data[264:328])
	udfEntity(data[344:376], udfImplementationId, false, false)
	udfTimestamp(data[376:388], plan.timestamp)
	udfEntity(data[388:420], udfImplementationId, false, false)
	udfU16(data, 488, 1)
	udfSetTag(data, 1, location, 512)
	return data, nil
}

func udfImplementationDescriptor(plan *UdfLayoutPlan, location uint32) ([]byte, error) {
	data := make([]byte, LogicalSectorBytes)
	udfU32(data, 16, 2)
	udfEntity(data[20:52], "*UDF LV Info", false, true)
	udfCharspec(data[52:116])
	if err := udfDstring(data[116:244], plan.volumeLabel); err != nil {
		return nil, err
	}
	udfEntity(data[352:384], udfImplementationId, false, false)
	udfSetTag(data, 4, location, 512)
	return data, nil
}

func udfPartitionDescriptor(plan *UdfLayoutPlan, location uint32) []byte {
	data := make([]byte, LogicalSectorBytes)
	udfU32(data, 16, 3)
	udfU16(data, 20, 1)
	udfEntity(data[24:56], "+NSR03", false, false)
	udfU32(data, 184, 1) // Read-only partition; there is no allocatable free space.
	udfU32(data, 188, plan.partitionStartLba)
	udfU32(data, 192, plan.PartitionSectors())
	udfEntity(data[196:228], udfImplementationId, false, false)
	udfSetTag(data, 5, location, 512)
	return data
}

func udfLogicalVolumeDescriptor(plan *UdfLayoutPlan, location uint32) ([]byte, error) {
	data := make([]byte, LogicalSectorBytes)
	udfU32(data, 16, 4)
	udfCharspec(data[20:84])
	if err := udfDstring(data[84:212], plan.volumeLabel); err != nil {
		return nil, err
	}
	udfU32(data, 212, LogicalSectorBytes)
	udfEntity(data[216:248], "*OSTA UDF Compliant", true, false)
	udfExtent(data[248:264], LogicalSectorBytes*2, 0)
	udfU32(data, 264, 6)
	udfU32(data, 268, 1)
	udfEntity(data[272:304], udfImplementationId, false, false)
	udfExtent(data[432:440], LogicalSectorBytes*4, UdfIntegrityLba)
	data[440] = 1
	data[441] = 6
	udfU16(data, 442, 1) // Volume sequence number; partition number is zero.
	udfSetTag(data, 6, location, 446)
	return data, nil
}

func udfIntegrityDescriptor(plan *UdfLayoutPlan) []byte {
	data := make([]byte, LogicalSectorBytes)
	udfTimestamp(data[16:28], plan.timestamp)
	udfU32(data, 28, 1) // Closed logical volume.
	udfU64(data, 40, uint64(len(plan.nodes))+15)
	udfU32(data, 72, 1)
	udfU32(data, 76, 46)
	udfU32(data, 84, plan.PartitionSectors())
	udfEntity(data[88:120], udfImplementationId, false, false)
	udfU32(data, 120, uint32(plan.fileCount))
	udfU32(data, 124, uint32(plan.directoryCount))
	udfU16(data, 128, 0x0201)
	udfU16(data, 130, 0x0201)
	udfU16(data, 132, 0x0201)
	udfSetTag(data, 9, UdfIntegrityLba, 134)
	return data
}

func udfAnchor(plan *UdfLayoutPlan, location uint32) []byte {
	data := make([]byte, LogicalSectorBytes)
	udfExtent(data[16:24], UdfVdsSectors*LogicalSectorBytes, UdfMainVdsLba)
	udfExtent(data[24:32], UdfVdsSectors*LogicalSectorBytes, plan.ReserveVdsLba())
	udfSetTag(data, 2, location, 512)
	return data
}

func udfFileSetDescriptor(plan *UdfLayoutPlan) ([]byte, error) {
	data := make([]byte, LogicalSectorBytes)
	udfTimestamp(data[16:28], plan.timestamp)
	udfU16(data, 28, 3)
	udfU16(data, 30, 3)
	udfU32(data, 32, 1)
	udfU32(data, 36, 1)
	udfCharspec(data[48:112])
	if err := udfDstring(data[112:240], plan.volumeLabel); err != nil {
		return nil, err
	}
	udfCharspec(data[240:304])
	label32, err := udfFitDstring(plan.volumeLabel, 32)
	if err != nil {
		return nil, err
	}
	if err := udfDstring(data[304:336], label32); err != nil {
		return nil, err
	}
	udfExtent(data[400:416], LogicalSectorBytes, plan.nodes[0].entryLba-plan.partitionStartLba)
	udfEntity(data[416:448], "*OSTA UDF Compliant", true, false)
	udfSetTag(data, 256, 0, 512)
	return data, nil
}

// udfPermissionsFromMode maps Unix rwxrwxrwx permission bits to the
// ECMA-167 file entry permission word (bits 0-4 others, 5-9 group, 10-14
// user; within each group: execute, write, read, [change attribute],
// [delete]). The historical fixed defaults 0o444/0o555 map to 0x1084/0x14a5.
func udfPermissionsFromMode(mode uint32) uint32 {
	var permissions uint32
	if mode&0o400 != 0 {
		permissions |= 1 << 12
	}
	if mode&0o200 != 0 {
		permissions |= 1 << 11
	}
	if mode&0o100 != 0 {
		permissions |= 1 << 10
	}
	if mode&0o040 != 0 {
		permissions |= 1 << 7
	}
	if mode&0o020 != 0 {
		permissions |= 1 << 6
	}
	if mode&0o010 != 0 {
		permissions |= 1 << 5
	}
	if mode&0o004 != 0 {
		permissions |= 1 << 2
	}
	if mode&0o002 != 0 {
		permissions |= 1 << 1
	}
	if mode&0o001 != 0 {
		permissions |= 1
	}
	return permissions
}

func udfFileEntry(plan *UdfLayoutPlan, node *udfNode) []byte {
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
	// Ownership defaults to uid:gid 0:0 (root); the mode is copied from the
	// source. Symlinks keep the fixed read-only word (their source mode is
	// the link's own, not the target's).
	udfU32(data, 36, 0)
	udfU32(data, 40, 0)
	if node.isSymlink {
		udfU32(data, 44, 0x1084)
	} else {
		udfU32(data, 44, udfPermissionsFromMode(node.mode))
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
	remaining := node.dataLength
	lbn := node.dataLba - plan.partitionStartLba
	for remaining > 0 {
		length := remaining
		if length > UdfMaximumExtentBytes {
			length = UdfMaximumExtentBytes
		}
		udfU32(data, offset, uint32(length))
		udfU32(data, offset+4, lbn)
		remaining -= length
		lbn += uint32(sectorCount(length))
		offset += 8
	}
	udfU32(data, 172, uint32(offset-176))
	udfSetTag(data, 261, node.entryLba-plan.partitionStartLba, offset)
	return data
}

func udfFileIdentifier(plan *UdfLayoutPlan, target *udfNode, identifier []byte, parent bool, location uint32,
	directoryOffset int64) []byte {
	data := make([]byte, udfFileIdentifierLength(len(identifier), directoryOffset))
	implementationBytes := len(data) - ((38 + len(identifier) + 3) / 4 * 4)
	udfU16(data, 16, 1)
	switch {
	case parent:
		data[18] = 10
	case target.isDirectory:
		data[18] = 2
	default:
		data[18] = 0
	}
	data[19] = byte(len(identifier))
	udfExtent(data[20:36], LogicalSectorBytes, target.entryLba-plan.partitionStartLba)
	// long_ad ImplementationUse: 2 flag bytes followed by the UDF unique ID.
	udfU32(data, 32, uint32(target.uniqueId))
	udfU16(data, 36, uint16(implementationBytes))
	if implementationBytes > 0 {
		udfEntity(data[38:70], udfImplementationId, false, false)
	}
	copy(data[38+implementationBytes:], identifier)
	udfSetTag(data, 257, location, len(data))
	return data
}

func udfFileIdentifierLength(identifierBytes int, directoryOffset int64) int {
	length := (38 + identifierBytes + 3) / 4 * 4
	nextOffset := (directoryOffset + int64(length)) % LogicalSectorBytes
	// UDF 2.01 section 2.3.4.4: on write-once media the next FID's 16-byte
	// descriptor tag cannot straddle a block. A nonempty IU field is at least 32 bytes.
	if nextOffset > LogicalSectorBytes-16 {
		return length + 32
	}
	return length
}

// udfEncodeSymlinkTarget encodes a link target as UDF path components
// (ECMA-167 4/14.16.2), matching the Linux fs/udf/symlink.c reading: records
// of {componentType, lengthComponentIdent, componentFileVersionNum u16le,
// componentIdent} with type 1=root, 3="..", 4=".", 5=name (CS0 ident).
func udfEncodeSymlinkTarget(target string) ([]byte, error) {
	var out []byte
	appendComponent := func(componentType byte, ident []byte) error {
		if len(ident) > 255 {
			return fmt.Errorf("UDF symlink path component too long: %s", target)
		}
		out = append(out, componentType, byte(len(ident)), 0, 0)
		out = append(out, ident...)
		return nil
	}
	rest := target
	for len(rest) > 0 {
		if strings.HasPrefix(rest, "/") {
			// Root (or collapse "//"): one leading slash marks the volume root.
			if len(out) == 0 {
				if err := appendComponent(1, nil); err != nil {
					return nil, err
				}
			}
			rest = strings.TrimLeft(rest, "/")
			continue
		}
		end := strings.IndexByte(rest, '/')
		var component string
		if end < 0 {
			component, rest = rest, ""
		} else {
			component, rest = rest[:end], rest[end+1:]
		}
		switch component {
		case "", ".":
			if err := appendComponent(4, nil); err != nil {
				return nil, err
			}
		case "..":
			if err := appendComponent(3, nil); err != nil {
				return nil, err
			}
		default:
			ident, err := udfEncodeIdentifier(component)
			if err != nil {
				return nil, err
			}
			if err := appendComponent(5, ident); err != nil {
				return nil, err
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("UDF symlink target is empty: %s", target)
	}
	if len(out) > LogicalSectorBytes {
		return nil, fmt.Errorf("UDF symlink target does not fit one logical block: %s", target)
	}
	return out, nil
}

// udfEncodeIdentifier ports UdfImageWriter.EncodeIdentifier (CS0 compressed
// Unicode, 8- or 16-bit per character).
func udfEncodeIdentifier(text string) ([]byte, error) {
	narrow := true
	for _, r := range text {
		if r == 0 || r == '\uFEFF' || r == '\uFFFE' || r > 0xFFFF {
			return nil, fmt.Errorf("UDF 2.01 filenames must use CS0 Unicode BMP characters without byte-order marks: %s", text)
		}
		if r > 0xFF {
			narrow = false
		}
	}
	length := 1 + utf16Length(text)
	if !narrow {
		length = 1 + utf16Length(text)*2
	}
	if length > math.MaxUint8 {
		return nil, fmt.Errorf("Filename exceeds the UDF CS0 identifier limit (254 single-byte or 127 two-byte characters): %s", text)
	}
	bytes := make([]byte, length)
	if narrow {
		bytes[0] = 8
		index := 1
		for _, r := range text {
			bytes[index] = byte(r)
			index++
		}
	} else {
		bytes[0] = 16
		copy(bytes[1:], bigEndianUnicode(text))
	}
	return bytes, nil
}

// udfFitDstring ports UdfImageWriter.FitDstring.
func udfFitDstring(text string, fieldBytes int) (string, error) {
	narrow := true
	for _, r := range text {
		if r == 0 || r == '\uFEFF' || r == '\uFFFE' || r > 0xFFFF {
			return "", errors.New("UDF volume labels must use CS0 Unicode BMP characters without byte-order marks.")
		}
		if r > 0xFF {
			narrow = false
		}
	}
	characters := fieldBytes - 2
	if !narrow {
		characters = (fieldBytes - 2) / 2
	}
	if utf16Length(text) <= characters {
		return text, nil
	}
	return truncateUTF16(text, characters), nil
}

// udfDstring ports UdfImageWriter.Dstring: a CS0 identifier with the used
// byte count in the field's last byte.
func udfDstring(field []byte, text string) error {
	if len(text) == 0 {
		return nil
	}
	bytes, err := udfEncodeIdentifier(text)
	if err != nil {
		return err
	}
	if len(bytes) >= len(field) {
		return errors.New("UDF dstring does not fit its descriptor field.")
	}
	copy(field, bytes)
	field[len(field)-1] = byte(len(bytes))
	return nil
}

func udfCharspec(field []byte) {
	copy(field[1:], "OSTA Compressed Unicode")
}

func udfEntity(field []byte, id string, domain bool, udfRevision bool) {
	copy(field[1:24], id)
	if domain || udfRevision {
		binary.LittleEndian.PutUint16(field[24:26], 0x0201)
	}
	if domain {
		field[26] = 1 // Permanently closed volume; hard write protected.
	}
}

// udfTimestamp ports UdfImageWriter.Timestamp. Ticks are 100-ns units since
// 0001-01-01.
func udfTimestamp(field []byte, timestamp time.Time) {
	utc := timestamp.UTC()
	binary.LittleEndian.PutUint16(field[0:2], 0x1000) // Local time with UTC offset zero.
	binary.LittleEndian.PutUint16(field[2:4], uint16(utc.Year()))
	field[4] = byte(utc.Month())
	field[5] = byte(utc.Day())
	field[6] = byte(utc.Hour())
	field[7] = byte(utc.Minute())
	field[8] = byte(utc.Second())
	ticks := (utc.Unix()+62135596800)*10000000 + int64(utc.Nanosecond())/100
	field[9] = byte(utc.Nanosecond() / 1000000 / 10)
	field[10] = byte((ticks / 1000) % 100)
	field[11] = byte((ticks / 10) % 100)
}

func udfExtent(data []byte, length uint32, lba uint32) {
	binary.LittleEndian.PutUint32(data[0:4], length)
	binary.LittleEndian.PutUint32(data[4:8], lba)
}

func udfU16(data []byte, offset int, value uint16) {
	binary.LittleEndian.PutUint16(data[offset:offset+2], value)
}

func udfU32(data []byte, offset int, value uint32) {
	binary.LittleEndian.PutUint32(data[offset:offset+4], value)
}

func udfU64(data []byte, offset int, value uint64) {
	binary.LittleEndian.PutUint64(data[offset:offset+8], value)
}

func udfDescriptor(id uint16, location uint32, descriptorLength int) []byte {
	data := make([]byte, LogicalSectorBytes)
	udfSetTag(data, id, location, descriptorLength)
	return data
}

// udfSetTag ports UdfImageWriter.SetTag: descriptor tag with CRC-CCITT over
// bytes 16..descriptorLength and the checksum over the first 16 bytes
// skipping index 4.
func udfSetTag(data []byte, id uint16, location uint32, descriptorLength int) {
	udfU16(data, 0, id)
	udfU16(data, 2, 3) // ECMA-167 third edition.
	udfU16(data, 6, 1)
	var crc uint16
	for i := 16; i < descriptorLength; i++ {
		crc ^= uint16(data[i]) << 8
		for bit := 0; bit < 8; bit++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc = crc << 1
			}
		}
	}
	udfU16(data, 8, crc)
	udfU16(data, 10, uint16(descriptorLength-16))
	udfU32(data, 12, location)
	checksum := 0
	for i := 0; i < 16; i++ {
		if i != 4 {
			checksum += int(data[i])
		}
	}
	data[4] = byte(checksum)
}

func udfValidateOutput(plan *UdfLayoutPlan, output io.Writer) error {
	if plan == nil {
		return errors.New("Value cannot be null. (Parameter 'plan')")
	}
	if output == nil {
		return errors.New("Value cannot be null. (Parameter 'output')")
	}
	return nil
}

func udfRejectReparsePoint(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Reparse points are not supported in the UDF data directory: %s", path)
	}
	return nil
}

func udfValidateSources(plan *UdfLayoutPlan, ctx context.Context) error {
	for _, node := range plan.nodes {
		if err := checkCancelled(ctx); err != nil {
			return err
		}
		if len(node.sourcePath) == 0 {
			continue
		}
		if node.isDirectory {
			if err := udfRejectReparsePoint(node.sourcePath); err != nil {
				return err
			}
			info, err := os.Stat(node.sourcePath)
			if err != nil || !info.IsDir() {
				return fmt.Errorf("Source directory changed after UDF planning: %s", node.sourcePath)
			}
			// NTFS can return a stale last-write timestamp from parent enumeration.
			// Check the actual children instead, which also catches additions and removals.
			expected := make(map[string]*udfNode, len(node.children))
			for _, child := range node.children {
				expected[child.name] = child
			}
			count := 0
			entries, err := os.ReadDir(node.sourcePath)
			if err != nil {
				return fmt.Errorf("Source directory changed after UDF planning: %s", node.sourcePath)
			}
			for _, entry := range entries {
				if err := checkCancelled(ctx); err != nil {
					return err
				}
				entryInfo, err := entry.Info()
				if err != nil {
					return fmt.Errorf("Source directory changed after UDF planning: %s", node.sourcePath)
				}
				child, ok := expected[entry.Name()]
				isSymlinkNow := entryInfo.Mode()&os.ModeSymlink != 0
				if !ok || entryInfo.IsDir() != child.isDirectory || isSymlinkNow != child.isSymlink {
					return fmt.Errorf("Source directory changed after UDF planning: %s", node.sourcePath)
				}
				count++
			}
			if count != len(expected) {
				return fmt.Errorf("Source directory changed after UDF planning: %s", node.sourcePath)
			}
		} else {
			if err := udfValidateFile(node); err != nil {
				return err
			}
		}
	}
	return nil
}

func udfValidateFile(file *udfNode) error {
	for directory := file.parent; directory != nil; directory = directory.parent {
		if err := udfRejectReparsePoint(directory.sourcePath); err != nil {
			return err
		}
	}
	if file.isSymlink {
		target, err := os.Readlink(file.sourcePath)
		if err != nil || target != file.linkTarget {
			return fmt.Errorf("Source file changed after UDF planning: %s", file.sourcePath)
		}
		return nil
	}
	info, err := os.Lstat(file.sourcePath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() ||
		info.Size() != file.dataLength ||
		info.ModTime().UTC().UnixNano()/100 != file.timestamp.UnixNano()/100 {
		return fmt.Errorf("Source file changed after UDF planning: %s", file.sourcePath)
	}
	return nil
}

// udfSequentialWriter ports UdfImageWriter.SequentialWriter.
type udfSequentialWriter struct {
	output   io.Writer
	ctx      context.Context
	buffer   []byte
	zero     [udfCopyBufferBytes]byte
	position int64
}

func newUdfSequentialWriter(output io.Writer, startLba uint32, ctx context.Context) *udfSequentialWriter {
	return &udfSequentialWriter{
		output:   output,
		ctx:      ctx,
		buffer:   make([]byte, udfCopyBufferBytes),
		position: int64(startLba) * LogicalSectorBytes,
	}
}

func (w *udfSequentialWriter) writeAtLba(lba uint32, data []byte) error {
	if err := w.padToLba(lba); err != nil {
		return err
	}
	return w.write(data)
}

func (w *udfSequentialWriter) write(data []byte) error {
	if err := checkCancelled(w.ctx); err != nil {
		return err
	}
	if err := writeAll(w.output, data); err != nil {
		return err
	}
	w.position += int64(len(data))
	return nil
}

func (w *udfSequentialWriter) writeFile(file *udfNode) error {
	if err := w.padToLba(file.dataLba); err != nil {
		return err
	}
	if err := udfValidateFile(file); err != nil {
		return err
	}
	if file.isSymlink {
		return w.write(file.linkContent)
	}
	input, err := os.Open(file.sourcePath)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if info.Size() != file.dataLength {
		return fmt.Errorf("Source file size changed while streaming UDF: %s", file.sourcePath)
	}
	remaining := file.dataLength
	for remaining > 0 {
		if err := checkCancelled(w.ctx); err != nil {
			return err
		}
		want := int64(len(w.buffer))
		if remaining < want {
			want = remaining
		}
		count, err := input.Read(w.buffer[:want])
		if err != nil && err != io.EOF {
			return err
		}
		if count == 0 {
			return fmt.Errorf("Source file ended early while streaming UDF: %s", file.sourcePath)
		}
		if err := w.write(w.buffer[:count]); err != nil {
			return err
		}
		remaining -= int64(count)
	}
	info, err = input.Stat()
	if err != nil {
		return err
	}
	if info.Size() != file.dataLength {
		return fmt.Errorf("Source file size changed while streaming UDF: %s", file.sourcePath)
	}
	return udfValidateFile(file)
}

func (w *udfSequentialWriter) padToLba(lba uint32) error {
	target := int64(lba) * LogicalSectorBytes
	if target < w.position {
		return fmt.Errorf("Sequential UDF layout moved backwards from %d to %d.", w.position, target)
	}
	for w.position < target {
		count := target - w.position
		if count > int64(len(w.zero)) {
			count = int64(len(w.zero))
		}
		if err := w.write(w.zero[:count]); err != nil {
			return err
		}
	}
	return nil
}
