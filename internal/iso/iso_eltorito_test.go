package iso

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestBridge builds the layer used by the El Torito tests: a small volume
// with a 4 KiB boot image planned at LBA 300 and nothing exposed.
func newTestBridge(t *testing.T) *ElToritoBridge {
	t.Helper()
	bridge, err := NewElToritoBridge(4096, "TEST", "boot.img", 4096, "", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.SetPlacements(300, 0, nil); err != nil {
		t.Fatal(err)
	}
	return bridge
}

func mustBridgeSector(t *testing.T, bridge *ElToritoBridge, lba uint32) []byte {
	t.Helper()
	sector, ok, err := bridge.SectorContent(lba)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("sector %d not owned by the bridge", lba)
	}
	if len(sector) != LogicalSectorBytes {
		t.Fatalf("sector %d size = %d, want %d", lba, len(sector), LogicalSectorBytes)
	}
	return sector
}

func TestElToritoBridgeDescriptors(t *testing.T) {
	bridge := newTestBridge(t)

	pvd := mustBridgeSector(t, bridge, 16)
	if pvd[0] != 1 || string(pvd[1:6]) != "CD001" || pvd[6] != 1 {
		t.Fatal("sector 16 is not a primary volume descriptor")
	}
	if got := binary.LittleEndian.Uint32(pvd[80:84]); got != 4096 {
		t.Fatalf("PVD volume space size = %d, want 4096", got)
	}
	if got := binary.BigEndian.Uint32(pvd[84:88]); got != 4096 {
		t.Fatalf("PVD big-endian volume space size = %d, want 4096", got)
	}
	if got := string(pvd[40:72]); got != "TEST                            " {
		t.Fatalf("PVD volume id = %q", got)
	}
	if got := binary.LittleEndian.Uint32(pvd[132:136]); got != uint32(bridge.pathTableBytes) {
		t.Fatalf("PVD path table size = %d, want %d", got, bridge.pathTableBytes)
	}
	if got := binary.LittleEndian.Uint32(pvd[140:144]); got != bridgePathTableLba {
		t.Fatalf("PVD path table LBA = %d, want %d", got, bridgePathTableLba)
	}
	// Root directory record embedded at offset 156 points at sector 23.
	if got := binary.LittleEndian.Uint32(pvd[158:162]); got != bridge.root.primaryDirectoryLba {
		t.Fatalf("PVD root record LBA = %d", got)
	}
	if got := binary.LittleEndian.Uint32(pvd[166:170]); got != LogicalSectorBytes {
		t.Fatalf("PVD root record extent length = %d, want %d", got, LogicalSectorBytes)
	}

	bootRecord := mustBridgeSector(t, bridge, 17)
	if bootRecord[0] != 0 || string(bootRecord[1:6]) != "CD001" || bootRecord[6] != 1 {
		t.Fatal("sector 17 is not a boot record volume descriptor")
	}
	// The identifier is NUL-padded (SeaBIOS strcmp()s it).
	if got := string(bootRecord[7:30]); got != "EL TORITO SPECIFICATION" {
		t.Fatalf("boot record identifier = %q", got)
	}
	for i, v := range bootRecord[30:39] {
		if v != 0 {
			t.Fatalf("boot record identifier padding byte %d = %#x, want 0", 30+i, v)
		}
	}
	if got := binary.LittleEndian.Uint32(bootRecord[71:75]); got != bridge.catalogLba {
		t.Fatalf("boot catalog LBA at offset 71 = %d", got)
	}

	terminator := mustBridgeSector(t, bridge, bridgeTerminatorLba)
	if terminator[0] != 255 || string(terminator[1:6]) != "CD001" || terminator[6] != 1 {
		t.Fatal("sector 18 is not a volume descriptor set terminator")
	}

	pathTable := mustBridgeSector(t, bridge, bridgePathTableLba)
	if pathTable[0] != 1 || pathTable[1] != 0 ||
		binary.LittleEndian.Uint32(pathTable[2:6]) != bridge.root.primaryDirectoryLba ||
		binary.LittleEndian.Uint16(pathTable[6:8]) != 1 ||
		pathTable[8] != 0 {
		t.Fatalf("bad root-only path table: % x", pathTable[:10])
	}

	// Sectors owned by the UDF layer or the planner are not emitted.
	for _, lba := range []uint32{15, 20, 21, 22, 300, 4095} {
		if _, ok, err := bridge.SectorContent(lba); err != nil || ok {
			t.Fatalf("SectorContent(%d) = ok %v, err %v; want ok=false, err=nil", lba, ok, err)
		}
	}
}

func TestElToritoBridgeCatalog(t *testing.T) {
	bridge := newTestBridge(t)
	catalog := mustBridgeSector(t, bridge, bridge.catalogLba)

	// Validation entry: 16 little-endian uint16 words must sum to zero.
	var sum uint16
	for i := 0; i < 32; i += 2 {
		sum += binary.LittleEndian.Uint16(catalog[i:])
	}
	if sum != 0 {
		t.Fatalf("validation entry checksum sum = %#x, want 0", sum)
	}
	if catalog[0] != 1 || catalog[1] != 0xEF {
		t.Fatalf("validation entry header/platform = %#x/%#x, want 1/0xEF (EFI-only)", catalog[0], catalog[1])
	}
	if catalog[30] != 0x55 || catalog[31] != 0xAA {
		t.Fatalf("validation entry signature = %#02x %#02x, want 55 aa", catalog[30], catalog[31])
	}

	// Initial/Default entry.
	if catalog[32] != 0x88 {
		t.Fatalf("default entry boot indicator = %#x, want 0x88", catalog[32])
	}
	if catalog[33] != 0 {
		t.Fatalf("default entry media type = %d, want 0 (no emulation)", catalog[33])
	}
	if got := binary.LittleEndian.Uint16(catalog[34:36]); got != 0 {
		t.Fatalf("default entry load segment = %#x, want 0", got)
	}
	if catalog[36] != 0 || catalog[37] != 0 {
		t.Fatalf("default entry system type/unused = %#x/%#x, want 0/0", catalog[36], catalog[37])
	}
	if got := binary.LittleEndian.Uint16(catalog[38:40]); got != 8 {
		t.Fatalf("default entry sector count = %d, want 8 (4 KiB in 512-byte units)", got)
	}
	if got := binary.LittleEndian.Uint32(catalog[40:44]); got != 300 {
		t.Fatalf("default entry load LBA = %d, want 300", got)
	}
	// No further entries: the remainder of the sector is zero.
	for i, v := range catalog[64:] {
		if v != 0 {
			t.Fatalf("catalog byte %d beyond the default entry = %#x, want 0", 64+i, v)
		}
	}
}

type parsedDirRecord struct {
	lba   uint32
	size  uint32
	flags byte
	id    string
}

func parseDirRecords(t *testing.T, extent []byte) []parsedDirRecord {
	t.Helper()
	var records []parsedDirRecord
	for offset := 0; offset < len(extent) && extent[offset] != 0; {
		length := int(extent[offset])
		if length < 34 || offset+length > len(extent) {
			t.Fatalf("bad directory record length %d at offset %d", length, offset)
		}
		record := extent[offset : offset+length]
		idLength := int(record[32])
		records = append(records, parsedDirRecord{
			lba:   binary.LittleEndian.Uint32(record[2:10]),
			size:  binary.LittleEndian.Uint32(record[10:18]),
			flags: record[25],
			id:    string(record[33 : 33+idLength]),
		})
		offset += length
	}
	return records
}

func TestElToritoBridgeRootDirectory(t *testing.T) {
	bridge := newTestBridge(t)
	extent := mustBridgeSector(t, bridge, bridge.root.primaryDirectoryLba)
	records := parseDirRecords(t, extent)
	if len(records) != 4 {
		t.Fatalf("root directory has %d records, want 4", len(records))
	}

	want := []parsedDirRecord{
		{lba: bridge.root.primaryDirectoryLba, size: LogicalSectorBytes, flags: 2, id: "\x00"},
		{lba: bridge.root.primaryDirectoryLba, size: LogicalSectorBytes, flags: 2, id: "\x01"},
		{lba: bridge.catalogLba, size: LogicalSectorBytes, flags: 0, id: "BOOT.CAT;1"},
		{lba: 300, size: 4096, flags: 0, id: "BOOT.IMG;1"},
	}
	for i, w := range want {
		if records[i] != w {
			t.Fatalf("record %d = %+v, want %+v", i, records[i], w)
		}
	}
}

func TestElToritoBridgeAccessors(t *testing.T) {
	bridge := newTestBridge(t)
	if bridge.BootImageName() != "boot.img" || bridge.BootImageSize() != 4096 {
		t.Fatalf("accessors = %q/%d, want boot.img/4096", bridge.BootImageName(), bridge.BootImageSize())
	}

}

func TestElToritoBridgeValidation(t *testing.T) {
	if _, err := NewElToritoBridge(25, "TEST", "boot.img", 4096, "", 0, nil, nil); err == nil {
		t.Fatal("volumeSectors 25: want error")
	}
	if _, err := NewElToritoBridge(4096, "TEST", "boot.img", 0, "", 0, nil, nil); err == nil {
		t.Fatal("bootImageSize 0: want error")
	}
	if _, err := NewElToritoBridge(4096, "TEST", "boot.img", maxBootImageBytes+1, "", 0, nil, nil); err == nil {
		t.Fatal("bootImageSize > 8 GiB: want error")
	}
	if _, err := NewElToritoBridge(4096, "TEST", "...", 4096, "", 0, nil, nil); err == nil {
		t.Fatal("boot image name normalizing to empty: want error")
	}

	bridge, err := NewElToritoBridge(4096, "TEST", "boot.img", 4096, "", 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := bridge.SectorContent(16); err == nil {
		t.Fatal("SectorContent before SetPlacements: want error")
	}
	for _, lba := range []uint32{0, 24, 4096, 5000} {
		if err := bridge.SetPlacements(lba, 0, nil); err == nil {
			t.Fatalf("SetPlacements(%d): want error", lba)
		}
	}
	if err := bridge.SetPlacements(300, 0, nil); err != nil {
		t.Fatal(err)
	}
}

// TestElToritoBridgeXorriso writes a full image whose non-bridge sectors are
// zero and lets xorriso validate the El Torito structures.
func TestElToritoBridgeXorriso(t *testing.T) {
	xorriso, err := exec.LookPath("xorriso")
	if err != nil {
		t.Skip("xorriso not installed")
	}
	bridge := newTestBridge(t)
	const volumeSectors = 4096
	image := make([]byte, volumeSectors*LogicalSectorBytes)
	for _, extent := range bridge.MetadataExtents() {
		copy(image[int(extent.Lba)*LogicalSectorBytes:], mustBridgeSector(t, bridge, extent.Lba))
	}
	path := filepath.Join(t.TempDir(), "bridge.iso")
	if err := os.WriteFile(path, image, 0o644); err != nil {
		t.Fatal(err)
	}

	output, err := exec.Command(xorriso, "-indev", path, "-report_el_torito", "plain").CombinedOutput()
	if err != nil {
		t.Fatalf("xorriso failed: %v\n%s", err, output)
	}
	text := string(output)
	t.Logf("xorriso -report_el_torito plain:\n%s", text)
	if !strings.Contains(text, fmt.Sprintf("El Torito catalog  : %d", bridge.catalogLba)) {
		t.Fatalf("xorriso did not report the boot catalog:\n%s", text)
	}
	// The boot image line must report a UEFI no-emulation entry with the
	// 4 KiB size (8 512-byte sectors) at the boot image LBA (the bridge's
	// --boot-image entry is the EFI one).
	bootLine := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "El Torito boot img") {
			bootLine = line
		}
	}
	if bootLine == "" || !strings.Contains(bootLine, "UEFI") ||
		!strings.Contains(bootLine, "none") ||
		!strings.Contains(bootLine, " 8 ") ||
		!strings.Contains(bootLine, " 300") {
		t.Fatalf("xorriso did not report the UEFI no-emulation boot entry at LBA 300:\n%s", text)
	}
}

// TestElToritoBridgeExposedTree exposes /a/bar and /a/b/barz: the ISO9660
// side gets a directory /A holding BAR;1 and a subdirectory B holding BARZ;1.
func TestElToritoBridgeExposedTree(t *testing.T) {
	bridge, err := NewElToritoBridge(4096, "TEST", "boot.img", 4096,
		"", 0, []ExposedFile{{Path: "a/bar", Size: 100}, {Path: "a/b/barz", Size: 200}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Layout: path table @22 (3 entries × 10 bytes → 1 sector), then the
	// directory extents in path-table order (root@23, A@24, B@25), catalog@26.
	if err := bridge.SetPlacements(300, 0, []uint32{400, 500}); err != nil {
		t.Fatal(err)
	}

	// Path table: (root, A, B) with parents (1, 1, 2) and extents (23, 24, 25).
	pt := mustBridgeSector(t, bridge, bridgePathTableLba)
	type ptEntry struct {
		lba, parent uint32
		id          byte
	}
	want := []ptEntry{{51, 1, 0}, {52, 1, 'A'}, {53, 2, 'B'}}
	for i, w := range want {
		off := i * 10
		got := ptEntry{binary.LittleEndian.Uint32(pt[off+2:]), uint32(binary.LittleEndian.Uint16(pt[off+6:])), pt[off+8]}
		if pt[off] != 1 || pt[off+1] != 0 || got != w {
			t.Fatalf("path table entry %d = len %d lba %d parent %d id %q, want %+v", i, pt[off], got.lba, got.parent, got.id, w)
		}
	}

	root := parseDirRecords(t, mustBridgeSector(t, bridge, 51))
	wantRoot := []parsedDirRecord{
		{lba: 51, size: LogicalSectorBytes, flags: 2, id: "\x00"},
		{lba: 51, size: LogicalSectorBytes, flags: 2, id: "\x01"},
		{lba: bridge.catalogLba, size: LogicalSectorBytes, flags: 0, id: "BOOT.CAT;1"},
		{lba: 300, size: 4096, flags: 0, id: "BOOT.IMG;1"},
		{lba: 52, size: LogicalSectorBytes, flags: 2, id: "A"},
	}
	if len(root) != len(wantRoot) {
		t.Fatalf("root records = %+v", root)
	}
	for i, w := range wantRoot {
		if root[i] != w {
			t.Fatalf("root record %d = %+v, want %+v", i, root[i], w)
		}
	}

	dirA := parseDirRecords(t, mustBridgeSector(t, bridge, 52))
	wantA := []parsedDirRecord{
		{lba: 52, size: LogicalSectorBytes, flags: 2, id: "\x00"},
		{lba: 51, size: LogicalSectorBytes, flags: 2, id: "\x01"},
		{lba: 400, size: 100, flags: 0, id: "BAR;1"},
		{lba: 53, size: LogicalSectorBytes, flags: 2, id: "B"},
	}
	if len(dirA) != len(wantA) {
		t.Fatalf("/A records = %+v", dirA)
	}
	for i, w := range wantA {
		if dirA[i] != w {
			t.Fatalf("/A record %d = %+v, want %+v", i, dirA[i], w)
		}
	}

	dirB := parseDirRecords(t, mustBridgeSector(t, bridge, 53))
	wantB := []parsedDirRecord{
		{lba: 53, size: LogicalSectorBytes, flags: 2, id: "\x00"},
		{lba: 52, size: LogicalSectorBytes, flags: 2, id: "\x01"},
		{lba: 500, size: 200, flags: 0, id: "BARZ;1"},
	}
	if len(dirB) != len(wantB) {
		t.Fatalf("/A/B records = %+v", dirB)
	}
	for i, w := range wantB {
		if dirB[i] != w {
			t.Fatalf("/A/B record %d = %+v, want %+v", i, dirB[i], w)
		}
	}

	// The metadata extent list covers exactly the bridge sectors.
	var lbas []uint32
	for _, extent := range bridge.MetadataExtents() {
		lbas = append(lbas, extent.Lba)
	}
	wantLbas := []uint32{16, 17, 18, 19, 54, 55, 56, 57, 50, 51, 52, 53, 58}
	if len(lbas) != len(wantLbas) {
		t.Fatalf("MetadataExtents LBAs = %v", lbas)
	}
	for i := range wantLbas {
		if lbas[i] != wantLbas[i] {
			t.Fatalf("MetadataExtents[%d] = %d, want %d (all: %v)", i, lbas[i], wantLbas[i], lbas)
		}
	}
}

// TestElToritoBridgeExposedValidation: bad paths, empty files and oversized
// trees are rejected.
func TestElToritoBridgeExposedValidation(t *testing.T) {
	if _, err := NewElToritoBridge(4096, "TEST", "boot.img", 4096,
		"", 0, []ExposedFile{{Path: "a/../b", Size: 1}}, nil); err == nil {
		t.Fatal("path with '..': want error")
	}
	if _, err := NewElToritoBridge(4096, "TEST", "boot.img", 4096,
		"", 0, []ExposedFile{{Path: "", Size: 1}}, nil); err == nil {
		t.Fatal("empty path: want error")
	}
	// An empty exposed file is allowed; its record carries extent LBA 0.
	bridge, err := NewElToritoBridge(4096, "TEST", "boot.img", 4096,
		"", 0, []ExposedFile{{Path: "empty.bin", Size: 0}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.SetPlacements(300, 0, []uint32{0}); err != nil {
		t.Fatal(err)
	}
	records := parseDirRecords(t, mustBridgeSector(t, bridge, bridge.root.primaryDirectoryLba))
	last := records[len(records)-1]
	if last.id != "EMPTY.BIN;1" || last.lba != 0 || last.size != 0 {
		t.Fatalf("empty file record = %+v", last)
	}
	if err := bridge.SetPlacements(300, 0, []uint32{400}); err == nil {
		t.Fatal("nonzero LBA for an empty file: want error")
	}
	// Too many directories: the tree must end before the anchor at 256.
	var many []ExposedFile
	for i := 0; i < 250; i++ {
		many = append(many, ExposedFile{Path: fmt.Sprintf("d%03d/f", i), Size: 1})
	}
	if _, err := NewElToritoBridge(4096, "TEST", "boot.img", 4096, "", 0, many, nil); err == nil {
		t.Fatal("250 directories: want error (anchor at 256)")
	}
}

// TestElToritoBridgeLegacyCatalog: with only a legacy BIOS image the catalog
// is [validation(x86), default entry] with the load size rounded up to 2 KiB.
func TestElToritoBridgeLegacyCatalog(t *testing.T) {
	bridge, err := NewElToritoBridge(4096, "TEST", "", 0, "boot/loader.bin", 2500, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.SetPlacements(0, 300, nil); err != nil {
		t.Fatal(err)
	}
	catalog := mustBridgeSector(t, bridge, bridge.catalogLba)
	if catalog[0] != 1 || catalog[1] != 0 {
		t.Fatalf("validation entry header/platform = %#x/%#x, want 1/0 (x86)", catalog[0], catalog[1])
	}
	entry := catalog[32:64]
	if entry[0] != 0x88 || entry[1] != 0 {
		t.Fatalf("legacy entry indicator/media = %#x/%#x, want 0x88/0 (no emulation)", entry[0], entry[1])
	}
	// 2500 bytes -> 2 sectors of 2048 -> 8 sectors of 512 (rounded up to 2 KiB).
	if got := binary.LittleEndian.Uint16(entry[6:8]); got != 8 {
		t.Fatalf("legacy entry load size = %d, want 8 (2 KiB rounding)", got)
	}
	if got := binary.LittleEndian.Uint32(entry[8:12]); got != 300 {
		t.Fatalf("legacy entry LBA = %d, want 300", got)
	}
	for i, v := range catalog[64:] {
		if v != 0 {
			t.Fatalf("catalog byte %d beyond the default entry = %#x, want 0 (no EFI section)", 64+i, v)
		}
	}
}

// TestElToritoBridgeBothCatalog: with both images the catalog is [validation
// (x86), legacy BIOS default entry, 0x91 section header (platform 0xEF, one
// entry), EFI entry] — the legacy entry always precedes the EFI one.
func TestElToritoBridgeBothCatalog(t *testing.T) {
	bridge, err := NewElToritoBridge(4096, "TEST", "efi.img", 1024, "bios.img", 2500, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.SetPlacements(300, 400, nil); err != nil {
		t.Fatal(err)
	}
	catalog := mustBridgeSector(t, bridge, bridge.catalogLba)
	if catalog[0] != 1 || catalog[1] != 0 {
		t.Fatalf("validation entry header/platform = %#x/%#x, want 1/0 (x86)", catalog[0], catalog[1])
	}
	legacy := catalog[32:64]
	if legacy[0] != 0x88 || legacy[1] != 0 {
		t.Fatalf("legacy entry indicator/media = %#x/%#x", legacy[0], legacy[1])
	}
	if got := binary.LittleEndian.Uint16(legacy[6:8]); got != 8 {
		t.Fatalf("legacy load size = %d, want 8 (2500 bytes rounded up to 2 KiB)", got)
	}
	if got := binary.LittleEndian.Uint32(legacy[8:12]); got != 400 {
		t.Fatalf("legacy LBA = %d, want 400", got)
	}
	header := catalog[64:96]
	if header[0] != 0x91 || header[1] != 0xEF {
		t.Fatalf("section header = %#x/%#x, want 0x91/0xEF (final, EFI)", header[0], header[1])
	}
	if got := binary.LittleEndian.Uint16(header[2:4]); got != 1 {
		t.Fatalf("section entry count = %d, want 1", got)
	}
	efi := catalog[96:128]
	if efi[0] != 0x88 || efi[1] != 0 {
		t.Fatalf("EFI entry indicator/media = %#x/%#x", efi[0], efi[1])
	}
	if got := binary.LittleEndian.Uint16(efi[6:8]); got != 2 {
		t.Fatalf("EFI load size = %d, want 2 (1024 bytes in 512-byte units)", got)
	}
	if got := binary.LittleEndian.Uint32(efi[8:12]); got != 300 {
		t.Fatalf("EFI LBA = %d, want 300", got)
	}
	for i, v := range catalog[128:] {
		if v != 0 {
			t.Fatalf("catalog byte %d beyond the EFI entry = %#x, want 0", 128+i, v)
		}
	}
}

// TestElToritoBridgeBootImageRequired: at least one boot image is required.
func TestElToritoBridgeBootImageRequired(t *testing.T) {
	if _, err := NewElToritoBridge(4096, "TEST", "", 0, "", 0, nil, nil); err == nil {
		t.Fatal("no boot image: want error")
	}
	if _, err := NewElToritoBridge(4096, "TEST", "", 0, "bios.img", 0, nil, nil); err == nil {
		t.Fatal("empty legacy boot image: want error")
	}
}

// TestPatchBootInfoTable: the table lands in bytes 8..55 with the PVD/file
// LBAs, the image length and the checksum; everything else stays untouched.
func TestPatchBootInfoTable(t *testing.T) {
	sector := make([]byte, LogicalSectorBytes)
	for i := range sector {
		sector[i] = byte(i)
	}
	want := BootInfoTableChecksum(sector)
	PatchBootInfoTable(sector, 300, 1500, want)
	if got := binary.LittleEndian.Uint32(sector[8:12]); got != primaryVolumeDescriptorLba {
		t.Fatalf("bi_pvd = %d, want %d", got, primaryVolumeDescriptorLba)
	}
	if got := binary.LittleEndian.Uint32(sector[12:16]); got != 300 {
		t.Fatalf("bi_file = %d, want 300", got)
	}
	if got := binary.LittleEndian.Uint32(sector[16:20]); got != 1500 {
		t.Fatalf("bi_length = %d, want 1500", got)
	}
	if got := binary.LittleEndian.Uint32(sector[20:24]); got != want {
		t.Fatalf("bi_csum = %#x, want %#x", got, want)
	}
	for i := 24; i < 56; i++ {
		if sector[i] != 0 {
			t.Fatalf("reserved byte %d = %#x, want 0", i, sector[i])
		}
	}
	for i := 0; i < 8; i++ {
		if sector[i] != byte(i) {
			t.Fatalf("byte %d (before the table) was modified", i)
		}
	}
	for i := 56; i < LogicalSectorBytes; i++ {
		if sector[i] != byte(i) {
			t.Fatalf("byte %d (after the table) was modified", i)
		}
	}
}

// TestBootInfoTableChecksum: the checksum is the little-endian uint32 sum of
// bytes [64, len) with a zero-padded tail — byte-equal to xorriso/libisofs on
// these reference vectors (produced by xorriso -boot-info-table).
func TestBootInfoTableChecksum(t *testing.T) {
	content3000 := make([]byte, 3000)
	for i := range content3000 {
		content3000[i] = byte(i*7 + 3)
	}
	if got := BootInfoTableChecksum(content3000); got != 0xd1c3b32e {
		t.Fatalf("3000-byte checksum = %#x, want 0xd1c3b32e (xorriso reference)", got)
	}
	content3001 := append(content3000, byte(11)) // pattern value at i=3000
	if got := BootInfoTableChecksum(content3001); got != 0xd1c3b339 {
		t.Fatalf("3001-byte checksum = %#x, want 0xd1c3b339 (xorriso reference)", got)
	}
	if got := BootInfoTableChecksum(make([]byte, 100)); got != 0 {
		t.Fatalf("short/zero checksum = %#x, want 0", got)
	}
}
