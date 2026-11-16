package iso

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildIsoTestTree creates a small nested directory tree with files.
func buildIsoTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "data.bin"), bytes.Repeat([]byte{0xAB}, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "deep", "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func checkIsoVolume(t *testing.T, image []byte, volumeSectors uint32, expectFiles bool) {
	t.Helper()
	if len(image) != int(volumeSectors)*LogicalSectorBytes {
		t.Fatalf("ISO size = %d, want %d", len(image), int(volumeSectors)*LogicalSectorBytes)
	}
	sector16 := image[16*LogicalSectorBytes:]
	if sector16[0] != 1 || string(sector16[1:6]) != "CD001" {
		t.Fatalf("primary volume descriptor missing CD001 at sector 16")
	}
	sector17 := image[17*LogicalSectorBytes:]
	if sector17[0] != 2 || string(sector17[1:6]) != "CD001" {
		t.Fatalf("supplementary volume descriptor missing CD001 at sector 17")
	}
	if image[18*LogicalSectorBytes] != 255 {
		t.Fatalf("volume descriptor set terminator missing at sector 18")
	}
	if expectFiles && !bytes.Contains(image, []byte("hello world")) {
		t.Fatalf("file content missing from ISO image")
	}
}

func TestIso9660Sequential(t *testing.T) {
	root := buildIsoTestTree(t)
	const volumeSectors = 300
	plan, err := PlanIso(root, volumeSectors, "TEST", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FileCount() != 3 || plan.DirectoryCount() != 3 {
		t.Fatalf("FileCount=%d DirectoryCount=%d, want 3/3", plan.FileCount(), plan.DirectoryCount())
	}
	var buf bytes.Buffer
	buf.Grow(volumeSectors * LogicalSectorBytes)
	if err := WriteIsoSequentialPrefix(plan, &buf, volumeSectors, context.Background()); err != nil {
		t.Fatal(err)
	}
	checkIsoVolume(t, buf.Bytes(), volumeSectors, true)
}

func TestIso9660OuterRing(t *testing.T) {
	root := buildIsoTestTree(t)
	const volumeSectors = 300
	plan, err := PlanIso(root, volumeSectors, "TEST", true)
	if err != nil {
		t.Fatal(err)
	}
	boundary := plan.FileDataStartLba()
	var buf bytes.Buffer
	buf.Grow(volumeSectors * LogicalSectorBytes)
	if err := WriteIsoOuterRingPrefix(plan, &buf, boundary, context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := WriteIsoOuterRingSuffix(plan, &buf, boundary, context.Background()); err != nil {
		t.Fatal(err)
	}
	checkIsoVolume(t, buf.Bytes(), volumeSectors, true)
}

func checkUdfVolume(t *testing.T, image []byte, volumeSectors uint32, expectFiles bool) {
	t.Helper()
	if len(image) != int(volumeSectors)*LogicalSectorBytes {
		t.Fatalf("UDF size = %d, want %d", len(image), int(volumeSectors)*LogicalSectorBytes)
	}
	// Anchor volume descriptor pointer at sector 256: tag id 2.
	if got := binary.LittleEndian.Uint16(image[256*LogicalSectorBytes:]); got != 2 {
		t.Fatalf("anchor tag id at sector 256 = %d, want 2", got)
	}
	// Volume recognition sequence.
	if string(image[16*LogicalSectorBytes+1:16*LogicalSectorBytes+6]) != "BEA01" {
		t.Fatalf("BEA01 missing at sector 16")
	}
	if string(image[17*LogicalSectorBytes+1:17*LogicalSectorBytes+6]) != "NSR03" {
		t.Fatalf("NSR03 missing at sector 17")
	}
	// Primary volume descriptor of the main VDS at sector 32: tag id 1.
	if got := binary.LittleEndian.Uint16(image[32*LogicalSectorBytes:]); got != 1 {
		t.Fatalf("primary descriptor tag id at sector 32 = %d, want 1", got)
	}
	if expectFiles && !bytes.Contains(image, []byte("hello world")) {
		t.Fatalf("file content missing from UDF image")
	}
}

func TestUdfSequential(t *testing.T) {
	root := buildIsoTestTree(t)
	const volumeSectors = 640
	plan, err := PlanUdf(root, volumeSectors, "TEST", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FileCount() != 3 || plan.DirectoryCount() != 3 {
		t.Fatalf("FileCount=%d DirectoryCount=%d, want 3/3", plan.FileCount(), plan.DirectoryCount())
	}
	var buf bytes.Buffer
	buf.Grow(volumeSectors * LogicalSectorBytes)
	if err := WriteUdfSequentialPrefix(plan, &buf, plan.DrawingEndLbaExclusive(), context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := WriteUdfSequentialSuffix(plan, &buf, plan.DrawingEndLbaExclusive(), context.Background()); err != nil {
		t.Fatal(err)
	}
	checkUdfVolume(t, buf.Bytes(), volumeSectors, true)
}

func TestUdfOuterRing(t *testing.T) {
	root := buildIsoTestTree(t)
	const volumeSectors = 640
	plan, err := PlanUdf(root, volumeSectors, "TEST", true)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.Grow(volumeSectors * LogicalSectorBytes)
	if err := WriteUdfSequentialPrefix(plan, &buf, plan.DrawingEndLbaExclusive(), context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := WriteUdfSequentialSuffix(plan, &buf, plan.DrawingEndLbaExclusive(), context.Background()); err != nil {
		t.Fatal(err)
	}
	checkUdfVolume(t, buf.Bytes(), volumeSectors, true)
}

func TestIso9660EmptyVolume(t *testing.T) {
	const volumeSectors = 64
	plan, err := PlanIso("", volumeSectors, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteIsoSequentialPrefix(plan, &buf, volumeSectors, context.Background()); err != nil {
		t.Fatal(err)
	}
	checkIsoVolume(t, buf.Bytes(), volumeSectors, false)
}

func TestUdfEmptyVolume(t *testing.T) {
	const volumeSectors = 640
	plan, err := PlanUdf("", volumeSectors, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteUdfSequentialPrefix(plan, &buf, plan.DrawingEndLbaExclusive(), context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := WriteUdfSequentialSuffix(plan, &buf, plan.DrawingEndLbaExclusive(), context.Background()); err != nil {
		t.Fatal(err)
	}
	checkUdfVolume(t, buf.Bytes(), volumeSectors, false)
}

func TestIsWithinDirectory(t *testing.T) {
	if !IsWithinDirectory("/a/b/c", "/a/b") {
		t.Fatal("expected /a/b/c within /a/b")
	}
	if !IsWithinDirectory("/a/b", "/a/b") {
		t.Fatal("expected /a/b within /a/b")
	}
	if IsWithinDirectory("/a/bc", "/a/b") {
		t.Fatal("expected /a/bc outside /a/b")
	}
	if IsWithinDirectory("/a", "/a/b") {
		t.Fatal("expected /a outside /a/b")
	}
}

// Golden-image verification against images generated by the original .NET
// CLI (disc-image-studio cd-generate / bd-generate). The plan timestamps are
// recovered from the golden images themselves (the ISO volume date and the
// bd-data.iso.json udfTimestamp), so the Go output must match byte-for-byte.

func goldenDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("DISC_IMAGE_GOLDEN")
	if dir == "" {
		dir = "/tmp/opencode/golden"
	}
	if _, err := os.Stat(filepath.Join(dir, "data32.iso")); err != nil {
		t.Skip("golden images not available")
	}
	return dir
}

// withFixedNow pins the plan timestamp source for golden reproduction.
func withFixedNow(t *testing.T, ts time.Time) {
	t.Helper()
	old := nowFunc
	nowFunc = func() time.Time { return ts }
	t.Cleanup(func() { nowFunc = old })
}

func TestGoldenCdDataImage(t *testing.T) {
	dir := goldenDir(t)
	golden, err := os.ReadFile(filepath.Join(dir, "data32.iso"))
	if err != nil {
		t.Skip(err)
	}
	const volumeSectors = 400
	if len(golden) != volumeSectors*LogicalSectorBytes {
		t.Fatalf("golden size = %d", len(golden))
	}
	// The plan timestamp only surfaces in the volume descriptor as the two
	// volume dates (hundredths precision) and the embedded root record's
	// directory date (seconds precision); recover it from sector 16.
	stamp := string(golden[16*LogicalSectorBytes+813 : 16*LogicalSectorBytes+827])
	ts, err := time.Parse("20060102150405", stamp)
	if err != nil {
		t.Fatal(err)
	}
	hundredths := int(golden[16*LogicalSectorBytes+827]-'0')*10 + int(golden[16*LogicalSectorBytes+828]-'0')
	ts = ts.Add(time.Duration(hundredths) * 10 * time.Millisecond).UTC()
	withFixedNow(t, ts)

	plan, err := PlanIso(filepath.Join(dir, "data"), volumeSectors, "MYDISC", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DataEndLbaExclusive() != 29 {
		t.Fatalf("DataEndLbaExclusive = %d, want 29", plan.DataEndLbaExclusive())
	}
	var buf bytes.Buffer
	buf.Grow(volumeSectors * LogicalSectorBytes)
	if err := WriteIsoSequentialPrefix(plan, &buf, plan.DataEndLbaExclusive(), context.Background()); err != nil {
		t.Fatal(err)
	}
	compareBytes(t, "ISO9660 metadata", buf.Bytes(), golden[:29*LogicalSectorBytes])
}

func TestGoldenBdDataImage(t *testing.T) {
	dir := goldenDir(t)
	golden, err := os.ReadFile(filepath.Join(dir, "bd-data.iso"))
	if err != nil {
		t.Skip(err)
	}
	const volumeSectors = 2048
	if len(golden) != volumeSectors*LogicalSectorBytes {
		t.Fatalf("golden size = %d", len(golden))
	}
	var sidecar struct {
		UdfTimestamp string `json:"udfTimestamp"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "bd-data.iso.json"))
	if err != nil {
		t.Skip(err)
	}
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatal(err)
	}
	ts, err := time.Parse(time.RFC3339Nano, sidecar.UdfTimestamp)
	if err != nil {
		t.Fatal(err)
	}
	withFixedNow(t, ts.UTC())

	plan, err := PlanUdf(filepath.Join(dir, "data"), volumeSectors, "MYBD", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DrawingStartLba() != 320 || plan.DrawingEndLbaExclusive() != 1760 {
		t.Fatalf("drawing = [%d, %d), want [320, 1760)", plan.DrawingStartLba(), plan.DrawingEndLbaExclusive())
	}
	var buf bytes.Buffer
	buf.Grow(volumeSectors * LogicalSectorBytes)
	if err := WriteUdfSequentialPrefix(plan, &buf, plan.DrawingStartLba(), context.Background()); err != nil {
		t.Fatal(err)
	}
	// The drawing interval [320, 1760) is payload in the golden; skip it.
	buf.Grow(int(plan.DrawingEndLbaExclusive()-plan.DrawingStartLba()) * LogicalSectorBytes)
	buf.Write(make([]byte, int(plan.DrawingEndLbaExclusive()-plan.DrawingStartLba())*LogicalSectorBytes))
	if err := WriteUdfSequentialSuffix(plan, &buf, plan.DrawingEndLbaExclusive(), context.Background()); err != nil {
		t.Fatal(err)
	}
	image := buf.Bytes()
	maskUdfFileEntryBytes(image)
	maskUdfFileEntryBytes(golden)
	compareBytes(t, "UDF front metadata", image[:320*LogicalSectorBytes], golden[:320*LogicalSectorBytes])
	compareBytes(t, "UDF tail metadata", image[1760*LogicalSectorBytes:], golden[1760*LogicalSectorBytes:])
}

// maskUdfFileEntryBytes zeroes the uid/gid/permissions fields of every UDF
// file entry (tag 261) in the image, plus the tag checksum/CRC they feed:
// the writers deliberately differ from the original .NET CLI there (root
// ownership, uid:gid 0:0, and modes copied from the source).
func maskUdfFileEntryBytes(data []byte) {
	for lba := 0; (lba+1)*LogicalSectorBytes <= len(data); lba++ {
		sector := data[lba*LogicalSectorBytes:]
		if binary.LittleEndian.Uint16(sector[0:2]) != 261 {
			continue
		}
		sector[4] = 0 // tag checksum
		sector[8] = 0 // descriptor CRC
		sector[9] = 0
		for i := 36; i < 48; i++ { // uid, gid, permissions
			sector[i] = 0
		}
	}
}

func compareBytes(t *testing.T, what string, got, want []byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: size %d != %d", what, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: first diff at byte %d (sector %d offset %d): got %02x want %02x",
				what, i, i/LogicalSectorBytes, i%LogicalSectorBytes, got[i], want[i])
		}
	}
}
