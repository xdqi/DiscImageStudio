package dvd

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"disc-image-studio/internal/iso"
	"disc-image-studio/internal/raster"
)

// TestDispersedUdfEndToEnd generates a small dispersed UDF image and checks
// the structure and the file content round-trip.
func TestDispersedUdfEndToEnd(t *testing.T) {
	dir := t.TempDir()
	// A gradient image with mid-tones.
	pngPath := writeGradientPng(t, filepath.Join(dir, "gradient.png"), 64)
	dataDir := filepath.Join(dir, "data")
	sub := filepath.Join(dataDir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	hello := []byte("hello dispersed disc\n" + strings.Repeat("x", 300))
	if err := os.WriteFile(filepath.Join(dataDir, "hello.txt"), hello, 0o644); err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 3000)
	for i := range blob {
		blob[i] = byte(i * 7)
	}
	if err := os.WriteFile(filepath.Join(sub, "blob.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	isoPath := filepath.Join(dir, "disc.iso")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "4096", "--data-dir", dataDir, "--filesystem", "udf",
	})
	if code != 0 {
		t.Fatalf("solve exited %d", code)
	}
	info, err := os.Stat(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 4096*2048 {
		t.Fatalf("ISO size = %d, want %d", info.Size(), 4096*2048)
	}
	data, err := os.ReadFile(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	// UDF VRS at sectors 16..18.
	if string(data[16*2048+1:16*2048+6]) != "BEA01" {
		t.Fatal("no BEA01 at sector 16")
	}
	if string(data[17*2048+1:17*2048+6]) != "NSR03" {
		t.Fatal("no NSR03 at sector 17")
	}
	if string(data[18*2048+1:18*2048+6]) != "TEA01" {
		t.Fatal("no TEA01 at sector 18")
	}
	// The sidecar exists and lists the files.
	sidecar, err := os.ReadFile(isoPath + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), "dvd-dispersed-udf-v1") {
		t.Fatal("sidecar missing format")
	}
	if !strings.Contains(string(sidecar), "hello.txt") || !strings.Contains(string(sidecar), "blob.bin") {
		t.Fatal("sidecar missing file placements")
	}

	// Re-read the file bytes through the UDF structures: find the FSD at the
	// partition start, walk the root directory to the file entries, and read
	// through their extent lists.
	content := readUdfFileByName(t, data, "hello.txt")
	if !bytes.Equal(content, hello) {
		t.Fatalf("hello.txt round-trip mismatch (%d vs %d bytes)", len(content), len(hello))
	}
	content = readUdfFileByName(t, data, "blob.bin")
	if !bytes.Equal(content, blob) {
		t.Fatal("blob.bin round-trip mismatch")
	}
}

// udfTestVolume is a minimal UDF reader for tests: walks the anchor → main
// VDS → partition → FSD → root file entry, then reads entries/extents.
type udfTestVolume struct {
	t              *testing.T
	data           []byte
	partitionStart uint64
	rootFe         []byte
}

func newUdfTestVolume(t *testing.T, data []byte) *udfTestVolume {
	t.Helper()
	const sector = 2048
	// Anchor at 256: main VDS extent at offset 16 (length, lba).
	anchor := data[256*sector:]
	mainVdsLba := binary.LittleEndian.Uint32(anchor[20:24])
	// The partition descriptor is the 3rd descriptor in the main VDS; the
	// partition starts at offset 188. The LVD (4th descriptor) holds the FSD
	// long_ad at offset 248: length(4) at 248..252, lbn at 252..256.
	lvd := data[(mainVdsLba+3)*sector:]
	pd := data[(mainVdsLba+2)*sector:]
	partitionStart := uint64(binary.LittleEndian.Uint32(pd[188:192]))
	fsdLbn := binary.LittleEndian.Uint32(lvd[252:256])
	fsd := data[(partitionStart+uint64(fsdLbn))*sector:]
	// Root directory ICB: long_ad at offset 400: length at 400..404, lbn 404..408.
	rootLbn := binary.LittleEndian.Uint32(fsd[404:408])
	return &udfTestVolume{
		t:              t,
		data:           data,
		partitionStart: partitionStart,
		rootFe:         data[(partitionStart+uint64(rootLbn))*sector:],
	}
}

// extents parses a file entry's short_ad list into (lbn, length) pairs.
func (v *udfTestVolume) extents(fe []byte) (uint64, []uint64) {
	const sector = 2048
	adLen := binary.LittleEndian.Uint32(fe[172:176])
	infoLen := binary.LittleEndian.Uint64(fe[56:64])
	var extents []uint64
	for off := 176; off < 176+int(adLen); off += 8 {
		length := binary.LittleEndian.Uint32(fe[off : off+4])
		lbn := binary.LittleEndian.Uint32(fe[off+4 : off+8])
		if length == 0 {
			continue
		}
		extents = append(extents, uint64(lbn), uint64(length))
	}
	return infoLen, extents
}

// readFile reads a file entry's content through its extents.
func (v *udfTestVolume) readFile(fe []byte) []byte {
	const sector = 2048
	infoLen, extents := v.extents(fe)
	var out []byte
	for i := 0; i+1 < len(extents); i += 2 {
		lbn, length := extents[i], extents[i+1]
		start := (v.partitionStart + lbn) * sector
		out = append(out, v.data[start:start+length]...)
	}
	return out[:infoLen]
}

// readDir parses a directory's file identifier records into name → child file
// entry sector (the parent record excluded).
func (v *udfTestVolume) readDir(fe []byte) map[string][]byte {
	const sector = 2048
	dirData := v.readFile(fe)
	children := map[string][]byte{}
	for off := 0; off+38 <= len(dirData); {
		nameLen := int(dirData[off+19])
		implLen := int(binary.LittleEndian.Uint16(dirData[off+36 : off+38]))
		recLen := (38 + nameLen + 3) / 4 * 4
		if recLen == 0 || off+recLen > len(dirData) {
			break
		}
		characteristics := dirData[off+18]
		if nameLen > 1 && characteristics&8 == 0 { // not the parent record
			name := string(dirData[off+38+implLen+1 : off+38+implLen+nameLen])
			icbLbn := binary.LittleEndian.Uint32(dirData[off+24 : off+28])
			children[name] = v.data[(v.partitionStart+uint64(icbLbn))*sector:]
		}
		off += recLen
	}
	return children
}

// findFileEntry locates a file's entry sector by name, recursing into
// subdirectories (directories have characteristic bit 2 set).
func (v *udfTestVolume) findFileEntry(name string) []byte {
	var walk func(fe []byte) []byte
	walk = func(fe []byte) []byte {
		for childName, child := range v.readDir(fe) {
			if strings.EqualFold(childName, name) {
				return child
			}
		}
		for _, child := range v.readDir(fe) {
			// Recurse into directories (their file entry has fileType 4 at 27).
			if child[27] == 4 {
				if found := walk(child); found != nil {
					return found
				}
			}
		}
		return nil
	}
	return walk(v.rootFe)
}

// readUdfFileByName reads a file's content through the UDF structures.
func readUdfFileByName(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	v := newUdfTestVolume(t, data)
	if fe := v.findFileEntry(name); fe != nil {
		return v.readFile(fe)
	}
	t.Fatalf("file %q not found in UDF tree", name)
	return nil
}

// TestDispersedUdfBridgeEndToEnd: --boot-image names a file inside the data
// directory (image-root-relative path); it is placed contiguously and shared
// between the El Torito catalog and its UDF file entry — with no copy at the
// volume root.
func TestDispersedUdfBridgeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeGradientPng(t, filepath.Join(dir, "gradient.png"), 64)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	loader := make([]byte, 4096)
	for i := range loader {
		loader[i] = byte(i*13 + 5)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "boot", "loader.bin"), loader, 0o644); err != nil {
		t.Fatal(err)
	}
	hello := []byte("hello bridge\n")
	if err := os.WriteFile(filepath.Join(dataDir, "hello.txt"), hello, 0o644); err != nil {
		t.Fatal(err)
	}
	isoPath := filepath.Join(dir, "disc.iso")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "4096", "--data-dir", dataDir,
		"--filesystem", "udf-bridge", "--boot-image", "boot/loader.bin",
	})
	if code != 0 {
		t.Fatalf("solve exited %d", code)
	}
	data, err := os.ReadFile(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	const sector = 2048

	// The boot record points to the dynamically placed boot catalog.
	catalogLba := binary.LittleEndian.Uint32(data[17*sector+71:])
	catalog := data[int(catalogLba)*sector:]
	if catalog[32] != 0x88 {
		t.Fatalf("catalog default entry boot indicator = %#x, want 0x88", catalog[32])
	}
	if got := binary.LittleEndian.Uint16(catalog[38:40]); got != uint16(len(loader)/512) {
		t.Fatalf("catalog sector count = %d, want %d", got, len(loader)/512)
	}
	bootLba := binary.LittleEndian.Uint32(catalog[40:44])

	// The UDF file entry of boot/loader.bin: exactly one extent, at the
	// catalog's LBA, with the file's content.
	v := newUdfTestVolume(t, data)
	fe := v.findFileEntry("loader.bin")
	if fe == nil {
		t.Fatal("loader.bin not found in the UDF tree")
	}
	if got := binary.LittleEndian.Uint32(fe[172:176]); got != 8 {
		t.Fatalf("boot image has %d allocation-descriptor bytes, want 8 (one extent)", got)
	}
	extentLength := binary.LittleEndian.Uint32(fe[176:180])
	extentLbn := binary.LittleEndian.Uint32(fe[180:184])
	if extentLength != uint32(len(loader)) {
		t.Fatalf("boot image extent length = %d, want %d", extentLength, len(loader))
	}
	if v.partitionStart+uint64(extentLbn) != uint64(bootLba) {
		t.Fatalf("UDF extent LBA %d != El Torito catalog LBA %d", v.partitionStart+uint64(extentLbn), bootLba)
	}
	if got := v.readFile(fe); !bytes.Equal(got, loader) {
		t.Fatal("loader.bin content round-trip mismatch")
	}

	// No copy at the volume root: the root directory lists boot/ + hello.txt
	// only.
	rootNames := map[string]bool{}
	for name := range v.readDir(v.rootFe) {
		rootNames[name] = true
	}
	if rootNames["loader.bin"] {
		t.Fatal("loader.bin must not be copied to the volume root")
	}
	if !rootNames["boot"] || !rootNames["hello.txt"] {
		t.Fatalf("root names = %v, want boot + hello.txt", rootNames)
	}

	rootLba := binary.LittleEndian.Uint32(data[16*sector+158:])
	rootSize := binary.LittleEndian.Uint32(data[16*sector+166:])
	var bootDir iso9660TestRecord
	for _, r := range parseIso9660DirRecords(t, data, rootLba, rootSize) {
		if r.name == "LOADER.BIN;1" {
			t.Fatal("boot image duplicated at ISO root")
		}
		if r.name == "BOOT" {
			bootDir = r
		}
	}
	if bootDir.lba == 0 {
		t.Fatal("ISO9660 boot directory missing")
	}
	found := false
	for _, r := range parseIso9660DirRecords(t, data, bootDir.lba, bootDir.size) {
		if r.name == "LOADER.BIN;1" {
			found = true
			if r.lba != bootLba {
				t.Fatalf("boot LBA %d != %d", r.lba, bootLba)
			}
		}
	}
	if !found {
		t.Fatal("ISO9660 /BOOT/LOADER.BIN missing")
	}
	if sevenZip, err := exec.LookPath("7z"); err == nil {
		cmd := exec.Command(sevenZip, "x", "-so", "-tudf", isoPath, "boot/loader.bin")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil || !bytes.Equal(output, loader) {
			t.Fatalf("7z UDF extraction: %v, stderr %s", err, &stderr)
		}
	}
}

// parseIso9660DirRecords parses one ISO9660 directory extent's records.
type iso9660TestRecord struct {
	lba   uint32
	size  uint32
	flags byte
	name  string
}

func parseIso9660DirRecords(t *testing.T, data []byte, lba, size uint32) []iso9660TestRecord {
	t.Helper()
	extent := data[int(lba)*2048 : int(lba)*2048+int(size)]
	var records []iso9660TestRecord
	for offset := 0; offset+2 <= len(extent) && extent[offset] != 0; {
		length := int(extent[offset])
		if length < 34 || offset+length > len(extent) {
			t.Fatalf("bad record length %d at offset %d", length, offset)
		}
		record := extent[offset : offset+length]
		idLen := int(record[32])
		records = append(records, iso9660TestRecord{
			lba:   binary.LittleEndian.Uint32(record[2:6]),
			size:  binary.LittleEndian.Uint32(record[10:14]),
			flags: record[25],
			name:  string(record[33 : 33+idLen]),
		})
		offset += length
	}
	return records
}

// TestDispersedUdfBridgeExposeEndToEnd: --expose lists files on the ISO9660
// side of the bridge at their image paths (parent directories synthesized),
// each stored contiguously; exposing a directory expands it recursively.
// The image contains /boot/loader.bin, /a/foo, /a/bar, /a/b/barz, /a/b/link.
func TestDispersedUdfBridgeExposeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeGradientPng(t, filepath.Join(dir, "gradient.png"), 64)
	dataDir := filepath.Join(dir, "data")
	longName := strings.Repeat("long-name-", 9) + ".txt"
	for name, content := range map[string][]byte{
		"boot/loader.bin":   bytes.Repeat([]byte{0x5A}, 4096),
		".disk/Info File":   []byte("joliet data"),
		".disk/" + longName: []byte("long name"),
		"a/foo":             []byte("not exposed\n"),
		"a/bar":             []byte("exposed bar\n"),
		"a/b/barz":          bytes.Repeat([]byte{0x42}, 3000),
	} {
		path := filepath.Join(dataDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("barz", filepath.Join(dataDir, "a", "b", "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	isoPath := filepath.Join(dir, "disc.iso")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "4096", "--data-dir", dataDir,
		"--filesystem", "udf-bridge", "--boot-image", "boot/loader.bin",
		"--expose", "a/bar", "--expose=a/b", "--expose=.disk", "--expose=boot",
	})
	if code != 0 {
		t.Fatalf("solve exited %d", code)
	}
	data, err := os.ReadFile(isoPath)
	if err != nil {
		t.Fatal(err)
	}

	// UDF side: everything is present and round-trips.
	v := newUdfTestVolume(t, data)
	for name, want := range map[string][]byte{
		"loader.bin": bytes.Repeat([]byte{0x5A}, 4096),
		"foo":        []byte("not exposed\n"),
		"bar":        []byte("exposed bar\n"),
		"barz":       bytes.Repeat([]byte{0x42}, 3000),
	} {
		if got := readUdfFileByName(t, data, name); !bytes.Equal(got, want) {
			t.Fatalf("UDF %s round-trip mismatch", name)
		}
	}

	// The ISO9660 side: root has BOOT.CAT;1, LOADER.BIN;1 and directory A.
	pvd := data[16*2048:]
	rootLba := binary.LittleEndian.Uint32(pvd[158:162])
	rootSize := binary.LittleEndian.Uint32(pvd[166:170])
	root := parseIso9660DirRecords(t, data, rootLba, rootSize)
	byName := map[string]iso9660TestRecord{}
	for _, r := range root {
		byName[r.name] = r
	}
	if _, ok := byName["BOOT.CAT;1"]; !ok {
		t.Fatalf("root lacks BOOT.CAT;1: %v", root)
	}
	if _, ok := byName["BOOT"]; !ok {
		t.Fatalf("root lacks BOOT directory: %v", root)
	}
	if xorriso, err := exec.LookPath("xorriso"); err == nil {
		for path, want := range map[string][]byte{
			"/.disk/Info File":   []byte("joliet data"),
			"/.disk/" + longName: []byte("long name"),
			"/boot/loader.bin":   bytes.Repeat([]byte{0x5A}, 4096),
		} {
			dest := filepath.Join(t.TempDir(), "extracted")
			output, err := exec.Command(xorriso, "-osirrox", "on", "-indev", isoPath, "-extract", path, dest).CombinedOutput()
			if err != nil {
				t.Fatalf("xorriso extract %s: %v\n%s", path, err, output)
			}
			got, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("extract %s: content mismatch: %v", path, err)
			}
		}
		output, err := exec.Command(xorriso, "-indev", isoPath, "-report_el_torito", "plain").CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("El Torito boot img")) {
			t.Fatalf("El Torito report: %v\n%s", err, output)
		}
	}
	dirA, ok := byName["A"]
	if !ok || dirA.flags&2 == 0 {
		t.Fatalf("root lacks directory A: %v", root)
	}

	// /A holds BAR;1 (file, contiguous extent) and directory B; FOO is absent.
	recsA := parseIso9660DirRecords(t, data, dirA.lba, dirA.size)
	byNameA := map[string]iso9660TestRecord{}
	for _, r := range recsA {
		byNameA[r.name] = r
	}
	bar, ok := byNameA["BAR;1"]
	if !ok || bar.flags&2 != 0 {
		t.Fatalf("/A lacks BAR;1: %v", recsA)
	}
	if _, ok := byNameA["FOO;1"]; ok {
		t.Fatal("/A must not contain FOO;1 (foo was not exposed)")
	}
	dirB, ok := byNameA["B"]
	if !ok || dirB.flags&2 == 0 {
		t.Fatalf("/A lacks directory B: %v", recsA)
	}

	// /A/B holds BARZ;1 (the directory --expose expanded) and no symlink.
	recsB := parseIso9660DirRecords(t, data, dirB.lba, dirB.size)
	byNameB := map[string]iso9660TestRecord{}
	for _, r := range recsB {
		byNameB[r.name] = r
	}
	barz, ok := byNameB["BARZ;1"]
	if !ok {
		t.Fatalf("/A/B lacks BARZ;1: %v", recsB)
	}
	if _, ok := byNameB["LINK;1"]; ok {
		t.Fatal("the symlink in an exposed directory must be skipped")
	}

	// The ISO9660 record LBAs equal the files' UDF extents, each a single
	// contiguous extent.
	for name, record := range map[string]iso9660TestRecord{"bar": bar, "barz": barz} {
		fe := v.findFileEntry(name)
		if fe == nil {
			t.Fatalf("%s not in the UDF tree", name)
		}
		if adLen := binary.LittleEndian.Uint32(fe[172:176]); adLen != 8 {
			t.Fatalf("exposed %s has %d allocation-descriptor bytes, want 8 (one extent)", name, adLen)
		}
		extentLbn := binary.LittleEndian.Uint32(fe[180:184])
		if v.partitionStart+uint64(extentLbn) != uint64(record.lba) {
			t.Fatalf("%s: ISO9660 LBA %d != UDF extent LBA %d", name, record.lba, v.partitionStart+uint64(extentLbn))
		}
		if int64(record.size) != int64(len(map[string][]byte{"bar": []byte("exposed bar\n"), "barz": bytes.Repeat([]byte{0x42}, 3000)}[name])) {
			t.Fatalf("%s: record size %d", name, record.size)
		}
		// Content at the shared extent matches.
		start := int(record.lba) * 2048
		if got := data[start : start+int(record.size)]; !bytes.Equal(got, map[string][]byte{"bar": []byte("exposed bar\n"), "barz": bytes.Repeat([]byte{0x42}, 3000)}[name]) {
			t.Fatalf("%s: content at the shared extent mismatches", name)
		}
	}
}

// TestDispersedUdfBridgeBootImageErrors: --boot-image must resolve inside the
// data directory.
func TestDispersedUdfBridgeBootImageErrors(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeGradientPng(t, filepath.Join(dir, "gradient.png"), 64)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bootImage := range []string{"missing.bin", "sub/../hello.txt/.."} {
		code := RunCommand([]string{
			"solve", "--image", pngPath, "--iso-output", filepath.Join(dir, "out.iso"),
			"--total-sectors", "4096", "--data-dir", dataDir,
			"--filesystem", "udf-bridge", "--boot-image", bootImage,
		})
		if code == 0 {
			t.Fatalf("solve --boot-image %q succeeded, want failure", bootImage)
		}
	}
}

func writeGradientPng(t *testing.T, path string, size int) string {
	t.Helper()
	img := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			v := byte(y * 255 / (size - 1))
			o := (y*size + x) * 4
			img[o] = v
			img[o+1] = v
			img[o+2] = v
			img[o+3] = 255
		}
	}
	if err := raster.SavePng(path, size, size, img, false); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPlaceDispersionWorstFitDecreasing verifies the file-data allocator:
// largest file remainder into the largest free run, with the run/file
// leftovers re-entering their heaps (deterministic ties).
func TestPlaceDispersionWorstFitDecreasing(t *testing.T) {
	const total = 4096
	eligible := make([]bool, total)
	mark := func(start, end uint32) {
		for i := start; i < end; i++ {
			eligible[i] = true
		}
	}
	mark(100, 220) // run A: 120
	mark(300, 420) // run B: 120
	mark(500, 520) // run C: 20
	in := dispersePlannerInputs{
		totalSectors: total,
		fileSizes:    []int64{70 * 2048, 70 * 2048, 100 * 2048},
		filePaths:    []string{"a", "b", "c"},
	}
	plan, err := placeDispersion(in, eligible, make([]bool, total))
	if err != nil {
		t.Fatal(err)
	}
	type ext struct {
		lba     uint32
		offset  int64
		sectors uint32
	}
	got := map[int][]ext{}
	for index, list := range plan.fileExtents {
		for _, e := range list {
			got[index] = append(got[index], ext{e.lba, e.offset, e.sectors})
		}
	}
	// File 2 (the largest) gets first pick: run A, one extent.
	want := map[int][]ext{
		0: {{300, 0, 70}},                       // run B, whole
		1: {{370, 0, 50}, {200, 50 * 2048, 20}}, // B's leftover, then A's leftover
		2: {{100, 0, 100}},                      // run A, whole
	}
	for index, wantList := range want {
		gotList := got[index]
		if len(gotList) != len(wantList) {
			t.Fatalf("file %d: %d extents %v, want %v", index, len(gotList), gotList, wantList)
		}
		for i, w := range wantList {
			if gotList[i] != w {
				t.Fatalf("file %d extent %d = %v, want %v (all: %v)", index, i, gotList[i], w, gotList)
			}
		}
	}
	// Fragment numbers run 0,1 in content order for the fragmented file.
	var fragments []int
	for _, p := range plan.placed {
		if p.Role == "file" && p.Path == "1" {
			fragments = append(fragments, p.Fragment)
		}
	}
	if len(fragments) != 2 || fragments[0] != 0 || fragments[1] != 1 {
		t.Fatalf("file 1 fragment numbers = %v, want [0 1]", fragments)
	}
}

// TestPlaceDispersionEmptyFile: a zero-length file gets no extents.
func TestPlaceDispersionEmptyFile(t *testing.T) {
	const total = 4096
	eligible := make([]bool, total)
	for i := 100; i < 200; i++ {
		eligible[i] = true
	}
	in := dispersePlannerInputs{
		totalSectors: total,
		fileSizes:    []int64{0, 2048},
		filePaths:    []string{"empty", "full"},
	}
	plan, err := placeDispersion(in, eligible, make([]bool, total))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.fileExtents[0]) != 0 {
		t.Fatalf("empty file got %d extents", len(plan.fileExtents[0]))
	}
	if len(plan.fileExtents[1]) != 1 || plan.fileExtents[1][0].lba != 100 {
		t.Fatalf("full file extents = %v", plan.fileExtents[1])
	}
}

// TestPlaceDispersionTooManyExtents: a file that must fragment into more
// pieces than one UDF file entry supports yields a retryable
// tooManyExtentsError naming the file.
func TestPlaceDispersionTooManyExtents(t *testing.T) {
	const total = 4096
	eligible := make([]bool, total)
	for i := 100; i < 200; i++ { // one 100-sector run
		eligible[i] = true
	}
	for k := 0; k < 300; k++ { // 300 single-sector runs
		eligible[1000+2*k] = true
	}
	in := dispersePlannerInputs{
		totalSectors: total,
		fileSizes:    []int64{400 * 2048},
		filePaths:    []string{"big.bin"},
	}
	_, err := placeDispersion(in, eligible, make([]bool, total))
	if err == nil {
		t.Fatal("placeDispersion succeeded, want tooManyExtentsError")
	}
	tooMany, ok := err.(*tooManyExtentsError)
	if !ok {
		t.Fatalf("error type = %T (%v), want *tooManyExtentsError", err, err)
	}
	if tooMany.path != "big.bin" || tooMany.extents != 301 {
		t.Fatalf("tooManyExtentsError = %q/%d, want big.bin/301", tooMany.path, tooMany.extents)
	}
}

// TestToneMapMatchesWriterGeometry: a landmark feature must be protected at
// the position the WRITER renders it, not at a mirrored/rotated position.
// (Regression: the tone map once sampled sampleDisc(x, -y), mirroring the
// picture around the horizontal axis — protection landed on the mirror image
// and data fell on the asymmetric features, e.g. the Debian logo's text.)
func TestToneMapMatchesWriterGeometry(t *testing.T) {
	// 64x64 gray image with a white square at x[45,48), y[26,29) (right of
	// center, ABOVE the horizontal axis; its mirror is below).
	const size = 64
	img := rasterImageCreateForTest(size, size, 130, 130, 130, 255)
	for y := 26; y < 29; y++ {
		for x := 45; x < 48; x++ {
			o := (y*size + x) * 4
			img.bgra[o+0], img.bgra[o+1], img.bgra[o+2] = 255, 255, 255
		}
	}
	img.hasBinaryGrayscalePalette = false // force the plain sampling path
	opts := imageMappingOptions{
		TotalSectors: 393216, InnerRadiusMm: 24, OuterRadiusMm: 58, ChannelBitLengthNm: 133.3,
	}
	tm, err := buildDisperseToneMap(img, opts)
	if err != nil {
		t.Fatal(err)
	}
	eligible := tm.classifySectors(120, 136, 0, 0)

	scale := float64(size-1) / (2 * opts.samplingOuterRadiusMm())
	inSquare := func(px, py float64) bool { return px >= 44 && px < 49 && py >= 25 && py < 30 }
	inMirror := func(px, py float64) bool { return px >= 44 && px < 49 && py >= 34 && py < 39 }
	var protSquare, protMirror int
	for s := 0; s < int(opts.TotalSectors); s++ {
		if eligible[s] {
			continue
		}
		// The writer's pixel position of this sector's arc midpoint
		// (CW direction, start angle 0).
		arcMm := (float64(s) + 0.5) * tm.sectorArcMm
		radius, trackAngle := tm.spiral.atArcLengthFast(arcMm)
		x := radius * math.Cos(-trackAngle)
		y := radius * math.Sin(-trackAngle)
		wpx := float64(size-1)/2 + x*scale
		wpy := float64(size-1)/2 - y*scale
		switch {
		case inSquare(wpx, wpy):
			protSquare++
		case inMirror(wpx, wpy):
			protMirror++
		}
	}
	if protMirror > 0 {
		t.Fatalf("tone map is mirrored: %d protected sectors map to the landmark's mirror position", protMirror)
	}
	if protSquare == 0 {
		t.Fatal("no protected sector over the landmark")
	}
}

// fakeDisperseToneMap builds a tone map from per-sector sample rows so the
// planner can be tested without a real picture. sectorPattern returns the
// fakeSamplesPerSector luminance samples of a sector; use solidLum for
// gray-in-band sectors and mixLum for everything else (a mixed row is never
// a "pure" tone, so the erosion-grid interior path is never consulted and
// the spiral can stay nil).
const fakeSamplesPerSector = 8

func fakeDisperseToneMap(t *testing.T, total int, sectorPattern func(sector int) []byte) *disperseToneMap {
	t.Helper()
	samples := make([]byte, total*fakeSamplesPerSector)
	for s := 0; s < total; s++ {
		row := sectorPattern(s)
		if len(row) != fakeSamplesPerSector {
			t.Fatalf("sector %d pattern has %d samples", s, len(row))
		}
		slices.Sort(row)
		copy(samples[s*fakeSamplesPerSector:(s+1)*fakeSamplesPerSector], row)
	}
	return &disperseToneMap{
		sectorSamples:      samples,
		sectorSampleStride: fakeSamplesPerSector,
		lumThreshold:       128,
		gridTurns:          1,
		gridArcs:           erosionArcBins,
		turnRadii:          []float64{25},
		gridLum:            make([]byte, erosionArcBins),
		tainted:            make([]bool, total),
	}
}

func solidLum(lum byte) []byte {
	row := make([]byte, fakeSamplesPerSector)
	for k := range row {
		row[k] = lum
	}
	return row
}

// mixLum returns a row of 7×a + 1×b samples.
func mixLum(a, b byte) []byte {
	row := solidLum(a)
	row[fakeSamplesPerSector-1] = b
	return row
}

// TestPlanDispersionWidensBandForExtents: at the initial gray band a file
// fits only by fragmenting beyond the UDF file-entry limit; the planner must
// widen the band (merging in a previously-marginal run) and re-plan until
// the file fits in one extent.
func TestPlanDispersionWidensBandForExtents(t *testing.T) {
	const total = 4096
	pattern := func(sector int) []byte {
		switch {
		case sector >= 500 && sector < 600: // run A: 100 sectors, gray
			return solidLum(128)
		case sector >= 1000 && sector < 2000 && sector%2 == 0: // 500 single-sector runs
			return solidLum(128)
		case sector >= 2500 && sector < 3100: // 600 sectors, just below the initial band
			return mixLum(118, 128) // gray once the band reaches 118
		default:
			return mixLum(250, 128) // near-white, never gray, never pure
		}
	}
	tm := fakeDisperseToneMap(t, total, pattern)
	in := dispersePlannerInputs{
		totalSectors:   total,
		fileSizes:      []int64{400 * 2048},
		filePaths:      []string{"big.bin"},
		headroom:       1.1,
		grayBand:       [2]int{-1, -1},
		marginWhite:    0.35,
		marginBlack:    0.5,
		toneMap:        tm,
		reservedBefore: 300,
		reservedAfter:  total - 257,
	}
	plan, err := planDispersion(in)
	if err != nil {
		t.Fatal(err)
	}
	// The band must have widened past 118 so the 600-sector run turned gray.
	if plan.band[0] > 118 {
		t.Fatalf("band = [%d,%d], want lower bound <= 118", plan.band[0], plan.band[1])
	}
	// The file now fits in a single extent inside the 600-sector run.
	list := plan.fileExtents[0]
	if len(list) != 1 {
		t.Fatalf("file has %d extents, want 1", len(list))
	}
	if list[0].lba != 2500 || list[0].sectors != 400 || list[0].offset != 0 {
		t.Fatalf("extent = %+v, want lba=2500 sectors=400 offset=0", list[0])
	}
}

// TestPlanDispersionTooManyExtentsFatal: when even the widest band leaves a
// file over the descriptor limit, the error names the file and the count.
func TestPlanDispersionTooManyExtentsFatal(t *testing.T) {
	const total = 4096
	pattern := func(sector int) []byte {
		if sector >= 500 && sector < 1200 && sector%2 == 0 {
			return solidLum(128) // 350 single-sector runs, always gray
		}
		return mixLum(250, 128)
	}
	tm := fakeDisperseToneMap(t, total, pattern)
	in := dispersePlannerInputs{
		totalSectors:   total,
		fileSizes:      []int64{300 * 2048},
		filePaths:      []string{"big.bin"},
		headroom:       1.1,
		grayBand:       [2]int{-1, -1},
		marginWhite:    0.35,
		marginBlack:    0.5,
		toneMap:        tm,
		reservedBefore: 300,
		reservedAfter:  total - 257,
	}
	_, err := planDispersion(in)
	if err == nil {
		t.Fatal("planDispersion succeeded, want a tooManyExtentsError-derived failure")
	}
	if !strings.Contains(err.Error(), "big.bin") || !strings.Contains(err.Error(), "300") {
		t.Fatalf("error %q should name the file and its descriptor count", err)
	}
}

// TestPlanDispersionRelaxesHeadroom: when even the widest band and smallest
// margins lack the capacity for the full headroom, the planner relaxes the
// headroom target toward 1.0 and restarts from the initial narrow band
// (fidelity beats slack).
func TestPlanDispersionRelaxesHeadroom(t *testing.T) {
	const total = 4096
	pattern := func(sector int) []byte {
		if sector >= 500 && sector < 1500 { // one 1000-sector gray run
			return solidLum(128)
		}
		return mixLum(250, 128) // never gray, never pure
	}
	tm := fakeDisperseToneMap(t, total, pattern)
	in := dispersePlannerInputs{
		totalSectors:   total,
		fileSizes:      []int64{950 * 2048}, // fits at headroom 1.04, not 1.10
		filePaths:      []string{"big.bin"},
		headroom:       1.1,
		grayBand:       [2]int{-1, -1},
		marginWhite:    0.35,
		marginBlack:    0.5,
		toneMap:        tm,
		reservedBefore: 300,
		reservedAfter:  total - 257,
	}
	plan, err := planDispersion(in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.headroom >= 1.1 || plan.headroom <= 1.0 {
		t.Fatalf("headroom = %v, want a relaxed value in (1.0, 1.1)", plan.headroom)
	}
	// The search restarts from the initial narrow band after relaxing.
	if plan.band != [2]byte{120, 136} {
		t.Fatalf("band = [%d,%d], want the initial [120,136]", plan.band[0], plan.band[1])
	}
	list := plan.fileExtents[0]
	if len(list) != 1 || list[0].sectors != 950 {
		t.Fatalf("file extents = %+v, want one 950-sector extent", list)
	}
}

// TestGrayBandRendering: with the dispersed writer's gray-render band set,
// in-band pixels render as zero payload (the payload scrambler turns that
// into the same mid-gray noise as data); out-of-band pixels keep the
// black/white palette.
func TestGrayBandRendering(t *testing.T) {
	mapping := imageMappingOptions{
		TotalSectors:       4096,
		InnerRadiusMm:      24,
		OuterRadiusMm:      58,
		ChannelBitLengthNm: 133.3,
	}
	cases := []struct {
		name string
		lum  byte
		band *[2]byte
		gray bool // want zero payload; else the scrambled palette byte ^ mask
	}{
		{"no band renders binary", 130, nil, false},
		{"in-band renders gray", 130, &[2]byte{120, 136}, true},
		{"below band stays black", 100, &[2]byte{120, 136}, false},
		{"above band stays white", 200, &[2]byte{120, 136}, false},
	}
	for _, reuse := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				img := rasterImageCreateForTest(64, 64, tc.lum, tc.lum, tc.lum, 255)
				sampler, err := newPayloadImageSamplerFromImage(img, mapping)
				if err != nil {
					t.Fatal(err)
				}
				if tc.band != nil {
					sampler.setGrayRenderBand(*tc.band)
				}
				options := multiBlockSolveOptions{
					FillSectors:        dvdEccSectorCount,
					ZeroInitialPayload: true,
					UseSampleReuse:     reuse,
					ImageMapping:       mapping,
				}
				payloads := make([]byte, dvdPayloadBytesPerBlock)
				block, err := generateFastBlock(0, sampler, options, payloads)
				if err != nil {
					t.Fatal(err)
				}
				for sector := 0; sector < dvdEccSectorCount; sector++ {
					mask := payloadScramblerMask(uint32(sector)) // StartLba=0, PsnOffset=0
					for i := 0; i < dvdPayloadBytesPerSector; i++ {
						got := block.payloads[sector*dvdPayloadBytesPerSector+i]
						want := byte(0)
						if !tc.gray {
							palette := byte(dvdWhiteScrambledByte)
							if tc.lum < 128 {
								palette = dvdBlackScrambledByte
							}
							want = palette ^ mask[i]
						}
						if got != want {
							t.Fatalf("sector %d byte %d = %#02x, want %#02x", sector, i, got, want)
						}
					}
				}
			})
		}
	}
}

// TestDispersedUdfBridgeLegacyEndToEnd: --legacy-boot-image writes the x86
// legacy BIOS default entry (no-emulation, load size rounded up to 2 KiB)
// and patches a Boot Information Table into the boot image's on-disc content
// (bytes 8..55), shared by UDF and the catalog.
func TestDispersedUdfBridgeLegacyEndToEnd(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeGradientPng(t, filepath.Join(dir, "gradient.png"), 64)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	loader := make([]byte, 3000)
	for i := range loader {
		loader[i] = byte(i*17 + 3)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "boot", "loader.bin"), loader, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	isoPath := filepath.Join(dir, "disc.iso")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "4096", "--data-dir", dataDir,
		"--filesystem", "udf-bridge", "--legacy-boot-image", "boot/loader.bin",
	})
	if code != 0 {
		t.Fatalf("solve exited %d", code)
	}
	data, err := os.ReadFile(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	const sector = 2048

	// Catalog (dynamically placed; the boot record at 17 carries its LBA).
	catalogLba := binary.LittleEndian.Uint32(data[17*sector+71:])
	catalog := data[int(catalogLba)*sector:]
	if catalog[0] != 1 || catalog[1] != 0 {
		t.Fatalf("validation entry header/platform = %#x/%#x, want 1/0 (x86)", catalog[0], catalog[1])
	}
	entry := catalog[32:64]
	if entry[0] != 0x88 || entry[1] != 0 {
		t.Fatalf("legacy entry = %#x/%#x, want 0x88/0 (bootable, no emulation)", entry[0], entry[1])
	}
	// 3000 bytes -> 2 KiB-rounded load size: 2 sectors of 2048 -> 8 of 512.
	if got := binary.LittleEndian.Uint16(entry[6:8]); got != 8 {
		t.Fatalf("legacy load size = %d, want 8 (2 KiB rounding)", got)
	}
	bootLba := binary.LittleEndian.Uint32(entry[8:12])

	// The legacy image's UDF entry: a single contiguous extent at that LBA.
	v := newUdfTestVolume(t, data)
	fe := v.findFileEntry("loader.bin")
	if fe == nil {
		t.Fatal("loader.bin not found in the UDF tree")
	}
	if got := binary.LittleEndian.Uint32(fe[172:176]); got != 8 {
		t.Fatalf("legacy boot image has %d AD bytes, want 8 (one extent)", got)
	}
	extentLbn := binary.LittleEndian.Uint32(fe[180:184])
	if v.partitionStart+uint64(extentLbn) != uint64(bootLba) {
		t.Fatalf("UDF extent LBA %d != catalog LBA %d", v.partitionStart+uint64(extentLbn), bootLba)
	}

	// The Boot Information Table is patched into the on-disc first sector;
	// everything outside bytes 8..55 matches the source.
	onDisc := data[int(bootLba)*sector : int(bootLba+1)*sector]
	if !bytes.Equal(onDisc[:8], loader[:8]) || !bytes.Equal(onDisc[56:], loader[56:2048]) {
		t.Fatal("legacy boot image content differs outside the BootInfoTable region")
	}
	if got := binary.LittleEndian.Uint32(onDisc[8:12]); got != 16 {
		t.Fatalf("bi_pvd = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint32(onDisc[12:16]); got != bootLba {
		t.Fatalf("bi_file = %d, want %d", got, bootLba)
	}
	if got := binary.LittleEndian.Uint32(onDisc[16:20]); got != uint32(len(loader)) {
		t.Fatalf("bi_length = %d, want %d", got, len(loader))
	}
	// bi_csum covers the whole file (verified byte-equal to xorriso).
	if got := binary.LittleEndian.Uint32(onDisc[20:24]); got != iso.BootInfoTableChecksum(loader) {
		t.Fatalf("bi_csum = %#x, want %#x", got, iso.BootInfoTableChecksum(loader))
	}
	for i := 24; i < 56; i++ {
		if onDisc[i] != 0 {
			t.Fatalf("reserved BootInfoTable byte %d = %#x, want 0", i, onDisc[i])
		}
	}
	// And the UDF copy carries the same patched bytes (shared extent).
	if got := v.readFile(fe); !bytes.Equal(got[:2048], onDisc) {
		t.Fatal("UDF copy of the legacy boot image lacks the BootInfoTable")
	}
}

// TestDispersedUdfBridgeBothBootImages: with --boot-image (EFI) and
// --legacy-boot-image (x86 BIOS), the catalog's BIOS entry comes first.
func TestDispersedUdfBridgeBothBootImages(t *testing.T) {
	xorriso, err := exec.LookPath("xorriso")
	if err != nil {
		t.Skip("xorriso not installed")
	}
	dir := t.TempDir()
	pngPath := writeGradientPng(t, filepath.Join(dir, "gradient.png"), 64)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "boot", "loader.bin"), bytes.Repeat([]byte{0x5A}, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "efi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "efi", "efi.bin"), bytes.Repeat([]byte{0x77}, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	isoPath := filepath.Join(dir, "disc.iso")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "4096", "--data-dir", dataDir,
		"--filesystem", "udf-bridge",
		"--boot-image", "efi/efi.bin", "--legacy-boot-image", "boot/loader.bin",
	})
	if code != 0 {
		t.Fatalf("solve exited %d", code)
	}
	output, err := exec.Command(xorriso, "-indev", isoPath, "-report_el_torito", "plain").CombinedOutput()
	if err != nil {
		t.Fatalf("xorriso: %v\n%s", err, output)
	}
	text := string(output)
	t.Logf("xorriso -report_el_torito plain:\n%s", text)
	biosAt := strings.Index(text, "BIOS")
	uefiAt := strings.Index(text, "UEFI")
	if biosAt < 0 || uefiAt < 0 {
		t.Fatalf("xorriso did not report both a BIOS and a UEFI entry:\n%s", text)
	}
	if biosAt > uefiAt {
		t.Fatalf("the BIOS (legacy) entry must appear before the UEFI one:\n%s", text)
	}
}

// TestPlaceDispersionIsoSectionLimit: with a full-tree bridge, a file may
// not need more ISO9660 sections than Linux follows, even when UDF's own
// allocation-descriptor limit is still met.
func TestPlaceDispersionIsoSectionLimit(t *testing.T) {
	const total = 4096
	eligible := make([]bool, total)
	for i := 100; i < 200; i++ { // one 100-sector run
		eligible[i] = true
	}
	for k := 0; k < 120; k++ { // 120 single-sector runs
		eligible[1000+2*k] = true
	}
	in := dispersePlannerInputs{
		totalSectors: total,
		fileSizes:    []int64{200 * 2048},
		filePaths:    []string{"big.bin"},
	}
	if _, err := placeDispersion(in, eligible, make([]bool, total)); err != nil {
		t.Fatalf("101 fragments fit UDF's descriptor limit: %v", err)
	}
	in.maxIsoSections = iso.IsoLinuxMaxSections
	_, err := placeDispersion(in, eligible, make([]bool, total))
	tooMany, ok := err.(*tooManyExtentsError)
	if !ok {
		t.Fatalf("error = %v (%T), want *tooManyExtentsError", err, err)
	}
	if tooMany.path != "big.bin" || tooMany.extents != 101 || tooMany.isoLimit != iso.IsoLinuxMaxSections {
		t.Fatalf("tooManyExtentsError = %+v, want big.bin/101 sections over the ISO limit", tooMany)
	}
	if !strings.Contains(err.Error(), "ISO9660 file sections") {
		t.Fatalf("error %q should name the ISO9660 limit", err)
	}
}

// TestDispersedUdfBridgeFullTreeEndToEnd: --filesystem udf-full lists every
// file, directory and symlink on the ISO9660 side (Rock Ridge), each file at
// the same extents as its UDF file entry; --expose keeps files contiguous.
func TestDispersedUdfBridgeFullTreeEndToEnd(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeGradientPng(t, filepath.Join(dir, "gradient.png"), 64)
	dataDir := filepath.Join(dir, "data")
	files := map[string][]byte{
		"boot/loader.bin": bytes.Repeat([]byte{0x5A}, 4096),
		".disk/info":      []byte("full tree\n"),
		"a/foo":           []byte("not exposed, still listed\n"),
		"a/b/barz":        bytes.Repeat([]byte("barz"), 5000),
		"a/empty":         nil,
		"Long Name With Spaces and Unicode é.txt": []byte("rock ridge name\n"),
	}
	for name, content := range files {
		path := filepath.Join(dataDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("barz", filepath.Join(dataDir, "a", "b", "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	isoPath := filepath.Join(dir, "disc.iso")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "4096", "--data-dir", dataDir,
		"--filesystem", "udf-full",
		"--boot-image", "boot/loader.bin", "--expose", ".disk",
	})
	if code != 0 {
		t.Fatalf("solve exited %d", code)
	}
	data, err := os.ReadFile(isoPath)
	if err != nil {
		t.Fatal(err)
	}

	// The primary tree lists everything; /A/B/BARZ;1 sits at barz's UDF
	// extent.
	pvd := data[16*2048:]
	root := parseIso9660DirRecords(t, data, binary.LittleEndian.Uint32(pvd[158:162]), binary.LittleEndian.Uint32(pvd[166:170]))
	byName := map[string]iso9660TestRecord{}
	for _, r := range root {
		byName[r.name] = r
	}
	for _, name := range []string{"A", "BOOT", "DISK", "EMPTYDIR", "BOOT.CAT;1"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("ISO9660 root lacks %s: %v", name, root)
		}
	}
	dirB := iso9660TestRecord{}
	for _, r := range parseIso9660DirRecords(t, data, byName["A"].lba, byName["A"].size) {
		if r.name == "B" {
			dirB = r
		}
	}
	// Every ISO9660 section of barz is the matching UDF allocation
	// descriptor (same LBA, same length), in content order; the multi-extent
	// flag marks all but the last.
	var barz []iso9660TestRecord
	for _, r := range parseIso9660DirRecords(t, data, dirB.lba, dirB.size) {
		if r.name == "BARZ;1" {
			barz = append(barz, r)
		}
	}
	v := newUdfTestVolume(t, data)
	fe := v.findFileEntry("barz")
	if fe == nil || len(barz) == 0 {
		t.Fatalf("barz missing (UDF %v, ISO %d records)", fe != nil, len(barz))
	}
	_, udfExtents := v.extents(fe)
	if len(udfExtents) != 2*len(barz) {
		t.Fatalf("barz: %d ISO9660 sections for %d UDF extents", len(barz), len(udfExtents)/2)
	}
	for i, r := range barz {
		lba, length := v.partitionStart+udfExtents[2*i], udfExtents[2*i+1]
		if uint64(r.lba) != lba || uint64(r.size) != length {
			t.Fatalf("barz section %d: ISO9660 %d/%d, UDF %d/%d", i, r.lba, r.size, lba, length)
		}
		if last := i == len(barz)-1; (r.flags&0x80 != 0) == last {
			t.Fatalf("barz section %d: multi-extent flag %#x", i, r.flags)
		}
	}

	sidecar, err := os.ReadFile(isoPath + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(sidecar, []byte(`"filesystem": "udf-full"`)) || !bytes.Contains(sidecar, []byte(`"iso9660-tree"`)) {
		t.Fatalf("sidecar lacks the full-tree records:\n%s", sidecar)
	}

	xorriso, err := exec.LookPath("xorriso")
	if err != nil {
		t.Skip("xorriso not installed")
	}
	for name, want := range files {
		dest := filepath.Join(t.TempDir(), "out")
		if output, err := exec.Command(xorriso, "-osirrox", "on", "-indev", isoPath, "-extract", "/"+name, dest).CombinedOutput(); err != nil {
			t.Fatalf("xorriso extract %s: %v\n%s", name, err, output)
		}
		if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("xorriso extract %s: content mismatch (%v)", name, err)
		}
	}
	output, err := exec.Command(xorriso, "-indev", isoPath, "-lsdl", "/a/b/link", "/a/foo", "/emptydir").CombinedOutput()
	if err != nil {
		t.Fatalf("xorriso -lsdl: %v\n%s", err, output)
	}
	for _, want := range []string{"'/a/b/link' -> 'barz'", "-rw-r-----", "'/emptydir'"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("xorriso -lsdl output lacks %q:\n%s", want, output)
		}
	}
}

// TestDispersedUdfBridgeGrub2BootInfo: --grub2-boot-info patches a GRUB2
// eltorito.img like xorriso (its start in 512-byte blocks + 5 at byte 2548,
// included in the Boot Information Table checksum), --legacy-boot-load-size
// sets the catalog load size, and a full tree keeps the volume label
// verbatim (dracut's root=live:CDLABEL= matches it exactly).
func TestDispersedUdfBridgeGrub2BootInfo(t *testing.T) {
	dir := t.TempDir()
	pngPath := writeGradientPng(t, filepath.Join(dir, "gradient.png"), 64)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "boot"), 0o755); err != nil {
		t.Fatal(err)
	}
	image := make([]byte, 9000)
	for i := range image {
		image[i] = byte(i*29 + 7)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "boot", "eltorito.img"), image, 0o644); err != nil {
		t.Fatal(err)
	}
	isoPath := filepath.Join(dir, "disc.iso")
	code := RunCommand([]string{
		"solve", "--image", pngPath, "--iso-output", isoPath,
		"--total-sectors", "4096", "--data-dir", dataDir,
		"--filesystem", "udf-full",
		"--legacy-boot-image", "boot/eltorito.img", "--grub2-boot-info", "true",
		"--legacy-boot-load-size", "4", "--volume-label", "Fedora-WS-Live-45",
	})
	if code != 0 {
		t.Fatalf("solve exited %d", code)
	}
	data, err := os.ReadFile(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	const sector = 2048
	if got := strings.TrimRight(string(data[16*sector+40:16*sector+72]), " "); got != "Fedora-WS-Live-45" {
		t.Fatalf("PVD volume identifier = %q, want it verbatim", got)
	}
	catalogLba := binary.LittleEndian.Uint32(data[17*sector+71:])
	entry := data[int(catalogLba)*sector+32 : int(catalogLba)*sector+64]
	if got := binary.LittleEndian.Uint16(entry[6:8]); got != 4 {
		t.Fatalf("legacy load size = %d, want 4", got)
	}
	bootLba := binary.LittleEndian.Uint32(entry[8:12])
	onDisc := data[int(bootLba)*sector : int(bootLba)*sector+len(image)]
	if got := binary.LittleEndian.Uint64(onDisc[iso.Grub2BootInfoOffset:]); got != uint64(bootLba)*4+5 {
		t.Fatalf("GRUB2 boot info = %d, want %d", got, uint64(bootLba)*4+5)
	}
	want := append([]byte(nil), image...)
	iso.PatchGrub2BootInfo(want, bootLba)
	iso.PatchBootInfoTable(want, bootLba, int64(len(image)), iso.BootInfoTableChecksum(want))
	if !bytes.Equal(onDisc, want) {
		t.Fatal("on-disc boot image differs from the source with the BootInfoTable and GRUB2 boot info patched")
	}
	if got := binary.LittleEndian.Uint32(onDisc[20:24]); got != iso.BootInfoTableChecksum(onDisc) {
		t.Fatalf("bi_csum %#x does not cover the final image (%#x)", got, iso.BootInfoTableChecksum(onDisc))
	}
}
