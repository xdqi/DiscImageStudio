package iso

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fullRecord is one parsed directory record with its Rock Ridge fields.
type fullRecord struct {
	lba    uint32
	size   uint32
	flags  byte
	id     string
	susp   map[string][]byte // first entry of each signature (body after the 4-byte header)
	nmName string            // concatenated NM content
}

func parseFullRecords(t *testing.T, image []byte, lba, size uint32) []fullRecord {
	t.Helper()
	extent := image[int(lba)*LogicalSectorBytes : int(lba)*LogicalSectorBytes+int(size)]
	var records []fullRecord
	for offset := 0; offset < len(extent); {
		if extent[offset] == 0 { // sector padding
			offset = (offset/LogicalSectorBytes + 1) * LogicalSectorBytes
			continue
		}
		length := int(extent[offset])
		record := extent[offset : offset+length]
		if length%2 != 0 {
			t.Fatalf("record at offset %d has odd length %d", offset, length)
		}
		if offset/LogicalSectorBytes != (offset+length-1)/LogicalSectorBytes {
			t.Fatalf("record at offset %d spans a sector boundary", offset)
		}
		idLength := int(record[32])
		r := fullRecord{
			lba:   binary.LittleEndian.Uint32(record[2:6]),
			size:  binary.LittleEndian.Uint32(record[10:14]),
			flags: record[25],
			id:    string(record[33 : 33+idLength]),
			susp:  map[string][]byte{},
		}
		if binary.BigEndian.Uint32(record[6:10]) != r.lba || binary.BigEndian.Uint32(record[14:18]) != r.size {
			t.Fatalf("record %q: both-endian fields disagree", r.id)
		}
		sua := record[33+idLength+(1-idLength%2):]
		for len(sua) >= 4 && sua[2] >= 4 {
			signature, entryLength := string(sua[:2]), int(sua[2])
			if _, seen := r.susp[signature]; !seen {
				r.susp[signature] = sua[4:entryLength]
			}
			if signature == "NM" {
				r.nmName += string(sua[5:entryLength])
			}
			sua = sua[entryLength:]
		}
		records = append(records, r)
		offset += length
	}
	return records
}

func pxMode(t *testing.T, r fullRecord) uint32 {
	t.Helper()
	px, ok := r.susp["PX"]
	if !ok {
		t.Fatalf("record %q has no PX entry", r.id)
	}
	return binary.LittleEndian.Uint32(px[0:4])
}

// buildFullTestImage builds a full-tree bridge over a small tree whose
// a/big.bin is three NON-adjacent, out-of-order fragments, and assembles the
// image (bridge sectors plus file content at the extents).
func buildFullTestImage(t *testing.T) (*ElToritoBridge, []byte, map[string][]byte) {
	t.Helper()
	const volumeSectors = 8192
	ts := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	content := map[string][]byte{
		".disk/info":   []byte("Debian-ish\n"),
		"a/big.bin":    bytes.Repeat([]byte("0123456789abcdef"), (3*2048+100)/16+1)[:3*2048+100],
		"boot/efi.img": bytes.Repeat([]byte{0xEF}, 4096),
	}
	tree := []BridgeTreeEntry{
		{Path: "", IsDirectory: true, Mode: 0o755, ModTime: ts, FileIndex: -1},
		{Path: ".disk", IsDirectory: true, Mode: 0o755, ModTime: ts, FileIndex: -1},
		{Path: "a", IsDirectory: true, Mode: 0o750, ModTime: ts, FileIndex: -1},
		{Path: "boot", IsDirectory: true, Mode: 0o755, ModTime: ts, FileIndex: -1},
		{Path: ".disk/info", Size: int64(len(content[".disk/info"])), Mode: 0o644, ModTime: ts, FileIndex: 0},
		{Path: "a/big.bin", Size: int64(len(content["a/big.bin"])), Mode: 0o600, ModTime: ts, FileIndex: 1},
		{Path: "a/empty", Mode: 0o644, ModTime: ts, FileIndex: 2},
		{Path: "a/link", IsSymlink: true, LinkTarget: "big.bin", Mode: 0o777, ModTime: ts, FileIndex: 3},
		{Path: "boot/efi.img", Size: 4096, Mode: 0o644, ModTime: ts, FileIndex: 4},
	}
	bridge, err := NewElToritoBridgeFull(volumeSectors, "FULL", "boot/efi.img", 4096, "", 0, tree)
	if err != nil {
		t.Fatal(err)
	}
	need, err := bridge.FullTreeSectors(map[int]int{1: 3})
	if err != nil {
		t.Fatal(err)
	}
	extents := map[int][]UdfFileExtent{
		0: {{FileIndex: 0, Offset: 0, Lba: 700, Sectors: 1}},
		1: { // out of order on the disc, with gaps
			{FileIndex: 1, Offset: 0, Lba: 2000, Sectors: 1},
			{FileIndex: 1, Offset: 2048, Lba: 1500, Sectors: 1},
			{FileIndex: 1, Offset: 4096, Lba: 3000, Sectors: 2},
		},
		3: {{FileIndex: 3, Offset: 0, Lba: 710, Sectors: 1}}, // the UDF symlink body: not an ISO extent
		4: {{FileIndex: 4, Offset: 0, Lba: 800, Sectors: 2}},
	}
	if err := bridge.SetFullPlacements(300, need, 800, 0, extents); err != nil {
		t.Fatal(err)
	}
	image := make([]byte, volumeSectors*LogicalSectorBytes)
	for lba := uint32(0); lba < volumeSectors; lba++ {
		sector, ok, err := bridge.SectorContent(lba)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			if lba < 16 || (lba > 19 && (lba < 300 || lba >= 300+need)) {
				t.Fatalf("bridge owns sector %d outside 16..19 and the tree region [300, %d)", lba, 300+need)
			}
			copy(image[int(lba)*LogicalSectorBytes:], sector)
		}
	}
	paths := map[int]string{0: ".disk/info", 1: "a/big.bin", 4: "boot/efi.img"}
	for index, list := range extents {
		data, ok := content[paths[index]]
		if !ok {
			continue
		}
		for _, e := range list {
			copy(image[int(e.Lba)*LogicalSectorBytes:], data[e.Offset:min(int64(len(data)), e.Offset+int64(e.Sectors)*LogicalSectorBytes)])
		}
	}
	return bridge, image, content
}

func TestElToritoBridgeFullTree(t *testing.T) {
	bridge, image, content := buildFullTestImage(t)
	pvd := image[16*LogicalSectorBytes:]
	if binary.LittleEndian.Uint32(pvd[140:144]) == 0 || binary.BigEndian.Uint32(pvd[148:152]) == 0 {
		t.Fatal("the full tree must record both the L and the M path table")
	}
	rootLba := binary.LittleEndian.Uint32(pvd[158:162])
	rootSize := binary.LittleEndian.Uint32(pvd[166:170])
	root := parseFullRecords(t, image, rootLba, rootSize)

	// Root ".": SUSP SP first, the RRIP ER, and a directory PX.
	if _, ok := root[0].susp["SP"]; !ok {
		t.Fatal("root '.' lacks the SUSP SP entry")
	}
	if !bytes.Contains(root[0].susp["ER"], []byte("RRIP_1991A")) {
		t.Fatal("root '.' lacks the RRIP ER entry")
	}
	if got := pxMode(t, root[0]); got != 0o040755 {
		t.Fatalf("root mode = %o, want 40755", got)
	}
	byName := map[string]fullRecord{}
	var ids []string
	for _, r := range root[2:] {
		byName[r.nmName] = r
		ids = append(ids, r.id)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Fatalf("root records are not ordered by identifier: %v", ids)
		}
	}
	for _, name := range []string{".disk", "a", "boot", "boot.cat"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("root lacks Rock Ridge name %q (have %v)", name, ids)
		}
	}
	if catalog := byName["boot.cat"]; catalog.lba != bridge.CatalogLba() || catalog.size != LogicalSectorBytes {
		t.Fatalf("boot.cat record = %d/%d, want the catalog at %d", catalog.lba, catalog.size, bridge.CatalogLba())
	}
	dirA := byName["a"]
	if dirA.flags&2 == 0 || pxMode(t, dirA) != 0o040750 {
		t.Fatalf("a: flags %#x mode %o, want a directory with mode 40750", dirA.flags, pxMode(t, dirA))
	}

	// /a: big.bin is three consecutive records, multi-extent flag on all
	// but the last, each at its own (non-adjacent) extent.
	recordsA := parseFullRecords(t, image, dirA.lba, dirA.size)
	var big []fullRecord
	var link, empty fullRecord
	for _, r := range recordsA[2:] {
		switch r.nmName {
		case "big.bin":
			big = append(big, r)
		case "link":
			link = r
		case "empty":
			empty = r
		}
	}
	want := []fullRecord{{lba: 2000, size: 2048, flags: 0x80}, {lba: 1500, size: 2048, flags: 0x80}, {lba: 3000, size: 2148, flags: 0}}
	if len(big) != len(want) {
		t.Fatalf("big.bin has %d records, want %d", len(big), len(want))
	}
	for i := range want {
		if big[i].lba != want[i].lba || big[i].size != want[i].size || big[i].flags != want[i].flags || big[i].id != "BIG.BIN;1" {
			t.Fatalf("big.bin section %d = %+v, want lba %d size %d flags %#x", i, big[i], want[i].lba, want[i].size, want[i].flags)
		}
		if pxMode(t, big[i]) != 0o100600 {
			t.Fatalf("big.bin section %d mode %o, want 100600", i, pxMode(t, big[i]))
		}
	}
	if empty.lba != 0 || empty.size != 0 {
		t.Fatalf("empty: lba %d size %d, want 0/0", empty.lba, empty.size)
	}
	if pxMode(t, link) != 0o120777 || link.size != 0 || link.lba != 0 {
		t.Fatalf("link: mode %o lba %d size %d, want a 120777 symlink without extent", pxMode(t, link), link.lba, link.size)
	}
	if sl := link.susp["SL"]; !bytes.Contains(sl, []byte("big.bin")) {
		t.Fatalf("link SL entry %q lacks the target", sl)
	}

	// Joliet: same files, no symlink.
	svd := image[18*LogicalSectorBytes:]
	jolietRoot := parseFullRecords(t, image, binary.LittleEndian.Uint32(svd[158:162]), binary.LittleEndian.Uint32(svd[166:170]))
	var jolietA fullRecord
	for _, r := range jolietRoot {
		if r.id == string(bigEndianUnicode("a")) {
			jolietA = r
		}
	}
	if jolietA.lba == 0 {
		t.Fatal("Joliet root lacks a")
	}
	sections := 0
	for _, r := range parseFullRecords(t, image, jolietA.lba, jolietA.size) {
		switch r.id {
		case string(bigEndianUnicode("big.bin;1")):
			sections++
		case string(bigEndianUnicode("link;1")), string(bigEndianUnicode("link")):
			t.Fatal("Joliet must not list the symlink")
		}
	}
	if sections != 3 {
		t.Fatalf("Joliet big.bin has %d sections, want 3", sections)
	}

	// Independent readers: xorriso (libisofs) and 7-Zip follow each
	// section's own extent.
	path := filepath.Join(t.TempDir(), "full.iso")
	if err := os.WriteFile(path, image, 0o644); err != nil {
		t.Fatal(err)
	}
	if xorriso, err := exec.LookPath("xorriso"); err == nil {
		for name, data := range content {
			dest := filepath.Join(t.TempDir(), "out")
			if output, err := exec.Command(xorriso, "-osirrox", "on", "-indev", path, "-extract", "/"+name, dest).CombinedOutput(); err != nil {
				t.Fatalf("xorriso extract %s: %v\n%s", name, err, output)
			}
			if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("xorriso extract %s: content mismatch (%v)", name, err)
			}
		}
		output, err := exec.Command(xorriso, "-indev", path, "-lsdl", "/a/link").CombinedOutput()
		if err != nil || !strings.Contains(string(output), "'/a/link' -> 'big.bin'") {
			t.Fatalf("xorriso -lsdl /a/link does not show the symlink: %v\n%s", err, output)
		}
	}
	if sevenZip, err := exec.LookPath("7z"); err == nil {
		output, err := exec.Command(sevenZip, "e", "-so", path, "a/big.bin").Output()
		if err != nil || !bytes.Equal(output, content["a/big.bin"]) {
			t.Fatalf("7z extract a/big.bin: content mismatch (%v)", err)
		}
	}
}

func TestElToritoBridgeFullTreeValidation(t *testing.T) {
	ts := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	tree := func(extra ...BridgeTreeEntry) []BridgeTreeEntry {
		return append([]BridgeTreeEntry{
			{Path: "", IsDirectory: true, Mode: 0o755, ModTime: ts, FileIndex: -1},
			{Path: "boot.img", Size: 4096, Mode: 0o644, ModTime: ts, FileIndex: 0},
		}, extra...)
	}
	if _, err := NewElToritoBridgeFull(8192, "X", "missing.img", 4096, "", 0, tree()); err == nil {
		t.Fatal("a boot image outside the tree must be rejected")
	}
	if _, err := NewElToritoBridgeFull(8192, "X", "boot.img", 4096, "", 0,
		tree(BridgeTreeEntry{Path: "d/f", Size: 1, FileIndex: 1})); err == nil {
		t.Fatal("an entry before its parent directory must be rejected")
	}
	long := strings.Repeat("n", 200)
	if _, err := NewElToritoBridgeFull(8192, "X", "boot.img", 4096, "", 0,
		tree(BridgeTreeEntry{Path: long, Size: 1, FileIndex: 1})); err == nil {
		t.Fatal("a name that overflows the 255-byte record must be rejected")
	}
	// A root file named boot.cat moves the catalog to another name.
	b, err := NewElToritoBridgeFull(8192, "X", "boot.img", 4096, "", 0,
		tree(BridgeTreeEntry{Path: "boot.cat", Size: 1, Mode: 0o644, FileIndex: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if b.catalogNode.name == "boot.cat" {
		t.Fatal("the catalog must not reuse a tree file's name")
	}

	bridge, err := NewElToritoBridgeFull(8192, "X", "boot.img", 4096, "", 0, tree())
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.SetPlacements(400, 0, nil); err == nil {
		t.Fatal("SetPlacements must refuse a full-tree bridge")
	}
	need, err := bridge.FullTreeSectors(nil)
	if err != nil {
		t.Fatal(err)
	}
	fragmented := map[int][]UdfFileExtent{0: {{FileIndex: 0, Offset: 0, Lba: 400, Sectors: 1}, {FileIndex: 0, Offset: 2048, Lba: 500, Sectors: 1}}}
	if err := bridge.SetFullPlacements(300, need, 400, 0, fragmented); err == nil {
		t.Fatal("a fragmented boot image must be rejected")
	}
	contiguous := map[int][]UdfFileExtent{0: {{FileIndex: 0, Offset: 0, Lba: 400, Sectors: 2}}}
	if err := bridge.SetFullPlacements(300, need-1, 400, 0, contiguous); err == nil {
		t.Fatal("a reserve smaller than the tree must be rejected")
	}
	if err := bridge.SetFullPlacements(300, need, 300, 0, map[int][]UdfFileExtent{0: {{FileIndex: 0, Lba: 300, Sectors: 2}}}); err == nil {
		t.Fatal("file data inside the tree region must be rejected")
	}
	if err := bridge.SetFullPlacements(300, need, 400, 0, contiguous); err != nil {
		t.Fatal(err)
	}
}

func TestIsoSectionsSplitLongFragments(t *testing.T) {
	node := &isoNode{name: "huge", dataLength: IsoMaxSectionBytes + 3*LogicalSectorBytes + 7}
	sectors := uint32(sectorCount(node.dataLength))
	sections, err := isoSectionsFromExtents(node, []UdfFileExtent{{Offset: 0, Lba: 1000, Sectors: sectors}}, 1000, 1<<31)
	if err != nil {
		t.Fatal(err)
	}
	want := []isoSection{{lba: 1000, length: IsoMaxSectionBytes}, {lba: 1000 + IsoMaxSectionBytes/LogicalSectorBytes, length: 3*LogicalSectorBytes + 7}}
	if len(sections) != 2 || sections[0] != want[0] || sections[1] != want[1] {
		t.Fatalf("sections = %+v, want %+v", sections, want)
	}
}

// TestFullTreeVolumeLabel: a full tree keeps printable-ASCII labels of up to
// 32 bytes verbatim (label lookups such as root=live:CDLABEL= compare them
// exactly) and falls back to d-character normalization otherwise.
func TestFullTreeVolumeLabel(t *testing.T) {
	for label, want := range map[string]string{
		"Fedora-WS-Live-45":     "Fedora-WS-Live-45",
		"Debian 13.7.0 amd64 n": "Debian 13.7.0 amd64 n",
		"ARCH_202611":           "ARCH_202611",
		"Été 2026":              normalizePrimaryIdentifier("Été 2026", 32, "DVD_IMAGE"),
		strings.Repeat("x", 33): normalizePrimaryIdentifier(strings.Repeat("x", 33), 32, "DVD_IMAGE"),
		"":                      "DVD_IMAGE",
	} {
		if got := fullTreeVolumeLabel(label); got != want {
			t.Errorf("fullTreeVolumeLabel(%q) = %q, want %q", label, got, want)
		}
	}
}
