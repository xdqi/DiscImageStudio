package cd

import (
	"context"
	"fmt"
	"strings"

	"disc-image-studio/internal/core"
)

// csharpBool renders a bool like C# bool.ToString (True/False).
func csharpBool(value bool) string {
	if value {
		return "True"
	}
	return "False"
}

// ImageRequest ports CdImageRequest.
type ImageRequest struct {
	SourceImage      string
	OutputPath       string
	Parameters       *DiscParameters
	Tone             core.ToneProfile
	Interleave       bool
	WriteCue         bool
	DataDirectory    string
	VolumeLabel      string
	FilesAtOuterRing bool
}

// GenerateImage ports CdImageGenerator.Generate.
func GenerateImage(ctx context.Context, request ImageRequest, progress core.ProgressFunc) (core.DiscImageResult, error) {
	if request.Parameters == nil {
		return core.DiscImageResult{}, fmt.Errorf("parameters are required")
	}
	if err := ctx.Err(); err != nil {
		return core.DiscImageResult{}, err
	}
	if err := request.Tone.Validate("tone"); err != nil {
		return core.DiscImageResult{}, err
	}
	dataDirectory := strings.TrimSpace(request.DataDirectory)
	if dataDirectory != "" {
		plan, err := PrepareDataLayout(dataDirectory, request.Parameters, request.VolumeLabel, request.FilesAtOuterRing)
		if err != nil {
			return core.DiscImageResult{}, err
		}
		dataSummary, err := GenerateDataImage(ctx, request.SourceImage, request.OutputPath,
			request.Parameters, plan, func(value Progress) {
				if progress != nil {
					progress(core.DiscJobProgress{
						ModuleID:       ModuleID,
						Stage:          "generate",
						Message:        fmt.Sprintf("CD data progress %.1f %%", value.Fraction()*100),
						Fraction:       value.Fraction(),
						HasFraction:    true,
						CompletedUnits: value.CompletedBytes,
						TotalUnits:     value.TotalBytes,
						HasUnits:       true,
					})
				}
			}, request.Tone)
		if err != nil {
			return core.DiscImageResult{}, err
		}
		position := "inner"
		if request.FilesAtOuterRing {
			position = "outer"
		}
		return core.DiscImageResult{
			OutputPath: dataSummary.OutputTrack,
			Summary: fmt.Sprintf("CD data image complete: %d files, %d sectors, files=%s, output=%s",
				plan.FileCount(), plan.TotalSectors(), position, dataSummary.OutputTrack),
		}, nil
	}

	summary, err := GenerateTrack(request.SourceImage, request.OutputPath, request.Parameters,
		request.Interleave, request.WriteCue, func(value Progress) {
			if progress != nil {
				progress(core.DiscJobProgress{
					ModuleID:       ModuleID,
					Stage:          "generate",
					Message:        fmt.Sprintf("CD progress %.1f %%", value.Fraction()*100),
					Fraction:       value.Fraction(),
					HasFraction:    true,
					CompletedUnits: value.CompletedBytes,
					TotalUnits:     value.TotalBytes,
					HasUnits:       true,
				})
			}
		}, ctx, request.Tone)
	if err != nil {
		return core.DiscImageResult{}, err
	}
	message := fmt.Sprintf("CD track complete: %d sectors, %d bytes, interleave=%s, elapsed=%.1fs, output=%s",
		summary.Sectors, summary.BytesWritten, csharpBool(summary.Interleaved), summary.Elapsed.Seconds(), summary.OutputTrack)
	if summary.CueSheetPath != "" {
		message += fmt.Sprintf(", cue=%s", summary.CueSheetPath)
	}
	return core.DiscImageResult{OutputPath: summary.OutputTrack, Summary: message}, nil
}
