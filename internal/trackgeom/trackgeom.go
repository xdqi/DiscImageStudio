// Package trackgeom ports DiscImageStudio.Core/DvdTrackGeometry.cs: the
// spiral shared by CD, DVD and Blu-ray whose local radial pitch is a cubic in
// normalized radius. Only the scalar evaluation paths are ported; the C#
// SIMD paths are documented to preserve the scalar results.
package trackgeom

import (
	"errors"
	"fmt"
	"math"
)

// Positive half of the eight-point Gauss-Legendre rule.
var nodes = [4]float64{0.1834346424956498, 0.5255324099163290, 0.7966664774136267, 0.9602898564975363}
var weights = [4]float64{0.3626837833783620, 0.3137066458778873, 0.2223810344533745, 0.1012285362903763}

var errPitchResolution = errors.New("the pitch curve cannot be resolved numerically")

type integral struct {
	length     float64
	angle      float64
	derivative float64
}

func (a integral) add(b integral) integral {
	return integral{a.length + b.length, a.angle + b.angle, a.derivative + b.derivative}
}

type segment struct {
	radiusStart float64
	width       float64
	arcStart    float64
	angleStart  float64
	arcDelta    float64
	angleDelta  float64
	arcDerStart float64
	arcDerEnd   float64
	angDerStart float64
	angDerEnd   float64
}

// TrackGeometry is the variable-pitch spiral.
type TrackGeometry struct {
	innerRadiusMm float64
	outerRadiusMm float64
	trackLengthMm float64

	linear    float64
	quadratic float64
	cubic     float64
	rnge      float64
	segments  []segment
	lenScale  float64

	constantInnerRadiusSquared float64
	constantGrowthSquared      float64
	constantTwiceGrowth        float64
	constantInnerPrimitive     float64

	radialGrowthPerRadianMm float64
	totalAngleRadians       float64
	isConstantPitch         bool
}

// New creates a geometry, validating like the C# factory.
func New(innerRadiusMm, outerRadiusMm, trackLengthMm, pitchLinear, pitchQuadratic, pitchCubic float64) (*TrackGeometry, error) {
	if math.IsNaN(innerRadiusMm) || math.IsInf(innerRadiusMm, 0) || innerRadiusMm <= 0 {
		return nil, fmt.Errorf("inner radius must be positive")
	}
	if math.IsNaN(outerRadiusMm) || math.IsInf(outerRadiusMm, 0) || outerRadiusMm <= innerRadiusMm {
		return nil, fmt.Errorf("outer radius must exceed inner radius")
	}
	if math.IsNaN(trackLengthMm) || math.IsInf(trackLengthMm, 0) || trackLengthMm <= outerRadiusMm-innerRadiusMm {
		return nil, fmt.Errorf("track length must exceed the radial distance between the two radii")
	}
	if err := ValidatePitchCoefficients(pitchLinear, pitchQuadratic, pitchCubic); err != nil {
		return nil, err
	}
	g := &TrackGeometry{
		innerRadiusMm: innerRadiusMm,
		outerRadiusMm: outerRadiusMm,
		trackLengthMm: trackLengthMm,
		rnge:          outerRadiusMm - innerRadiusMm,
		linear:        pitchLinear,
		quadratic:     pitchQuadratic,
		cubic:         pitchCubic,
		lenScale:      1,
	}
	g.isConstantPitch = pitchLinear == 0 && pitchQuadratic == 0 && pitchCubic == 0

	if g.isConstantPitch {
		low := 1e-15
		high := math.Max(outerRadiusMm, 1.0)
		for constantArc(innerRadiusMm, outerRadiusMm, high) > trackLengthMm {
			high *= 2
		}
		for i := 0; i < 120; i++ {
			middle := (low + high) / 2
			if constantArc(innerRadiusMm, outerRadiusMm, middle) > trackLengthMm {
				low = middle
			} else {
				high = middle
			}
		}
		g.radialGrowthPerRadianMm = (low + high) / 2
		g.constantInnerRadiusSquared = innerRadiusMm * innerRadiusMm
		g.constantGrowthSquared = g.radialGrowthPerRadianMm * g.radialGrowthPerRadianMm
		g.constantTwiceGrowth = 2 * g.radialGrowthPerRadianMm
		g.constantInnerPrimitive = primitive(innerRadiusMm, g.radialGrowthPerRadianMm)
		g.totalAngleRadians = g.rnge / g.radialGrowthPerRadianMm
		return g, nil
	}

	// Solve in reciprocal growth.
	thin, err := g.integrateSafe(innerRadiusMm, outerRadiusMm, 1, true)
	if err != nil {
		return nil, err
	}
	thinLength := thin.length
	lower := 0.0
	upper := trackLengthMm / thinLength
	inverseGrowth := upper
	for iteration := 0; iteration < 80; iteration++ {
		value, ierr := g.integrateSafe(innerRadiusMm, outerRadiusMm, inverseGrowth, false)
		if ierr != nil {
			return nil, ierr
		}
		lengthError := value.length - trackLengthMm
		if math.Abs(lengthError) <= trackLengthMm*2e-15 {
			break
		}
		if lengthError > 0 {
			upper = inverseGrowth
		} else {
			lower = inverseGrowth
		}
		next := inverseGrowth - lengthError/value.derivative
		if math.IsNaN(next) || math.IsInf(next, 0) || next <= lower || next >= upper {
			next = lower + (upper-lower)/2
		}
		if next == inverseGrowth {
			break
		}
		inverseGrowth = next
	}
	g.radialGrowthPerRadianMm = 1 / inverseGrowth
	if math.IsNaN(g.radialGrowthPerRadianMm) || math.IsInf(g.radialGrowthPerRadianMm, 0) || g.radialGrowthPerRadianMm <= 0 {
		return nil, fmt.Errorf("the pitch coefficients cannot be resolved at this track length")
	}

	var segments []segment
	if err := g.buildSegments(innerRadiusMm, outerRadiusMm, inverseGrowth, &segments, 0); err != nil {
		return nil, err
	}
	g.segments = segments
	arc, angle, arcError, angleError := 0.0, 0.0, 0.0, 0.0
	for i := range g.segments {
		seg := g.segments[i]
		g.segments[i].arcStart = arc
		g.segments[i].angleStart = angle
		arc, arcError = addCompensated(arc, arcError, seg.arcDelta)
		angle, angleError = addCompensated(angle, angleError, seg.angleDelta)
	}
	// Remove the final few rounding bits so logical progress ends exactly at ro.
	g.lenScale = trackLengthMm / arc
	g.totalAngleRadians = angle
	return g, nil
}

// TryGetFastSamplingBounds bounds the actual interpolated curve used by fast
// sampling. Ported from DvdTrackGeometry.TryGetFastSamplingBounds.
func (g *TrackGeometry) TryGetFastSamplingBounds() (positionErrorMm, tangentError, accelerationBound float64, ok bool) {
	if g.isConstantPitch || g.innerRadiusMm < 10 ||
		g.radialGrowthPerRadianMm < 1e-6 || g.radialGrowthPerRadianMm > 1e-3 ||
		math.Abs(g.linear)+math.Abs(g.quadratic)+math.Abs(g.cubic) > .5 {
		return 0, 0, 0, false
	}
	const epsilon = 2.2204460492503131e-16
	for _, seg := range g.segments {
		width := seg.width
		radius := seg.radiusStart + width
		s0 := width * seg.arcDerStart
		s1 := width * seg.arcDerEnd
		sq := 3*seg.arcDelta - 2*s0 - s1
		sc := s0 + s1 - 2*seg.arcDelta
		derivativeBudget := 128 * epsilon * (math.Abs(s0) + 2*math.Abs(sq) + 3*math.Abs(sc))
		minimum := math.Min(s0, s0+2*sq+3*sc)
		if sc != 0 {
			vertex := -sq / (3 * sc)
			if vertex > 0 && vertex < 1 {
				minimum = math.Min(minimum, s0+vertex*(2*sq+3*sc*vertex))
			}
		}
		minimum -= derivativeBudget
		second := math.Max(math.Abs(2*sq), math.Abs(2*sq+6*sc)) + derivativeBudget
		if math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum <= 0 || second/minimum > .5 {
			return 0, 0, 0, false
		}
		a0 := width * seg.angDerStart
		a1 := width * seg.angDerEnd
		aq := 3*seg.angleDelta - 2*a0 - a1
		ac := a0 + a1 - 2*seg.angleDelta
		angularBudget := 128 * epsilon * (math.Abs(a0) + 2*math.Abs(aq) + 3*math.Abs(ac))
		angularFirst := math.Abs(a0) + 2*math.Abs(aq) + 3*math.Abs(ac) + angularBudget
		angularSecond := math.Max(math.Abs(2*aq), math.Abs(2*aq+6*ac)) + angularBudget
		scaledMinimum := g.lenScale * minimum * (1 - 16*epsilon)
		scaledSecond := g.lenScale * second * (1 + 16*epsilon)
		firstPosition := width + radius*angularFirst
		secondPosition := radius*angularFirst*angularFirst + 2*width*angularFirst + radius*angularSecond
		acceleration := secondPosition/(scaledMinimum*scaledMinimum) +
			firstPosition*scaledSecond/(scaledMinimum*scaledMinimum*scaledMinimum)
		accelerationBound = math.Max(accelerationBound, acceleration*(1+128*epsilon))

		r0 := seg.radiusStart
		speedDifference := math.Abs(r0*a0-g.lenScale*s0) +
			math.Abs(width*a0+2*r0*aq-2*g.lenScale*sq) +
			math.Abs(2*width*aq+3*r0*ac-3*g.lenScale*sc) +
			math.Abs(3*width*ac) +
			256*epsilon*(radius*angularFirst+g.lenScale*(math.Abs(s0)+2*math.Abs(sq)+3*math.Abs(sc)))
		tangentError = math.Max(tangentError, (width+speedDifference)/scaledMinimum)

		iterationError := 128*epsilon*(math.Abs(s0)+math.Abs(sq)+math.Abs(sc)+seg.arcDelta)/minimum + 32*epsilon
		tError := 1.0
		for iteration := 0; iteration < 4; iteration++ {
			tError = second/(2*minimum)*tError*tError + iterationError
		}
		err := firstPosition * tError
		if math.IsNaN(err) || math.IsInf(err, 0) {
			return 0, 0, 0, false
		}
		positionErrorMm = math.Max(positionErrorMm, err)
	}
	positionErrorMm = 2 * (positionErrorMm +
		256*epsilon*g.trackLengthMm*(1+tangentError) +
		256*epsilon*g.outerRadiusMm*(1+g.totalAngleRadians))
	if math.IsNaN(positionErrorMm) || math.IsInf(positionErrorMm, 0) ||
		math.IsNaN(tangentError) || math.IsInf(tangentError, 0) ||
		math.IsNaN(accelerationBound) || math.IsInf(accelerationBound, 0) ||
		tangentError >= .01 || positionErrorMm >= .01 {
		return 0, 0, 0, false
	}
	return positionErrorMm, tangentError, accelerationBound, true
}

// TrackLengthMm is the physical spiral length.
func (g *TrackGeometry) TrackLengthMm() float64 { return g.trackLengthMm }

// InnerRadiusMm is the spiral's inner radius.
func (g *TrackGeometry) InnerRadiusMm() float64 { return g.innerRadiusMm }

// OuterRadiusMm is the spiral's outer radius.
func (g *TrackGeometry) OuterRadiusMm() float64 { return g.outerRadiusMm }

// RadialGrowthPerRadianMm is the solved constant growth scale.
func (g *TrackGeometry) RadialGrowthPerRadianMm() float64 { return g.radialGrowthPerRadianMm }

// TrackPitchMm is the nominal track pitch.
func (g *TrackGeometry) TrackPitchMm() float64 { return 2 * math.Pi * g.radialGrowthPerRadianMm }

// TotalAngleRadians is the total spiral angle.
func (g *TrackGeometry) TotalAngleRadians() float64 { return g.totalAngleRadians }

// IsConstantPitch reports whether all pitch coefficients are zero.
func (g *TrackGeometry) IsConstantPitch() bool { return g.isConstantPitch }

// ValidatePitchCoefficients checks both edges and every stationary point.
func ValidatePitchCoefficients(linear, quadratic, cubic float64) error {
	if math.IsNaN(linear) || math.IsInf(linear, 0) || math.IsNaN(quadratic) || math.IsInf(quadratic, 0) ||
		math.IsNaN(cubic) || math.IsInf(cubic, 0) || !positiveOnInterval(linear, quadratic, cubic, 0, 1) {
		return fmt.Errorf("pitch coefficients must be finite and 1 + c1*x + c2*x² + c3*x³ must stay positive for 0 <= x <= 1")
	}
	return nil
}

// RadiusAtArcLength solves the radius for an arc length.
func (g *TrackGeometry) RadiusAtArcLength(arcLengthMm float64) (float64, error) {
	if math.IsNaN(arcLengthMm) || math.IsInf(arcLengthMm, 0) || arcLengthMm < 0 || arcLengthMm > g.trackLengthMm {
		return 0, fmt.Errorf("arc length out of range")
	}
	if !g.isConstantPitch {
		return g.RadiusAtArcLengthFast(arcLengthMm), nil
	}
	b := g.radialGrowthPerRadianMm
	radius := clampf(math.Sqrt(g.innerRadiusMm*g.innerRadiusMm+2*b*arcLengthMm), g.innerRadiusMm, g.outerRadiusMm)
	for i := 0; i < 5; i++ {
		err := constantArc(g.innerRadiusMm, radius, b) - arcLengthMm
		derivative := math.Sqrt(radius*radius+b*b) / b
		radius = clampf(radius-err/derivative, g.innerRadiusMm, g.outerRadiusMm)
	}
	return radius, nil
}

// RadiusAtArcLengthFast is the table-driven radius evaluation.
func (g *TrackGeometry) RadiusAtArcLengthFast(arcLengthMm float64) float64 {
	if g.isConstantPitch {
		return g.radiusAtConstantArcLengthFast(arcLengthMm)
	}
	if arcLengthMm <= 0 {
		return g.innerRadiusMm
	}
	if arcLengthMm >= g.trackLengthMm {
		return g.outerRadiusMm
	}
	if math.IsNaN(arcLengthMm) || math.IsInf(arcLengthMm, 0) {
		panic("arc length must be finite")
	}
	unscaled := arcLengthMm / g.lenScale
	seg := g.segments[g.findSegment(unscaled, false)]
	t := invertHermite(unscaled-seg.arcStart, seg.arcDelta, seg.width*seg.arcDerStart, seg.width*seg.arcDerEnd)
	return seg.radiusStart + seg.width*t
}

func (g *TrackGeometry) radiusAtConstantArcLengthFast(arcLengthMm float64) float64 {
	b := g.radialGrowthPerRadianMm
	radius := clampf(math.Sqrt(g.constantInnerRadiusSquared+g.constantTwiceGrowth*arcLengthMm),
		g.innerRadiusMm, g.outerRadiusMm)
	root := math.Sqrt(radius*radius + g.constantGrowthSquared)
	prim := 0.5 * (radius*root + g.constantGrowthSquared*math.Asinh(radius/b))
	err := (prim-g.constantInnerPrimitive)/b - arcLengthMm
	derivative := root / b
	return clampf(radius-err/derivative, g.innerRadiusMm, g.outerRadiusMm)
}

// AngleAtArcLength solves the angle for an arc length.
func (g *TrackGeometry) AngleAtArcLength(arcLengthMm float64) float64 {
	r, err := g.RadiusAtArcLength(arcLengthMm)
	if err != nil {
		panic(err)
	}
	return g.AngleAtRadius(r)
}

// AtArcLengthFast returns radius and angle with one table search.
func (g *TrackGeometry) AtArcLengthFast(arcLengthMm float64) (radius, angle float64) {
	if g.isConstantPitch {
		r := g.radiusAtConstantArcLengthFast(arcLengthMm)
		return r, (r - g.innerRadiusMm) / g.radialGrowthPerRadianMm
	}
	if arcLengthMm <= 0 {
		return g.innerRadiusMm, 0
	}
	if arcLengthMm >= g.trackLengthMm {
		return g.outerRadiusMm, g.totalAngleRadians
	}
	if math.IsNaN(arcLengthMm) || math.IsInf(arcLengthMm, 0) {
		panic("arc length must be finite")
	}
	unscaled := arcLengthMm / g.lenScale
	seg := g.segments[g.findSegment(unscaled, false)]
	t := invertHermite(unscaled-seg.arcStart, seg.arcDelta, seg.width*seg.arcDerStart, seg.width*seg.arcDerEnd)
	return seg.radiusStart + seg.width*t,
		seg.angleStart + hermite(t, seg.angleDelta, seg.width*seg.angDerStart, seg.width*seg.angDerEnd)
}

// AngleAtRadius evaluates the spiral angle at a radius.
func (g *TrackGeometry) AngleAtRadius(radius float64) float64 {
	if g.isConstantPitch {
		return (radius - g.innerRadiusMm) / g.radialGrowthPerRadianMm
	}
	if radius < g.innerRadiusMm || radius > g.outerRadiusMm {
		value, ok := g.tryExtendedIntegral(radius)
		if !ok {
			panic("radius out of range")
		}
		return value.angle
	}
	seg := g.segments[g.findRadiusSegment(radius)]
	t := (radius - seg.radiusStart) / seg.width
	return seg.angleStart + hermite(t, seg.angleDelta, seg.width*seg.angDerStart, seg.width*seg.angDerEnd)
}

// ArcLengthAtRadius evaluates the arc length at a radius.
func (g *TrackGeometry) ArcLengthAtRadius(radius float64) float64 {
	if g.isConstantPitch {
		return constantArc(g.innerRadiusMm, radius, g.radialGrowthPerRadianMm)
	}
	if radius < g.innerRadiusMm || radius > g.outerRadiusMm {
		value, ok := g.tryExtendedIntegral(radius)
		if !ok {
			panic("radius out of range")
		}
		return value.length
	}
	if radius == g.outerRadiusMm {
		return g.trackLengthMm
	}
	seg := g.segments[g.findRadiusSegment(radius)]
	t := (radius - seg.radiusStart) / seg.width
	return g.lenScale * (seg.arcStart + hermite(t, seg.arcDelta, seg.width*seg.arcDerStart, seg.width*seg.arcDerEnd))
}

// ArcLengthAtAngle evaluates the arc length at an angle.
func (g *TrackGeometry) ArcLengthAtAngle(angleRadians float64) float64 {
	angle := clampf(angleRadians, 0, g.totalAngleRadians)
	if g.isConstantPitch {
		return constantArc(g.innerRadiusMm, g.innerRadiusMm+g.radialGrowthPerRadianMm*angle, g.radialGrowthPerRadianMm)
	}
	if angle == 0 {
		return 0
	}
	if angle == g.totalAngleRadians {
		return g.trackLengthMm
	}
	seg := g.segments[g.findSegment(angle, true)]
	t := invertHermite(angle-seg.angleStart, seg.angleDelta, seg.width*seg.angDerStart, seg.width*seg.angDerEnd)
	return g.lenScale * (seg.arcStart + hermite(t, seg.arcDelta, seg.width*seg.arcDerStart, seg.width*seg.arcDerEnd))
}

// RadialGrowthAtRadius is the local radial growth.
func (g *TrackGeometry) RadialGrowthAtRadius(radius float64) float64 {
	return g.radialGrowthPerRadianMm * g.shape(radius)
}

// TryProgressAtRadiusExtended evaluates progress even outside [ri, ro].
func (g *TrackGeometry) TryProgressAtRadiusExtended(radius float64) (float64, bool) {
	if math.IsNaN(radius) || math.IsInf(radius, 0) || radius <= 0 || math.IsInf(radius*radius, 0) {
		return math.NaN(), false
	}
	var progress float64
	if radius >= g.innerRadiusMm && radius <= g.outerRadiusMm {
		progress = g.ArcLengthAtRadius(radius) / g.trackLengthMm
	} else if g.isConstantPitch {
		progress = constantArc(g.innerRadiusMm, radius, g.radialGrowthPerRadianMm) / g.trackLengthMm
	} else if value, ok := g.tryExtendedIntegral(radius); ok {
		progress = value.length / g.trackLengthMm
	}
	if math.IsNaN(progress) || math.IsInf(progress, 0) {
		return math.NaN(), false
	}
	return progress, true
}

// TryAtProgressExtended evaluates radius/angle even outside [0, 1] progress.
func (g *TrackGeometry) TryAtProgressExtended(progress float64) (radius, angle float64, ok bool) {
	nan := math.NaN()
	if math.IsNaN(progress) || math.IsInf(progress, 0) || math.IsInf(progress*g.trackLengthMm, 0) {
		return nan, nan, false
	}
	if progress >= 0 && progress <= 1 {
		r, err := g.RadiusAtArcLength(progress * g.trackLengthMm)
		if err != nil {
			return nan, nan, false
		}
		return r, g.AngleAtRadius(r), true
	}
	if g.isConstantPitch {
		return g.tryConstantAtProgressExtended(progress)
	}

	outward := progress > 1
	target := progress * g.trackLengthMm
	anchor := g.innerRadiusMm
	if outward {
		anchor = g.outerRadiusMm
	}
	validRadius := anchor
	boundary := 0.0
	if outward {
		boundary = anchor + g.rnge
	}
	low, high := 0.0, 0.0
	bracketed := false
	for iteration := 0; iteration < 100; iteration++ {
		candidate := boundary
		integ, okInt := g.tryExtendedIntegral(candidate)
		if candidate <= 0 || !okInt {
			boundary = validRadius + (boundary-validRadius)/2
			if boundary == validRadius {
				return nan, nan, false
			}
			continue
		}
		reached := integ.length <= target
		if outward {
			reached = integ.length >= target
		}
		if reached {
			low = math.Min(validRadius, candidate)
			high = math.Max(validRadius, candidate)
			bracketed = true
			break
		}
		validRadius = candidate
		if outward {
			boundary = anchor + 2*(candidate-anchor)
		} else {
			boundary = candidate / 2
		}
		if math.IsInf(boundary, 0) || math.IsNaN(boundary) {
			return nan, nan, false
		}
	}
	if !bracketed {
		return nan, nan, false
	}
	radius = low + (high-low)/2
	for iteration := 0; iteration < 64; iteration++ {
		integ, okInt := g.tryExtendedIntegral(radius)
		if !okInt {
			return nan, nan, false
		}
		err := integ.length - target
		if math.Abs(err) <= math.Max(1, math.Abs(target))*4e-15 {
			return radius, integ.angle, true
		}
		if err > 0 {
			high = radius
		} else {
			low = radius
		}
		growth := g.RadialGrowthAtRadius(radius)
		derivative := hypot(radius, growth) / growth
		next := radius - err/derivative
		if math.IsNaN(next) || math.IsInf(next, 0) || next <= low || next >= high {
			next = low + (high-low)/2
		}
		if next == radius {
			return radius, integ.angle, true
		}
		radius = next
	}
	return nan, nan, false
}

func (g *TrackGeometry) tryConstantAtProgressExtended(progress float64) (radius, angle float64, ok bool) {
	nan := math.NaN()
	b := g.radialGrowthPerRadianMm
	prim := primitive(g.innerRadiusMm, b) + progress*(b*g.trackLengthMm)
	if math.IsNaN(prim) || math.IsInf(prim, 0) || prim <= 0 {
		return nan, nan, false
	}
	low := 0.0
	high := math.Min(math.Sqrt(prim)*math.Sqrt(2), prim/b)
	radius = high
	for iteration := 0; iteration < 64; iteration++ {
		err := primitive(radius, b) - prim
		if math.Abs(err) <= prim*4e-15 {
			angle := g.AngleAtRadius(radius)
			if math.IsNaN(angle) || math.IsInf(angle, 0) {
				return nan, nan, false
			}
			return radius, angle, true
		}
		if err > 0 {
			high = radius
		} else {
			low = radius
		}
		next := radius - err/hypot(radius, b)
		if math.IsNaN(next) || math.IsInf(next, 0) || next <= low || next >= high {
			next = low + (high-low)/2
		}
		if next == radius {
			return nan, nan, false
		}
		radius = next
	}
	return nan, nan, false
}

func (g *TrackGeometry) tryExtendedIntegral(radius float64) (integral, bool) {
	if math.IsNaN(radius) || math.IsInf(radius, 0) || radius <= 0 || math.IsInf(radius*radius, 0) {
		return integral{}, false
	}
	x := (radius - g.innerRadiusMm) / g.rnge
	if !positiveOnInterval(g.linear, g.quadratic, g.cubic, math.Min(0, x), math.Max(1, x)) {
		return integral{}, false
	}
	edge := g.outerRadiusMm
	if radius < g.innerRadiusMm {
		edge = g.innerRadiusMm
	}
	extra, err := g.integrateSafe(math.Min(edge, radius), math.Max(edge, radius), 1/g.radialGrowthPerRadianMm, false)
	if err != nil {
		return integral{}, false
	}
	var value integral
	if radius < g.innerRadiusMm {
		value = integral{-extra.length, -extra.angle, 0}
	} else {
		value = integral{g.trackLengthMm + extra.length, g.totalAngleRadians + extra.angle, 0}
	}
	if math.IsNaN(value.length) || math.IsInf(value.length, 0) || math.IsNaN(value.angle) || math.IsInf(value.angle, 0) {
		return integral{}, false
	}
	return value, true
}

func (g *TrackGeometry) integrate(first, last, inverseGrowth float64, thin bool) integral {
	evaluations := 0
	v, err := g.integrateAdaptive(first, last, inverseGrowth, thin, 0, &evaluations)
	if err != nil {
		panic(err)
	}
	return v
}

func (g *TrackGeometry) integrateSafe(first, last, inverseGrowth float64, thin bool) (integral, error) {
	evaluations := 0
	return g.integrateAdaptive(first, last, inverseGrowth, thin, 0, &evaluations)
}

func (g *TrackGeometry) integrateAdaptive(first, last, inverseGrowth float64, thin bool,
	depth int, evaluations *int) (integral, error) {
	*evaluations += 3
	if *evaluations > 32768 {
		return integral{}, errPitchResolution
	}
	whole := g.quadrature(first, last, inverseGrowth, thin)
	if math.IsNaN(whole.length) || math.IsInf(whole.length, 0) || math.IsNaN(whole.angle) || math.IsInf(whole.angle, 0) {
		return integral{}, errPitchResolution
	}
	middle := first + (last-first)/2
	left := g.quadrature(first, middle, inverseGrowth, thin)
	right := g.quadrature(middle, last, inverseGrowth, thin)
	split := left.add(right)
	if math.Abs(split.length-whole.length) <= math.Abs(split.length)*8e-15 &&
		math.Abs(split.angle-whole.angle) <= math.Abs(split.angle)*8e-15 {
		return split, nil
	}
	if depth >= 24 || middle == first || middle == last {
		return integral{}, errPitchResolution
	}
	l, err := g.integrateAdaptive(first, middle, inverseGrowth, thin, depth+1, evaluations)
	if err != nil {
		return integral{}, err
	}
	r, err := g.integrateAdaptive(middle, last, inverseGrowth, thin, depth+1, evaluations)
	if err != nil {
		return integral{}, err
	}
	return l.add(r), nil
}

func (g *TrackGeometry) quadrature(first, last, inverseGrowth float64, thin bool) integral {
	middle := first + (last-first)/2
	half := (last - first) / 2
	length, angle, derivative := 0.0, 0.0, 0.0
	for i := 0; i < len(nodes); i++ {
		for sign := -1; sign <= 1; sign += 2 {
			radius := middle + float64(sign)*half*nodes[i]
			inverseShape := 1 / g.shape(radius)
			radial := radius * inverseShape
			z := inverseGrowth * radial
			ds := radial
			if !thin {
				ds = hypot(1, z)
			}
			length += weights[i] * ds
			angle += weights[i] * inverseShape * inverseGrowth
			if !thin {
				derivative += weights[i] * radial * (z / ds)
			}
		}
	}
	return integral{half * length, half * angle, half * derivative}
}

func (g *TrackGeometry) buildSegments(first, last, inverseGrowth float64, segments *[]segment, depth int) error {
	if len(*segments) >= 32768 {
		return errPitchResolution
	}
	width := last - first
	middle := first + width/2
	whole := g.quadrature(first, last, inverseGrowth, false)
	left := g.quadrature(first, middle, inverseGrowth, false)
	right := g.quadrature(middle, last, inverseGrowth, false)
	split := left.add(right)
	a0 := inverseGrowth / g.shape(first)
	a1 := inverseGrowth / g.shape(last)
	s0 := hypot(1, first*a0)
	s1 := hypot(1, last*a1)
	const angleTolerance = 5e-10
	lengthTolerance := angleTolerance * g.innerRadiusMm
	accepted := math.Abs(split.angle-whole.angle) < angleTolerance/16 &&
		math.Abs(split.length-whole.length) < lengthTolerance/16
	for i := 1; accepted && i <= 3; i++ {
		t := float64(i) / 4.0
		actual := left
		if i != 2 {
			actual = g.quadrature(first, first+width*t, inverseGrowth, false)
		}
		accepted = math.Abs(hermite(t, split.angle, width*a0, width*a1)-actual.angle) < angleTolerance &&
			math.Abs(hermite(t, split.length, width*s0, width*s1)-actual.length) < lengthTolerance
	}
	if !accepted && depth < 24 && middle != first && middle != last {
		if err := g.buildSegments(first, middle, inverseGrowth, segments, depth+1); err != nil {
			return err
		}
		return g.buildSegments(middle, last, inverseGrowth, segments, depth+1)
	}
	if !accepted || math.IsNaN(split.length) || math.IsInf(split.length, 0) ||
		math.IsNaN(split.angle) || math.IsInf(split.angle, 0) || split.length <= 0 {
		return errPitchResolution
	}
	*segments = append(*segments, segment{
		radiusStart: first, width: width,
		arcDelta: split.length, angleDelta: split.angle,
		arcDerStart: s0, arcDerEnd: s1,
		angDerStart: a0, angDerEnd: a1,
	})
	return nil
}

func (g *TrackGeometry) findRadiusSegment(radius float64) int {
	low, high := 0, len(g.segments)-1
	for low < high {
		middle := (low + high + 1) / 2
		if g.segments[middle].radiusStart <= radius {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return low
}

func (g *TrackGeometry) findSegment(value float64, byAngle bool) int {
	low, high := 0, len(g.segments)-1
	for low < high {
		middle := (low + high + 1) / 2
		start := g.segments[middle].arcStart
		if byAngle {
			start = g.segments[middle].angleStart
		}
		if start <= value {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return low
}

func hermite(t, delta, derivativeStart, derivativeEnd float64) float64 {
	return t * (derivativeStart + t*(3*delta-2*derivativeStart-derivativeEnd+
		t*(derivativeStart+derivativeEnd-2*delta)))
}

func invertHermite(value, delta, derivativeStart, derivativeEnd float64) float64 {
	t := clampf(value/delta, 0, 1)
	quadratic := 3*delta - 2*derivativeStart - derivativeEnd
	cubic := derivativeStart + derivativeEnd - 2*delta
	for i := 0; i < 4; i++ {
		err := t*(derivativeStart+t*(quadratic+t*cubic)) - value
		derivative := derivativeStart + t*(2*quadratic+3*t*cubic)
		t = clampf(t-err/derivative, 0, 1)
	}
	return t
}

func (g *TrackGeometry) shape(radius float64) float64 {
	return polynomial(g.linear, g.quadratic, g.cubic, (radius-g.innerRadiusMm)/g.rnge)
}

func polynomial(linear, quadratic, cubic, x float64) float64 {
	return ((cubic*x+quadratic)*x+linear)*x + 1
}

func positiveOnInterval(linear, quadratic, cubic, first, last float64) bool {
	positive := func(x float64) bool {
		value := polynomial(linear, quadratic, cubic, x)
		return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
	}
	if !positive(first) || !positive(last) {
		return false
	}
	scale := math.Max(math.Abs(linear), math.Max(math.Abs(quadratic), math.Abs(cubic)))
	if scale == 0 {
		return true
	}
	a := 3 * (cubic / scale)
	b := 2 * (quadratic / scale)
	c := linear / scale
	check := func(x float64) bool {
		return !(x > first && x < last) || positive(x)
	}
	if a == 0 {
		return b == 0 || check(-c/b)
	}
	discriminant := b*b - 4*a*c
	if discriminant < 0 {
		return true
	}
	q := -0.5 * (b + math.Copysign(math.Sqrt(discriminant), b))
	if q == 0 {
		return check(-b / (2 * a))
	}
	return check(q/a) && check(c/q)
}

func hypot(first, second float64) float64 {
	first = math.Abs(first)
	second = math.Abs(second)
	maximum := math.Max(first, second)
	if maximum == 0 {
		return 0
	}
	ratio := math.Min(first, second) / maximum
	return maximum * math.Sqrt(1+ratio*ratio)
}

func addCompensated(sum, errorTerm, value float64) (newSum, newError float64) {
	adjusted := value - errorTerm
	next := sum + adjusted
	return next, (next - sum) - adjusted
}

func constantArc(first, second, b float64) float64 {
	return (primitive(second, b) - primitive(first, b)) / b
}

func primitive(radius, b float64) float64 {
	return 0.5 * (radius*math.Sqrt(radius*radius+b*b) + b*b*math.Asinh(radius/b))
}

func clampf(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
