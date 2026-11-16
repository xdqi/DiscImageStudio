package dvd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"disc-image-studio/internal/iso"
)

// This file implements the dispersed 3-tone hybrid: the picture decides where
// the data goes (mid-tone areas and flat interiors render gray = data), and
// UDF's fragmentable extents pour the files into those sectors.

// solveDispersedUdf ports the new `solve --filesystem udf|udf-bridge` path.
func solveDispersedUdf(arguments *cliArguments) error {
	imagePath, err := arguments.Require("image")
	if err != nil {
		return err
	}
	isoOutputPath, err := arguments.Require("iso-output")
	if err != nil {
		return err
	}
	dataDir, err := arguments.Require("data-dir")
	if err != nil {
		return err
	}
	filesystem := strings.ToLower(arguments.Get("filesystem", "udf"))
	bridge := filesystem == "udf-bridge"
	if filesystem != "udf" && !bridge {
		return newArgumentError("--filesystem must be udf or udf-bridge.")
	}
	var bootImagePath, legacyBootImagePath string
	if bridge {
		bootImagePath = arguments.Get("boot-image", "")
		legacyBootImagePath = arguments.Get("legacy-boot-image", "")
		if bootImagePath == "" && legacyBootImagePath == "" {
			return newArgumentError("--filesystem udf-bridge requires --boot-image or --legacy-boot-image.")
		}
	} else if arguments.Has("boot-image") || arguments.Has("legacy-boot-image") {
		return newArgumentError("--boot-image and --legacy-boot-image require --filesystem udf-bridge.")
	}
	exposePaths := arguments.GetAll("expose")
	if len(exposePaths) > 0 && !bridge {
		return newArgumentError("--expose requires --filesystem udf-bridge.")
	}
	volumeLabel := arguments.Get("volume-label", "DVD_IMAGE")

	imageMapping, err := readImageMappingOptions(arguments, 1)
	if err != nil {
		return err
	}
	if imageMapping.TotalSectors == 0 || imageMapping.TotalSectors%1 != 0 {
		return newArgumentError("Dispersed UDF output requires --total-sectors.")
	}
	if imageMapping.TotalSectors%dvdEccSectorCount != 0 {
		return newArgumentError("Dispersed UDF output requires a 16-sector aligned --total-sectors.")
	}

	// 3-tone mapper options.
	band, err := readGrayBand(arguments)
	if err != nil {
		return err
	}
	marginWhite, err := arguments.GetDouble("interior-margin", 0.35)
	if err != nil {
		return err
	}
	marginBlack, err := arguments.GetDouble("interior-margin-black", 0.5)
	if err != nil {
		return err
	}
	headroom, err := arguments.GetDouble("data-headroom", 1.10)
	if err != nil {
		return err
	}

	image, err := rasterImageLoad(imagePath)
	if err != nil {
		return err
	}

	fmt.Println("mapping picture tones…")
	toneMap, err := buildDisperseToneMap(image, imageMapping)
	if err != nil {
		return err
	}

	// The boot image (bridge mode) is a file inside the data directory, named
	// by its image-root-relative path; it is placed contiguously and shared
	// between the El Torito catalog and its UDF file entry — no extra copy.
	vrsStart := uint32(16)
	if bridge {
		vrsStart = 20
	}
	udfPlan, err := iso.PrepareUdfDispersed(dataDir, imageMapping.TotalSectors, volumeLabel, vrsStart, nil)
	if err != nil {
		return err
	}
	bootImageIndex, legacyBootImageIndex := -1, -1
	var exposed []iso.ExposedFile
	var exposedIndexes []int
	var extraDirs []string
	if bridge {
		if bootImagePath != "" {
			index, found := udfPlan.FindFile(bootImagePath)
			if !found {
				return newArgumentError(fmt.Sprintf("--boot-image must name a regular file inside the data directory (path relative to the image root): %s", bootImagePath))
			}
			bootImageIndex = index
		}
		if legacyBootImagePath != "" {
			index, found := udfPlan.FindFile(legacyBootImagePath)
			if !found {
				return newArgumentError(fmt.Sprintf("--legacy-boot-image must name a regular file inside the data directory (path relative to the image root): %s", legacyBootImagePath))
			}
			legacyBootImageIndex = index
		}
		// --expose: a regular file, or a directory expanded recursively.
		// Overlapping exposures are deduplicated by file index.
		seen := map[int]bool{}
		for _, raw := range exposePaths {
			files, dirs, ok := udfPlan.WalkExposure(strings.ReplaceAll(raw, "\\", "/"))
			if !ok {
				return newArgumentError(fmt.Sprintf("--expose must name a regular file or directory inside the data directory (path relative to the image root): %s", raw))
			}
			extraDirs = append(extraDirs, dirs...)
			for _, file := range files {
				if seen[file.Index] {
					continue
				}
				// A boot image is already listed at its image path on the
				// ISO9660 side; exposing it needs no second record.
				if file.Index == bootImageIndex || file.Index == legacyBootImageIndex {
					continue
				}
				seen[file.Index] = true
				exposed = append(exposed, iso.ExposedFile{Path: file.Path, Size: udfPlan.FileSize(file.Index)})
				exposedIndexes = append(exposedIndexes, file.Index)
			}
		}
	}

	// The El Torito / ISO9660 layer (bridge only) is built before planning:
	// its tree fixes the metadata sectors to reserve.
	metaEnd := udfPlan.MetaEndLbaExclusive()
	totalSectors := imageMapping.TotalSectors
	var bridgeLayer *iso.ElToritoBridge
	var bootImageSize, legacyBootImageSize int64
	if bridge {
		if bootImageIndex >= 0 {
			bootImageSize = udfPlan.FileSize(bootImageIndex)
		}
		if legacyBootImageIndex >= 0 {
			legacyBootImageSize = udfPlan.FileSize(legacyBootImageIndex)
		}
		bridgeLayer, err = iso.NewElToritoBridge(totalSectors, volumeLabel,
			strings.ReplaceAll(bootImagePath, "\\", "/"), bootImageSize,
			strings.ReplaceAll(legacyBootImagePath, "\\", "/"), legacyBootImageSize,
			exposed, extraDirs)
		if err != nil {
			return err
		}
	}

	// Fixed constraints: the filesystem metadata that cannot move. The whole
	// metadata region [0, metaEnd) and the reserve tail [N-257, N) are excluded
	// from placement via the reserved ranges; this list is the sidecar report.
	u32 := func(v uint32) *uint32 { return &v }
	fixed := []isoConstraint{
		{role: "udf-vrs", length: 3, fixedAt: u32(vrsStart)},
		{role: "udf-main-vds", length: 16, fixedAt: u32(32)},
		{role: "udf-integrity", length: 2, fixedAt: u32(48)},
		{role: "udf-anchor", length: 1, fixedAt: u32(256)},
		{role: "udf-partition-meta", length: metaEnd - 288, fixedAt: u32(288)},
		{role: "udf-reserve-anchor", length: 1, fixedAt: u32(totalSectors - 257)},
		{role: "udf-reserve-vds", length: 16, fixedAt: u32(totalSectors - 256)},
		{role: "udf-anchor", length: 1, fixedAt: u32(totalSectors - 1)},
	}
	if bridge {
		for _, extent := range bridgeLayer.MetadataExtents() {
			fixed = append(fixed, isoConstraint{role: extent.Role, length: 1, fixedAt: u32(extent.Lba)})
		}
	}

	// Planner: the boot images and every exposed file are continuous demands
	// linked to their tree files; everything else is aggregate file data.
	var continuous []continuousDemand
	if bridge {
		// The x86 legacy BIOS image first: it is the catalog's default entry.
		if legacyBootImageIndex >= 0 {
			continuous = append(continuous, continuousDemand{
				Role: "legacy-boot-image", Path: filepath.ToSlash(legacyBootImagePath),
				Sectors:   uint32((legacyBootImageSize + 2047) / 2048),
				FileIndex: legacyBootImageIndex,
			})
		}
		if bootImageIndex >= 0 && bootImageIndex != legacyBootImageIndex {
			continuous = append(continuous, continuousDemand{
				Role: "boot-image", Path: filepath.ToSlash(bootImagePath),
				Sectors:   uint32((bootImageSize + 2047) / 2048),
				FileIndex: bootImageIndex,
			})
		}
		for i := range exposed {
			if exposed[i].Size == 0 {
				continue // an empty file has no extent (ISO9660 record LBA 0)
			}
			continuous = append(continuous, continuousDemand{
				Role: "iso-expose", Path: exposed[i].Path,
				Sectors:   uint32((exposed[i].Size + 2047) / 2048),
				FileIndex: exposedIndexes[i],
			})
		}
	}
	fileCount := udfPlan.FileCount()
	fileSizes := make([]int64, fileCount)
	filePaths := make([]string, fileCount)
	for i := 0; i < fileCount; i++ {
		fileSizes[i] = udfPlan.FileSize(i)
		filePaths[i] = udfPlan.FileSourcePath(i)
	}

	plan, err := planDispersion(dispersePlannerInputs{
		totalSectors:   totalSectors,
		fixed:          toDataConstraints(fixed),
		continuous:     continuous,
		fileSizes:      fileSizes,
		filePaths:      filePaths,
		headroom:       headroom,
		grayBand:       band,
		marginWhite:    marginWhite,
		marginBlack:    marginBlack,
		toneMap:        toneMap,
		reservedBefore: metaEnd,
		reservedAfter:  totalSectors - 257,
	})
	if err != nil {
		return err
	}

	// Finalize the UDF plan with the assigned extents.
	udfExtents := make(map[int][]iso.UdfFileExtent)
	for fileIndex, extents := range plan.fileExtents {
		list := make([]iso.UdfFileExtent, len(extents))
		for i, e := range extents {
			list[i] = iso.UdfFileExtent{FileIndex: fileIndex, Offset: e.offset, Lba: e.lba, Sectors: e.sectors}
		}
		udfExtents[fileIndex] = list
	}
	if err := udfPlan.AssignExtents(udfExtents); err != nil {
		return err
	}

	// The El Torito / ISO9660 layer gets the planner-assigned LBAs.
	var bootLba, legacyLba uint32
	if bridge {
		byRole := map[string]uint32{}
		for _, p := range plan.placed {
			byRole[p.Role+"/"+p.Path] = p.Lba
		}
		if legacyBootImageIndex >= 0 {
			legacyLba = byRole["legacy-boot-image/"+filepath.ToSlash(legacyBootImagePath)]
		}
		if bootImageIndex >= 0 {
			bootLba = byRole["boot-image/"+filepath.ToSlash(bootImagePath)]
			if bootImageIndex == legacyBootImageIndex {
				bootLba = legacyLba
			}
		}
		exposedLbas := make([]uint32, len(exposed))
		for i := range exposed {
			if exposed[i].Size == 0 {
				continue // empty file: no extent (ISO9660 record LBA 0)
			}
			lba, ok := byRole["iso-expose/"+exposed[i].Path]
			if !ok {
				return fmt.Errorf("internal: no placement for exposed file %s", exposed[i].Path)
			}
			exposedLbas[i] = lba
		}
		if err := bridgeLayer.SetPlacements(bootLba, legacyLba, exposedLbas); err != nil {
			return err
		}
	}

	// The interleaved writer: picture everywhere except data sectors.
	source := &dispersedSectorSource{
		descriptorAt: func(lba uint32) ([]byte, bool) {
			if bridgeLayer != nil {
				if data, ok, err := bridgeLayer.SectorContent(lba); err != nil {
					panic(err)
				} else if ok {
					return data, true
				}
			}
			data, ok := udfPlan.DescriptorAt(lba)
			return data, ok
		},
		gray:       plan.eligible,
		grayBefore: metaEnd,
		grayFrom:   totalSectors - 257,
		readFileContent: func(fileIndex int, offset int64, buf []byte) error {
			content, ok, err := udfPlan.FileContent(fileIndex)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("internal: no content for file %d", fileIndex)
			}
			copy(buf, content[offset:])
			return nil
		},
	}
	// The legacy BIOS boot image gets its Boot Information Table patched into
	// bytes 8..55 of its first sector (both UDF and the catalog share the
	// extent, exactly like mkisofs does). The checksum covers the whole file,
	// so the content is read once here.
	if bridge && legacyBootImageIndex >= 0 {
		content, ok, err := udfPlan.FileContent(legacyBootImageIndex)
		if err != nil {
			return err
		}
		if !ok {
			content, err = os.ReadFile(udfPlan.FileSourcePath(legacyBootImageIndex))
			if err != nil {
				return err
			}
		}
		source.bootInfo = &bootInfoTablePatch{
			fileIndex: legacyBootImageIndex,
			lba:       legacyLba,
			size:      legacyBootImageSize,
			csum:      iso.BootInfoTableChecksum(content),
		}
	}
	var extents []fileExtent
	for _, list := range plan.fileExtents {
		extents = append(extents, list...)
	}
	sortFileExtents(extents)
	source.fileExtents = extents

	options := multiBlockSolveOptions{
		StartLba:              0,
		FillSectors:           totalSectors,
		PsnOffset:             0x30000,
		IterationsPerBlock:    1,
		MutationBytes:         4,
		RandomSeed:            1,
		InitialTemperature:    64.0,
		FinalTemperature:      0.25,
		ZeroInitialPayload:    false,
		ImageMapping:          imageMapping,
		Algorithm:             solverAlgorithmDispersion,
		FastParallelism:       0,
		UseSampleReuse:        true,
		PreserveExistingIso:   false,
		ReadCompatibility:     false,
		ReadCompatibilityMode: dvdReadCompatibilityCurrent,
	}

	if err := ctxCancelled(context.Background()); err != nil {
		return err
	}
	if err := ensureParentDirectory(isoOutputPath); err != nil {
		return err
	}
	output, err := os.Create(isoOutputPath)
	if err != nil {
		return err
	}
	sampler, err := newPayloadImageSamplerFromImage(image, imageMapping)
	if err != nil {
		output.Close()
		return err
	}
	progressEvery := maxInt(1, int(options.FillSectors/dvdEccSectorCount)/100)
	summary, err := writeDispersedIso(sampler, output, options, source, func(p fastDvdStreamProgress) {
		if p.CompletedBlocks == 1 || p.CompletedBlocks == p.TotalBlocks || p.CompletedBlocks%progressEvery == 0 {
			fmt.Printf("block %d/%d, LBA=%d, elapsed=%.1fs\n", p.CompletedBlocks, p.TotalBlocks, p.BlockLba, p.Elapsed.Seconds())
		}
	}, context.Background())
	closeErr := output.Close()
	source.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Println()

	resolvedBand := [2]int{int(plan.band[0]), int(plan.band[1])}
	if err := writeDisperseSidecar(isoOutputPath, plan, udfPlan, bridgeLayer != nil, resolvedBand, marginWhite, marginBlack); err != nil {
		return err
	}
	fmt.Printf("done: dispersed UDF %s, files=%d, gray band=[%d,%d], output=%s\n",
		filesystem, udfPlan.FileCount(), plan.band[0], plan.band[1], isoOutputPath)
	_ = summary
	return nil
}

// isoConstraint is the local constraint record before conversion.
type isoConstraint struct {
	role    string
	length  uint32
	fixedAt *uint32
}

func toDataConstraints(in []isoConstraint) []dataConstraint {
	out := make([]dataConstraint, len(in))
	for i, c := range in {
		out[i] = dataConstraint{Role: c.role, Length: c.length, FixedAt: c.fixedAt, Continuous: false}
	}
	return out
}

func readGrayBand(arguments *cliArguments) ([2]int, error) {
	none := [2]int{-1, -1}
	if !arguments.Has("gray-band") {
		return none, nil
	}
	value := arguments.Get("gray-band", "")
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return none, newArgumentError("--gray-band must be T1:T2 (e.g. 110:150).")
	}
	var t1, t2 int
	if _, err := fmt.Sscanf(parts[0], "%d", &t1); err != nil {
		return none, newArgumentError("--gray-band must be T1:T2 (e.g. 110:150).")
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &t2); err != nil {
		return none, newArgumentError("--gray-band must be T1:T2 (e.g. 110:150).")
	}
	if t1 < 0 || t2 > 255 || t1 > t2 {
		return none, newArgumentError("--gray-band must satisfy 0 <= T1 <= T2 <= 255.")
	}
	return [2]int{t1, t2}, nil
}

func sortFileExtents(extents []fileExtent) {
	for i := 1; i < len(extents); i++ {
		for j := i; j > 0 && extents[j].lba < extents[j-1].lba; j-- {
			extents[j], extents[j-1] = extents[j-1], extents[j]
		}
	}
}

func ctxCancelled(ctx context.Context) error { return ctx.Err() }
