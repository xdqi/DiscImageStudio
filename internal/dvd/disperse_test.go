package dvd

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// readUdfFileByName is a minimal UDF reader for the test: walks the anchor →
// main VDS → partition → FSD → root directory (recursing into subdirectories)
// → file entry → extents.
func readUdfFileByName(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	const sector = 2048
	// Anchor at 256: main VDS extent at offset 16 (length, lba).
	anchor := data[256*sector:]
	mainVdsLba := binary.LittleEndian.Uint32(anchor[20:24])
	// Logical volume descriptor is the 4th descriptor in the main VDS (32+3).
	lvd := data[(mainVdsLba+3)*sector:]
	// Logical volume contents use: FSD at partition start is in the LVD's
	// logical volume contents use long_ad at offset 248: (length, lbn). The
	// partition map is at offset 264: ... partition reference at 268? Use the
	// partition descriptor for the start: PD is at mainVdsLba+2, partition
	// start at offset 188.
	pd := data[(mainVdsLba+2)*sector:]
	partitionStart := uint64(binary.LittleEndian.Uint32(pd[188:192]))
	// LVD long_ad at offset 248: length(4) at 248..252, lbn at 252..256.
	fsdLbn := binary.LittleEndian.Uint32(lvd[252:256])
	fsd := data[(partitionStart+uint64(fsdLbn))*sector:]
	// Root directory ICB: long_ad at offset 400: length at 400..404, lbn 404..408.
	rootLbn := binary.LittleEndian.Uint32(fsd[404:408])
	rootFe := data[(partitionStart+uint64(rootLbn))*sector:]
	// Root FE: allocation descriptors at 176; short_ad list: (length, lbn).
	readExtents := func(fe []byte) (uint64, []uint64) {
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
	readFile := func(fe []byte) []byte {
		infoLen, extents := readExtents(fe)
		var out []byte
		for i := 0; i+1 < len(extents); i += 2 {
			lbn, length := extents[i], extents[i+1]
			start := (partitionStart + lbn) * sector
			out = append(out, data[start:start+length]...)
		}
		return out[:infoLen]
	}
	// Walk directories recursively: FIDs with file characteristics 10 = parent,
	// 2 = directory, 0 = file.
	var walk func(fe []byte) []byte
	walk = func(fe []byte) []byte {
		dirData := readFile(fe)
		for off := 0; off+38 <= len(dirData); {
			nameLen := int(dirData[off+19])
			implLen := int(binary.LittleEndian.Uint16(dirData[off+36 : off+38]))
			recLen := (38 + nameLen + 3) / 4 * 4
			if recLen == 0 || off+recLen > len(dirData) {
				break
			}
			characteristics := dirData[off+18]
			if nameLen > 1 {
				fidName := string(dirData[off+38+implLen+1 : off+38+implLen+nameLen])
				icbLbn := binary.LittleEndian.Uint32(dirData[off+24 : off+28])
				child := data[(partitionStart+uint64(icbLbn))*sector:]
				if characteristics&2 != 0 {
					if found := walk(child); found != nil {
						return found
					}
				} else if strings.EqualFold(fidName, name) {
					return readFile(child)
				}
			}
			off += recLen
		}
		return nil
	}
	if found := walk(rootFe); found != nil {
		return found
	}
	t.Fatalf("file %q not found in UDF tree", name)
	return nil
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

// fakeDisperseToneMap builds a tone map from per-sector sample rows so the
// planner can be tested without a real picture. sectorPattern returns the
// mapSamplesPerSector luminance samples of a sector; use solidLum for
// gray-in-band sectors and mixLum for everything else (a mixed row is never
// a "pure" tone, so the erosion-grid interior path is never consulted and
// the spiral can stay nil).
func fakeDisperseToneMap(t *testing.T, total int, sectorPattern func(sector int) []byte) *disperseToneMap {
	t.Helper()
	samples := make([][]byte, total)
	for s := 0; s < total; s++ {
		row := sectorPattern(s)
		if len(row) != mapSamplesPerSector {
			t.Fatalf("sector %d pattern has %d samples", s, len(row))
		}
		samples[s] = row
	}
	return &disperseToneMap{
		sectorSamples: samples,
		lumThreshold:  128,
		gridTurns:     1,
		gridArcs:      erosionArcBins,
		turnRadii:     []float64{25},
		gridLum:       make([]byte, erosionArcBins),
	}
}

func solidLum(lum byte) []byte {
	row := make([]byte, mapSamplesPerSector)
	for k := range row {
		row[k] = lum
	}
	return row
}

// mixLum returns a row of 7×a + 1×b samples.
func mixLum(a, b byte) []byte {
	row := solidLum(a)
	row[mapSamplesPerSector-1] = b
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
