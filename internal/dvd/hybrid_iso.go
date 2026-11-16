package dvd

import (
	"context"
	"os"
	"path/filepath"

	"disc-image-studio/internal/iso"
)

// hybridIsoWriteSummary ports HybridIsoWriteSummary.
type hybridIsoWriteSummary struct {
	Mode                    string
	VolumeSectors           uint32
	LogicalSectorBytes      int
	SourceDirectory         string
	FileCount               int
	DirectoryCount          int
	FileBytes               int64
	DataEndLbaExclusive     uint32
	SparseFile              bool
	Iso9660                 bool
	Joliet                  bool
	FilesAtOuterRing        bool
	MetadataEndLbaExclusive uint32
	FileDataStartLba        uint32
}

// hybridIsoPlan ports HybridIsoImageWriter.Plan.
func hybridIsoPlan(sourceDirectory string, volumeSectors uint32, volumeLabel string, filesAtOuterRing bool) (*iso.IsoLayoutPlan, error) {
	return iso.PlanIso(sourceDirectory, volumeSectors, volumeLabel, filesAtOuterRing)
}

// hybridIsoCreate ports HybridIsoImageWriter.Create.
func hybridIsoCreate(plan *iso.IsoLayoutPlan, outputPath string, ctx context.Context) (*hybridIsoWriteSummary, error) {
	sourceFullPath := plan.SourceDirectory()
	volumeSectors := plan.VolumeSectors()
	outputFullPath := fullPath(outputPath)
	if iso.IsWithinDirectory(outputFullPath, sourceFullPath) {
		return nil, newArgumentError("--iso-output must be outside --data-dir so the generated image cannot include itself.")
	}
	outputDirectory := filepath.Dir(outputFullPath)
	if outputDirectory == "" {
		return nil, newArgumentErrorParam("ISO output path has no parent directory.", "outputPath")
	}
	if err := os.MkdirAll(outputDirectory, 0o777); err != nil {
		return nil, err
	}
	temporaryPath := filepath.Join(outputDirectory, "."+filepath.Base(outputFullPath)+"."+newGuidN()+".tmp")
	sparse := false
	committed := false
	defer func() {
		// Cleanup must not replace the original write or cancellation exception.
		if !committed {
			func() {
				defer func() { recover() }()
				if _, err := os.Stat(temporaryPath); err == nil {
					os.Remove(temporaryPath)
				}
			}()
		}
	}()
	{
		output, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o666)
		if err != nil {
			return nil, err
		}
		sparse = isoTryMarkSparse()
		err = output.Truncate(int64(volumeSectors) * isoLogicalSectorBytes)
		if err == nil {
			if plan.FilesAtOuterRing() {
				err = iso.WriteIsoOuterRingPrefix(plan, output, plan.MetadataEndLbaExclusive(), ctx)
				if err == nil {
					_, err = output.Seek(int64(plan.FileDataStartLba())*isoLogicalSectorBytes, 0)
				}
				if err == nil {
					err = iso.WriteIsoOuterRingSuffix(plan, output, plan.FileDataStartLba(), ctx)
				}
			} else {
				err = iso.WriteIsoSequentialPrefix(plan, output, plan.DataEndLbaExclusive(), ctx)
			}
		}
		if err == nil {
			err = output.Sync()
		}
		closeErr := output.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if err := os.Rename(temporaryPath, outputFullPath); err != nil {
		return nil, err
	}
	committed = true
	return &hybridIsoWriteSummary{
		Mode:                    "hybrid-iso9660-joliet",
		VolumeSectors:           volumeSectors,
		LogicalSectorBytes:      isoLogicalSectorBytes,
		SourceDirectory:         sourceFullPath,
		FileCount:               plan.FileCount(),
		DirectoryCount:          plan.DirectoryCount(),
		FileBytes:               plan.FileBytes(),
		DataEndLbaExclusive:     plan.DataEndLbaExclusive(),
		SparseFile:              sparse,
		Iso9660:                 true,
		Joliet:                  true,
		FilesAtOuterRing:        plan.FilesAtOuterRing(),
		MetadataEndLbaExclusive: plan.MetadataEndLbaExclusive(),
		FileDataStartLba:        plan.FileDataStartLba(),
	}, nil
}
