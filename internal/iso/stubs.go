package iso

import (
	"context"
	"io"
)

// The functions below are the pinned API. Their bodies are implemented in
// iso9660.go and udf.go.

// PlanIso ports Iso9660ImageWriter.Plan. An empty sourceDirectory means a
// volume without files.
func PlanIso(sourceDirectory string, volumeSectors uint32, volumeLabel string, filesAtOuterRing bool) (*IsoLayoutPlan, error) {
	return planIso(sourceDirectory, volumeSectors, volumeLabel, filesAtOuterRing)
}

// WriteIsoSequentialPrefix ports Iso9660ImageWriter.WriteSequentialPrefix.
func WriteIsoSequentialPrefix(plan *IsoLayoutPlan, output io.Writer, endLbaExclusive uint32, ctx context.Context) error {
	return writeIsoSequentialPrefix(plan, output, endLbaExclusive, ctx)
}

// WriteIsoOuterRingPrefix ports Iso9660ImageWriter.WriteOuterRingPrefix.
func WriteIsoOuterRingPrefix(plan *IsoLayoutPlan, output io.Writer, endLbaExclusive uint32, ctx context.Context) error {
	return writeIsoOuterRingPrefix(plan, output, endLbaExclusive, ctx)
}

// WriteIsoOuterRingSuffix ports Iso9660ImageWriter.WriteOuterRingSuffix.
func WriteIsoOuterRingSuffix(plan *IsoLayoutPlan, output io.Writer, startLba uint32, ctx context.Context) error {
	return writeIsoOuterRingSuffix(plan, output, startLba, ctx)
}

// IsWithinDirectory ports Iso9660ImageWriter.IsWithinDirectory.
func IsWithinDirectory(path, directory string) bool {
	return isWithinDirectory(path, directory)
}

// PlanUdf ports UdfImageWriter.Plan.
func PlanUdf(sourceDirectory string, volumeSectors uint32, volumeLabel string, filesAtOuterRing bool) (*UdfLayoutPlan, error) {
	return planUdf(sourceDirectory, volumeSectors, volumeLabel, filesAtOuterRing)
}

// WriteUdfSequentialPrefix ports UdfImageWriter.WriteSequentialPrefix.
func WriteUdfSequentialPrefix(plan *UdfLayoutPlan, output io.Writer, endLbaExclusive uint32, ctx context.Context) error {
	return writeUdfSequentialPrefix(plan, output, endLbaExclusive, ctx)
}

// WriteUdfSequentialSuffix ports UdfImageWriter.WriteSequentialSuffix.
func WriteUdfSequentialSuffix(plan *UdfLayoutPlan, output io.Writer, startLba uint32, ctx context.Context) error {
	return writeUdfSequentialSuffix(plan, output, startLba, ctx)
}
