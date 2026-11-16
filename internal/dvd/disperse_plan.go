package dvd

import (
	"fmt"
	"sort"
)

// Constraint classes for the dispersed DVD data plan.

// dataConstraint is one required data range: either pinned to an exact LBA
// (FixedAt != nil), or one contiguous run at a position the planner assigns
// (Continuous), or freely fragmentable (neither — only the aggregate matters).
type dataConstraint struct {
	Role       string  `json:"role"`
	Length     uint32  `json:"length"` // 2048-byte sectors
	FixedAt    *uint32 `json:"fixedAt,omitempty"`
	Continuous bool    `json:"continuous,omitempty"`
}

// placedExtent is a constraint with its final position.
type placedExtent struct {
	Role     string
	Path     string // for file results
	Lba      uint32
	Sectors  uint32
	Fragment int
}

// fileExtent is one placed fragment of file content.
type fileExtent struct {
	lba       uint32
	offset    int64 // byte offset within the file's content
	sectors   uint32
	fileIndex int    // the iso plan's file index (-1 = synthetic/zero fill)
	filePath  string // source path; "" = zeros
}

// dispersePlan is the resolved placement.
type dispersePlan struct {
	constraints []dataConstraint
	placed      []placedExtent
	// fileExtents maps the iso package's file index to its extents (in order).
	fileExtents map[int][]fileExtent
	eligible    []bool // final per-sector eligibility (post exclusions)
	band        [2]byte
	marginWhite float64
	marginBlack float64
}

// dispersePlannerInputs carries everything the planner needs.
type dispersePlannerInputs struct {
	totalSectors   uint32
	fixed          []dataConstraint   // metadata extents (fixed positions)
	continuous     []continuousDemand // boot image/catalog etc.
	fileSizes      []int64            // per-file byte sizes (assignment order)
	filePaths      []string           // per-file source paths ("" = zero fill)
	headroom       float64
	grayBand       [2]int // manual override when both >= 0; [-1,-1] = auto
	marginWhite    float64
	marginBlack    float64
	toneMap        *disperseToneMap
	reservedBefore uint32 // [0, reservedBefore) is never picture/data-placement
	reservedAfter  uint32 // [reservedAfter, totalSectors) is never picture/placement
}

type continuousDemand struct {
	Role    string
	Path    string
	Sectors uint32
}

// planDispersion computes the placement, widening the gray band and shrinking
// margins until the data fits.
func planDispersion(in dispersePlannerInputs) (*dispersePlan, error) {
	if in.headroom <= 0 {
		in.headroom = 1.10
	}
	band := in.grayBand
	if band[0] < 0 || band[1] < 0 || band[0] > band[1] {
		band = [2]int{120, 136}
	}
	marginWhite, marginBlack := in.marginWhite, in.marginBlack

	fixedUsed := make([]bool, in.totalSectors)
	for _, c := range in.fixed {
		if c.FixedAt == nil {
			continue
		}
		at := *c.FixedAt
		if uint64(at)+uint64(c.Length) > uint64(in.totalSectors) {
			return nil, fmt.Errorf("fixed extent %s at LBA %d exceeds the disc", c.Role, at)
		}
		for lba := at; lba < at+c.Length; lba++ {
			if lba >= uint32(len(fixedUsed)) {
				return nil, fmt.Errorf("fixed extent %s out of range", c.Role)
			}
			fixedUsed[lba] = true
		}
	}

	fileSectorsTotal := uint64(0)
	for _, size := range in.fileSizes {
		fileSectorsTotal += uint64((size + 2047) / 2048)
	}
	continuousTotal := uint64(0)
	for _, d := range in.continuous {
		continuousTotal += uint64(d.Sectors)
	}
	needed := uint64(float64(fileSectorsTotal+continuousTotal) * in.headroom)

	for attempt := 0; attempt < 100; attempt++ {
		eligible := in.toneMap.classifySectors(byte(band[0]), byte(band[1]), marginWhite, marginBlack)
		free := make([]bool, in.totalSectors)
		count := uint64(0)
		for i := uint32(0); i < in.totalSectors; i++ {
			if i < in.reservedBefore || i >= in.reservedAfter {
				continue
			}
			if eligible[i] && !fixedUsed[i] {
				free[i] = true
				count++
			}
		}
		if count >= needed {
			plan, err := placeDispersion(in, free, fixedUsed)
			if err == nil {
				plan.eligible = free
				plan.band = [2]byte{byte(band[0]), byte(band[1])}
				plan.marginWhite, plan.marginBlack = marginWhite, marginBlack
				return plan, nil
			}
			if _, ok := err.(*noContinuousRunError); !ok {
				return nil, err
			}
			// else: no continuous run — widen further.
		}
		// Capacity loop: widen the band, then shrink the margins.
		if band[0] > 64 || band[1] < 192 {
			band[0] -= 2
			band[1] += 2
			continue
		}
		if marginWhite > 0.05 || marginBlack > 0.05 {
			marginWhite *= 0.7
			marginBlack *= 0.7
			continue
		}
		return nil, fmt.Errorf("the picture's mid-tone and flat areas hold %d sectors, but the data needs %d (with headroom); use a picture with more tonal range or fewer files", count, needed)
	}
	return nil, fmt.Errorf("placement failed")
}

type noContinuousRunError struct{ msg string }

func (e *noContinuousRunError) Error() string { return e.msg }

// placeDispersion assigns every constraint a final position: fixed extents
// first, then continuous runs (best-fit into the eligible set), then the
// per-file extents in LBA order.
func placeDispersion(in dispersePlannerInputs, eligible []bool, fixedUsed []bool) (*dispersePlan, error) {
	plan := &dispersePlan{fileExtents: make(map[int][]fileExtent)}
	used := make([]bool, in.totalSectors)
	copy(used, fixedUsed)

	// Continuous demands: first-fit into the eligible set, in LBA order.
	for _, d := range in.continuous {
		placedAt, found := findFreeRun(used, eligible, d.Sectors)
		if !found {
			return nil, &noContinuousRunError{msg: fmt.Sprintf("no continuous run of %d sectors remains for %s; the picture's gray areas are too scattered", d.Sectors, d.Role)}
		}
		markUsed(used, placedAt, d.Sectors)
		plan.placed = append(plan.placed, placedExtent{Role: d.Role, Path: d.Path, Lba: placedAt, Sectors: d.Sectors})
	}

	// File data: pour into the remaining eligible sectors, best-fit runs per
	// file (smallest sufficient free run), fragmenting when needed.
	runs := collectFreeRuns(used, eligible)
	for fileIndex, size := range in.fileSizes {
		sectorsLeft := uint32((size + 2047) / 2048)
		offset := int64(0)
		fragment := 0
		for sectorsLeft > 0 {
			runIndex := bestFitRun(runs, sectorsLeft)
			if runIndex < 0 {
				// Fragment: take the largest remaining run.
				runIndex = largestRun(runs)
				if runIndex < 0 {
					return nil, fmt.Errorf("file data exceeds the picture's gray capacity")
				}
			}
			r := runs[runIndex]
			take := sectorsLeft
			if r.end-r.start < take {
				take = r.end - r.start
			}
			plan.fileExtents[fileIndex] = append(plan.fileExtents[fileIndex], fileExtent{
				lba: r.start, offset: offset, sectors: take, fileIndex: fileIndex, filePath: in.filePaths[fileIndex],
			})
			plan.placed = append(plan.placed, placedExtent{
				Role: "file", Path: fmt.Sprint(fileIndex), Lba: r.start, Sectors: take, Fragment: fragment,
			})
			offset += int64(take) * 2048
			sectorsLeft -= take
			fragment++
			runs[runIndex].start += take
			if runs[runIndex].start >= runs[runIndex].end {
				runs = append(runs[:runIndex], runs[runIndex+1:]...)
			}
		}
	}
	plan.constraints = in.fixed
	return plan, nil
}

func findFreeRun(used, eligible []bool, sectors uint32) (uint32, bool) {
	n := uint32(len(eligible))
	for i := uint32(0); i+sectors <= n; i++ {
		if !eligible[i] || used[i] {
			continue
		}
		ok := true
		for k := i; k < i+sectors; k++ {
			if used[k] || !eligible[k] {
				ok = false
				break
			}
		}
		if ok {
			return i, true
		}
	}
	return 0, false
}

func markUsed(used []bool, start, sectors uint32) {
	for k := start; k < start+sectors; k++ {
		used[k] = true
	}
}

func collectFreeRuns(used, eligible []bool) []struct{ start, end uint32 } {
	var runs []struct{ start, end uint32 }
	n := uint32(len(eligible))
	for i := uint32(0); i < n; {
		if used[i] || !eligible[i] {
			i++
			continue
		}
		start := i
		for i < n && eligible[i] && !used[i] {
			i++
		}
		runs = append(runs, struct{ start, end uint32 }{start, i})
	}
	return runs
}

// bestFitRun finds the smallest run that fits sectors; returns -1 if none.
func bestFitRun(runs []struct{ start, end uint32 }, sectors uint32) int {
	best := -1
	var bestSize uint32
	for i, r := range runs {
		size := r.end - r.start
		if size >= sectors && (best < 0 || size < bestSize) {
			best = i
			bestSize = size
		}
	}
	return best
}

func largestRun(runs []struct{ start, end uint32 }) int {
	best := -1
	var bestSize uint32
	for i, r := range runs {
		size := r.end - r.start
		if best < 0 || size > bestSize {
			best = i
			bestSize = size
		}
	}
	if best >= 0 && bestSize == 0 {
		return -1
	}
	return best
}

var _ = sort.Slice
