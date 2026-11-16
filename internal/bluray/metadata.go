package bluray

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"disc-image-studio/internal/core"
	"disc-image-studio/internal/trackgeom"
)

// IsoMetadataFormatName ports BluRayIsoMetadata.FormatName.
const IsoMetadataFormatName = "bd-r-image-v1"

// IsoMetadata ports BluRayIsoMetadata: the sidecar next to a generated BD ISO
// carrying the geometry, drawing bounds, polarity and tone needed to reopen
// the produced image without the original project.
type IsoMetadata struct {
	SourceImage               string
	OutputIso                 string
	IsoBytes                  int64
	TotalSectors              uint32
	InnerRadiusMm             float64
	OuterRadiusMm             float64
	InvertMapping             bool
	Tone                      core.ToneProfile
	DrawingStartSector        uint32
	DrawingEndSectorExclusive uint32
	DrawingStartRadiusMm      float64
	DrawingEndRadiusMm        float64
	VolumeLabel               string
	DataDirectory             string // "" represents null
	UdfTimestamp              *time.Time
	GeneratedUtc              time.Time
	FilesAtOuterRing          bool
	PitchLinear               float64
	PitchQuadratic            float64
	PitchCubic                float64
	UsableSectors             *uint32
}

// IsoMetadataFromPlan ports BluRayIsoMetadata.FromPlan.
func IsoMetadataFromPlan(plan *IsoPlan, sourceImage, outputIso string, invertMapping bool,
	tone core.ToneProfile, generatedUtc time.Time) (*IsoMetadata, error) {
	sourceFull, err := filepath.Abs(sourceImage)
	if err != nil {
		return nil, err
	}
	outputFull, err := filepath.Abs(outputIso)
	if err != nil {
		return nil, err
	}
	volumeLabel := plan.VolumeLabel()
	if volumeLabel == "" {
		volumeLabel = "BD_IMAGE"
	}
	return &IsoMetadata{
		SourceImage:               sourceFull,
		OutputIso:                 outputFull,
		IsoBytes:                  plan.ContentLength(),
		TotalSectors:              plan.Disc().TotalSectors,
		InnerRadiusMm:             plan.Disc().InnerRadiusMm,
		OuterRadiusMm:             plan.Disc().OuterRadiusMm,
		InvertMapping:             invertMapping,
		Tone:                      tone.Normalized(),
		DrawingStartSector:        plan.DrawingStartSector(),
		DrawingEndSectorExclusive: plan.DrawingEndSectorExclusive(),
		DrawingStartRadiusMm:      plan.DrawingStartRadiusMm(),
		DrawingEndRadiusMm:        plan.DrawingEndRadiusMm(),
		VolumeLabel:               volumeLabel,
		DataDirectory:             plan.DataDirectory(),
		UdfTimestamp:              plan.UdfTimestamp(),
		GeneratedUtc:              generatedUtc,
		FilesAtOuterRing:          plan.FilesAtOuterRing(),
		PitchLinear:               plan.Disc().PitchLinear,
		PitchQuadratic:            plan.Disc().PitchQuadratic,
		PitchCubic:                plan.Disc().PitchCubic,
		UsableSectors:             plan.Disc().UsableSectors,
	}, nil
}

// OutputSectors is UsableSectors ?? TotalSectors.
func (m *IsoMetadata) OutputSectors() uint32 {
	if m.UsableSectors != nil {
		return *m.UsableSectors
	}
	return m.TotalSectors
}

// ContentLength is checked((long)OutputSectors * BytesPerSector).
func (m *IsoMetadata) ContentLength() int64 {
	return int64(m.OutputSectors()) * BytesPerSector
}

// IsoMetadataPathFor ports BluRayIsoMetadata.PathFor.
func IsoMetadataPathFor(isoPath string) string { return isoPath + ".json" }

// marshalJSON serializes exactly like System.Text.Json with the camelCase
// naming policy and WriteIndented (record positional properties first, the
// "format" property last).
func (m *IsoMetadata) marshalJSON() string {
	w := newJSONWriter()
	w.BeginObject()
	w.Property("sourceImage")
	w.StringValue(m.SourceImage)
	w.Property("outputIso")
	w.StringValue(m.OutputIso)
	w.Property("isoBytes")
	w.Int64Value(m.IsoBytes)
	w.Property("totalSectors")
	w.Uint32Value(m.TotalSectors)
	w.Property("innerRadiusMm")
	w.DoubleValue(m.InnerRadiusMm)
	w.Property("outerRadiusMm")
	w.DoubleValue(m.OuterRadiusMm)
	w.Property("invertMapping")
	w.BoolValue(m.InvertMapping)
	w.Property("tone")
	w.BeginObject()
	w.Property("ditherStrength")
	w.DoubleValue(m.Tone.DitherStrength)
	w.Property("levels")
	w.IntValue(m.Tone.Levels)
	w.Property("brightness")
	w.DoubleValue(m.Tone.Brightness)
	w.Property("srgbLinearization")
	w.BoolValue(m.Tone.SrgbLinearization)
	w.EndObject()
	w.Property("drawingStartSector")
	w.Uint32Value(m.DrawingStartSector)
	w.Property("drawingEndSectorExclusive")
	w.Uint32Value(m.DrawingEndSectorExclusive)
	w.Property("drawingStartRadiusMm")
	w.DoubleValue(m.DrawingStartRadiusMm)
	w.Property("drawingEndRadiusMm")
	w.DoubleValue(m.DrawingEndRadiusMm)
	w.Property("volumeLabel")
	w.StringValue(m.VolumeLabel)
	w.Property("dataDirectory")
	if m.DataDirectory == "" {
		w.Null()
	} else {
		w.StringValue(m.DataDirectory)
	}
	w.Property("udfTimestamp")
	if m.UdfTimestamp == nil {
		w.Null()
	} else {
		w.DateTimeOffsetValue(*m.UdfTimestamp)
	}
	w.Property("generatedUtc")
	w.DateTimeOffsetValue(m.GeneratedUtc)
	w.Property("filesAtOuterRing")
	w.BoolValue(m.FilesAtOuterRing)
	w.Property("pitchLinear")
	w.DoubleValue(m.PitchLinear)
	w.Property("pitchQuadratic")
	w.DoubleValue(m.PitchQuadratic)
	w.Property("pitchCubic")
	w.DoubleValue(m.PitchCubic)
	w.Property("usableSectors")
	if m.UsableSectors == nil {
		w.Null()
	} else {
		w.Uint32Value(*m.UsableSectors)
	}
	w.Property("format")
	w.StringValue(IsoMetadataFormatName)
	w.EndObject()
	return w.String()
}

// Save ports BluRayIsoMetadata.Save: JSON plus a single Environment.NewLine.
func (m *IsoMetadata) Save(isoPath string) error {
	if isNullOrWhiteSpace(isoPath) {
		return argNullOrWhiteSpace("isoPath")
	}
	if err := m.Validate(); err != nil {
		return err
	}
	full, err := filepath.Abs(isoPath)
	if err != nil {
		return err
	}
	path := IsoMetadataPathFor(full)
	return os.WriteFile(path, []byte(m.marshalJSON()+"\n"), 0o666)
}

// Validate ports the private BluRayIsoMetadata.Validate.
func (m *IsoMetadata) Validate() error {
	if m.TotalSectors == 0 || m.TotalSectors > SectorsPerLayer || m.TotalSectors%SectorsPerCluster != 0 {
		return errors.New("BD sidecar sectors must be a 32-aligned single-layer size.")
	}
	if m.OutputSectors() == 0 || m.OutputSectors() > m.TotalSectors || m.OutputSectors()%SectorsPerCluster != 0 {
		return errors.New("BD sidecar output sectors must be a 32-aligned range within the geometry.")
	}
	if isNotFinite(m.InnerRadiusMm) || isNotFinite(m.OuterRadiusMm) ||
		m.InnerRadiusMm <= 0 || m.OuterRadiusMm <= m.InnerRadiusMm || m.OuterRadiusMm > 60 {
		return errors.New("BD sidecar radii are outside the supported data zone.")
	}
	if m.DrawingStartSector >= m.DrawingEndSectorExclusive || m.DrawingEndSectorExclusive > m.OutputSectors() {
		return errors.New("BD sidecar drawing bounds are invalid.")
	}
	if err := m.Tone.Validate("tone"); err != nil {
		return err
	}
	if err := trackgeom.ValidatePitchCoefficients(m.PitchLinear, m.PitchQuadratic, m.PitchCubic); err != nil {
		return errors.New("BD sidecar pitch coefficients are invalid.")
	}
	return nil
}

// TryLoadIsoMetadata ports BluRayIsoMetadata.TryLoad: returns (nil, nil) when
// the sidecar is absent or invalid.
func TryLoadIsoMetadata(isoPath string) (*IsoMetadata, error) {
	if isNullOrWhiteSpace(isoPath) {
		return nil, argNullOrWhiteSpace("isoPath")
	}
	path := IsoMetadataPathFor(isoPath)
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, nil // IOException → null
	}
	metadata, formatOk, err := parseIsoMetadata(content)
	if err != nil {
		return nil, nil
	}
	if !formatOk || metadata.TotalSectors == 0 {
		return nil, nil
	}
	if err := metadata.Validate(); err != nil {
		return nil, nil
	}
	return metadata, nil
}

// isoMetadataJSON mirrors the sidecar's JSON shape for lenient reading
// (case-insensitive keys, trailing commas tolerated).
type isoMetadataJSON struct {
	SourceImage               string            `json:"sourceImage"`
	OutputIso                 string            `json:"outputIso"`
	IsoBytes                  int64             `json:"isoBytes"`
	TotalSectors              uint32            `json:"totalSectors"`
	InnerRadiusMm             float64           `json:"innerRadiusMm"`
	OuterRadiusMm             float64           `json:"outerRadiusMm"`
	InvertMapping             bool              `json:"invertMapping"`
	Tone                      *core.ToneProfile `json:"tone"`
	DrawingStartSector        uint32            `json:"drawingStartSector"`
	DrawingEndSectorExclusive uint32            `json:"drawingEndSectorExclusive"`
	DrawingStartRadiusMm      float64           `json:"drawingStartRadiusMm"`
	DrawingEndRadiusMm        float64           `json:"drawingEndRadiusMm"`
	VolumeLabel               string            `json:"volumeLabel"`
	DataDirectory             *string           `json:"dataDirectory"`
	UdfTimestamp              *time.Time        `json:"udfTimestamp"`
	GeneratedUtc              time.Time         `json:"generatedUtc"`
	FilesAtOuterRing          bool              `json:"filesAtOuterRing"`
	PitchLinear               float64           `json:"pitchLinear"`
	PitchQuadratic            float64           `json:"pitchQuadratic"`
	PitchCubic                float64           `json:"pitchCubic"`
	UsableSectors             *uint32           `json:"usableSectors"`
	Format                    string            `json:"format"`
}

func parseIsoMetadata(content []byte) (*IsoMetadata, bool, error) {
	content = stripJSONTrailingCommas(content)
	var raw isoMetadataJSON
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	if err := decoder.Decode(&raw); err != nil {
		return nil, false, err
	}
	metadata := &IsoMetadata{
		SourceImage:               raw.SourceImage,
		OutputIso:                 raw.OutputIso,
		IsoBytes:                  raw.IsoBytes,
		TotalSectors:              raw.TotalSectors,
		InnerRadiusMm:             raw.InnerRadiusMm,
		OuterRadiusMm:             raw.OuterRadiusMm,
		InvertMapping:             raw.InvertMapping,
		DrawingStartSector:        raw.DrawingStartSector,
		DrawingEndSectorExclusive: raw.DrawingEndSectorExclusive,
		DrawingStartRadiusMm:      raw.DrawingStartRadiusMm,
		DrawingEndRadiusMm:        raw.DrawingEndRadiusMm,
		VolumeLabel:               raw.VolumeLabel,
		GeneratedUtc:              raw.GeneratedUtc,
		FilesAtOuterRing:          raw.FilesAtOuterRing,
		PitchLinear:               raw.PitchLinear,
		PitchQuadratic:            raw.PitchQuadratic,
		PitchCubic:                raw.PitchCubic,
		UsableSectors:             raw.UsableSectors,
		UdfTimestamp:              raw.UdfTimestamp,
	}
	if raw.Tone != nil {
		metadata.Tone = *raw.Tone
	}
	if raw.DataDirectory != nil {
		metadata.DataDirectory = *raw.DataDirectory
	}
	return metadata, raw.Format == IsoMetadataFormatName, nil
}

// stripJSONTrailingCommas tolerates trailing commas like the C# reader
// (AllowTrailingCommas = true).
func stripJSONTrailingCommas(content []byte) []byte {
	out := make([]byte, 0, len(content))
	inString := false
	escaped := false
	for i := 0; i < len(content); i++ {
		c := content[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			// Look ahead past whitespace for } or ].
			j := i + 1
			for j < len(content) && (content[j] == ' ' || content[j] == '\t' || content[j] == '\r' || content[j] == '\n') {
				j++
			}
			if j < len(content) && (content[j] == '}' || content[j] == ']') {
				continue // drop the trailing comma
			}
		}
		out = append(out, c)
	}
	return out
}
