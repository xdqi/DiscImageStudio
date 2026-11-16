package dvd

import (
	"fmt"

	"disc-image-studio/internal/iso"
	"os"
	"strings"
)

// The sidecar records the resolved placement in the roadmap's constraint
// shape: every data extent with {"length", "fixedAt"} plus a role, then the
// per-file fragment list as the placement result.

type sidecarConstraint struct {
	Role       string  `json:"role"`
	Length     uint32  `json:"length"`
	FixedAt    *uint32 `json:"fixedAt"`
	Continuous bool    `json:"continuous,omitempty"`
}

type sidecarFilePlacement struct {
	Path      string          `json:"path"`
	Fragments []sidecarExtent `json:"fragments"`
}

type sidecarExtent struct {
	Lba    uint32 `json:"lba"`
	Length uint32 `json:"length"` // sectors
	Offset int64  `json:"offset"` // byte offset within the file
}

type disperseSidecar struct {
	Format           string                 `json:"format"`
	Filesystem       string                 `json:"filesystem"`
	VolumeSectors    uint32                 `json:"volumeSectors"`
	Constraints      []sidecarConstraint    `json:"constraints"`
	GrayBand         [2]int                 `json:"grayBand"`
	InteriorMarginMm [2]float64             `json:"interiorMarginMm"`
	Files            []sidecarFilePlacement `json:"files"`
}

func writeDisperseSidecar(isoPath string, plan *dispersePlan, udfPlan *iso.UdfDispersedPlan, bridge bool,
	band [2]int, marginWhite, marginBlack float64) error {
	filesystem := "udf"
	if bridge {
		filesystem = "udf-bridge"
	}
	var constraints []sidecarConstraint
	for _, c := range plan.constraints {
		constraints = append(constraints, sidecarConstraint{
			Role: c.Role, Length: c.Length, FixedAt: c.FixedAt, Continuous: c.Continuous,
		})
	}
	for _, p := range plan.placed {
		if p.Role == "file" {
			continue // per-file detail is in Files
		}
		lba := p.Lba
		constraints = append(constraints, sidecarConstraint{
			Role: p.Role + " (assigned)", Length: p.Sectors, FixedAt: &lba, Continuous: true,
		})
	}
	var files []sidecarFilePlacement
	fileCount := udfPlan.FileCount()
	for i := 0; i < fileCount; i++ {
		extents := plan.fileExtents[i]
		if len(extents) == 0 {
			continue
		}
		placement := sidecarFilePlacement{Path: udfPlan.FileName(i)}
		for _, e := range extents {
			placement.Fragments = append(placement.Fragments, sidecarExtent{Lba: e.lba, Length: e.sectors, Offset: e.offset})
		}
		files = append(files, placement)
	}
	sidecar := disperseSidecar{
		Format:           "dvd-dispersed-udf-v1",
		Filesystem:       filesystem,
		VolumeSectors:    udfPlan.VolumeSectors(),
		Constraints:      constraints,
		GrayBand:         band,
		InteriorMarginMm: [2]float64{marginWhite, marginBlack},
		Files:            files,
	}
	var sb strings.Builder
	writeSidecarJSON(&sb, sidecar)
	return os.WriteFile(isoPath+".json", []byte(sb.String()), 0o644)
}

// Minimal indented JSON writer to keep the sidecar stable without encoding/json
// field-order surprises.
func writeSidecarJSON(sb *strings.Builder, s disperseSidecar) {
	sb.WriteString("{\n")
	fmt.Fprintf(sb, "  \"format\": %q,\n", s.Format)
	fmt.Fprintf(sb, "  \"filesystem\": %q,\n", s.Filesystem)
	fmt.Fprintf(sb, "  \"volumeSectors\": %d,\n", s.VolumeSectors)
	sb.WriteString("  \"constraints\": [")
	if len(s.Constraints) == 0 {
		sb.WriteString("],\n")
	} else {
		sb.WriteString("\n")
		for i, c := range s.Constraints {
			fmt.Fprintf(sb, "    {\"role\": %q, \"length\": %d, \"fixedAt\": %s%s}",
				c.Role, c.Length, u32JSON(c.FixedAt), boolJSON(c.Continuous, ", \"continuous\": true"))
			if i+1 < len(s.Constraints) {
				sb.WriteString(",")
			}
			sb.WriteString("\n")
		}
		sb.WriteString("  ],\n")
	}
	fmt.Fprintf(sb, "  \"grayBand\": [%d, %d],\n", s.GrayBand[0], s.GrayBand[1])
	fmt.Fprintf(sb, "  \"interiorMarginMm\": [%v, %v],\n", s.InteriorMarginMm[0], s.InteriorMarginMm[1])
	sb.WriteString("  \"files\": [")
	if len(s.Files) == 0 {
		sb.WriteString("]\n")
	} else {
		sb.WriteString("\n")
		for i, f := range s.Files {
			fmt.Fprintf(sb, "    {\"path\": %q, \"fragments\": [", f.Path)
			for j, e := range f.Fragments {
				fmt.Fprintf(sb, "{\"lba\": %d, \"length\": %d, \"offset\": %d}", e.Lba, e.Length, e.Offset)
				if j+1 < len(f.Fragments) {
					sb.WriteString(", ")
				}
			}
			sb.WriteString("]}")
			if i+1 < len(s.Files) {
				sb.WriteString(",")
			}
			sb.WriteString("\n")
		}
		sb.WriteString("  ]\n")
	}
	sb.WriteString("}\n")
}

func u32JSON(v *uint32) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprint(*v)
}

func boolJSON(b bool, text string) string {
	if b {
		return text
	}
	return ""
}
