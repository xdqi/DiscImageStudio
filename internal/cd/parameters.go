package cd

import (
	"fmt"
	"math"

	"disc-image-studio/internal/trackgeom"
)

// BytesPerSector is the CD audio sector size.
const BytesPerSector = 2352

// StandardLinearVelocityMmPerSecond is the ECMA-130 CLV scanning speed.
const StandardLinearVelocityMmPerSecond = 1200.0

// StandardImageOuterRadiusMm is the radius on the source image canvas that
// maps to the disc outer edge.
const StandardImageOuterRadiusMm = 57.5

// DiscParameters ports CdDiscParameters. Always construct via NewDiscParameters.
type DiscParameters struct {
	InnerRadiusMm             float64
	OuterRadiusMm             float64
	Sectors                   int64
	LinearVelocityMmPerSecond float64
	StartAngleRadians         float64
	ImageOuterRadiusMm        float64
	PitchLinear               float64
	PitchQuadratic            float64
	PitchCubic                float64
	UsableSectors             int64
	HasUsableSectors          bool
	SourceImageOuterRadiusMm  float64
	HasSourceImageOuterRadius bool

	variablePitchGeometry *trackgeom.TrackGeometry
}

// NewDiscParameters creates and validates a parameter set, precomputing the
// variable-pitch geometry exactly like the C# record's lazy cache.
func NewDiscParameters(innerRadiusMm, outerRadiusMm float64, sectors int64,
	linearVelocityMmPerSecond, startAngleRadians, imageOuterRadiusMm,
	pitchLinear, pitchQuadratic, pitchCubic float64,
	usableSectors int64, hasUsableSectors bool,
	sourceImageOuterRadiusMm float64, hasSourceImageOuterRadius bool) (*DiscParameters, error) {
	p := &DiscParameters{
		InnerRadiusMm:             innerRadiusMm,
		OuterRadiusMm:             outerRadiusMm,
		Sectors:                   sectors,
		LinearVelocityMmPerSecond: linearVelocityMmPerSecond,
		StartAngleRadians:         startAngleRadians,
		ImageOuterRadiusMm:        imageOuterRadiusMm,
		PitchLinear:               pitchLinear,
		PitchQuadratic:            pitchQuadratic,
		PitchCubic:                pitchCubic,
		UsableSectors:             usableSectors,
		HasUsableSectors:          hasUsableSectors,
		SourceImageOuterRadiusMm:  sourceImageOuterRadiusMm,
		HasSourceImageOuterRadius: hasSourceImageOuterRadius,
	}
	if pitchLinear != 0 || pitchQuadratic != 0 || pitchCubic != 0 {
		geometry, err := trackgeom.New(innerRadiusMm, outerRadiusMm,
			p.TotalTimeSeconds()*linearVelocityMmPerSecond,
			pitchLinear, pitchQuadratic, pitchCubic)
		if err != nil {
			return nil, err
		}
		p.variablePitchGeometry = geometry
	}
	return p, nil
}

// VariablePitchGeometry returns the cubic-pitch geometry, or nil.
func (p *DiscParameters) VariablePitchGeometry() *trackgeom.TrackGeometry {
	return p.variablePitchGeometry
}

// OutputSectors is the number of sectors actually written.
func (p *DiscParameters) OutputSectors() int64 {
	if p.HasUsableSectors {
		return p.UsableSectors
	}
	return p.Sectors
}

// TotalBytes is the output byte count.
func (p *DiscParameters) TotalBytes() int64 { return p.OutputSectors() * BytesPerSector }

// GeometryBytes is the full geometry byte count.
func (p *DiscParameters) GeometryBytes() int64 { return p.Sectors * BytesPerSector }

// OutputOuterRadiusMm is the radius where output data ends.
func (p *DiscParameters) OutputOuterRadiusMm() float64 { return p.RadiusFromByte(p.TotalBytes()) }

// PreviewImageOuterRadiusMm is the image radius used by previews.
func (p *DiscParameters) PreviewImageOuterRadiusMm() float64 {
	if p.HasUsableSectors {
		return p.OutputOuterRadiusMm()
	}
	return p.ImageOuterRadiusMm
}

// SamplingImageOuterRadiusMm is the image radius used when sampling the source.
func (p *DiscParameters) SamplingImageOuterRadiusMm() float64 {
	if p.HasSourceImageOuterRadius {
		return p.SourceImageOuterRadiusMm
	}
	return p.PreviewImageOuterRadiusMm()
}

// TotalTimeSeconds is the audio duration at 75 sectors per second.
func (p *DiscParameters) TotalTimeSeconds() float64 { return float64(p.Sectors) / 75.0 }

func (p *DiscParameters) radiusSquaredRange() float64 {
	return p.OuterRadiusMm*p.OuterRadiusMm - p.InnerRadiusMm*p.InnerRadiusMm
}

// Validate checks all constraints, mirroring the C# validation.
func (p *DiscParameters) Validate() error {
	if p.Sectors <= 0 || p.OutputSectors() <= 0 || p.OutputSectors() > p.Sectors {
		return fmt.Errorf("CD sector count must be positive. (Parameter 'Sectors')")
	}
	if !isFinite(p.InnerRadiusMm) || !isFinite(p.OuterRadiusMm) ||
		p.InnerRadiusMm <= 0 || p.OuterRadiusMm <= p.InnerRadiusMm {
		return fmt.Errorf("CD outer radius must be greater than its positive inner radius. (Parameter 'OuterRadiusMm')")
	}
	if !isFinite(p.LinearVelocityMmPerSecond) || p.LinearVelocityMmPerSecond <= 0 {
		return fmt.Errorf("CD linear velocity must be positive. (Parameter 'LinearVelocityMmPerSecond')")
	}
	if !isFinite(p.StartAngleRadians) {
		return fmt.Errorf("Specified argument was out of the range of valid values. (Parameter 'StartAngleRadians')")
	}
	if !isFinite(p.ImageOuterRadiusMm) || p.ImageOuterRadiusMm <= 0 {
		return fmt.Errorf("Image outer radius must be positive. (Parameter 'ImageOuterRadiusMm')")
	}
	if p.HasSourceImageOuterRadius && (!isFinite(p.SourceImageOuterRadiusMm) || p.SourceImageOuterRadiusMm <= 0) {
		return fmt.Errorf("Source image radius must be positive and finite. (Parameter 'SourceImageOuterRadiusMm')")
	}
	_ = p.GeometryBytes()
	_ = p.TotalBytes()
	if err := trackgeom.ValidatePitchCoefficients(p.PitchLinear, p.PitchQuadratic, p.PitchCubic); err != nil {
		return err
	}
	return nil
}

func (p *DiscParameters) arcLengthFromByte(globalByte int64) float64 {
	if globalByte <= 0 {
		return 0
	}
	if globalByte >= p.GeometryBytes() {
		return p.TotalTimeSeconds() * p.LinearVelocityMmPerSecond
	}
	return clampF(float64(globalByte)*p.LinearVelocityMmPerSecond/(75.0*BytesPerSector),
		0, p.TotalTimeSeconds()*p.LinearVelocityMmPerSecond)
}

// RadiusFromByte maps a logical byte index to a radius.
func (p *DiscParameters) RadiusFromByte(globalByte int64) float64 {
	if p.variablePitchGeometry != nil {
		return p.variablePitchGeometry.RadiusAtArcLengthFast(p.arcLengthFromByte(globalByte))
	}
	fraction := clampF(float64(globalByte)/float64(p.GeometryBytes()), 0.0, 1.0)
	return math.Sqrt(p.InnerRadiusMm*p.InnerRadiusMm + fraction*p.radiusSquaredRange())
}

// ThetaFromRadius maps a radius to a spiral angle.
func (p *DiscParameters) ThetaFromRadius(radiusMm float64) float64 {
	if p.variablePitchGeometry != nil {
		return p.StartAngleRadians + p.variablePitchGeometry.AngleAtRadius(radiusMm)
	}
	return p.StartAngleRadians +
		(2.0*p.LinearVelocityMmPerSecond*p.TotalTimeSeconds()/p.radiusSquaredRange())*
			(radiusMm-p.InnerRadiusMm)
}

// PolarFromByte maps a logical byte index to polar coordinates.
func (p *DiscParameters) PolarFromByte(globalByte int64) (radiusMm, thetaRadians float64) {
	if p.variablePitchGeometry != nil {
		radius, angle := p.variablePitchGeometry.AtArcLengthFast(p.arcLengthFromByte(globalByte))
		return radius, p.StartAngleRadians + angle
	}
	radius := p.RadiusFromByte(globalByte)
	return radius, p.ThetaFromRadius(radius)
}

// ImagePointFromByte maps a byte to preview-image pixel coordinates.
func (p *DiscParameters) ImagePointFromByte(globalByte int64, imageSize int) (x, y float64) {
	return p.pointFromByte(globalByte, imageSize, p.PreviewImageOuterRadiusMm())
}

// SourceImagePointFromByte maps a byte to source-image pixel coordinates.
func (p *DiscParameters) SourceImagePointFromByte(globalByte int64, imageSize int) (x, y float64) {
	return p.pointFromByte(globalByte, imageSize, p.SamplingImageOuterRadiusMm())
}

func (p *DiscParameters) pointFromByte(globalByte int64, imageSize int, imageOuterRadiusMm float64) (x, y float64) {
	radius, theta := p.PolarFromByte(globalByte)
	imageRadius := float64(imageSize) / 2.0
	pixelRadius := imageRadius * radius / imageOuterRadiusMm
	centre := float64(imageSize) / 2.0
	return centre + pixelRadius*math.Cos(theta), centre + pixelRadius*math.Sin(theta)
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func clampF(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
