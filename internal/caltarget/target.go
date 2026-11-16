package caltarget

import (
	"fmt"
	"math"
)

// wideBands are the v13 coarse-target dark bands in thousandths of the
// drawable track (CalibrationWideBandPattern.AlternatingBands).
var wideBands = [][2]int{{35, 170}, {305, 510}, {705, 955}}

// band describes one dark interval in progress space.
type band struct {
	Start float64
	End   float64 // exclusive
}

// angleSweepStation ports CalibrationAngleSweepStation.
type angleSweepStation struct {
	StartProgress        float64
	EndProgress          float64
	StartVirtualRadiusMm float64
	EndVirtualRadiusMm   float64
	trackLength          float64
	squaredSlope         float64
}

func (s *angleSweepStation) virtualRadiusAt(progress float64) float64 {
	return math.Sqrt(s.StartVirtualRadiusMm*s.StartVirtualRadiusMm +
		(progress-s.StartProgress)*s.squaredSlope)
}

// PhaseAt is the unwrapped clock phase in radians, zero at station start.
func (s *angleSweepStation) PhaseAt(progress float64) float64 {
	return 2 * s.trackLength * (progress - s.StartProgress) /
		(s.StartVirtualRadiusMm + s.virtualRadiusAt(progress))
}

// sweepTone ports CalibrationAngleSweepStation.Tone.
func sweepTone(radians float64) byte {
	if r := math.Remainder(radians, 2*math.Pi); r >= 0 && r < math.Pi {
		return 0
	}
	return 255
}

// Target is the current-schema calibration target (v13 coarse, v16 boundary;
// the v14 fine pattern is the embedded reference PNG instead).
type Target struct {
	Params Parameters

	// Geometry with pitch zeroed for coarse targets (ParametersForGeometry).
	geometry *Geometry
	// Bands in full-track progress space.
	bands []band
	// Band radii (inner, outer) in mm.
	bandInner []float64
	bandOuter []float64
	stations  []angleSweepStation

	drawingInnerMm float64
	drawingOuterMm float64
}

// NewTarget ports the pattern-relevant parts of CalibrationTarget.Create for
// the coarse and boundary stages.
func NewTarget(p Parameters, stage Stage) (*Target, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	t := &Target{Params: p}
	geometryParams := p
	if stage == StageCoarse {
		// ParametersForGeometry: coarse targets are always constant pitch.
		geometryParams.PitchLinear, geometryParams.PitchQuadratic, geometryParams.PitchCubic = 0, 0, 0
	}
	geometry, err := NewGeometry(geometryParams)
	if err != nil {
		return nil, err
	}
	t.geometry = geometry

	t.drawingInnerMm = p.InnerRadiusMm
	if p.UsableSectors != 0 {
		r, _ := geometry.AtProgress(float64(p.UsableSectors) / float64(p.Sectors))
		t.drawingOuterMm = r
	} else {
		t.drawingOuterMm = p.OuterRadiusMm
	}

	if stage == StageCoarse {
		if err := t.buildCoarse(); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// DrawingInnerRadiusMm is the pattern's inner clip radius.
func (t *Target) DrawingInnerRadiusMm() float64 { return t.drawingInnerMm }

// DrawingOuterRadiusMm is the pattern's outer clip radius.
func (t *Target) DrawingOuterRadiusMm() float64 { return t.drawingOuterMm }

// buildCoarse sets up the v13 wide bands and angle-sweep stations.
func (t *Target) buildCoarse() error {
	p := t.Params
	// CalibrationWideBandPattern.Supports.
	if p.Kind == CD {
		if p.LinearDensity != CdStandardLinearVelocityMmPerSec ||
			p.Sectors < 315000 || p.Sectors > 382500 ||
			p.OuterRadiusMm <= 40 || p.OuterRadiusMm > 60 {
			return fmt.Errorf("the coarse wide-band target needs a 70–85 minute, 1200 mm/s standard 12 cm CD")
		}
	} else {
		if p.Sectors <= 0 || p.Sectors > math.MaxUint32 || p.OuterRadiusMm > 60 {
			return fmt.Errorf("the coarse wide-band target does not support these disc parameters")
		}
		unit := int64(1)
		if p.Kind == BluRay {
			unit = BluRaySectorsPerCluster
		}
		length := p.OutputSectors()
		mediaUnit := unit
		if p.Kind == DVD {
			mediaUnit = 16
		}
		if p.OutputSectors()%mediaUnit != 0 ||
			length/unit < 23 || float64(length) < .30*float64(p.Sectors) {
			return fmt.Errorf("the coarse wide-band target needs complete DVD ECC blocks / Blu-ray clusters and enough drawing range to resolve all six edges")
		}
	}

	// Band progress ranges. CD uses plain fractions; DVD and BD use
	// sector/cluster-quantized ranges divided by the total sector count.
	t.bands = make([]band, len(wideBands))
	t.bandInner = make([]float64, len(wideBands))
	t.bandOuter = make([]float64, len(wideBands))
	for i, wb := range wideBands {
		var start, end float64
		if p.Kind == CD {
			start = float64(wb[0]) / 1000.0
			end = float64(wb[1]) / 1000.0
		} else {
			u := int64(1)
			if p.Kind == BluRay {
				u = BluRaySectorsPerCluster
			}
			units := p.OutputSectors() / u
			startUnits := (units*int64(wb[0]) + 999) / 1000
			endUnits := (units*int64(wb[1]) + 999) / 1000
			start = float64(u*startUnits) / float64(p.Sectors)
			end = float64(u*endUnits) / float64(p.Sectors)
		}
		t.bands[i] = band{start, end}
		innerR, _ := t.geometry.AtProgress(start)
		outerR, _ := t.geometry.AtProgress(end)
		t.bandInner[i] = innerR
		t.bandOuter[i] = outerR
	}

	// CalibrationWideBandPattern.SupportsAngleSweep.
	if p.InnerRadiusMm < 7.5 || p.OuterRadiusMm > 60 || p.OuterRadiusMm <= p.InnerRadiusMm {
		return fmt.Errorf("the disc radii do not support the coarse angle-sweep target")
	}
	qLength := float64(p.OutputSectors()) / float64(p.Sectors)
	r2 := p.InnerRadiusMm * p.InnerRadiusMm
	span := p.OuterRadiusMm*p.OuterRadiusMm - r2
	turns := p.TrackLengthMm() / (math.Pi * (p.InnerRadiusMm + p.OuterRadiusMm))
	if !finite(turns) || turns*qLength < 300 || turns > 300000 {
		return fmt.Errorf("the disc capacity or drawing range does not support the coarse angle-sweep target")
	}
	for i := range wideBands {
		q0 := 0.0
		inner := math.Sqrt(r2 + span*(q0+qLength*float64(wideBands[i][0])/1000.0))
		outer := math.Sqrt(r2 + span*(q0+qLength*float64(wideBands[i][1])/1000.0))
		if outer-inner < 4.15 {
			return fmt.Errorf("the coarse target's bands are too narrow for the angle-sweep reader")
		}
	}

	// Stations.
	t.stations = make([]angleSweepStation, len(wideBands))
	for i, b := range t.bands {
		guard := .14 * (b.End - b.Start)
		start, end := b.Start+guard, b.End-guard
		offset := .65
		if i%2 != 0 {
			offset = -.65
		}
		startR, _ := t.geometry.AtProgress(start)
		endR, _ := t.geometry.AtProgress(end)
		innerV, outerV := startR-offset, endR+offset
		t.stations[i] = angleSweepStation{
			StartProgress:        start,
			EndProgress:          end,
			StartVirtualRadiusMm: innerV,
			EndVirtualRadiusMm:   outerV,
			trackLength:          p.TrackLengthMm(),
			squaredSlope:         (outerV - innerV) * (outerV + innerV) / (end - start),
		}
	}
	return nil
}

// BoundaryPhaseAtSourceRadius ports CalibrationTarget.BoundaryPhaseAtSourceRadius (v16).
func (t *Target) BoundaryPhaseAtSourceRadius(radius float64) float64 {
	return math.Pi / 2 * math.Sin(6*math.Pi*(radius-t.drawingInnerMm)/(t.drawingOuterMm-t.drawingInnerMm))
}

// Sample ports CalibrationTarget.Sample for the coarse and boundary stages.
// Coordinates are millimetres from the disc centre, x right, y down.
func (t *Target) Sample(xMm, yMm float64) byte {
	if !finite(xMm) || !finite(yMm) {
		return 255
	}
	radiusSquared := xMm*xMm + yMm*yMm
	if radiusSquared < t.drawingInnerMm*t.drawingInnerMm-1e-10 ||
		radiusSquared > t.drawingOuterMm*t.drawingOuterMm+1e-10 {
		return 255
	}
	if len(t.bands) == 0 {
		// v16 wavy boundary.
		phase := t.BoundaryPhaseAtSourceRadius(math.Sqrt(radiusSquared))
		if xMm*math.Cos(phase)+yMm*math.Sin(phase) < 0 {
			return 0
		}
		return 255
	}
	// v13 coarse wide bands with angle-sweep pilots.
	radius := math.Sqrt(radiusSquared)
	for i := range t.bands {
		if radius >= t.bandInner[i] && radius < t.bandOuter[i] {
			station := &t.stations[i]
			q := t.geometry.ProgressAtRadius(radius)
			if q >= station.StartProgress && q < station.EndProgress {
				phase := math.Atan2(yMm, xMm) - t.geometry.AngleAtRadius(radius) + station.PhaseAt(q)
				return sweepTone(phase)
			}
			return 0
		}
	}
	return 255
}
