package iso

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestSymlinkTree exercises both the contiguous (planUdf) and ISO9660 writers
// with a tree containing symlinks: the links must land as UDF file type 12
// with kernel-compatible path-component content, and the ISO9660 image must
// carry Rock Ridge SL entries (plus SP/ER on the root directory).
func TestSymlinkTree(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	sub := filepath.Join(dataDir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "real.txt"), []byte("real file content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "deep.bin"), []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	mklink := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(dataDir, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	mklink("real.txt", "link.txt")      // relative
	mklink("/etc/hostname", "abs-link") // absolute
	if err := os.Symlink("../real.txt", filepath.Join(sub, "rel-up")); err != nil {
		t.Fatal(err)
	}

	// --- UDF (contiguous writer) ---
	udfPlan, err := planUdf(dataDir, 4096, "SYMLINKS", false)
	if err != nil {
		t.Fatal(err)
	}
	image := make([]byte, 4096*LogicalSectorBytes)
	sw := &sliceWriter{buf: image}
	if err := writeUdfSequentialPrefix(udfPlan, sw, udfPlan.DrawingStartLba(), context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := writeUdfSequentialSuffix(udfPlan, sw, udfPlan.DrawingEndLbaExclusive(), context.Background()); err != nil {
		t.Fatal(err)
	}
	checkUdfSymlinks(t, image, map[string]bool{
		"real.txt": true, "/etc/hostname": true, "../real.txt": true,
	})

	// --- ISO9660 with Rock Ridge ---
	isoPlan, err := planIso(dataDir, 4096, "SYMLINKS", false)
	if err != nil {
		t.Fatal(err)
	}
	if !isoPlan.hasSymlinks {
		t.Fatal("the plan did not notice the symlinks")
	}
	isoImage := make([]byte, 4096*LogicalSectorBytes)
	iw := newSequentialIsoWriter(&sliceWriter{buf: isoImage}, context.Background(), 0)
	if err := isoWriteMetadata(isoPlan, iw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(isoImage, []byte{'S', 'P', 7, 1, 0xBE, 0xEF, 0}) {
		t.Fatal("no SUSP SP entry found")
	}
	if !bytes.Contains(isoImage, []byte("RRIP_1991A")) {
		t.Fatal("no RRIP ER entry found")
	}
	for _, want := range [][]byte{
		{'S', 'L', 22, 1, 0, 0x08, 0, 0, 3, 'e', 't', 'c', 0, 8, 'h', 'o', 's', 't', 'n', 'a', 'm', 'e'},
		{'S', 'L', 15, 1, 0, 0, 8, 'r', 'e', 'a', 'l', '.', 't', 'x', 't'},
		{'S', 'L', 17, 1, 0, 0x04, 0, 0, 8, 'r', 'e', 'a', 'l', '.', 't', 'x', 't'},
	} {
		if !bytes.Contains(isoImage, want) {
			t.Fatalf("SL entry %x not found", want)
		}
	}
}

// sliceWriter is an in-memory io.Writer for image construction.
type sliceWriter struct {
	buf []byte
	off int64
}

func (w *sliceWriter) Write(p []byte) (int, error) {
	copy(w.buf[w.off:], p)
	w.off += int64(len(p))
	return len(p), nil
}

// checkUdfSymlinks finds the fileType-12 file entries and decodes their
// content per the Linux fs/udf/symlink.c udf_pc_to_char algorithm.
func checkUdfSymlinks(t *testing.T, image []byte, want map[string]bool) {
	t.Helper()
	found := map[string]bool{}
	for lba := 288; lba < 340; lba++ {
		fe := image[lba*LogicalSectorBytes:]
		if binary.LittleEndian.Uint16(fe[0:2]) != 261 || fe[27] != 12 {
			continue
		}
		adLen := binary.LittleEndian.Uint32(fe[172:176])
		if adLen != 8 {
			t.Fatalf("symlink FE @%d has %d AD bytes, want one short_ad", lba, adLen)
		}
		length := binary.LittleEndian.Uint32(fe[176:180]) & 0x3fffffff
		pos := binary.LittleEndian.Uint32(fe[180:184])
		// short_ad positions are partition-relative LBNs; the partition starts at 288.
		start := uint64(288+pos) * LogicalSectorBytes
		content := image[start : start+uint64(length)]
		decoded := decodeUdfSymlink(t, content)
		if !want[decoded] {
			t.Fatalf("unexpected symlink target %q", decoded)
		}
		found[decoded] = true
	}
	if len(found) != len(want) {
		t.Fatalf("decoded %v, want %v", found, want)
	}
}

// decodeUdfSymlink decodes UDF path components per the Linux kernel's
// fs/udf/symlink.c udf_pc_to_char.
func decodeUdfSymlink(t *testing.T, content []byte) string {
	t.Helper()
	out := ""
	for elen := 0; elen+4 <= len(content); {
		ctype := content[elen]
		clen := int(content[elen+1])
		if elen+4+clen > len(content) {
			t.Fatalf("symlink component overruns the content")
		}
		ident := content[elen+4 : elen+4+clen]
		switch ctype {
		case 1: // root (media-specific): skip the ident when present
			if clen > 0 {
				elen += 4 + clen
				continue
			}
			out = "/"
			elen += 4
		case 2: // root
			out = "/"
			elen += 4
		case 3: // parent
			out += "../"
			elen += 4
		case 4: // current
			out += "./"
			elen += 4
		case 5: // name, CS0-encoded
			if len(ident) == 0 {
				t.Fatalf("empty name component")
			}
			if ident[0] == 8 {
				out += string(ident[1:]) + "/"
			} else {
				var runes []rune
				for i := 1; i+1 < len(ident); i += 2 {
					runes = append(runes, rune(binary.BigEndian.Uint16(ident[i:])))
				}
				out += string(runes) + "/"
			}
			elen += 4 + clen
		default:
			elen += 4
		}
	}
	for len(out) > 1 && out[len(out)-1] == '/' {
		out = out[:len(out)-1]
	}
	return out
}
