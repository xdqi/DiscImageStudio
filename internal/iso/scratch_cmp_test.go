package iso

// Throwaway byte-exactness comparison against reference images produced by
// the original C# DiscImageStudio.Iso writers (fixed plan timestamp
// 1700000000, tree at /tmp/opencode/isocmp/tree with fixed mtimes).
// Enabled with ISO_CMP=1.

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

const cmpDir = "/tmp/opencode/isocmp"

func TestCmpAgainstCSharp(t *testing.T) {
	if os.Getenv("ISO_CMP") == "" {
		t.Skip("set ISO_CMP=1 to compare against C# reference images")
	}
	old := nowFunc
	nowFunc = func() time.Time { return time.Unix(1700000000, 0).UTC() }
	defer func() { nowFunc = old }()
	tree := cmpDir + "/tree"
	ctx := context.Background()

	run := func(name string, volumeSectors uint32, label string, outer bool, udf bool) {
		t.Helper()
		want, err := os.ReadFile(cmpDir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		buf.Grow(len(want))
		if udf {
			plan, err := planUdf(tree, volumeSectors, label, outer)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeUdfSequentialPrefix(plan, &buf, plan.DrawingEndLbaExclusive(), ctx); err != nil {
				t.Fatal(err)
			}
			if err := writeUdfSequentialSuffix(plan, &buf, plan.DrawingEndLbaExclusive(), ctx); err != nil {
				t.Fatal(err)
			}
		} else {
			plan, err := planIso(tree, volumeSectors, label, outer)
			if err != nil {
				t.Fatal(err)
			}
			if outer {
				if err := writeIsoOuterRingPrefix(plan, &buf, plan.FileDataStartLba(), ctx); err != nil {
					t.Fatal(err)
				}
				if err := writeIsoOuterRingSuffix(plan, &buf, plan.FileDataStartLba(), ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := writeIsoSequentialPrefix(plan, &buf, volumeSectors, ctx); err != nil {
					t.Fatal(err)
				}
			}
		}
		got := buf.Bytes()
		if len(got) != len(want) {
			t.Fatalf("%s: size %d != %d", name, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s: first diff at byte %d (sector %d offset %d): got %02x want %02x",
					name, i, i/LogicalSectorBytes, i%LogicalSectorBytes, got[i], want[i])
			}
		}
		t.Logf("%s: %d bytes identical", name, len(got))
	}

	run("cs-iso-seq.img", 400, "MYDISC", false, false)
	run("cs-iso-outer.img", 400, "MYDISC", true, false)
	run("cs-udf-seq.img", 1024, "MYBD", false, true)
	run("cs-udf-outer.img", 1024, "MYBD", true, true)
}
