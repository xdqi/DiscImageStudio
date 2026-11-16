package iso

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildDispersedTestTree creates a directory with two small files.
func buildDispersedTestTree(t *testing.T) (root string, alpha, beta []byte) {
	t.Helper()
	root = t.TempDir()
	alpha = make([]byte, 3000)
	for i := range alpha {
		alpha[i] = byte('A' + i%26)
	}
	beta = make([]byte, 5000)
	for i := range beta {
		beta[i] = byte(i*7 + 3)
	}
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), alpha, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "beta.bin"), beta, 0o644); err != nil {
		t.Fatal(err)
	}
	return root, alpha, beta
}

// assembleDispersedImage builds the full volume bytes: pre-rendered
// descriptor sectors plus file data at the assigned extent LBAs (picture and
// all other sectors stay zero).
func assembleDispersedImage(t *testing.T, plan *UdfDispersedPlan, content func(index int) []byte) []byte {
	t.Helper()
	image := make([]byte, int(plan.VolumeSectors())*LogicalSectorBytes)
	for lba := uint32(0); lba < plan.VolumeSectors(); lba++ {
		if data, ok := plan.DescriptorAt(lba); ok {
			copy(image[int(lba)*LogicalSectorBytes:], data)
		}
	}
	for _, extent := range plan.FileExtents() {
		data := content(extent.FileIndex)
		available := int64(extent.Sectors) * LogicalSectorBytes
		if tail := int64(len(data)) - extent.Offset; available > tail {
			available = tail
		}
		copy(image[int(extent.Lba)*LogicalSectorBytes:], data[extent.Offset:extent.Offset+available])
	}
	return image
}

// TestUdfDispersedParity verifies that a dispersed plan whose extents are
// placed exactly where planUdf puts the file data produces byte-identical
// descriptor/metadata sectors, except for the uid/gid/permissions fields of
// file entries (and the tag checksum/CRC they feed): the dispersed writer
// defaults ownership to root and copies modes from the source, while the
// contiguous writer keeps the C# fixed defaults for .NET byte parity.
func TestUdfDispersedParity(t *testing.T) {
	root := buildIsoTestTree(t)
	const volumeSectors = 2048
	withFixedNow(t, time.Unix(1700000000, 0).UTC())
	ctx := context.Background()

	contiguous, err := PlanUdf(root, volumeSectors, "MYBD", false)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.Grow(volumeSectors * LogicalSectorBytes)
	if err := WriteUdfSequentialPrefix(contiguous, &buf, contiguous.DrawingEndLbaExclusive(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := WriteUdfSequentialSuffix(contiguous, &buf, contiguous.DrawingEndLbaExclusive(), ctx); err != nil {
		t.Fatal(err)
	}
	golden := buf.Bytes()

	dispersed, err := PrepareUdfDispersed(root, volumeSectors, "MYBD", 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Place every file's data exactly where planUdf placed it.
	extents := map[int][]UdfFileExtent{}
	fileIndex := 0
	var firstDataLba uint32
	for _, node := range contiguous.nodes {
		if node.isDirectory {
			continue
		}
		if node.dataLength > 0 {
			if fileIndex == 0 {
				firstDataLba = node.dataLba
			}
			extents[fileIndex] = []UdfFileExtent{{
				FileIndex: fileIndex,
				Offset:    0,
				Lba:       node.dataLba,
				Sectors:   uint32(sectorCount(node.dataLength)),
			}}
		}
		fileIndex++
	}
	if err := dispersed.AssignExtents(extents); err != nil {
		t.Fatal(err)
	}
	if dispersed.MetaEndLbaExclusive() != firstDataLba {
		t.Fatalf("MetaEndLbaExclusive = %d, want %d (planUdf's first file data LBA)",
			dispersed.MetaEndLbaExclusive(), firstDataLba)
	}
	if dispersed.FileCount() != 3 || dispersed.DirectoryCount() != 3 {
		t.Fatalf("FileCount=%d DirectoryCount=%d, want 3/3", dispersed.FileCount(), dispersed.DirectoryCount())
	}

	sectors := 0
	for lba := uint32(0); lba < volumeSectors; lba++ {
		data, ok := dispersed.DescriptorAt(lba)
		if !ok {
			continue
		}
		sectors++
		want := golden[int(lba)*LogicalSectorBytes : int(lba+1)*LogicalSectorBytes]
		if !bytes.Equal(maskIntentionalFileEntryDiffs(data), maskIntentionalFileEntryDiffs(want)) {
			for i := range want {
				if data[i] != want[i] {
					t.Fatalf("descriptor sector %d: first diff at offset %d: got %02x want %02x", lba, i, data[i], want[i])
				}
			}
		}
	}
	// VRS 3 + main VDS 16 + integrity 2 + anchors 3 + reserve VDS 16 +
	// FSD+terminator 2 + 6 file entries + 3 directory data sectors.
	if sectors != 51 {
		t.Fatalf("DescriptorAt covered %d sectors, want 51", sectors)
	}
	// File data LBAs of the contiguous plan must not be descriptor sectors.
	var dataSpans [][2]uint32
	for _, node := range contiguous.nodes {
		if node.isDirectory || node.dataLength == 0 {
			continue
		}
		if _, ok := dispersed.DescriptorAt(node.dataLba); ok {
			t.Fatalf("DescriptorAt(%d) = ok, want file data", node.dataLba)
		}
		dataSpans = append(dataSpans, [2]uint32{node.dataLba, node.dataLba + uint32(sectorCount(node.dataLength))})
	}
	// Every nonzero golden sector is either a descriptor sector or file data:
	// no metadata sector may be missing from DescriptorAt's coverage.
	isZero := func(sector []byte) bool {
		for _, b := range sector {
			if b != 0 {
				return false
			}
		}
		return true
	}
	for lba := uint32(0); lba < volumeSectors; lba++ {
		sector := golden[int(lba)*LogicalSectorBytes : int(lba+1)*LogicalSectorBytes]
		if isZero(sector) {
			continue
		}
		if _, ok := dispersed.DescriptorAt(lba); ok {
			continue
		}
		fileData := false
		for _, span := range dataSpans {
			if lba >= span[0] && lba < span[1] {
				fileData = true
				break
			}
		}
		if !fileData {
			t.Fatalf("golden sector %d is nonzero but DescriptorAt = false and it is not file data", lba)
		}
	}
}

// maskIntentionalFileEntryDiffs zeroes the bytes where the dispersed writer
// deliberately differs from the contiguous one (uid/gid/permissions — copied
// ownership and modes — plus the tag checksum/CRC they feed), so everything
// else is still compared byte-for-byte.
func maskIntentionalFileEntryDiffs(sector []byte) []byte {
	if binary.LittleEndian.Uint16(sector[0:2]) != 261 { // file entry tag
		return sector
	}
	masked := append([]byte(nil), sector...)
	masked[4] = 0 // tag checksum
	masked[8] = 0 // descriptor CRC
	masked[9] = 0
	for i := 36; i < 48; i++ { // uid, gid, permissions
		masked[i] = 0
	}
	return masked
}

// udfDisperseTestAd is one parsed short allocation descriptor.
type udfDisperseTestAd struct {
	length uint32
	lba    uint32
}

func udfDisperseTestFileEntry(t *testing.T, image []byte, lba uint32) (int64, []udfDisperseTestAd) {
	t.Helper()
	sector := image[int(lba)*LogicalSectorBytes : int(lba+1)*LogicalSectorBytes]
	if got := binary.LittleEndian.Uint16(sector[0:2]); got != 261 {
		t.Fatalf("file entry tag id at sector %d = %d, want 261", lba, got)
	}
	dataLength := int64(binary.LittleEndian.Uint64(sector[56:64]))
	adBytes := int(binary.LittleEndian.Uint32(sector[172:176]))
	var ads []udfDisperseTestAd
	for offset := 176; offset < 176+adBytes; offset += 8 {
		length := binary.LittleEndian.Uint32(sector[offset:]) & 0x3fffffff
		position := binary.LittleEndian.Uint32(sector[offset+4:])
		ads = append(ads, udfDisperseTestAd{length, udfFirstPartitionLba + position})
	}
	return dataLength, ads
}

// udfDisperseTestReadDir parses a directory's file identifier records into a
// name -> file entry LBA map (parent entry excluded).
func udfDisperseTestReadDir(t *testing.T, image []byte, feLba uint32) map[string]uint32 {
	t.Helper()
	dataLength, ads := udfDisperseTestFileEntry(t, image, feLba)
	var directory []byte
	for _, ad := range ads {
		start := int(ad.lba) * LogicalSectorBytes
		directory = append(directory, image[start:start+int(ad.length)]...)
	}
	directory = directory[:dataLength]
	files := map[string]uint32{}
	for offset := 0; offset+38 <= len(directory); {
		if tag := binary.LittleEndian.Uint16(directory[offset:]); tag != 257 {
			t.Fatalf("file identifier tag id = %d at offset %d, want 257", tag, offset)
		}
		characteristics := directory[offset+18]
		identifierBytes := int(directory[offset+19])
		implementationBytes := int(binary.LittleEndian.Uint16(directory[offset+36:]))
		icbLbn := binary.LittleEndian.Uint32(directory[offset+24:])
		if characteristics&8 == 0 { // not the parent directory record
			identifier := directory[offset+38+implementationBytes : offset+38+implementationBytes+identifierBytes]
			if len(identifier) == 0 || identifier[0] != 8 {
				t.Fatalf("unexpected file identifier encoding %v", identifier)
			}
			files[string(identifier[1:])] = udfFirstPartitionLba + icbLbn
		}
		offset += (38+identifierBytes+3)/4*4 + implementationBytes
	}
	return files
}

func udfDisperseTestReadFile(t *testing.T, image []byte, feLba uint32) []byte {
	t.Helper()
	dataLength, ads := udfDisperseTestFileEntry(t, image, feLba)
	var out []byte
	for _, ad := range ads {
		start := int(ad.lba) * LogicalSectorBytes
		out = append(out, image[start:start+int(ad.length)]...)
	}
	if int64(len(out)) != dataLength {
		t.Fatalf("read %d bytes via extent list, file entry says %d", len(out), dataLength)
	}
	return out
}

// TestUdfDispersedStructure disperses two tree files and one synthetic extra
// file across non-contiguous extents, then re-parses the resulting image.
func TestUdfDispersedStructure(t *testing.T) {
	root, alpha, beta := buildDispersedTestTree(t)
	extraContent := make([]byte, 4100)
	for i := range extraContent {
		extraContent[i] = byte(0xE0 + i%13)
	}
	extras := []UdfExtraFile{{
		Name: "EXTRA.DAT",
		Size: int64(len(extraContent)),
		Fill: func() ([]byte, error) { return extraContent, nil },
	}}
	const volumeSectors = 4096
	plan, err := PrepareUdfDispersed(root, volumeSectors, "HYBRID", 16, extras)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FileCount() != 3 || plan.DirectoryCount() != 1 {
		t.Fatalf("FileCount=%d DirectoryCount=%d, want 3/1", plan.FileCount(), plan.DirectoryCount())
	}
	if plan.FileBytes() != int64(len(alpha)+len(beta)+len(extraContent)) {
		t.Fatalf("FileBytes = %d", plan.FileBytes())
	}
	if plan.PartitionStartLba() != udfFirstPartitionLba {
		t.Fatalf("PartitionStartLba = %d, want %d", plan.PartitionStartLba(), udfFirstPartitionLba)
	}
	// Root + 3 files = 4 file entries at 290..293; root directory data 1 sector at 294.
	if plan.MetaEndLbaExclusive() != 295 {
		t.Fatalf("MetaEndLbaExclusive = %d, want 295", plan.MetaEndLbaExclusive())
	}
	if plan.FileName(0) != "alpha.txt" || plan.FileName(1) != "beta.bin" || plan.FileName(2) != "EXTRA.DAT" {
		t.Fatalf("FileName = %q %q %q", plan.FileName(0), plan.FileName(1), plan.FileName(2))
	}
	if plan.FileSize(0) != int64(len(alpha)) || plan.FileSize(2) != int64(len(extraContent)) {
		t.Fatalf("FileSize = %d/%d", plan.FileSize(0), plan.FileSize(2))
	}
	if plan.FileSourcePath(2) != "" {
		t.Fatalf("FileSourcePath(2) = %q, want \"\" for the extra file", plan.FileSourcePath(2))
	}
	if _, ok := plan.DescriptorAt(udfFirstPartitionLba); ok {
		t.Fatal("DescriptorAt must not be callable before AssignExtents")
	}

	err = plan.AssignExtents(map[int][]UdfFileExtent{
		0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 1}, {FileIndex: 0, Offset: 2048, Lba: 2000, Sectors: 1}},
		1: {{FileIndex: 1, Offset: 0, Lba: 1500, Sectors: 2}, {FileIndex: 1, Offset: 4096, Lba: 3000, Sectors: 1}},
		2: {{FileIndex: 2, Offset: 0, Lba: 2500, Sectors: 2}, {FileIndex: 2, Offset: 4096, Lba: 3200, Sectors: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// DescriptorAt coverage.
	for lba := uint32(288); lba < plan.MetaEndLbaExclusive(); lba++ {
		if _, ok := plan.DescriptorAt(lba); !ok {
			t.Fatalf("DescriptorAt(%d) = !ok inside the metadata block", lba)
		}
	}
	for _, lba := range []uint32{16, 17, 18, 32, 33, 34, 35, 36, 37, 38, 47, 48, 49, 256,
		volumeSectors - 257, volumeSectors - 256, volumeSectors - 241, volumeSectors - 1} {
		if _, ok := plan.DescriptorAt(lba); !ok {
			t.Fatalf("DescriptorAt(%d) = !ok, want descriptor", lba)
		}
	}
	for _, lba := range []uint32{0, 15, 19, 31, 50, 255, 257, 287, 295, 500, 999, 1000, 1001,
		1500, 1501, 2000, 2500, 2501, 3000, 3200, 3838, 3856, 4094} {
		if _, ok := plan.DescriptorAt(lba); ok {
			t.Fatalf("DescriptorAt(%d) = ok, want file data or picture", lba)
		}
	}

	// FileExtents sorted by LBA.
	gotExtents := plan.FileExtents()
	wantLbas := []uint32{1000, 1500, 2000, 2500, 3000, 3200}
	if len(gotExtents) != len(wantLbas) {
		t.Fatalf("FileExtents has %d extents, want %d", len(gotExtents), len(wantLbas))
	}
	for i, lba := range wantLbas {
		if gotExtents[i].Lba != lba {
			t.Fatalf("FileExtents[%d].Lba = %d, want %d", i, gotExtents[i].Lba, lba)
		}
	}

	contents := map[int][]byte{0: alpha, 1: beta, 2: extraContent}
	image := assembleDispersedImage(t, plan, func(index int) []byte { return contents[index] })

	// Structural self-checks.
	if string(image[16*LogicalSectorBytes+1:16*LogicalSectorBytes+6]) != "BEA01" ||
		string(image[17*LogicalSectorBytes+1:17*LogicalSectorBytes+6]) != "NSR03" ||
		string(image[18*LogicalSectorBytes+1:18*LogicalSectorBytes+6]) != "TEA01" {
		t.Fatal("VRS identifiers missing at sectors 16-18")
	}
	for _, lba := range []uint32{256, volumeSectors - 257, volumeSectors - 1} {
		if got := binary.LittleEndian.Uint16(image[int(lba)*LogicalSectorBytes:]); got != 2 {
			t.Fatalf("anchor tag id at sector %d = %d, want 2", lba, got)
		}
	}
	if got := binary.LittleEndian.Uint16(image[udfFirstPartitionLba*LogicalSectorBytes:]); got != 256 {
		t.Fatalf("file set descriptor tag id at sector %d = %d, want 256", udfFirstPartitionLba, got)
	}
	// Root file entry ICB comes from the file set descriptor.
	fsd := image[udfFirstPartitionLba*LogicalSectorBytes:]
	rootFeLba := udfFirstPartitionLba + binary.LittleEndian.Uint32(fsd[404:408])
	files := udfDisperseTestReadDir(t, image, rootFeLba)
	want := map[string][]byte{"alpha.txt": alpha, "beta.bin": beta, "EXTRA.DAT": extraContent}
	if len(files) != len(want) {
		t.Fatalf("root directory lists %d files, want %d", len(files), len(want))
	}
	for name, content := range want {
		feLba, ok := files[name]
		if !ok {
			t.Fatalf("file %s not found in root directory", name)
		}
		if got := udfDisperseTestReadFile(t, image, feLba); !bytes.Equal(got, content) {
			t.Fatalf("file %s content mismatch (%d bytes)", name, len(got))
		}
	}
}

// TestUdfDispersedVrsStart19 verifies the UDF bridge recognition sequence
// position when ISO9660 occupies sectors 16-18.
func TestUdfDispersedVrsStart19(t *testing.T) {
	root, _, _ := buildDispersedTestTree(t)
	const volumeSectors = 4096
	plan, err := PrepareUdfDispersed(root, volumeSectors, "BRIDGE", 19, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = plan.AssignExtents(map[int][]UdfFileExtent{
		0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 2}},
		1: {{FileIndex: 1, Offset: 0, Lba: 1002, Sectors: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, lba := range []uint32{16, 17, 18} {
		if _, ok := plan.DescriptorAt(lba); ok {
			t.Fatalf("DescriptorAt(%d) = ok, want ISO9660 layer sector", lba)
		}
	}
	want := []string{"BEA01", "NSR03", "TEA01"}
	for i, identifier := range want {
		data, ok := plan.DescriptorAt(19 + uint32(i))
		if !ok {
			t.Fatalf("DescriptorAt(%d) = !ok, want VRS sector", 19+i)
		}
		if string(data[1:6]) != identifier {
			t.Fatalf("VRS sector %d identifier = %q, want %q", 19+i, data[1:6], identifier)
		}
	}
}

// TestUdfDispersedAssignErrors exercises AssignExtents validation.
func TestUdfDispersedAssignErrors(t *testing.T) {
	root := t.TempDir()
	small := bytes.Repeat([]byte{0x53}, 100)
	big := bytes.Repeat([]byte{0x42}, 235*LogicalSectorBytes)
	if err := os.WriteFile(filepath.Join(root, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "small.txt"), small, 0o644); err != nil {
		t.Fatal(err)
	}
	const volumeSectors = 4096
	prepare := func(t *testing.T) *UdfDispersedPlan {
		t.Helper()
		plan, err := PrepareUdfDispersed(root, volumeSectors, "ERRORS", 16, nil)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	// big.bin enumerates before small.txt.
	many := make([]UdfFileExtent, 235)
	for i := range many {
		many[i] = UdfFileExtent{FileIndex: 0, Offset: int64(i) * LogicalSectorBytes, Lba: uint32(1000 + i), Sectors: 1}
	}
	cases := []struct {
		name    string
		extents map[int][]UdfFileExtent
		want    string
	}{
		{"out of range key", map[int][]UdfFileExtent{5: {{FileIndex: 5, Offset: 0, Lba: 1000, Sectors: 1}}}, "out of range"},
		{"file index mismatch", map[int][]UdfFileExtent{
			0: {{FileIndex: 1, Offset: 0, Lba: 1000, Sectors: 235}},
			1: {{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 1}},
		}, "does not match"},
		{"zero sectors", map[int][]UdfFileExtent{0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 0}}}, "zero sectors"},
		{"offset gap", map[int][]UdfFileExtent{
			0: {{FileIndex: 0, Offset: 2048, Lba: 1000, Sectors: 235}},
			1: {{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 1}},
		}, "contiguous"},
		{"not covered", map[int][]UdfFileExtent{
			0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 1}},
			1: {{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 1}},
		}, "cover"},
		{"beyond end of file", map[int][]UdfFileExtent{
			0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 235}},
			1: {{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 1}, {FileIndex: 1, Offset: 100, Lba: 2001, Sectors: 1}},
		}, "beyond the end"},
		{"tail too long", map[int][]UdfFileExtent{
			0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 235}},
			1: {{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 2}},
		}, "tail extent"},
		{"inside metadata", map[int][]UdfFileExtent{
			0: {{FileIndex: 0, Offset: 0, Lba: 290, Sectors: 235}},
			1: {{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 1}},
		}, "metadata block"},
		{"inside reserve tail", map[int][]UdfFileExtent{
			0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 235}},
			1: {{FileIndex: 1, Offset: 0, Lba: volumeSectors - 258, Sectors: 2}},
		}, "reserve metadata tail"},
		{"overlap", map[int][]UdfFileExtent{
			0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 200}, {FileIndex: 0, Offset: 200 * LogicalSectorBytes, Lba: 1199, Sectors: 35}},
			1: {{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 1}},
		}, "overlap"},
		{"too many extents", map[int][]UdfFileExtent{
			0: many,
			1: {{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 1}},
		}, "allocation descriptors"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := prepare(t).AssignExtents(tc.extents)
			if err == nil {
				t.Fatal("AssignExtents succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
	// A valid assignment leaves the plan usable.
	plan := prepare(t)
	if _, ok := plan.DescriptorAt(udfFirstPartitionLba); ok {
		t.Fatal("DescriptorAt before AssignExtents = ok, want false")
	}
	if err := plan.AssignExtents(map[int][]UdfFileExtent{
		0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 235}},
		1: {{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 1}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := plan.DescriptorAt(udfFirstPartitionLba); !ok {
		t.Fatal("DescriptorAt after AssignExtents = false, want ok")
	}
}

// TestUdfDispersedXorriso validates a generated dispersed image with xorriso.
//
// This libisofs/xorriso build has no UDF filesystem reader (verified: no
// UDF/NSR03/BEA01 strings in libisofs.so; a pure-UDF image is reported as
// "No ISO 9660 image at LBA 0"). So the image is built as a UDF bridge:
// PrepareUdfDispersed with vrsStart=19 plus a minimal hand-written ISO9660
// layer (PVD at 16, terminator at 17, path tables, root directory) whose file
// records point at the dispersed extent LBAs. xorriso then reads the file
// bytes through exactly the sectors AssignExtents placed, which externally
// validates the dispersed data placement; the UDF descriptor structures
// themselves are re-parsed by the Go structural test above.
func TestUdfDispersedXorriso(t *testing.T) {
	xorriso, err := exec.LookPath("xorriso")
	if err != nil {
		t.Skip("xorriso not installed")
	}
	root, alpha, beta := buildDispersedTestTree(t)
	const volumeSectors = 4096
	plan, err := PrepareUdfDispersed(root, volumeSectors, "HYBRID", 19, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Single extents at scattered LBAs (ISO9660 cannot represent fragments;
	// fragmented extents are covered by the structural test).
	if err := plan.AssignExtents(map[int][]UdfFileExtent{
		0: {{FileIndex: 0, Offset: 0, Lba: 1000, Sectors: 2}},
		1: {{FileIndex: 1, Offset: 0, Lba: 1500, Sectors: 3}},
	}); err != nil {
		t.Fatal(err)
	}
	contents := map[int][]byte{0: alpha, 1: beta}
	image := assembleDispersedImage(t, plan, func(index int) []byte { return contents[index] })
	udfDisperseTestWriteIsoBridge(t, image, volumeSectors, "HYBRID", [][3]uint32{
		{1000, uint32(len(alpha)), 0}, // lba, size, 0 (filled with name index below)
		{1500, uint32(len(beta)), 1},
	})
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "dispersed.iso")
	if err := os.WriteFile(imagePath, image, 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(xorriso, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("xorriso %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	toc := run("-indev", imagePath, "-toc")
	t.Logf("xorriso -toc:\n%s", toc)
	if !strings.Contains(toc, "4096") {
		t.Fatalf("xorriso -toc does not report 4096 blocks:\n%s", toc)
	}
	lsdl := run("-indev", imagePath, "-find", "/", "-type", "f", "-exec", "lsdl")
	t.Logf("xorriso -find / -type f -exec lsdl:\n%s", lsdl)
	for _, name := range []string{"ALPHA.TXT", "BETA.BIN"} {
		if !strings.Contains(lsdl, name) {
			t.Fatalf("xorriso listing does not contain %s:\n%s", name, lsdl)
		}
	}
	for name, want := range map[string][]byte{"ALPHA.TXT": alpha, "BETA.BIN": beta} {
		dest := filepath.Join(dir, "extracted-"+name)
		run("-osirrox", "on", "-indev", imagePath, "-extract", "/"+name, dest)
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("extracted %s: %d bytes, content mismatch", name, len(got))
		}
	}
}

// udfDisperseTestBothEndian16 writes an ISO9660 both-endian 16-bit value.
func udfDisperseTestBothEndian16(field []byte, value uint16) {
	binary.LittleEndian.PutUint16(field[0:2], value)
	binary.BigEndian.PutUint16(field[2:4], value)
}

// udfDisperseTestBothEndian32 writes an ISO9660 both-endian 32-bit value.
func udfDisperseTestBothEndian32(field []byte, value uint32) {
	binary.LittleEndian.PutUint32(field[0:4], value)
	binary.BigEndian.PutUint32(field[4:8], value)
}

// udfDisperseTestDirRecord writes an ISO9660 directory record, returning its
// padded length.
func udfDisperseTestDirRecord(record []byte, lba uint32, size uint32, flags byte, identifier []byte) int {
	udfDisperseTestBothEndian32(record[2:10], lba)
	udfDisperseTestBothEndian32(record[10:18], size)
	record[18] = 126 // 2026-11-14
	record[19] = 11
	record[20] = 14
	record[25] = flags
	udfDisperseTestBothEndian16(record[28:32], 1)
	record[32] = byte(len(identifier))
	copy(record[33:], identifier)
	length := 33 + len(identifier)
	if length%2 != 0 {
		length++
	}
	record[0] = byte(length)
	return length
}

// udfDisperseTestWriteIsoBridge writes a minimal ISO9660 layer into the
// image: PVD at 16, descriptor set terminator at 17, little-endian path table
// at 18, big-endian at 23, root directory at 22. File records point at the
// given extent LBAs under the names ALPHA.TXT;1 / BETA.BIN;1.
func udfDisperseTestWriteIsoBridge(t *testing.T, image []byte, volumeSectors uint32, volumeID string,
	files [][3]uint32) {
	t.Helper()
	names := [][]byte{[]byte("ALPHA.TXT;1"), []byte("BETA.BIN;1")}
	if len(files) != len(names) {
		t.Fatalf("bridge helper expects %d files, got %d", len(names), len(files))
	}
	sector := func(lba uint32) []byte {
		return image[int(lba)*LogicalSectorBytes : int(lba+1)*LogicalSectorBytes]
	}
	const (
		pathTableLba   = 18
		pathTableMLba  = 23
		rootDirLba     = 22
		pathTableBytes = 10
	)

	// Root directory sector.
	rootDir := sector(rootDirLba)
	offset := udfDisperseTestDirRecord(rootDir, rootDirLba, LogicalSectorBytes, 2, []byte{0})
	offset += udfDisperseTestDirRecord(rootDir[offset:], rootDirLba, LogicalSectorBytes, 2, []byte{1})
	for _, file := range files {
		offset += udfDisperseTestDirRecord(rootDir[offset:], file[0], file[1], 0, names[file[2]])
	}

	// Path tables (single root record each).
	pathTableL := sector(pathTableLba)
	pathTableL[0], pathTableL[8] = 1, 0
	binary.LittleEndian.PutUint32(pathTableL[2:6], rootDirLba)
	binary.LittleEndian.PutUint16(pathTableL[6:8], 1)
	pathTableM := sector(pathTableMLba)
	pathTableM[0], pathTableM[8] = 1, 0
	binary.BigEndian.PutUint32(pathTableM[2:6], rootDirLba)
	binary.BigEndian.PutUint16(pathTableM[6:8], 1)

	// Primary volume descriptor.
	pvd := sector(16)
	pvd[0], pvd[6] = 1, 1
	copy(pvd[1:6], "CD001")
	for i := 8; i < 72; i++ {
		pvd[i] = ' '
	}
	copy(pvd[40:72], volumeID)
	udfDisperseTestBothEndian32(pvd[80:88], volumeSectors)
	udfDisperseTestBothEndian16(pvd[120:124], 1) // volume set size
	udfDisperseTestBothEndian16(pvd[124:128], 1) // volume sequence number
	udfDisperseTestBothEndian16(pvd[128:132], LogicalSectorBytes)
	udfDisperseTestBothEndian32(pvd[132:140], pathTableBytes)
	binary.LittleEndian.PutUint32(pvd[140:144], pathTableLba)
	binary.BigEndian.PutUint32(pvd[148:152], pathTableMLba)
	if recordLength := udfDisperseTestDirRecord(pvd[156:190], rootDirLba, LogicalSectorBytes, 2, []byte{0}); recordLength != 34 {
		t.Fatalf("root record length = %d, want 34", recordLength)
	}
	for i := 190; i < 813; i++ {
		pvd[i] = ' '
	}
	copy(pvd[813:830], "2026111400000000\x00") // creation
	copy(pvd[830:847], "2026111400000000\x00") // modification
	copy(pvd[847:864], "0000000000000000\x00") // expiration: unspecified
	copy(pvd[864:881], "2026111400000000\x00") // effective
	pvd[881] = 1                               // file structure version

	// Volume descriptor set terminator.
	terminator := sector(17)
	terminator[0], terminator[6] = 255, 1
	copy(terminator[1:6], "CD001")
}

// TestUdfDispersedFindFile exercises image-root-relative path resolution for
// the boot-image option: nested files resolve, directories, symlinks and
// missing names do not.
func TestUdfDispersedFindFile(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"top.txt":           "top",
		"sub/inner.txt":     "inner",
		"sub/deep/deep.txt": "deep",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("top.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	extras := []UdfExtraFile{{Name: "EXTRA.DAT", Size: 3, Fill: func() ([]byte, error) { return []byte("abc"), nil }}}
	plan, err := PrepareUdfDispersed(root, 4096, "FIND", 16, extras)
	if err != nil {
		t.Fatal(err)
	}
	indexOf := func(name string) int {
		for i := 0; i < plan.FileCount(); i++ {
			if plan.FileName(i) == name {
				return i
			}
		}
		return -1
	}
	for _, path := range []string{"top.txt", "/top.txt", "sub/inner.txt", "./sub//inner.txt", "sub/deep/deep.txt", "EXTRA.DAT"} {
		index, ok := plan.FindFile(path)
		if !ok {
			t.Fatalf("FindFile(%q) = not found", path)
		}
		want := filepath.Base(path)
		if plan.FileName(index) != want {
			t.Fatalf("FindFile(%q) resolves to %q", path, plan.FileName(index))
		}
		if index != indexOf(want) {
			t.Fatalf("FindFile(%q) index %d, want %d", path, index, indexOf(want))
		}
	}
	for _, path := range []string{"", ".", "sub", "sub/deep", "missing.txt", "sub/missing.txt", "link.txt", "top.txt/extra"} {
		if index, ok := plan.FindFile(path); ok {
			t.Fatalf("FindFile(%q) = %d, want not found", path, index)
		}
	}
}

// TestUdfDispersedOwnershipAndModes: the dispersed writer defaults ownership
// to uid:gid 0:0 (root) and copies each node's permission bits from the
// source; extras keep the historical read-only word.
func TestUdfDispersedOwnershipAndModes(t *testing.T) {
	root := t.TempDir()
	write := func(name string, perm os.FileMode) {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, perm); err != nil {
			t.Fatal(err)
		}
	}
	write("secret.txt", 0o600) // owner rw
	write("script.sh", 0o755)  // rwxr-xr-x
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o750); err != nil { // rwxr-x---
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o711); err != nil { // rwx--x--x
		t.Fatal(err)
	}
	extras := []UdfExtraFile{{Name: "EXTRA.DAT", Size: 1, Fill: func() ([]byte, error) { return []byte("x"), nil }}}
	plan, err := PrepareUdfDispersed(root, 4096, "MODES", 16, extras)
	if err != nil {
		t.Fatal(err)
	}
	lba := plan.MetaEndLbaExclusive()
	extents := map[int][]UdfFileExtent{}
	for i := 0; i < plan.FileCount(); i++ {
		sectors := uint32((plan.FileSize(i) + LogicalSectorBytes - 1) / LogicalSectorBytes)
		if sectors > 0 {
			extents[i] = []UdfFileExtent{{FileIndex: i, Offset: 0, Lba: lba, Sectors: sectors}}
			lba += sectors
		}
	}
	if err := plan.AssignExtents(extents); err != nil {
		t.Fatal(err)
	}
	image := assembleDispersedImage(t, plan, func(int) []byte { return []byte("x") })

	check := func(feLba uint32, wantPermissions uint32, label string) {
		t.Helper()
		sector := image[int(feLba)*LogicalSectorBytes:]
		if binary.LittleEndian.Uint16(sector[0:2]) != 261 {
			t.Fatalf("%s: entry at %d is not a file entry", label, feLba)
		}
		if got := binary.LittleEndian.Uint32(sector[36:40]); got != 0 {
			t.Fatalf("%s: uid = %d, want 0", label, got)
		}
		if got := binary.LittleEndian.Uint32(sector[40:44]); got != 0 {
			t.Fatalf("%s: gid = %d, want 0", label, got)
		}
		if got := binary.LittleEndian.Uint32(sector[44:48]); got != wantPermissions {
			t.Fatalf("%s: permissions = %#x, want %#x", label, got, wantPermissions)
		}
	}

	fsd := image[udfFirstPartitionLba*LogicalSectorBytes:]
	rootFeLba := udfFirstPartitionLba + binary.LittleEndian.Uint32(fsd[404:408])
	check(rootFeLba, 0x1c21, "root directory") // rwx--x--x
	files := udfDisperseTestReadDir(t, image, rootFeLba)
	check(files["secret.txt"], 0x1800, "secret.txt") // rw-------
	check(files["script.sh"], 0x1ca5, "script.sh")   // rwxr-xr-x
	check(files["sub"], 0x1ca0, "sub")               // rwxr-x---
	check(files["EXTRA.DAT"], 0x1084, "EXTRA.DAT")   // extras stay read-only
}
