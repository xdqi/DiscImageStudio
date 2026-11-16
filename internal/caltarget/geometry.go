// Package caltarget generates the calibration target discs (coarse, boundary,
// fine) from the embedded or analytically rendered patterns, so users do not
// need to provide a source image. It ports the pattern half of
// DiscImageStudio.Core/Calibration; the measurement half runs in the browser
// (see internal/webui).
package caltarget

import (
	"fmt"
	"math"

	"disc-image-studio/internal/trackgeom"
)

// DiscKind mirrors CalibrationDiscKind.
type DiscKind int

const (
	CD DiscKind = iota
	DVD
	BluRay
)

// Stage mirrors the current-schema calibration stages.
type Stage int

const (
	// StageCoarse is the v13 wide-band + angle-sweep target.
	StageCoarse Stage = iota
	// StageBoundary is the v16 wavy-boundary target.
	StageBoundary
	// StageFine is the v14 reference rings + cross target.
	StageFine
)

// Parameters ports the geometry-relevant part of CalibrationParameters.
type Parameters struct {
	Kind           DiscKind
	InnerRadiusMm  float64
	OuterRadiusMm  float64
	Sectors        int64
	LinearDensity  float64 // CD: mm/s (1200); DVD: channel-bit nm (133.3); BD: nm (74.5)
	PitchLinear    float64
	PitchQuadratic float64
	PitchCubic     float64
	UsableSectors  int64 // 0 = unset
}

// DVD/BD physical constants.
const (
	DvdChannelBitsPerSector           = 38688
	BluRaySectorsPerCluster           = 32
	BluRayChannelBitsPerRecordingUnit = 962136
	BluRayChannelBitLengthNm          = 74.5
	DvdStandardChannelBitLengthNm     = 133.3
	CdStandardLinearVelocityMmPerSec  = 1200.0
)

// TrackLengthMm is the physical spiral length.
func (p Parameters) TrackLengthMm() float64 {
	switch p.Kind {
	case CD:
		return float64(p.Sectors) / 75.0 * p.LinearDensity
	case DVD:
		return float64(p.Sectors) * DvdChannelBitsPerSector * p.LinearDensity * 1e-6
	case BluRay:
		return float64(p.Sectors) / BluRaySectorsPerCluster * BluRayChannelBitsPerRecordingUnit * p.LinearDensity * 1e-6
	}
	return math.NaN()
}

// OutputSectors is the written sector count.
func (p Parameters) OutputSectors() int64 {
	if p.UsableSectors != 0 {
		return p.UsableSectors
	}
	return p.Sectors
}

// Validate ports CalibrationParameters.Validate's geometry checks.
func (p Parameters) Validate() error {
	trackLength := p.TrackLengthMm()
	if p.Sectors <= 0 || !finite(p.InnerRadiusMm) || !finite(p.OuterRadiusMm) ||
		p.InnerRadiusMm <= 0 || p.OuterRadiusMm <= p.InnerRadiusMm ||
		!finite(p.LinearDensity) || p.LinearDensity <= 0 ||
		!finite(trackLength) || trackLength <= p.OuterRadiusMm-p.InnerRadiusMm ||
		!finite(p.OuterRadiusMm*p.OuterRadiusMm) ||
		!finite(2*trackLength/(p.OuterRadiusMm*p.OuterRadiusMm-p.InnerRadiusMm*p.InnerRadiusMm)) {
		return fmt.Errorf("invalid calibration parameters: check disc type, radii, sector count and linear density")
	}
	if err := trackgeom.ValidatePitchCoefficients(p.PitchLinear, p.PitchQuadratic, p.PitchCubic); err != nil {
		return err
	}
	alignment := int64(1)
	if p.Kind == DVD {
		alignment = 16
	} else if p.Kind == BluRay {
		alignment = 32
	}
	if p.OutputSectors() <= 0 || p.OutputSectors() > p.Sectors ||
		(p.UsableSectors != 0 && p.OutputSectors()%alignment != 0) {
		return fmt.Errorf("the usable sector count must be positive, not exceed the total, and satisfy the media alignment")
	}
	if p.Kind == BluRay && (p.Sectors > math.MaxUint32 || p.Sectors%BluRaySectorsPerCluster != 0 ||
		p.LinearDensity != BluRayChannelBitLengthNm || p.OuterRadiusMm > 60) {
		return fmt.Errorf("Blu-ray calibration needs complete 32-sector clusters, 74.5 nm bit length and outer radius at most 60 mm")
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// constantPitchGeometry ports ConstantPitchCalibrationGeometry.
type constantPitchGeometry struct {
	params         Parameters
	Growth         float64
	innerPrimitive float64
}

func newConstantPitchGeometry(p Parameters) *constantPitchGeometry {
	g := &constantPitchGeometry{params: p}
	low, high := 1e-15, 1.0
	length := func(growth float64) float64 {
		return primitiveArc(p.OuterRadiusMm, growth) - primitiveArc(p.InnerRadiusMm, growth)
	}
	for length(high) > p.TrackLengthMm() {
		high *= 2
	}
	for i := 0; i < 100; i++ {
		middle := (low + high) / 2
		if length(middle) > p.TrackLengthMm() {
			low = middle
		} else {
			high = middle
		}
	}
	g.Growth = (low + high) / 2
	g.innerPrimitive = primitiveArc(p.InnerRadiusMm, g.Growth)
	return g
}

func (g *constantPitchGeometry) ProgressAtRadius(radius float64) float64 {
	return (primitiveArc(radius, g.Growth) - g.innerPrimitive) / g.params.TrackLengthMm()
}

// TryAtProgress solves radius for progress, allowing continuation.
func (g *constantPitchGeometry) TryAtProgress(progress float64) (float64, bool) {
	targetPrimitive := g.innerPrimitive + progress*g.params.TrackLengthMm()
	if !finite(targetPrimitive) || targetPrimitive <= 0 {
		return math.NaN(), false
	}
	squared := g.params.InnerRadiusMm*g.params.InnerRadiusMm + 2*g.Growth*progress*g.params.TrackLengthMm()
	radius := math.Sqrt(math.Max(squared, g.Growth*g.Growth))
	iterations := 12
	if g.Growth <= 0.01 {
		iterations = 2
	}
	for i := 0; i < iterations; i++ {
		next := radius - (primitiveArc(radius, g.Growth)-targetPrimitive)*g.Growth/math.Sqrt(radius*radius+g.Growth*g.Growth)
		if next > 0 {
			radius = next
		} else {
			radius = radius / 2
		}
	}
	if progress == 0 {
		radius = g.params.InnerRadiusMm
	}
	if progress == 1 {
		radius = g.params.OuterRadiusMm
	}
	return radius, finite(radius) && radius > 0
}

func primitiveArc(radius, growth float64) float64 {
	return (radius*math.Sqrt(radius*radius+growth*growth) +
		growth*growth*math.Asinh(radius/growth)) / (2 * growth)
}

// Geometry ports CalibrationGeometry (the subset needed by the patterns and
// the browser port's parity checks).
type Geometry struct {
	Params       Parameters
	b            float64
	squaredRange float64
	track        *trackgeom.TrackGeometry
	constant     *constantPitchGeometry
}

// NewGeometry builds the calibration geometry. For coarse targets the caller
// must pass parameters with the pitch coefficients already zeroed
// (ParametersForGeometry in C#).
func NewGeometry(p Parameters) (*Geometry, error) {
	g := &Geometry{
		Params:       p,
		squaredRange: p.OuterRadiusMm*p.OuterRadiusMm - p.InnerRadiusMm*p.InnerRadiusMm,
	}
	g.b = g.squaredRange / (2 * p.TrackLengthMm())
	if p.Kind == DVD || p.PitchLinear != 0 || p.PitchQuadratic != 0 || p.PitchCubic != 0 {
		track, err := trackgeom.New(p.InnerRadiusMm, p.OuterRadiusMm, p.TrackLengthMm(),
			p.PitchLinear, p.PitchQuadratic, p.PitchCubic)
		if err != nil {
			return nil, err
		}
		g.track = track
		g.b = track.RadialGrowthPerRadianMm()
	} else if p.Kind == BluRay {
		g.constant = newConstantPitchGeometry(p)
		g.b = g.constant.Growth
	}
	return g, nil
}

// UsesAreaApproximation reports the CD constant-pitch area model.
func (g *Geometry) UsesAreaApproximation() bool {
	return g.Params.Kind == CD && g.track == nil
}

// RadialGrowthPerRadianMm is the spiral growth scale.
func (g *Geometry) RadialGrowthPerRadianMm() float64 { return g.b }

// ProgressAtRadius maps radius to track progress in [0, 1].
func (g *Geometry) ProgressAtRadius(radius float64) float64 {
	var progress float64
	switch {
	case g.constant != nil:
		progress = g.constant.ProgressAtRadius(radius)
	case g.track == nil:
		progress = (radius*radius - g.Params.InnerRadiusMm*g.Params.InnerRadiusMm) / g.squaredRange
	default:
		progress = g.track.ArcLengthAtRadius(radius) / g.Params.TrackLengthMm()
	}
	if progress < 0 {
		return 0
	}
	if progress > 1 {
		return 1
	}
	return progress
}

// AngleAtRadius is the spiral angle at a radius.
func (g *Geometry) AngleAtRadius(radius float64) float64 {
	if g.track != nil {
		return g.track.AngleAtRadius(radius)
	}
	return (radius - g.Params.InnerRadiusMm) / g.b
}

// AtProgress evaluates radius and angle at a progress fraction.
func (g *Geometry) AtProgress(progress float64) (radius, angle float64) {
	if g.constant != nil {
		r, ok := g.constant.TryAtProgress(progress)
		if !ok {
			panic("no valid positive-radius track at this progress")
		}
		return r, g.AngleAtRadius(r)
	}
	if g.track == nil {
		radius = math.Sqrt(g.Params.InnerRadiusMm*g.Params.InnerRadiusMm + progress*g.squaredRange)
	} else {
		r, err := g.track.RadiusAtArcLength(progress * g.Params.TrackLengthMm())
		if err != nil {
			panic(err)
		}
		radius = r
	}
	return radius, g.AngleAtRadius(radius)
}
