package cd

import (
	"math"

	"disc-image-studio/internal/trackgeom"
)

// imageSampler is the scalar port of CdImageSampler (raster source only; the
// analytic and calibration-reference constructors are GUI-only). The C# SIMD
// fast paths preserve the scalar decisions, so sampling each byte with the
// scalar path reproduces the same output.
type imageSampler struct {
	source             *rasterImage
	parameters         *DiscParameters
	innerSquared       float64
	radiusSquaredRange float64
	angleScale         float64
	imageOuterRadius   float64
	geometry           *trackgeom.TrackGeometry
}

func newImageSampler(source *rasterImage, parameters *DiscParameters) *imageSampler {
	innerSquared := parameters.InnerRadiusMm * parameters.InnerRadiusMm
	radiusSquaredRange := parameters.OuterRadiusMm*parameters.OuterRadiusMm - innerSquared
	return &imageSampler{
		source:             source,
		parameters:         parameters,
		innerSquared:       innerSquared,
		radiusSquaredRange: radiusSquaredRange,
		angleScale:         2.0 * parameters.LinearVelocityMmPerSecond * parameters.TotalTimeSeconds() / radiusSquaredRange,
		imageOuterRadius:   parameters.SamplingImageOuterRadiusMm(),
		geometry:           parameters.VariablePitchGeometry(),
	}
}

// Sample returns the source grey for a logical byte index.
func (s *imageSampler) Sample(globalByte int64) byte {
	if s.geometry != nil {
		radius, angle := s.geometry.AtArcLengthFast(s.parameters.arcLengthFromByte(globalByte))
		angle += s.parameters.StartAngleRadians
		image := s.source
		centre := float64(image.Width) / 2.0
		pixelRadius := centre * radius / s.imageOuterRadius
		return image.GrayAt(centre+pixelRadius*math.Cos(angle), centre+pixelRadius*math.Sin(angle), 255)
	}
	x, y := s.parameters.SourceImagePointFromByte(globalByte, s.source.Width)
	return s.source.GrayAt(x, y, 255)
}
