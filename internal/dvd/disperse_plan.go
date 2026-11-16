package dvd

import (
	"container/heap"
	"fmt"

	"disc-image-studio/internal/iso"
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

	var placementErr error
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
			switch err.(type) {
			case *noContinuousRunError, *tooManyExtentsError:
				// Widening the band (or shrinking the margins) merges and
				// lengthens the gray runs: more capacity and fewer fragments.
				placementErr = err
			default:
				return nil, err
			}
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
		if tooMany, ok := placementErr.(*tooManyExtentsError); ok {
			return nil, fmt.Errorf("the picture's gray areas are too scattered for %s: it needs %d UDF allocation descriptors, but one file entry supports %d; use a picture with larger mid-tone or flat areas, or fewer files",
				tooMany.path, tooMany.extents, iso.UdfMaxAllocationDescriptors)
		}
		return nil, fmt.Errorf("the picture's mid-tone and flat areas hold %d sectors, but the data needs %d (with headroom); use a picture with more tonal range or fewer files", count, needed)
	}
	return nil, fmt.Errorf("placement failed")
}

type noContinuousRunError struct{ msg string }

func (e *noContinuousRunError) Error() string { return e.msg }

// tooManyExtentsError reports a file whose placement needs more UDF
// allocation descriptors than one file entry holds. It is retryable: a wider
// gray band merges and lengthens the free runs, reducing fragmentation.
type tooManyExtentsError struct {
	path    string
	extents int
}

func (e *tooManyExtentsError) Error() string {
	return fmt.Sprintf("%s needs %d UDF allocation descriptors (one file entry supports %d)",
		e.path, e.extents, iso.UdfMaxAllocationDescriptors)
}

// placeDispersion assigns every constraint a final position: fixed extents
// first, then continuous runs (first-fit into the eligible set), then the
// per-file extents, largest file remainder into the largest free run. Each
// file's extent list is in content-offset order, as UDF requires.
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

	// File data: worst-fit decreasing. The unplaced files (and the unplaced
	// remainders of fragmented files) sit in a max-heap by size; the free
	// runs sit in a max-heap by length. Each step takes the largest file
	// remainder and puts it into the largest free run; the leftover of the
	// run (or of the file) re-enters its heap. Big files thus get first pick
	// of the long runs — important because every fragment costs one UDF
	// allocation descriptor and a file entry holds at most 234.
	runs := &runHeap{}
	for _, r := range collectFreeRuns(used, eligible) {
		heap.Push(runs, r)
	}
	files := &fileDemandHeap{}
	for fileIndex, size := range in.fileSizes {
		if sectors := uint32((size + 2047) / 2048); sectors > 0 {
			heap.Push(files, fileDemand{fileIndex: fileIndex, sectors: sectors})
		}
	}
	for files.Len() > 0 {
		f := heap.Pop(files).(fileDemand)
		if runs.Len() == 0 {
			return nil, fmt.Errorf("file data exceeds the picture's gray capacity")
		}
		r := heap.Pop(runs).(freeRun)
		take := f.sectors
		if r.end-r.start < take {
			take = r.end - r.start
		}
		plan.fileExtents[f.fileIndex] = append(plan.fileExtents[f.fileIndex], fileExtent{
			lba: r.start, offset: f.offset, sectors: take, fileIndex: f.fileIndex, filePath: in.filePaths[f.fileIndex],
		})
		plan.placed = append(plan.placed, placedExtent{
			Role: "file", Path: fmt.Sprint(f.fileIndex), Lba: r.start, Sectors: take, Fragment: f.fragment,
		})
		if r.start+take < r.end {
			heap.Push(runs, freeRun{start: r.start + take, end: r.end})
		}
		if take < f.sectors {
			heap.Push(files, fileDemand{
				fileIndex: f.fileIndex, offset: f.offset + int64(take)*2048,
				sectors: f.sectors - take, fragment: f.fragment + 1,
			})
		}
	}
	// Every fragment costs one allocation descriptor (over-long extents split
	// at UdfMaximumExtentBytes exactly like AssignExtents counts them); a UDF
	// file entry holds at most UdfMaxAllocationDescriptors. Report the worst
	// offender so the caller can widen the gray band and re-plan.
	worstIndex, worstDescriptors := -1, 0
	for fileIndex := range in.fileSizes {
		descriptors := 0
		for _, e := range plan.fileExtents[fileIndex] {
			cover := uint64(e.sectors) * 2048
			descriptors += int((cover + iso.UdfMaximumExtentBytes - 1) / iso.UdfMaximumExtentBytes)
		}
		if descriptors > worstDescriptors {
			worstIndex, worstDescriptors = fileIndex, descriptors
		}
	}
	if worstDescriptors > iso.UdfMaxAllocationDescriptors {
		path := in.filePaths[worstIndex]
		if path == "" {
			path = fmt.Sprintf("file #%d", worstIndex)
		}
		return nil, &tooManyExtentsError{path: path, extents: worstDescriptors}
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

// freeRun is a half-open range [start, end) of free, data-eligible sectors.
type freeRun struct{ start, end uint32 }

func collectFreeRuns(used, eligible []bool) []freeRun {
	var runs []freeRun
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
		runs = append(runs, freeRun{start, i})
	}
	return runs
}

// runHeap is a max-heap of free runs by length (ties: lowest start first, so
// the placement is deterministic).
type runHeap []freeRun

func (h runHeap) Len() int { return len(h) }
func (h runHeap) Less(i, j int) bool {
	li, lj := h[i].end-h[i].start, h[j].end-h[j].start
	if li != lj {
		return li > lj
	}
	return h[i].start < h[j].start
}
func (h runHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *runHeap) Push(x any)   { *h = append(*h, x.(freeRun)) }
func (h *runHeap) Pop() any {
	old := *h
	n := len(old)
	r := old[n-1]
	*h = old[:n-1]
	return r
}

// fileDemand is the unplaced remainder of a file's content.
type fileDemand struct {
	fileIndex int
	offset    int64  // byte offset within the file's content
	sectors   uint32 // sectors still to place
	fragment  int    // fragment number of the next placed piece
}

// fileDemandHeap is a max-heap of unplaced file remainders by size (ties:
// lowest file index, then offset — the placement stays deterministic).
type fileDemandHeap []fileDemand

func (h fileDemandHeap) Len() int { return len(h) }
func (h fileDemandHeap) Less(i, j int) bool {
	if h[i].sectors != h[j].sectors {
		return h[i].sectors > h[j].sectors
	}
	if h[i].fileIndex != h[j].fileIndex {
		return h[i].fileIndex < h[j].fileIndex
	}
	return h[i].offset < h[j].offset
}
func (h fileDemandHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *fileDemandHeap) Push(x any)   { *h = append(*h, x.(fileDemand)) }
func (h *fileDemandHeap) Pop() any {
	old := *h
	n := len(old)
	f := old[n-1]
	*h = old[:n-1]
	return f
}
