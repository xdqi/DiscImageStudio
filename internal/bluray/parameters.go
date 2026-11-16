// Package bluray ports DiscImageStudio.BluRay: the BD-R (ISO/IEC 30190)
// module with its geometry, payload mappings, ECC/17PP encoding and the
// bd-info/bd-probe/bd-encode/bd-generate commands.
package bluray

import (
	"fmt"

	"disc-image-studio/internal/trackgeom"
)

// BluRayDiscParameters constants (ISO/IEC 30190:2021 data-zone model).
const (
	BytesPerSector                   = 2048
	SectorsPerCluster                = 32
	PayloadBytesPerCluster           = BytesPerSector * SectorsPerCluster
	SectorsPerLayer           uint32 = 12219392
	FirstLayer0PhysicalSector uint32 = 0x00100000
	// ChannelBitsPerPhysicalCluster is 496 * 1932.
	ChannelBitsPerPhysicalCluster = 496 * 1932
	ChannelBitsPerRecordingUnit   = 2760 + ChannelBitsPerPhysicalCluster + 1104
	ChannelBitLengthNm            = 74.5
	NominalTrackPitchMicrometers  = 0.320
)

// DiscParameters ports the BluRayDiscParameters record. UsableSectors is nil
// when the full geometry is used.
type DiscParameters struct {
	TotalSectors      uint32
	LayerCount        int
	InnerRadiusMm     float64
	OuterRadiusMm     float64
	StartAngleDegrees float64
	PitchLinear       float64
	PitchQuadratic    float64
	PitchCubic        float64
	UsableSectors     *uint32
}

// DefaultDiscParameters returns the C# positional-record defaults.
func DefaultDiscParameters() *DiscParameters {
	return &DiscParameters{
		TotalSectors:  SectorsPerLayer,
		LayerCount:    1,
		InnerRadiusMm: 24.0,
		OuterRadiusMm: 58.0,
	}
}

// OutputSectors is UsableSectors ?? TotalSectors.
func (d *DiscParameters) OutputSectors() uint32 {
	if d.UsableSectors != nil {
		return *d.UsableSectors
	}
	return d.TotalSectors
}

// ContentLength is checked((long)OutputSectors * BytesPerSector).
func (d *DiscParameters) ContentLength() int64 {
	return int64(d.OutputSectors()) * BytesPerSector
}

// OutputOuterRadiusMm mirrors the C# property.
func (d *DiscParameters) OutputOuterRadiusMm() float64 {
	if d.OutputSectors() == d.TotalSectors {
		return d.OuterRadiusMm
	}
	g, err := NewTrackGeometry(d)
	if err != nil {
		return d.OuterRadiusMm // unreachable after Validate
	}
	return g.atSector(float64(d.OutputSectors())).RadiusMm
}

// HasVariablePitch reports whether any pitch coefficient is non-zero.
func (d *DiscParameters) HasVariablePitch() bool {
	return d.PitchLinear != 0 || d.PitchQuadratic != 0 || d.PitchCubic != 0
}

// Clone returns a deep copy (including the UsableSectors pointer).
func (d *DiscParameters) Clone() *DiscParameters {
	c := *d
	if d.UsableSectors != nil {
		v := *d.UsableSectors
		c.UsableSectors = &v
	}
	return &c
}

// Validate ports BluRayDiscParameters.Validate.
func (d *DiscParameters) Validate() error {
	if d.LayerCount != 1 {
		return argOutOfRangeMsg("LayerCount", "Only single-layer 25 GB BD-R is currently supported.")
	}
	if d.TotalSectors == 0 || d.TotalSectors%SectorsPerCluster != 0 || d.TotalSectors > SectorsPerLayer {
		return argOutOfRangeMsg("TotalSectors",
			fmt.Sprintf("BD-R total sectors must be a positive multiple of 32, no greater than %d.", SectorsPerLayer))
	}
	if d.OutputSectors() == 0 || d.OutputSectors() > d.TotalSectors || d.OutputSectors()%SectorsPerCluster != 0 {
		return argOutOfRangeMsg("UsableSectors", "BD-R output sectors must be a positive multiple of 32 within the geometry.")
	}
	if isNotFinite(d.InnerRadiusMm) || isNotFinite(d.OuterRadiusMm) ||
		d.InnerRadiusMm <= 0 || d.OuterRadiusMm <= d.InnerRadiusMm || d.OuterRadiusMm > 60 {
		return argOutOfRangeMsg("OuterRadiusMm", "BD-R radii must satisfy 0 < inner < outer <= 60 mm.")
	}
	if isNotFinite(d.StartAngleDegrees) {
		return argOutOfRange("StartAngleDegrees")
	}
	if d.HasVariablePitch() {
		if err := trackgeom.ValidatePitchCoefficients(d.PitchLinear, d.PitchQuadratic, d.PitchCubic); err != nil {
			return err
		}
	}
	return nil
}

// GetPhysicalSectorNumber ports the nominal data-zone address mapping.
func (d *DiscParameters) GetPhysicalSectorNumber(sector uint32) (uint32, error) {
	if err := d.Validate(); err != nil {
		return 0, err
	}
	if sector >= d.TotalSectors {
		return 0, argOutOfRange("sector")
	}
	return FirstLayer0PhysicalSector + sector, nil
}

// DiscPreset ports BluRayDiscPreset.
type DiscPreset struct {
	ID            string
	DisplayName   string
	LayerCount    int
	Geometry      *DiscParameters
	InvertMapping bool
	IsCustom      bool
	Contributor   string
}

// TotalSectors ports the C# property.
func (p *DiscPreset) TotalSectors() uint32 {
	if p.Geometry != nil {
		return p.Geometry.TotalSectors
	}
	return SectorsPerLayer * uint32(p.LayerCount)
}

// UsableSectors ports the C# property.
func (p *DiscPreset) UsableSectors() *uint32 {
	if p.Geometry != nil {
		return p.Geometry.UsableSectors
	}
	return nil
}

// ResolveUsableSectors ports the static helper.
func ResolveUsableSectors(totalSectors uint32, usableSectors *uint32) uint32 {
	if usableSectors != nil {
		return *usableSectors
	}
	return totalSectors / 32 * 32
}

// ResolvedUsableSectors ports the C# property.
func (p *DiscPreset) ResolvedUsableSectors() uint32 {
	return ResolveUsableSectors(p.TotalSectors(), p.UsableSectors())
}

// ContentLength ports the C# property.
func (p *DiscPreset) ContentLength() int64 {
	return int64(p.ResolvedUsableSectors()) * BytesPerSector
}

// CreateParameters ports the C# factory.
func (p *DiscPreset) CreateParameters() *DiscParameters {
	var d *DiscParameters
	if p.Geometry != nil {
		d = p.Geometry.Clone()
	} else {
		d = DefaultDiscParameters()
		d.TotalSectors = p.TotalSectors()
		d.LayerCount = p.LayerCount
	}
	usable := p.ResolvedUsableSectors()
	d.UsableSectors = &usable
	return d
}

// ManualPreset ports BluRayDiscPreset.Manual.
var ManualPreset = &DiscPreset{ID: "__manual__", DisplayName: "自定义参数（未保存）", LayerCount: 1, IsCustom: true}

// BuiltInPresets ports BluRayDiscPreset.BuiltIn, in order.
var BuiltInPresets = []*DiscPreset{
	{ID: "bd-r-sl-25gb", DisplayName: "BD-R 单层 25 GB", LayerCount: 1},
	{ID: "bd-r-ritek-10x", DisplayName: "铼德 BD-R 10x", LayerCount: 1,
		Geometry: &DiscParameters{TotalSectors: 12219392, LayerCount: 1,
			InnerRadiusMm: 23.98822105616562, OuterRadiusMm: 58.023234731100764, StartAngleDegrees: 0}},
	{ID: "bd-r-ritek-12x", DisplayName: "铼德 BD-R 12x", LayerCount: 1,
		Geometry: &DiscParameters{TotalSectors: 12219392, LayerCount: 1,
			InnerRadiusMm: 23.98795, OuterRadiusMm: 58.02236, StartAngleDegrees: 0},
		Contributor: "DeepAQ"},
	{ID: "bd-r-verbatim-64099", DisplayName: "Verbatim BD-R (64099)", LayerCount: 1,
		Geometry: &DiscParameters{TotalSectors: 12219392, LayerCount: 1,
			InnerRadiusMm: 23.987042157339335, OuterRadiusMm: 57.987960341783236, StartAngleDegrees: 0}},
	{ID: "bd-r-verbatim-64293", DisplayName: "Verbatim BD-R (64293)", LayerCount: 1,
		Geometry: &DiscParameters{TotalSectors: 12219392, LayerCount: 1,
			InnerRadiusMm: 23.987042157339335, OuterRadiusMm: 57.987960341783236, StartAngleDegrees: 0},
		Contributor: "bczhc"},
}

// FindPreset ports BluRayDiscPreset.Find (ordinal-ignore-case).
func FindPreset(id string) (*DiscPreset, error) {
	for _, p := range BuiltInPresets {
		if equalFold(p.ID, id) {
			return p, nil
		}
	}
	return nil, argErr(fmt.Sprintf("Unknown BD-R preset '%s'.", id), "id")
}
