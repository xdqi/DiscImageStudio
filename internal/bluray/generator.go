package bluray

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"disc-image-studio/internal/core"
)

// ImageRequest ports BluRayImageRequest: a full-area BD drawing, optionally
// including an inner or outer UDF filesystem.
type ImageRequest struct {
	SourceImage      string
	OutputPath       string
	Parameters       *DiscParameters
	Tone             core.ToneProfile
	InvertMapping    bool
	DataDirectory    string // "" represents null
	VolumeLabel      string
	FilesAtOuterRing bool
}

// ImageGenerator ports BluRayImageGenerator.
type ImageGenerator struct {
	imageLoader ImageLoaderFunc
}

// NewImageGenerator ports the C# constructor.
func NewImageGenerator(imageLoader ImageLoaderFunc) (*ImageGenerator, error) {
	if imageLoader == nil {
		return nil, argNull("imageLoader")
	}
	return &ImageGenerator{imageLoader: imageLoader}, nil
}

// Generate writes the image and recovery sidecar, preserving errors for the
// host.
func (g *ImageGenerator) Generate(request *ImageRequest, progress core.ProgressFunc,
	ctx context.Context) (core.DiscImageResult, error) {
	if err := ctx.Err(); err != nil {
		return core.DiscImageResult{}, err
	}
	plan, err := PrepareImage(request)
	if err != nil {
		return core.DiscImageResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.DiscImageResult{}, err
	}
	rasterImage, err := g.imageLoader(request.SourceImage, ctx)
	if err != nil {
		return core.DiscImageResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.DiscImageResult{}, err
	}
	rasterImage, err = ApplyToneMapping(rasterImage, request.Tone, ctx)
	if err != nil {
		return core.DiscImageResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.DiscImageResult{}, err
	}
	mapping, err := NewImagePayloadMapping(request.Parameters, rasterImage,
		NewRunLengthMapping(request.InvertMapping))
	if err != nil {
		return core.DiscImageResult{}, err
	}
	nextProgressReport := time.Duration(0)
	report := func(value StreamingProgress) {
		if value.Elapsed < nextProgressReport && value.CompletedBytes != value.TotalBytes {
			return
		}
		nextProgressReport = value.Elapsed + 250*time.Millisecond
		if progress != nil {
			progress(core.DiscJobProgress{
				ModuleID: ModuleID, Stage: "iso",
				Message:  fmt.Sprintf("BD %d/%d bytes", value.CompletedBytes, value.TotalBytes),
				Fraction: value.Fraction(), HasFraction: true,
				CompletedUnits: value.CompletedBytes, TotalUnits: value.TotalBytes, HasUnits: true,
			})
		}
	}
	summary, err := GenerateIsoFile(request.OutputPath, plan, mapping, report, ctx)
	if err != nil {
		return core.DiscImageResult{}, err
	}
	metadata, err := IsoMetadataFromPlan(plan, request.SourceImage, request.OutputPath,
		request.InvertMapping, request.Tone, time.Now())
	if err != nil {
		return core.DiscImageResult{}, err
	}
	if err := metadata.Save(request.OutputPath); err != nil {
		return core.DiscImageResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.DiscImageResult{}, err
	}
	interpretation := "dark-to-3T / light-to-6T run-count bias"
	if request.InvertMapping {
		interpretation = "dark-to-6T / light-to-3T run-count bias"
	}
	filesystem := "full-area drawing, no filesystem"
	if plan.HasFileSystem() {
		filesystem = "UDF 2.01"
	}
	placement := "inner"
	if plan.FilesAtOuterRing() {
		placement = "outer"
	}
	full, err := filepath.Abs(request.OutputPath)
	if err != nil {
		return core.DiscImageResult{}, err
	}
	return core.DiscImageResult{
		OutputPath: full,
		Summary: fmt.Sprintf("BD %s: %d bytes, %d sectors; %d %s files; drawing sectors %d..%d; %s.",
			filesystem, summary.BytesWritten, request.Parameters.OutputSectors(), plan.FileCount(),
			placement, plan.DrawingStartSector(), plan.DrawingEndSectorExclusive()-1, interpretation),
	}, nil
}

// PrepareImage ports BluRayImageGenerator.Prepare.
func PrepareImage(request *ImageRequest) (*IsoPlan, error) {
	if request == nil {
		return nil, argNull("request")
	}
	if request.Parameters == nil {
		return nil, argNull("request.Parameters")
	}
	if isNullOrWhiteSpace(request.SourceImage) {
		return nil, argNullOrWhiteSpace("request.SourceImage")
	}
	if isNullOrWhiteSpace(request.OutputPath) {
		return nil, argNullOrWhiteSpace("request.OutputPath")
	}
	sourceFull, err := filepath.Abs(request.SourceImage)
	if err != nil {
		return nil, err
	}
	outputFull, err := filepath.Abs(request.OutputPath)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(sourceFull, outputFull) {
		return nil, errors.New("Input and output paths must be different.")
	}
	if err := request.Parameters.Validate(); err != nil {
		return nil, err
	}
	if err := request.Tone.Validate("tone"); err != nil {
		return nil, err
	}
	if request.Tone.Levels != core.TwoLevels {
		return nil, argErr("BD image generation accepts only two tone levels.", "request")
	}
	plan, err := PrepareDrawing(request.Parameters, request.DataDirectory,
		request.VolumeLabel, request.FilesAtOuterRing)
	if err != nil {
		return nil, err
	}
	if err := ValidateIsoOutputPath(request.OutputPath, plan); err != nil {
		return nil, err
	}
	return plan, nil
}
