package cd

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
)

// TrackMetadata is the sidecar recorded next to a produced track as
// <track>.json. Serialized with camelCase keys, two-space indentation and a
// trailing newline, matching the C# System.Text.Json output on Linux.
type TrackMetadata struct {
	SourceImage               string  `json:"sourceImage"`
	OutputTrack               string  `json:"outputTrack"`
	InnerRadiusMm             float64 `json:"innerRadiusMm"`
	OuterRadiusMm             float64 `json:"outerRadiusMm"`
	Sectors                   int64   `json:"sectors"`
	LinearVelocityMmPerSecond float64 `json:"linearVelocityMmPerSecond"`
	StartAngleDegrees         float64 `json:"startAngleDegrees"`
	ImageOuterRadiusMm        float64 `json:"imageOuterRadiusMm"`
	Interleaved               bool    `json:"interleaved"`
	ToneDitherStrength        float64 `json:"toneDitherStrength"`
	ToneLevels                int     `json:"toneLevels"`
	ToneBrightness            float64 `json:"toneBrightness"`
	ToneSrgbLinearization     bool    `json:"toneSrgbLinearization"`
	PitchLinear               float64 `json:"pitchLinear"`
	PitchQuadratic            float64 `json:"pitchQuadratic"`
	PitchCubic                float64 `json:"pitchCubic"`
	UsableSectors             int64   `json:"usableSectors"`
	SourceImageOuterRadiusMm  float64 `json:"sourceImageOuterRadiusMm"`
	Format                    string  `json:"format"`

	HasUsableSectors          bool `json:"-"`
	HasSourceImageOuterRadius bool `json:"-"`
}

// trackMetadataJSON is the serialized shape; nullable fields become null.
type trackMetadataJSON struct {
	SourceImage               string   `json:"sourceImage"`
	OutputTrack               string   `json:"outputTrack"`
	InnerRadiusMm             float64  `json:"innerRadiusMm"`
	OuterRadiusMm             float64  `json:"outerRadiusMm"`
	Sectors                   int64    `json:"sectors"`
	LinearVelocityMmPerSecond float64  `json:"linearVelocityMmPerSecond"`
	StartAngleDegrees         float64  `json:"startAngleDegrees"`
	ImageOuterRadiusMm        float64  `json:"imageOuterRadiusMm"`
	Interleaved               bool     `json:"interleaved"`
	ToneDitherStrength        float64  `json:"toneDitherStrength"`
	ToneLevels                int      `json:"toneLevels"`
	ToneBrightness            float64  `json:"toneBrightness"`
	ToneSrgbLinearization     bool     `json:"toneSrgbLinearization"`
	PitchLinear               float64  `json:"pitchLinear"`
	PitchQuadratic            float64  `json:"pitchQuadratic"`
	PitchCubic                float64  `json:"pitchCubic"`
	UsableSectors             *int64   `json:"usableSectors"`
	SourceImageOuterRadiusMm  *float64 `json:"sourceImageOuterRadiusMm"`
	Format                    string   `json:"format"`
}

// TrackMetadataFormatName is the serialized file marker.
const TrackMetadataFormatName = "cd-da-track-v1"

// CreateParameters rebuilds the generation parameters.
func (m TrackMetadata) CreateParameters() (*DiscParameters, error) {
	return NewDiscParameters(
		m.InnerRadiusMm, m.OuterRadiusMm, m.Sectors, m.LinearVelocityMmPerSecond,
		m.StartAngleDegrees*math.Pi/180.0, m.ImageOuterRadiusMm,
		m.PitchLinear, m.PitchQuadratic, m.PitchCubic,
		m.UsableSectors, m.HasUsableSectors,
		m.SourceImageOuterRadiusMm, m.HasSourceImageOuterRadius)
}

// TryLoadTrackMetadata reads the sidecar of a track, returning nil when it is
// absent or unreadable.
func TryLoadTrackMetadata(trackPath string) *TrackMetadata {
	path := trackPath + ".json"
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw trackMetadataJSON
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return nil
	}
	if raw.Format != TrackMetadataFormatName || raw.Sectors <= 0 {
		return nil
	}
	metadata := &TrackMetadata{
		SourceImage:               raw.SourceImage,
		OutputTrack:               raw.OutputTrack,
		InnerRadiusMm:             raw.InnerRadiusMm,
		OuterRadiusMm:             raw.OuterRadiusMm,
		Sectors:                   raw.Sectors,
		LinearVelocityMmPerSecond: raw.LinearVelocityMmPerSecond,
		StartAngleDegrees:         raw.StartAngleDegrees,
		ImageOuterRadiusMm:        raw.ImageOuterRadiusMm,
		Interleaved:               raw.Interleaved,
		ToneDitherStrength:        raw.ToneDitherStrength,
		ToneLevels:                raw.ToneLevels,
		ToneBrightness:            raw.ToneBrightness,
		ToneSrgbLinearization:     raw.ToneSrgbLinearization,
		PitchLinear:               raw.PitchLinear,
		PitchQuadratic:            raw.PitchQuadratic,
		PitchCubic:                raw.PitchCubic,
		Format:                    raw.Format,
	}
	if raw.UsableSectors != nil {
		metadata.UsableSectors = *raw.UsableSectors
		metadata.HasUsableSectors = true
	}
	if raw.SourceImageOuterRadiusMm != nil {
		metadata.SourceImageOuterRadiusMm = *raw.SourceImageOuterRadiusMm
		metadata.HasSourceImageOuterRadius = true
	}
	return metadata
}

// SaveTrackMetadata writes the sidecar next to the track.
func SaveTrackMetadata(trackPath string, metadata TrackMetadata) error {
	raw := trackMetadataJSON{
		SourceImage:               metadata.SourceImage,
		OutputTrack:               metadata.OutputTrack,
		InnerRadiusMm:             metadata.InnerRadiusMm,
		OuterRadiusMm:             metadata.OuterRadiusMm,
		Sectors:                   metadata.Sectors,
		LinearVelocityMmPerSecond: metadata.LinearVelocityMmPerSecond,
		StartAngleDegrees:         metadata.StartAngleDegrees,
		ImageOuterRadiusMm:        metadata.ImageOuterRadiusMm,
		Interleaved:               metadata.Interleaved,
		ToneDitherStrength:        metadata.ToneDitherStrength,
		ToneLevels:                metadata.ToneLevels,
		ToneBrightness:            metadata.ToneBrightness,
		ToneSrgbLinearization:     metadata.ToneSrgbLinearization,
		PitchLinear:               metadata.PitchLinear,
		PitchQuadratic:            metadata.PitchQuadratic,
		PitchCubic:                metadata.PitchCubic,
		Format:                    TrackMetadataFormatName,
	}
	if metadata.HasUsableSectors {
		raw.UsableSectors = &metadata.UsableSectors
	}
	if metadata.HasSourceImageOuterRadius {
		raw.SourceImageOuterRadiusMm = &metadata.SourceImageOuterRadiusMm
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(raw); err != nil {
		return err
	}
	return os.WriteFile(trackPath+".json", buffer.Bytes(), 0o644)
}
