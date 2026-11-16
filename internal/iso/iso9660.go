// iso9660.go ports DiscImageStudio.Iso/Iso9660ImageWriter.cs: a hybrid
// ISO9660 + Joliet image writer that plans a volume and writes it without
// seeking.
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
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"
)

const (
	primaryVolumeDescriptorLba       uint32 = 16
	supplementaryVolumeDescriptorLba uint32 = 17
	terminatorLba                    uint32 = 18
	firstMetadataLba                 uint32 = 19

	maximumPrimaryIdentifierCharacters = 31
	maximumJolietIdentifierCharacters  = 60

	isoCopyBufferBytes = 128 * 1024
)

// errOverflow is the C# checked-overflow (OverflowException) message.
var errOverflow = errors.New("Arithmetic operation resulted in an overflow.")

// nowFunc is the plan timestamp source (C# DateTimeOffset.UtcNow). Tests
// override it to reproduce golden images byte-for-byte.
var nowFunc = func() time.Time { return time.Now().UTC() }

// checkCancelled is CancellationToken.ThrowIfCancellationRequested.
func checkCancelled(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// writeAll writes the whole buffer; io.Writer.Write must not return
// n < len(b) with a nil error, but stay defensive like Stream.Write.
func writeAll(output io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := output.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// sectorCount is Iso9660ImageWriter/UdfImageWriter.SectorCount.
func sectorCount(byteCount int64) int64 {
	return (byteCount + LogicalSectorBytes - 1) / LogicalSectorBytes
}

// utf16Length counts UTF-16 code units, matching C# string.Length.
func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// truncateUTF16 ports C# Truncate: value[..maximumCharacters] measured in
// UTF-16 units. It never splits a surrogate pair; every caller normalizes
// astral characters away before truncating, so this cannot diverge.
func truncateUTF16(value string, maximumCharacters int) string {
	units := 0
	for i, r := range value {
		u := 1
		if r >= 0x10000 {
			u = 2
		}
		if units+u > maximumCharacters {
			return value[:i]
		}
		units += u
	}
	return value
}

// bigEndianUnicode is Encoding.BigEndianUnicode.GetBytes (manual UTF-16BE).
func bigEndianUnicode(s string) []byte {
	units := utf16.Encode([]rune(s))
	b := make([]byte, len(units)*2)
	for i, u := range units {
		binary.BigEndian.PutUint16(b[i*2:], u)
	}
	return b
}

// dirEntryInfo couples a directory entry name with its lstat FileInfo.
type dirEntryInfo struct {
	name string
	info os.FileInfo
}

// readDirSorted enumerates a directory like C# EnumerateFileSystemInfos
// followed by OrderBy/ThenBy. Entry info uses lstat semantics so symlinks
// are visible as reparse points.
func readDirSorted(path string, less func(a, b string) bool) ([]dirEntryInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	list := make([]dirEntryInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		list = append(list, dirEntryInfo{entry.Name(), info})
	}
	sort.SliceStable(list, func(i, j int) bool { return less(list[i].name, list[j].name) })
	return list, nil
}

// isoNameLess is OrderBy(Name, OrdinalIgnoreCase).ThenBy(Name, Ordinal).
func isoNameLess(a, b string) bool {
	ua, ub := strings.ToUpper(a), strings.ToUpper(b)
	if ua != ub {
		return ua < ub
	}
	return a < b
}

// isoNode ports Iso9660ImageWriter.IsoNode.
type isoNode struct {
	sourcePath            string
	name                  string
	isDirectory           bool
	parent                *isoNode
	children              []*isoNode
	primaryIdentifier     string
	jolietIdentifier      string
	directoryNumber       uint16
	primaryDirectoryLba   uint32
	primaryDirectoryBytes int
	jolietDirectoryLba    uint32
	jolietDirectoryBytes  int
	dataLba               uint32
	dataLength            int64
	timestamp             time.Time
}

// isoLayout ports Iso9660ImageWriter.Layout.
type isoLayout struct {
	primaryLittlePathTableLba uint32
	primaryBigPathTableLba    uint32
	jolietLittlePathTableLba  uint32
	jolietBigPathTableLba     uint32
	primaryPathTableBytes     int
	jolietPathTableBytes      int
	dataEndLbaExclusive       uint32
	metadataEndLbaExclusive   uint32
	fileDataStartLba          uint32
	filesAtOuterRing          bool
}

// planIso ports Iso9660ImageWriter.Plan. An empty sourceDirectory is the
// C# null (a volume without files). The timestamp is always UtcNow.
func planIso(sourceDirectory string, volumeSectors uint32, volumeLabel string, filesAtOuterRing bool) (*IsoLayoutPlan, error) {
	if volumeSectors <= firstMetadataLba {
		return nil, errors.New("Hybrid ISO volume is too small for filesystem metadata. (Parameter 'volumeSectors')")
	}

	primaryVolumeLabel := normalizePrimaryIdentifier(volumeLabel, 32, "DVD_IMAGE")
	jolietVolumeLabel, err := validateJolietVolumeLabel(volumeLabel)
	if err != nil {
		return nil, err
	}
	sourceFullPath := ""
	if sourceDirectory != "" {
		sourceFullPath, err = filepath.Abs(sourceDirectory)
		if err != nil {
			return nil, err
		}
		info, statErr := os.Stat(sourceFullPath)
		if statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("Data directory does not exist: %s", sourceFullPath)
		}
		linfo, lstatErr := os.Lstat(sourceFullPath)
		if lstatErr != nil {
			return nil, lstatErr
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Reparse points are not supported in --data-dir: %s", sourceFullPath)
		}
	}

	timestamp := nowFunc()
	var root *isoNode
	if sourceDirectory == "" {
		root = &isoNode{isDirectory: true, timestamp: timestamp}
	} else {
		root, err = isoReadTree(sourceFullPath)
		if err != nil {
			return nil, err
		}
	}
	directories, err := isoAssignDirectoryNumbers(root)
	if err != nil {
		return nil, err
	}
	if err := isoAssignIdentifiers(directories); err != nil {
		return nil, err
	}
	layout, err := isoBuildLayout(directories, volumeSectors, filesAtOuterRing)
	if err != nil {
		return nil, err
	}

	files := isoEnumerateFiles(root)
	var fileBytes int64
	for _, file := range files {
		fileBytes += file.dataLength
	}
	return &IsoLayoutPlan{
		sourceDirectory:         sourceFullPath,
		volumeSectors:           volumeSectors,
		fileCount:               len(files),
		directoryCount:          len(directories),
		fileBytes:               fileBytes,
		dataEndLbaExclusive:     layout.dataEndLbaExclusive,
		metadataEndLbaExclusive: layout.metadataEndLbaExclusive,
		fileDataStartLba:        layout.fileDataStartLba,
		filesAtOuterRing:        layout.filesAtOuterRing,
		root:                    root,
		directories:             directories,
		layout:                  layout,
		primaryVolumeLabel:      primaryVolumeLabel,
		jolietVolumeLabel:       jolietVolumeLabel,
		timestamp:               timestamp,
	}, nil
}

// writeIsoSequentialPrefix ports Iso9660ImageWriter.WriteSequentialPrefix.
func writeIsoSequentialPrefix(plan *IsoLayoutPlan, output io.Writer, endLbaExclusive uint32, ctx context.Context) error {
	if plan == nil {
		return errors.New("Value cannot be null. (Parameter 'plan')")
	}
	if output == nil {
		return errors.New("Value cannot be null. (Parameter 'output')")
	}
	if endLbaExclusive < plan.dataEndLbaExclusive || endLbaExclusive > plan.volumeSectors {
		return fmt.Errorf("Sequential prefix must end between LBA %d and %d. (Parameter 'endLbaExclusive')",
			plan.dataEndLbaExclusive, plan.volumeSectors)
	}

	if err := checkCancelled(ctx); err != nil {
		return err
	}
	for _, file := range isoEnumerateFiles(plan.root) {
		if err := checkCancelled(ctx); err != nil {
			return err
		}
		if err := isoValidateSourceFile(file); err != nil {
			return err
		}
	}
	writer := newSequentialIsoWriter(output, ctx, 0)
	if err := isoWriteFilesystem(plan, writer); err != nil {
		return err
	}
	return writer.padToLba(endLbaExclusive)
}

func isoWriteFilesystem(plan *IsoLayoutPlan, output *sequentialIsoWriter) error {
	if err := isoWriteMetadata(plan, output); err != nil {
		return err
	}
	return isoWriteFiles(plan, output)
}

// writeIsoOuterRingPrefix ports Iso9660ImageWriter.WriteOuterRingPrefix.
func writeIsoOuterRingPrefix(plan *IsoLayoutPlan, output io.Writer, endLbaExclusive uint32, ctx context.Context) error {
	if err := isoValidateOuterRange(plan, output, endLbaExclusive); err != nil {
		return err
	}
	if err := isoValidateSources(plan, ctx); err != nil {
		return err
	}
	writer := newSequentialIsoWriter(output, ctx, 0)
	if err := isoWriteMetadata(plan, writer); err != nil {
		return err
	}
	return writer.padToLba(endLbaExclusive)
}

// writeIsoOuterRingSuffix ports Iso9660ImageWriter.WriteOuterRingSuffix.
func writeIsoOuterRingSuffix(plan *IsoLayoutPlan, output io.Writer, startLba uint32, ctx context.Context) error {
	if err := isoValidateOuterRange(plan, output, startLba); err != nil {
		return err
	}
	if err := isoValidateSources(plan, ctx); err != nil {
		return err
	}
	writer := newSequentialIsoWriter(output, ctx, startLba)
	if err := isoWriteFiles(plan, writer); err != nil {
		return err
	}
	return writer.padToLba(plan.volumeSectors)
}

func isoValidateOuterRange(plan *IsoLayoutPlan, output io.Writer, boundary uint32) error {
	if plan == nil {
		return errors.New("Value cannot be null. (Parameter 'plan')")
	}
	if output == nil {
		return errors.New("Value cannot be null. (Parameter 'output')")
	}
	if !plan.filesAtOuterRing || boundary < plan.metadataEndLbaExclusive || boundary > plan.fileDataStartLba {
		return errors.New("Outer-file drawing boundary overlaps metadata or files. (Parameter 'boundary')")
	}
	return nil
}

func isoValidateSources(plan *IsoLayoutPlan, ctx context.Context) error {
	if err := checkCancelled(ctx); err != nil {
		return err
	}
	for _, file := range isoEnumerateFiles(plan.root) {
		if err := checkCancelled(ctx); err != nil {
			return err
		}
		if err := isoValidateSourceFile(file); err != nil {
			return err
		}
	}
	return nil
}

func isoWriteMetadata(plan *IsoLayoutPlan, output *sequentialIsoWriter) error {
	if err := output.writeAtLba(primaryVolumeDescriptorLba, isoBuildVolumeDescriptor(
		1,
		plan.volumeSectors,
		plan.primaryVolumeLabel,
		plan.layout.primaryPathTableBytes,
		plan.layout.primaryLittlePathTableLba,
		plan.layout.primaryBigPathTableLba,
		plan.root.primaryDirectoryLba,
		plan.root.primaryDirectoryBytes,
		false,
		plan.timestamp)); err != nil {
		return err
	}
	if err := output.writeAtLba(supplementaryVolumeDescriptorLba, isoBuildVolumeDescriptor(
		2,
		plan.volumeSectors,
		plan.jolietVolumeLabel,
		plan.layout.jolietPathTableBytes,
		plan.layout.jolietLittlePathTableLba,
		plan.layout.jolietBigPathTableLba,
		plan.root.jolietDirectoryLba,
		plan.root.jolietDirectoryBytes,
		true,
		plan.timestamp)); err != nil {
		return err
	}
	if err := output.writeAtLba(terminatorLba, isoBuildTerminator()); err != nil {
		return err
	}
	if err := output.writeAtLba(
		plan.layout.primaryLittlePathTableLba,
		isoBuildPathTable(plan.directories, true, false)); err != nil {
		return err
	}
	if err := output.writeAtLba(
		plan.layout.primaryBigPathTableLba,
		isoBuildPathTable(plan.directories, false, false)); err != nil {
		return err
	}
	if err := output.writeAtLba(
		plan.layout.jolietLittlePathTableLba,
		isoBuildPathTable(plan.directories, true, true)); err != nil {
		return err
	}
	if err := output.writeAtLba(
		plan.layout.jolietBigPathTableLba,
		isoBuildPathTable(plan.directories, false, true)); err != nil {
		return err
	}

	for _, directory := range plan.directories {
		if err := output.writeAtLba(
			directory.primaryDirectoryLba,
			isoBuildDirectoryExtent(directory, false)); err != nil {
			return err
		}
	}

	for _, directory := range plan.directories {
		if err := output.writeAtLba(
			directory.jolietDirectoryLba,
			isoBuildDirectoryExtent(directory, true)); err != nil {
			return err
		}
	}
	return nil
}

func isoWriteFiles(plan *IsoLayoutPlan, output *sequentialIsoWriter) error {
	for _, file := range isoEnumerateFiles(plan.root) {
		if err := output.writeFileAtLba(file); err != nil {
			return err
		}
	}
	return nil
}

func isoReadTree(sourceFullPath string) (*isoNode, error) {
	info, err := os.Lstat(sourceFullPath)
	if err != nil {
		return nil, err
	}
	root := &isoNode{
		sourcePath:  sourceFullPath,
		isDirectory: true,
		timestamp:   info.ModTime().UTC(),
	}
	if err := isoPopulateDirectory(root); err != nil {
		return nil, err
	}
	return root, nil
}

func isoPopulateDirectory(directory *isoNode) error {
	entries, err := readDirSorted(directory.sourcePath, isoNameLess)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fullName := filepath.Join(directory.sourcePath, entry.name)
		if entry.info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Reparse points are not supported in --data-dir: %s", fullName)
		}

		if entry.info.IsDir() {
			child := &isoNode{
				sourcePath:  fullName,
				name:        entry.name,
				isDirectory: true,
				parent:      directory,
				timestamp:   entry.info.ModTime().UTC(),
			}
			directory.children = append(directory.children, child)
			if err := isoPopulateDirectory(child); err != nil {
				return err
			}
		} else {
			if uint64(entry.info.Size()) > math.MaxUint32 {
				return fmt.Errorf("ISO9660 files larger than 4 GiB minus 1 byte are not supported: %s (Parameter 'directory')", fullName)
			}

			directory.children = append(directory.children, &isoNode{
				sourcePath: fullName,
				name:       entry.name,
				parent:     directory,
				dataLength: entry.info.Size(),
				timestamp:  entry.info.ModTime().UTC(),
			})
		}
	}
	return nil
}

func isoAssignDirectoryNumbers(root *isoNode) ([]*isoNode, error) {
	var directories []*isoNode
	queue := []*isoNode{root}
	for len(queue) > 0 {
		directory := queue[0]
		queue = queue[1:]
		if len(directories) == math.MaxUint16 {
			return nil, errors.New("ISO contains too many directories.")
		}

		directory.directoryNumber = uint16(len(directories) + 1)
		directories = append(directories, directory)
		for _, child := range directory.children {
			if child.isDirectory {
				queue = append(queue, child)
			}
		}
	}

	return directories, nil
}

func isoAssignIdentifiers(directories []*isoNode) error {
	for _, directory := range directories {
		primaryUsed := make(map[string]struct{})
		jolietUsed := make(map[string]struct{})
		for _, child := range directory.children {
			primaryMaximum := maximumPrimaryIdentifierCharacters
			primaryFallback := "DIRECTORY"
			primarySuffix := ""
			jolietMaximum := maximumJolietIdentifierCharacters
			jolietFallback := "Directory"
			jolietSuffix := ""
			if !child.isDirectory {
				primaryMaximum -= 2
				primaryFallback = "FILE"
				primarySuffix = ";1"
				jolietMaximum -= 2
				jolietFallback = "File"
				jolietSuffix = ";1"
			}
			primaryBase := normalizePrimaryIdentifier(child.name, primaryMaximum, primaryFallback)
			primaryIdentifier, err := isoMakeUnique(
				primaryBase, primaryUsed, primarySuffix, maximumPrimaryIdentifierCharacters)
			if err != nil {
				return err
			}
			child.primaryIdentifier = primaryIdentifier

			jolietBase := normalizeJolietIdentifier(child.name, jolietMaximum, jolietFallback)
			jolietIdentifier, err := isoMakeUnique(
				jolietBase, jolietUsed, jolietSuffix, maximumJolietIdentifierCharacters)
			if err != nil {
				return err
			}
			child.jolietIdentifier = jolietIdentifier
		}
	}
	return nil
}

func isoBuildLayout(directories []*isoNode, volumeSectors uint32, filesAtOuterRing bool) (isoLayout, error) {
	var layout isoLayout
	primaryPathTableBytes := isoPathTableLength(directories, false)
	jolietPathTableBytes := isoPathTableLength(directories, true)
	cursor := uint64(firstMetadataLba)
	allocate := func(byteCount int64) (uint32, error) {
		lba := cursor
		cursor += uint64(sectorCount(byteCount))
		if cursor > math.MaxUint32 {
			return 0, errOverflow
		}
		return uint32(lba), nil
	}
	primaryLittlePathTableLba, err := allocate(int64(primaryPathTableBytes))
	if err != nil {
		return layout, err
	}
	primaryBigPathTableLba, err := allocate(int64(primaryPathTableBytes))
	if err != nil {
		return layout, err
	}
	jolietLittlePathTableLba, err := allocate(int64(jolietPathTableBytes))
	if err != nil {
		return layout, err
	}
	jolietBigPathTableLba, err := allocate(int64(jolietPathTableBytes))
	if err != nil {
		return layout, err
	}

	for _, directory := range directories {
		directory.primaryDirectoryBytes = isoDirectoryExtentLength(directory, false)
		directory.primaryDirectoryLba, err = allocate(int64(directory.primaryDirectoryBytes))
		if err != nil {
			return layout, err
		}
	}

	for _, directory := range directories {
		directory.jolietDirectoryBytes = isoDirectoryExtentLength(directory, true)
		directory.jolietDirectoryLba, err = allocate(int64(directory.jolietDirectoryBytes))
		if err != nil {
			return layout, err
		}
	}

	metadataEnd := cursor
	files := isoEnumerateFiles(directories[0])
	var fileSectors uint64
	for _, file := range files {
		fileSectors += uint64(sectorCount(file.dataLength))
	}
	if metadataEnd+fileSectors > uint64(volumeSectors) {
		return layout, errors.New("Data directory and filesystem metadata exceed the disc capacity.")
	}
	if filesAtOuterRing {
		cursor = uint64(volumeSectors) - fileSectors
	}
	fileDataStart := cursor
	for _, file := range files {
		if filesAtOuterRing && file.dataLength == 0 {
			file.dataLba = 0
		} else {
			file.dataLba = uint32(cursor)
		}
		cursor += uint64(sectorCount(file.dataLength))
	}

	if cursor > uint64(volumeSectors) {
		return layout, fmt.Errorf(
			"Data directory needs %d sectors including filesystem metadata, "+
				"but the disc has only %d sectors.", cursor, volumeSectors)
	}

	return isoLayout{
		primaryLittlePathTableLba: primaryLittlePathTableLba,
		primaryBigPathTableLba:    primaryBigPathTableLba,
		jolietLittlePathTableLba:  jolietLittlePathTableLba,
		jolietBigPathTableLba:     jolietBigPathTableLba,
		primaryPathTableBytes:     primaryPathTableBytes,
		jolietPathTableBytes:      jolietPathTableBytes,
		dataEndLbaExclusive:       uint32(cursor),
		metadataEndLbaExclusive:   uint32(metadataEnd),
		fileDataStartLba:          uint32(fileDataStart),
		filesAtOuterRing:          filesAtOuterRing,
	}, nil
}

func isoPathTableLength(directories []*isoNode, joliet bool) int {
	length := 0
	for _, directory := range directories {
		identifierBytes := 1
		if directory.parent != nil {
			identifierBytes = len(isoIdentifierBytes(directory, joliet))
		}
		length += 8 + identifierBytes + (identifierBytes & 1)
	}

	return length
}

func isoDirectoryExtentLength(directory *isoNode, joliet bool) int {
	recordLengths := []int{
		isoDirectoryRecordLength(1),
		isoDirectoryRecordLength(1),
	}
	for _, child := range directory.children {
		recordLengths = append(recordLengths,
			isoDirectoryRecordLength(len(isoIdentifierBytes(child, joliet))))
	}
	return isoPackedRecordLength(recordLengths)
}

func isoPackedRecordLength(recordLengths []int) int {
	offset := 0
	for _, recordLength := range recordLengths {
		sectorOffset := offset % LogicalSectorBytes
		if sectorOffset+recordLength > LogicalSectorBytes {
			offset += LogicalSectorBytes - sectorOffset
		}

		offset += recordLength
	}

	return int(sectorCount(int64(offset))) * LogicalSectorBytes
}

func isoBuildPathTable(directories []*isoNode, littleEndian bool, joliet bool) []byte {
	var table []byte
	numeric := make([]byte, 6)
	for _, directory := range directories {
		identifier := []byte{0}
		if directory.parent != nil {
			identifier = isoIdentifierBytes(directory, joliet)
		}
		table = append(table, byte(len(identifier)), 0)
		extentLba := directory.primaryDirectoryLba
		if joliet {
			extentLba = directory.jolietDirectoryLba
		}
		parentNumber := uint16(1)
		if directory.parent != nil {
			parentNumber = directory.parent.directoryNumber
		}
		if littleEndian {
			binary.LittleEndian.PutUint32(numeric[0:4], extentLba)
			binary.LittleEndian.PutUint16(numeric[4:6], parentNumber)
		} else {
			binary.BigEndian.PutUint32(numeric[0:4], extentLba)
			binary.BigEndian.PutUint16(numeric[4:6], parentNumber)
		}

		table = append(table, numeric...)
		table = append(table, identifier...)
		if len(identifier)&1 != 0 {
			table = append(table, 0)
		}
	}

	return table
}

func isoBuildDirectoryExtent(directory *isoNode, joliet bool) []byte {
	extentBytes := directory.primaryDirectoryBytes
	currentLba := directory.primaryDirectoryLba
	if joliet {
		extentBytes = directory.jolietDirectoryBytes
		currentLba = directory.jolietDirectoryLba
	}
	extent := make([]byte, extentBytes)
	offset := 0
	parent := directory.parent
	if parent == nil {
		parent = directory
	}
	parentLba := parent.primaryDirectoryLba
	parentBytes := parent.primaryDirectoryBytes
	if joliet {
		parentLba = parent.jolietDirectoryLba
		parentBytes = parent.jolietDirectoryBytes
	}
	offset = isoAddDirectoryRecord(extent, offset, isoBuildDirectoryRecord(
		currentLba,
		int64(extentBytes),
		true,
		[]byte{0},
		directory.timestamp))
	offset = isoAddDirectoryRecord(extent, offset, isoBuildDirectoryRecord(
		parentLba,
		int64(parentBytes),
		true,
		[]byte{1},
		parent.timestamp))
	for _, child := range directory.children {
		var childLba uint32
		var childBytes int64
		if child.isDirectory {
			childLba = child.primaryDirectoryLba
			childBytes = int64(child.primaryDirectoryBytes)
			if joliet {
				childLba = child.jolietDirectoryLba
				childBytes = int64(child.jolietDirectoryBytes)
			}
		} else {
			childLba = child.dataLba
			childBytes = child.dataLength
		}
		offset = isoAddDirectoryRecord(extent, offset, isoBuildDirectoryRecord(
			childLba,
			childBytes,
			child.isDirectory,
			isoIdentifierBytes(child, joliet),
			child.timestamp))
	}

	return extent
}

func isoAddDirectoryRecord(extent []byte, offset int, record []byte) int {
	sectorOffset := offset % LogicalSectorBytes
	if sectorOffset+len(record) > LogicalSectorBytes {
		offset += LogicalSectorBytes - sectorOffset
	}

	copy(extent[offset:], record)
	return offset + len(record)
}

func isoBuildVolumeDescriptor(
	descriptorType byte,
	volumeSectors uint32,
	volumeLabel string,
	pathTableBytes int,
	littlePathTableLba uint32,
	bigPathTableLba uint32,
	rootDirectoryLba uint32,
	rootDirectoryBytes int,
	joliet bool,
	timestamp time.Time) []byte {
	sector := make([]byte, LogicalSectorBytes)
	sector[0] = descriptorType
	copy(sector[1:], "CD001")
	sector[6] = 1
	if joliet {
		isoWritePaddedJoliet(sector, 8, 32, "DVDIMAGE")
		isoWritePaddedJoliet(sector, 40, 32, volumeLabel)
		sector[88] = '%'
		sector[89] = '/'
		sector[90] = 'E'
	} else {
		isoWritePaddedAscii(sector, 8, 32, "DVDIMAGE")
		isoWritePaddedAscii(sector, 40, 32, volumeLabel)
	}

	isoWriteBothEndian32(sector, 80, volumeSectors)
	isoWriteBothEndian16(sector, 120, 1)
	isoWriteBothEndian16(sector, 124, 1)
	isoWriteBothEndian16(sector, 128, LogicalSectorBytes)
	isoWriteBothEndian32(sector, 132, uint32(pathTableBytes))
	binary.LittleEndian.PutUint32(sector[140:144], littlePathTableLba)
	binary.BigEndian.PutUint32(sector[148:152], bigPathTableLba)
	copy(sector[156:], isoBuildDirectoryRecord(
		rootDirectoryLba,
		int64(rootDirectoryBytes),
		true,
		[]byte{0},
		timestamp))
	isoWriteVolumeDate(sector[813:830], timestamp)
	isoWriteVolumeDate(sector[830:847], timestamp)
	isoWriteUnspecifiedVolumeDate(sector[847:864])
	isoWriteUnspecifiedVolumeDate(sector[864:881])
	sector[881] = 1
	return sector
}

func isoBuildTerminator() []byte {
	sector := make([]byte, LogicalSectorBytes)
	sector[0] = 255
	copy(sector[1:], "CD001")
	sector[6] = 1
	return sector
}

func isoBuildDirectoryRecord(
	extentLba uint32,
	dataLength int64,
	isDirectory bool,
	identifier []byte,
	timestamp time.Time) []byte {
	length := isoDirectoryRecordLength(len(identifier))
	record := make([]byte, length)
	record[0] = byte(length)
	isoWriteBothEndian32(record, 2, extentLba)
	isoWriteBothEndian32(record, 10, uint32(dataLength))
	isoWriteDirectoryDate(record[18:25], timestamp)
	if isDirectory {
		record[25] = 0x02
	}
	isoWriteBothEndian16(record, 28, 1)
	record[32] = byte(len(identifier))
	copy(record[33:], identifier)
	return record
}

func isoDirectoryRecordLength(identifierBytes int) int {
	pad := 0
	if identifierBytes%2 == 0 {
		pad = 1
	}
	return 33 + identifierBytes + pad
}

func isoIdentifierBytes(node *isoNode, joliet bool) []byte {
	if joliet {
		return bigEndianUnicode(node.jolietIdentifier)
	}
	// Primary identifiers are normalized to pure ASCII.
	return []byte(node.primaryIdentifier)
}

func isoEnumerateFiles(root *isoNode) []*isoNode {
	var files []*isoNode
	var walk func(node *isoNode)
	walk = func(node *isoNode) {
		for _, child := range node.children {
			if child.isDirectory {
				walk(child)
			} else {
				files = append(files, child)
			}
		}
	}
	walk(root)
	return files
}

// isoMakeUnique ports Iso9660ImageWriter.MakeUnique. The used set mirrors a
// HashSet<string> with StringComparer.OrdinalIgnoreCase.
func isoMakeUnique(
	baseName string,
	used map[string]struct{},
	suffix string,
	maximumCharacters int) (string, error) {
	suffixCharacters := utf16Length(suffix)
	candidate := truncateUTF16(baseName, maximumCharacters-suffixCharacters) + suffix
	if isoTryAddUsed(used, candidate) {
		return candidate, nil
	}

	for index := 2; index < 1000000; index++ {
		discriminator := fmt.Sprintf("~%d", index)
		candidate = truncateUTF16(
			baseName,
			maximumCharacters-suffixCharacters-utf16Length(discriminator)) +
			discriminator +
			suffix
		if isoTryAddUsed(used, candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("Too many colliding ISO identifiers derived from '%s'.", baseName)
}

func isoTryAddUsed(used map[string]struct{}, candidate string) bool {
	key := strings.ToUpper(candidate)
	if _, exists := used[key]; exists {
		return false
	}
	used[key] = struct{}{}
	return true
}

func isoValidateSourceFile(file *isoNode) error {
	info, err := os.Lstat(file.sourcePath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() ||
		info.Size() != file.dataLength ||
		info.ModTime().UTC().UnixNano()/100 != file.timestamp.UnixNano()/100 {
		return fmt.Errorf("Source file changed after ISO planning: %s", file.sourcePath)
	}
	return nil
}

func normalizePrimaryIdentifier(value string, maximumLength int, fallback string) string {
	// C# iterates UTF-16 chars of value.Normalize(FormKC).ToUpperInvariant();
	// allowed characters are ASCII, everything else becomes '_' (one per
	// UTF-16 unit, so an astral rune produces two).
	upper := strings.ToUpper(norm.NFKC.String(value))
	result := make([]byte, 0, maximumLength)
loop:
	for _, r := range upper {
		c := byte('_')
		units := 1
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' {
			c = byte(r)
		} else if r >= 0x10000 {
			units = 2
		}
		for i := 0; i < units; i++ {
			result = append(result, c)
			if len(result) == maximumLength {
				break loop
			}
		}
	}

	normalizedValue := strings.Trim(string(result), ".")
	if len(normalizedValue) == 0 {
		return fallback
	}
	return normalizedValue
}

func normalizeJolietIdentifier(value string, maximumLength int, fallback string) string {
	normalized := normalizeJolietText(value)
	if len(normalized) == 0 {
		return fallback
	}
	return truncateUTF16(normalized, maximumLength)
}

func validateJolietVolumeLabel(volumeLabel string) (string, error) {
	normalized := normalizeJolietText(strings.TrimSpace(volumeLabel))
	if utf16Length(normalized) > MaximumJolietVolumeLabelCharacters {
		return "", fmt.Errorf("Joliet volume labels are limited to %d characters. (Parameter 'volumeLabel')",
			MaximumJolietVolumeLabelCharacters)
	}

	if len(normalized) == 0 {
		return "DVD_IMAGE", nil
	}
	return normalized, nil
}

// normalizeJolietText ports NormalizeJolietText: Normalize(FormC), then per
// UTF-16 unit replace '\0', '/', '\\', ':', ';' and surrogates with '_'.
func normalizeJolietText(value string) string {
	normalized := norm.NFC.String(value)
	var result strings.Builder
	result.Grow(len(normalized))
	for _, r := range normalized {
		switch {
		case r == 0 || r == '/' || r == '\\' || r == ':' || r == ';':
			result.WriteByte('_')
		case r >= 0x10000:
			// A supplementary character is a surrogate pair in UTF-16; C#
			// replaces each unit with '_'.
			result.WriteString("__")
		default:
			result.WriteRune(r)
		}
	}

	return result.String()
}

// isWithinDirectory ports Iso9660ImageWriter.IsWithinDirectory.
func isWithinDirectory(path, directory string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDirectory, err := filepath.Abs(directory)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absDirectory, absPath)
	if err != nil {
		return false
	}
	return relative == "." ||
		(!filepath.IsAbs(relative) &&
			relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
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
	year := utc.Year()
	if year < 1900 {
		year = 1900
	}
	if year > 2155 {
		year = 2155
	}
	destination[0] = byte(year - 1900)
	destination[1] = byte(utc.Month())
	destination[2] = byte(utc.Day())
	destination[3] = byte(utc.Hour())
	destination[4] = byte(utc.Minute())
	destination[5] = byte(utc.Second())
	destination[6] = 0
}

// isoWriteVolumeDate writes "yyyyMMddHHmmssff" (ff = hundredths of a second)
// plus a trailing zero byte.
func isoWriteVolumeDate(destination []byte, timestamp time.Time) {
	utc := timestamp.UTC()
	digits := fmt.Sprintf("%04d%02d%02d%02d%02d%02d%02d",
		utc.Year(), int(utc.Month()), utc.Day(),
		utc.Hour(), utc.Minute(), utc.Second(), utc.Nanosecond()/10000000)
	copy(destination[:16], digits)
	destination[16] = 0
}

func isoWriteUnspecifiedVolumeDate(destination []byte) {
	for i := range destination[:16] {
		destination[i] = '0'
	}
	destination[16] = 0
}

func isoWritePaddedAscii(destination []byte, offset int, length int, value string) {
	field := destination[offset : offset+length]
	for i := range field {
		field[i] = ' '
	}
	copy(field, value)
}

func isoWritePaddedJoliet(destination []byte, offset int, length int, value string) {
	field := destination[offset : offset+length]
	for index := 0; index < len(field); index += 2 {
		field[index] = 0
		field[index+1] = ' '
	}

	copy(field, bigEndianUnicode(value))
}

// sequentialIsoWriter ports Iso9660ImageWriter.SequentialIsoWriter.
type sequentialIsoWriter struct {
	output   io.Writer
	ctx      context.Context
	zero     [isoCopyBufferBytes]byte
	position int64
}

func newSequentialIsoWriter(output io.Writer, ctx context.Context, startLba uint32) *sequentialIsoWriter {
	return &sequentialIsoWriter{
		output:   output,
		ctx:      ctx,
		position: int64(startLba) * LogicalSectorBytes,
	}
}

func (w *sequentialIsoWriter) writeAtLba(lba uint32, bytes []byte) error {
	if err := w.padToPosition(int64(lba) * LogicalSectorBytes); err != nil {
		return err
	}
	if err := checkCancelled(w.ctx); err != nil {
		return err
	}
	if err := writeAll(w.output, bytes); err != nil {
		return err
	}
	w.position += int64(len(bytes))
	return nil
}

func (w *sequentialIsoWriter) writeFileAtLba(file *isoNode) error {
	if file.dataLength == 0 {
		return isoValidateSourceFile(file)
	}
	if err := w.padToPosition(int64(file.dataLba) * LogicalSectorBytes); err != nil {
		return err
	}
	if err := isoValidateSourceFile(file); err != nil {
		return err
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
		return fmt.Errorf("Source file size changed while streaming ISO: %s", file.sourcePath)
	}

	buffer := make([]byte, isoCopyBufferBytes)
	remaining := file.dataLength
	for remaining > 0 {
		if err := checkCancelled(w.ctx); err != nil {
			return err
		}
		requested := int64(len(buffer))
		if remaining < requested {
			requested = remaining
		}
		read, err := input.Read(buffer[:requested])
		if err != nil && err != io.EOF {
			return err
		}
		if read == 0 {
			return fmt.Errorf("Source file ended early while streaming ISO: %s", file.sourcePath)
		}

		if err := writeAll(w.output, buffer[:read]); err != nil {
			return err
		}
		w.position += int64(read)
		remaining -= int64(read)
	}
	if err := isoValidateSourceFile(file); err != nil {
		return err
	}
	info, err = input.Stat()
	if err != nil {
		return err
	}
	if info.Size() != file.dataLength {
		return fmt.Errorf("Source file size changed while streaming ISO: %s", file.sourcePath)
	}
	return nil
}

func (w *sequentialIsoWriter) padToLba(lba uint32) error {
	return w.padToPosition(int64(lba) * LogicalSectorBytes)
}

func (w *sequentialIsoWriter) padToPosition(target int64) error {
	if target < w.position {
		return fmt.Errorf("Sequential ISO layout moved backwards from byte %d to %d.", w.position, target)
	}

	remaining := target - w.position
	for remaining > 0 {
		if err := checkCancelled(w.ctx); err != nil {
			return err
		}
		count := int64(len(w.zero))
		if remaining < count {
			count = remaining
		}
		if err := writeAll(w.output, w.zero[:count]); err != nil {
			return err
		}
		w.position += count
		remaining -= count
	}
	return nil
}
