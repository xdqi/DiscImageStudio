package bluray

import (
	"math"

	"disc-image-studio/internal/trackgeom"
)

// TrackPoint ports BluRayTrackPoint.
type TrackPoint struct {
	Layer        int
	RadiusMm     float64
	AngleRadians float64
}

// Xmm is RadiusMm * cos(AngleRadians).
func (p TrackPoint) Xmm() float64 { return p.RadiusMm * math.Cos(p.AngleRadians) }

// Ymm is RadiusMm * sin(AngleRadians).
func (p TrackPoint) Ymm() float64 { return p.RadiusMm * math.Sin(p.AngleRadians) }

// TrackGeometry ports BluRayTrackGeometry: the nominal recording-unit spiral
// with optional cubic radial pitch. SIMD paths of the C# original are not
// ported; the scalar path is authoritative.
type TrackGeometry struct {
	disc               *DiscParameters
	growth             float64
	innerPrimitive     float64
	angleOffset        float64
	variablePitch      *trackgeom.TrackGeometry
	variablePixelReuse bool

	interpolationMargin         float64
	interpolationTangentMargin  float64
	interpolationCurvatureBound float64

	trackLengthPerLayerMm float64
}

// NewTrackGeometry ports the BluRayTrackGeometry constructor.
func NewTrackGeometry(disc *DiscParameters) (*TrackGeometry, error) {
	if disc == nil {
		return nil, argNull("disc")
	}
	if err := disc.Validate(); err != nil {
		return nil, err
	}
	g := &TrackGeometry{disc: disc}
	g.trackLengthPerLayerMm = float64(disc.TotalSectors) /
		float64(SectorsPerCluster) *
		ChannelBitsPerRecordingUnit *
		ChannelBitLengthNm * 1e-6
	g.angleOffset = math.Mod(disc.StartAngleDegrees, 360) * math.Pi / 180
	if disc.HasVariablePitch() {
		vp, err := trackgeom.New(disc.InnerRadiusMm, disc.OuterRadiusMm,
			g.trackLengthPerLayerMm, disc.PitchLinear, disc.PitchQuadratic, disc.PitchCubic)
		if err != nil {
			return nil, err
		}
		g.variablePitch = vp
		g.growth = vp.RadialGrowthPerRadianMm()
		margin, tangentMargin, acceleration, ok := vp.TryGetFastSamplingBounds()
		g.interpolationMargin = margin
		g.interpolationTangentMargin = tangentMargin
		g.variablePixelReuse = ok
		g.interpolationCurvatureBound = acceleration / 2
		return g, nil
	}
	lo, hi := 1e-15, 1.0
	for spiralPrimitive(disc.OuterRadiusMm, hi)-spiralPrimitive(disc.InnerRadiusMm, hi) > g.trackLengthPerLayerMm {
		hi *= 2
	}
	for i := 0; i < 100; i++ {
		b := (lo + hi) / 2
		length := spiralPrimitive(disc.OuterRadiusMm, b) - spiralPrimitive(disc.InnerRadiusMm, b)
		if length > g.trackLengthPerLayerMm {
			lo = b
		} else {
			hi = b
		}
	}
	g.growth = (lo + hi) / 2
	g.innerPrimitive = spiralPrimitive(disc.InnerRadiusMm, g.growth)
	return g, nil
}

// TrackLengthPerLayerMm ports the C# property.
func (g *TrackGeometry) TrackLengthPerLayerMm() float64 { return g.trackLengthPerLayerMm }

// EffectiveTrackPitchMicrometers ports the C# property.
func (g *TrackGeometry) EffectiveTrackPitchMicrometers() float64 {
	return 2 * math.Pi * g.growth * 1000
}

// supportsPixelReuse ports the internal C# property.
func (g *TrackGeometry) supportsPixelReuse() bool {
	return (g.variablePitch == nil || g.variablePixelReuse) && g.disc.InnerRadiusMm >= 10 &&
		g.growth >= 1e-6 && g.growth <= 1e-3
}

func (g *TrackGeometry) pixelReuseSafetyMarginMm() float64 {
	return 8192*2.2204460492503131e-16*g.disc.OuterRadiusMm*g.disc.OuterRadiusMm/g.growth*
		math.Max(1, g.disc.OuterRadiusMm/g.disc.InnerRadiusMm) +
		256*2.2204460492503131e-16*g.trackLengthPerLayerMm + 1e-8 + g.interpolationMargin
}

func (g *TrackGeometry) pixelReuseCurvatureBound() float64 {
	if g.variablePitch == nil {
		return 1 / g.disc.InnerRadiusMm
	}
	return g.interpolationCurvatureBound
}

func (g *TrackGeometry) pixelReuseTangentMargin() float64 { return g.interpolationTangentMargin }

func (g *TrackGeometry) millimetresPerChannelBit() float64 {
	return g.trackLengthPerLayerMm / float64(g.disc.TotalSectors) /
		(float64(ChannelBitsPerRecordingUnit) / 32.0)
}

// unitTangent ports the internal UnitTangent.
func (g *TrackGeometry) unitTangent(x, y, radius float64) (float64, float64) {
	if g.variablePitch != nil {
		return y / radius, -x / radius
	}
	length := math.Sqrt(radius*radius + g.growth*g.growth)
	return (g.growth*(x/radius) + y) / length,
		(g.growth*(y/radius) - x) / length
}

// AtLayerFraction ports the public C# method; progress=1 is allowed for its
// endpoint.
func (g *TrackGeometry) AtLayerFraction(layer int, progress float64) (TrackPoint, error) {
	if layer < 0 || layer >= g.disc.LayerCount {
		return TrackPoint{}, argOutOfRange("layer")
	}
	if isNotFinite(progress) || progress < 0 || progress > 1 {
		return TrackPoint{}, argOutOfRange("progress")
	}
	return g.atLayerFraction(layer, progress), nil
}

func (g *TrackGeometry) atLayerFraction(layer int, progress float64) TrackPoint {
	radialProgress := progress
	target := noFMA(radialProgress * g.trackLengthPerLayerMm)
	if g.variablePitch != nil {
		radius, angle := g.variablePitch.AtArcLengthFast(target)
		return TrackPoint{Layer: layer, RadiusMm: radius, AngleRadians: g.angleOffset - angle}
	}
	// The thin-spiral inverse is already close at optical-disc pitch. Newton
	// refinement retains the exact arc model without 60 searches per image byte.
	// noFMA barriers force individual roundings, preventing the FMA fusion
	// that Go would otherwise apply on arm64 (the reference .NET evaluation
	// rounds each operation).
	radius := math.Sqrt(noFMA(noFMA(g.disc.InnerRadiusMm*g.disc.InnerRadiusMm) + noFMA(2*g.growth*target)))
	iterations := 8
	if g.growth <= 0.01 {
		iterations = 2
	}
	for i := 0; i < iterations; i++ {
		errv := noFMA(noFMA(spiralPrimitive(radius, g.growth)-g.innerPrimitive) - target)
		errGrowth := noFMA(errv * g.growth)
		radius = clampFloat(noFMA(radius-noFMA(errGrowth/math.Sqrt(noFMA(noFMA(radius*radius)+noFMA(g.growth*g.growth))))),
			g.disc.InnerRadiusMm, g.disc.OuterRadiusMm)
	}
	if radialProgress == 0 {
		radius = g.disc.InnerRadiusMm
	} else if radialProgress == 1 {
		radius = g.disc.OuterRadiusMm
	}
	// The disc rotates counter-clockwise under the fixed objective; the next
	// material point at that objective therefore has a decreasing polar angle.
	angle := g.angleOffset - (radius-g.disc.InnerRadiusMm)/g.growth
	return TrackPoint{Layer: layer, RadiusMm: radius, AngleRadians: angle}
}

// AtSector ports the validated public method.
func (g *TrackGeometry) AtSector(sectorPosition float64) (TrackPoint, error) {
	if isNotFinite(sectorPosition) || sectorPosition < 0 || sectorPosition > float64(g.disc.TotalSectors) {
		return TrackPoint{}, argOutOfRange("sectorPosition")
	}
	return g.atSector(sectorPosition), nil
}

func (g *TrackGeometry) atSector(sectorPosition float64) TrackPoint {
	return g.atLayerFraction(0, sectorPosition/float64(g.disc.TotalSectors))
}

// TryGetSectorAtPoint finds the nearest spiral turn at a point in fixed disc
// coordinates.
func (g *TrackGeometry) TryGetSectorAtPoint(xMm, yMm float64) (float64, bool) {
	radius := math.Sqrt(xMm*xMm + yMm*yMm)
	if isNotFinite(radius) || radius < g.disc.InnerRadiusMm || radius >= g.disc.OuterRadiusMm {
		return 0, false
	}
	maxSector := math.Nextafter(float64(g.disc.TotalSectors), math.Inf(-1))
	if g.variablePitch != nil {
		angle := g.variablePitch.AngleAtRadius(radius)
		difference := math.Remainder(math.Atan2(yMm, xMm)-(g.angleOffset-angle), 2*math.Pi)
		nearestAngle := angle - difference
		if nearestAngle < 0 || nearestAngle >= g.variablePitch.TotalAngleRadians() {
			return 0, false
		}
		sector := clampFloat(g.variablePitch.ArcLengthAtAngle(nearestAngle)*
			(float64(g.disc.TotalSectors)/g.trackLengthPerLayerMm), 0, maxSector)
		return sector, true
	}

	growth := g.EffectiveTrackPitchMicrometers() / (2 * math.Pi * 1000)
	spiralAngle := g.angleOffset - (radius-g.disc.InnerRadiusMm)/growth
	delta := math.Remainder(math.Atan2(yMm, xMm)-spiralAngle, 2*math.Pi)
	nearestRadius := radius - delta*growth
	if nearestRadius < g.disc.InnerRadiusMm || nearestRadius >= g.disc.OuterRadiusMm {
		return 0, false
	}
	sector := clampFloat((spiralPrimitive(nearestRadius, growth)-spiralPrimitive(g.disc.InnerRadiusMm, growth))*
		(float64(g.disc.TotalSectors)/g.trackLengthPerLayerMm), 0, maxSector)
	return sector, true
}

func spiralPrimitive(radius, b float64) float64 {
	// .NET's Math.Asinh invokes the platform C library (musl on the
	// reference system); use the bit-exact musl port so computed radii match.
	// The float64 conversions force each operation to round separately,
	// preventing the FMA fusion Go would otherwise apply on arm64 (the
	// reference .NET evaluation rounds every operation individually).
	r2 := float64(radius * radius)
	b2 := float64(b * b)
	root := math.Sqrt(float64(r2 + b2))
	term1 := float64(radius * root)
	term2 := float64(b2 * muslAsinh(float64(radius/b)))
	return float64(term1+term2) / (2 * b)
}
