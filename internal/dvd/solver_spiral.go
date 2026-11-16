package dvd

import (
	"math"

	"disc-image-studio/internal/trackgeom"
)

// archimedeanSpiral ports ArchimedeanSpiral: a thin wrapper over the shared
// track geometry with the C# factory's exact validation and messages.
type archimedeanSpiral struct {
	geometry *trackgeom.TrackGeometry
}

func archimedeanSpiralCreate(innerRadiusMm, outerRadiusMm, trackLengthMm, pitchLinear, pitchQuadratic, pitchCubic float64) (*archimedeanSpiral, error) {
	if math.IsNaN(innerRadiusMm) || math.IsInf(innerRadiusMm, 0) || innerRadiusMm <= 0 {
		return nil, &argumentOutOfRangeError{param: "innerRadiusMm", msg: "Inner radius must be positive."}
	}
	if math.IsNaN(outerRadiusMm) || math.IsInf(outerRadiusMm, 0) || outerRadiusMm <= innerRadiusMm {
		return nil, &argumentOutOfRangeError{param: "outerRadiusMm", msg: "Outer radius must exceed inner radius."}
	}
	if math.IsNaN(trackLengthMm) || math.IsInf(trackLengthMm, 0) || trackLengthMm <= outerRadiusMm-innerRadiusMm {
		return nil, &argumentOutOfRangeError{param: "trackLengthMm", msg: "Track length must exceed the radial distance between the two radii."}
	}
	if err := validatePitchCoefficients(pitchLinear, pitchQuadratic, pitchCubic); err != nil {
		return nil, err
	}
	geometry, err := trackgeom.New(innerRadiusMm, outerRadiusMm, trackLengthMm, pitchLinear, pitchQuadratic, pitchCubic)
	if err != nil {
		// Numerical pitch-resolution failure: the C# original throws an
		// ArgumentException or PitchResolutionException with this text.
		return nil, newArgumentError("The pitch coefficients cannot be resolved at this track length.")
	}
	return &archimedeanSpiral{geometry: geometry}, nil
}

func validatePitchCoefficients(linear, quadratic, cubic float64) error {
	if math.IsNaN(linear) || math.IsInf(linear, 0) || math.IsNaN(quadratic) || math.IsInf(quadratic, 0) ||
		math.IsNaN(cubic) || math.IsInf(cubic, 0) || !pitchPositiveOnInterval(linear, quadratic, cubic, 0, 1) {
		return &argumentOutOfRangeError{param: "linear", msg: "Pitch coefficients must be finite and 1 + c1*x + c2*x² + c3*x³ must stay positive for 0 <= x <= 1."}
	}
	return nil
}

// pitchPositiveOnInterval mirrors DvdTrackGeometry.PositiveOnInterval.
func pitchPositiveOnInterval(linear, quadratic, cubic, first, last float64) bool {
	positive := func(x float64) bool {
		value := pitchPolynomial(linear, quadratic, cubic, x)
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

func pitchPolynomial(linear, quadratic, cubic, x float64) float64 {
	return ((cubic*x+quadratic)*x+linear)*x + 1
}

func (s *archimedeanSpiral) trackLengthMm() float64 { return s.geometry.TrackLengthMm() }
func (s *archimedeanSpiral) isConstantPitch() bool  { return s.geometry.IsConstantPitch() }
func (s *archimedeanSpiral) innerRadiusMm() float64 { return s.geometry.InnerRadiusMm() }
func (s *archimedeanSpiral) outerRadiusMm() float64 { return s.geometry.OuterRadiusMm() }
func (s *archimedeanSpiral) radialGrowthPerRadianMm() float64 {
	return s.geometry.RadialGrowthPerRadianMm()
}
func (s *archimedeanSpiral) trackPitchMm() float64      { return s.geometry.TrackPitchMm() }
func (s *archimedeanSpiral) totalAngleRadians() float64 { return s.geometry.TotalAngleRadians() }

func (s *archimedeanSpiral) radiusAtArcLength(arcLengthMm float64) (float64, error) {
	if math.IsNaN(arcLengthMm) || math.IsInf(arcLengthMm, 0) || arcLengthMm < 0 || arcLengthMm > s.geometry.TrackLengthMm() {
		return 0, &argumentOutOfRangeError{param: "arcLengthMm"}
	}
	radius, err := s.geometry.RadiusAtArcLength(arcLengthMm)
	if err != nil {
		return 0, &argumentOutOfRangeError{param: "arcLengthMm"}
	}
	return radius, nil
}

func (s *archimedeanSpiral) radiusAtArcLengthFast(arcLengthMm float64) float64 {
	return s.geometry.RadiusAtArcLengthFast(arcLengthMm)
}

func (s *archimedeanSpiral) angleAtArcLength(arcLengthMm float64) float64 {
	return s.geometry.AngleAtArcLength(arcLengthMm)
}

func (s *archimedeanSpiral) atArcLengthFast(arcLengthMm float64) (radius, angle float64) {
	return s.geometry.AtArcLengthFast(arcLengthMm)
}

func (s *archimedeanSpiral) angleAtRadius(radiusMm float64) float64 {
	return s.geometry.AngleAtRadius(radiusMm)
}

// tryGetSampleReuseBounds ports ArchimedeanSpiral.TryGetSampleReuseBounds.
func (s *archimedeanSpiral) tryGetSampleReuseBounds(startAngle float64) (positionMargin, tangentError, accelerationBound float64, ok bool) {
	positionMargin = 0
	tangentError, accelerationBound = 0, 0
	inner := s.innerRadiusMm()
	outer := s.outerRadiusMm()
	growth := s.radialGrowthPerRadianMm()
	if inner < 10 || growth < 1e-6 || growth > 1e-3 ||
		math.IsNaN(startAngle) || math.IsInf(startAngle, 0) || math.Abs(startAngle)+s.totalAngleRadians() > 1_000_000 {
		return 0, 0, 0, false
	}
	const epsilon = 2.2204460492503131e-16
	if !s.isConstantPitch() {
		pos, tangent, accel, okBounds := s.geometry.TryGetFastSamplingBounds()
		if !okBounds {
			return 0, 0, 0, false
		}
		positionMargin = pos + 512*epsilon*outer*(1+math.Abs(startAngle))
		tangentError = tangent
		accelerationBound = accel
	} else {
		// The thin-spiral seed omits at most b/2*log(ro/ri) of arc length,
		// hence its radial error is <= b²/(2ri)*log(ro/ri). One Newton step
		// leaves <= error²/(2ri), since max F''/min F' <= 1/ri. Convert
		// radial error to position error through theta=(r-ri)/b.
		seedError := growth * growth / (2 * inner) * math.Log(outer/inner)
		inverseError := seedError * seedError / (2 * inner) * (1 + outer/growth)
		positionMargin = 2*inverseError +
			8192*epsilon*outer*outer/growth*math.Max(1, outer/inner) +
			256*epsilon*s.trackLengthMm() +
			512*epsilon*outer*(1+math.Abs(startAngle)+s.totalAngleRadians()) + 1e-8
		tangentError = growth/inner + growth*growth/(2*inner*inner)
		accelerationBound = 2 / inner
	}
	isFinitePosition := !math.IsNaN(positionMargin) && !math.IsInf(positionMargin, 0)
	isFiniteTangent := !math.IsNaN(tangentError) && !math.IsInf(tangentError, 0)
	isFiniteAcceleration := !math.IsNaN(accelerationBound) && !math.IsInf(accelerationBound, 0)
	return positionMargin, tangentError, accelerationBound,
		isFinitePosition && positionMargin < .01 &&
			isFiniteTangent && tangentError >= 0 &&
			isFiniteAcceleration && accelerationBound > 0
}
