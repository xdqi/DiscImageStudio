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
