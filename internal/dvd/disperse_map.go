package dvd

import (
	"fmt"
	"math"
)

// This file implements the 3-tone mapper: the picture conversion that decides
// which sectors hold data. Per the design:
//   - black = clustered byte 0xA5 areas, white = dispersed byte 0x92 areas
//     (the two existing dispersion textures),
//   - gray = sectors containing data (scrambled content reads mid-gray).
// A sector becomes data-eligible when its codewords are (nearly) all in the
// gray luminance band [t1,t2], or when it lies deep inside a large pure-black
// or pure-white area (morphological interior with a physical margin).

const (
	// Samples per sector for the tone statistics.
	mapSamplesPerSector = 8

	// Polar erosion grid resolution: radial cells of ~0.05 mm, 256 arc bins
	// per turn. Used only for the interior-of-flat-area morphology.
	erosionArcBins  = 256
	erosionMmPerRad = 0.05
)

// disperseToneMap is the sampled tone model of the disc.
type disperseToneMap struct {
	image   *rasterImage
	spiral  *archimedeanSpiral
	options imageMappingOptions

	sectorArcMm float64

	// Per-sector luminance samples (in physical track order).
	sectorSamples  [][]byte
	alphaThreshold byte
	lumThreshold   byte

	// Polar erosion grid: turns × arcs, cell center luminance.
	gridTurns int
	gridArcs  int
	turnRadii []float64 // radius at each turn (center)
	gridLum   []byte    // turns*arcs
}

// buildDisperseToneMap samples the source image over the disc.
func buildDisperseToneMap(image *rasterImage, options imageMappingOptions) (*disperseToneMap, error) {
	if image == nil {
		return nil, newArgumentErrorParam("Value cannot be null.", "image")
	}
	m := &disperseToneMap{image: image, options: options}
	m.alphaThreshold = options.AlphaThreshold
	m.lumThreshold = options.LuminanceThreshold
	if m.lumThreshold == 0 {
		m.lumThreshold = 128
	}
	bitLengthMm := options.ChannelBitLengthNm * 1e-6
	trackLengthMm := float64(options.TotalSectors) * dvdChannelBitsPerSector * bitLengthMm
	spiral, err := archimedeanSpiralCreate(options.InnerRadiusMm, options.OuterRadiusMm,
		trackLengthMm, options.PitchLinear, options.PitchQuadratic, options.PitchCubic)
	if err != nil {
		return nil, err
	}
	m.spiral = spiral
	m.sectorArcMm = dvdChannelBitsPerSector * bitLengthMm

	sectors := int(options.TotalSectors)
	m.sectorSamples = make([][]byte, sectors)
	samplingOuter := options.samplingOuterRadiusMm()
	for sector := 0; sector < sectors; sector++ {
		samples := make([]byte, mapSamplesPerSector)
		for k := 0; k < mapSamplesPerSector; k++ {
			// Sample at the codeword-center positions across the sector's arc.
			channelBit := float64(sector)*dvdChannelBitsPerSector +
				(float64(k)+0.5)*dvdChannelBitsPerSector/mapSamplesPerSector
			arcMm := channelBit * bitLengthMm
			radius, trackAngle := spiral.atArcLengthFast(arcMm)
			polarAngle := -trackAngle // clockwise, like the writer
			x := radius * math.Cos(polarAngle)
			y := radius * math.Sin(polarAngle)
			// Image y-down vs physical y-up (the engine flips; a tone map is
			// mirror-invariant for luminance, but keep the exact mapping).
			sample := image.sampleDisc(x, -y, samplingOuter, true)
			samples[k] = sample.Luminance
			if sample.Alpha < m.alphaThreshold {
				samples[k] = 255 // unconstrained reads as white
			}
		}
		m.sectorSamples[sector] = samples
	}
	if err := m.buildErosionGrid(); err != nil {
		return nil, err
	}
	return m, nil
}

// buildErosionGrid samples the source image on a polar grid aligned to the
// spiral turns, for the flat-area interior morphology.
func (m *disperseToneMap) buildErosionGrid() error {
	samplingOuter := m.options.samplingOuterRadiusMm()
	// Turn count: radial pitch from the spiral.
	innerR := m.options.InnerRadiusMm
	outerR := m.options.OuterRadiusMm
	// Approximate pitch from the spiral's total angle.
	totalAngle := m.spiral.totalAngleRadians()
	turns := int(math.Ceil(totalAngle / (2 * math.Pi)))
	if turns < 1 {
		return fmt.Errorf("the track geometry has no complete turns")
	}
	// Radial cells of ~0.05mm: 8 turns per cell is too coarse for the margin;
	// instead keep one cell per erosionMmPerRad mm.
	radialCells := int(math.Ceil((outerR - innerR) / erosionMmPerRad))
	if radialCells < 1 || radialCells > 100000 {
		return fmt.Errorf("erosion grid radial size out of range")
	}
	m.gridTurns = radialCells
	m.gridArcs = erosionArcBins
	m.turnRadii = make([]float64, radialCells)
	m.gridLum = make([]byte, radialCells*erosionArcBins)
	for t := 0; t < radialCells; t++ {
		r := innerR + (float64(t)+0.5)*(outerR-innerR)/float64(radialCells)
		m.turnRadii[t] = r
		// Arc length at this radius → angle of the spiral there.
		arc := m.spiral.geometry.ArcLengthAtRadius(r)
		angle0 := m.spiral.angleAtArcLength(arc)
		for a := 0; a < erosionArcBins; a++ {
			// Cell center angle within the turn.
			angle := angle0 + (float64(a)+0.5)*(2*math.Pi)/erosionArcBins
			x := r * math.Cos(-angle)
			y := r * math.Sin(-angle)
			sample := m.image.sampleDisc(x, -y, samplingOuter, true)
			lum := sample.Luminance
			if sample.Alpha < m.alphaThreshold {
				lum = 255
			}
			m.gridLum[t*erosionArcBins+a] = lum
		}
	}
	return nil
}

// sectorClass computes the per-cell tone class under a gray band [t1,t2].
func (m *disperseToneMap) sectorToneStats(t1, t2 byte, sector int) (gray, white, black int) {
	for _, lum := range m.sectorSamples[sector] {
		switch {
		case lum >= t1 && lum <= t2:
			gray++
		case lum >= m.lumThreshold:
			white++
		default:
			black++
		}
	}
	return
}

// erodedInteriors computes, per grid cell, whether the cell is deep inside a
// pure-white (or pure-black) region: all cells within marginMm share the tone.
// Returns two masks (whiteInterior, blackInterior) of gridTurns*gridArcs.
func (m *disperseToneMap) erodedInteriors(marginMm float64) ([]bool, []bool) {
	turns, arcs := m.gridTurns, m.gridArcs
	whiteInt := make([]bool, turns*arcs)
	blackInt := make([]bool, turns*arcs)

	// Radial erosion per arc column: cell (t,a) is radially-interior-white iff
	// all turns with |radius - r_t| <= marginMm are pure white at column a.
	for _, wantWhite := range []bool{true, false} {
		target := whiteInt
		if !wantWhite {
			target = blackInt
		}
		// notPure[t][a]: cell is not the target tone.
		notPure := make([]uint8, turns*arcs)
		for t := 0; t < turns; t++ {
			for a := 0; a < arcs; a++ {
				lum := m.gridLum[t*arcs+a]
				pure := lum >= m.lumThreshold
				if wantWhite != pure {
					notPure[t*arcs+a] = 1
				}
			}
		}
		// Per-column prefix sums over turns for range counting.
		for a := 0; a < arcs; a++ {
			prefix := make([]int, turns+1)
			for t := 0; t < turns; t++ {
				prefix[t+1] = prefix[t] + int(notPure[t*arcs+a])
			}
			for t := 0; t < turns; t++ {
				r := m.turnRadii[t]
				// Turns window: |r' - r| <= marginMm (radii are monotone in t).
				lo := sortSearchFloat64(m.turnRadii, r-marginMm)
				hi := sortSearchFloat64(m.turnRadii, r+marginMm)
				// hi is the first index > r+margin; window [lo, hi).
				if prefix[hi]-prefix[lo] == 0 {
					target[t*arcs+a] = true
				}
			}
		}
		// Tangential erosion: neighboring arc bins ±1 (the arc cells are
		// ~1 mm apart; the margin is smaller, so ±1 is the conservative cover).
		src := target
		eroded := make([]bool, turns*arcs)
		for t := 0; t < turns; t++ {
			for a := 0; a < arcs; a++ {
				if !src[t*arcs+a] {
					continue
				}
				ok := true
				for _, da := range []int{-1, 0, 1} {
					aa := (a + da + arcs) % arcs
					if !src[t*arcs+aa] {
						ok = false
						break
					}
				}
				eroded[t*arcs+a] = ok
			}
		}
		if wantWhite {
			whiteInt = eroded
		} else {
			blackInt = eroded
		}
	}
	return whiteInt, blackInt
}

func sortSearchFloat64(values []float64, target float64) int {
	lo, hi := 0, len(values)
	for lo < hi {
		mid := (lo + hi) / 2
		if values[mid] <= target {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// sectorInterior looks up the erosion masks at the sector's center position;
// white masks use the white margin, black masks the black margin.
func (m *disperseToneMap) sectorInterior2(sector int, whiteIntW, blackIntW, whiteIntB, blackIntB []bool) (whiteInterior, blackInterior bool) {
	arcMm := (float64(sector) + 0.5) * m.sectorArcMm
	radius := m.spiral.radiusAtArcLengthFast(arcMm)
	t := sortSearchFloat64(m.turnRadii, radius)
	if t >= m.gridTurns {
		t = m.gridTurns - 1
	}
	if t > 0 && m.turnRadii[t] > radius {
		t--
	}
	trackAngle := m.spiral.angleAtArcLength(arcMm)
	turn := math.Floor(trackAngle / (2 * math.Pi))
	arc := trackAngle - turn*2*math.Pi
	a := int(math.Floor(arc / (2 * math.Pi) * float64(m.gridArcs)))
	if a >= m.gridArcs {
		a = m.gridArcs - 1
	}
	return whiteIntW[t*m.gridArcs+a], blackIntB[t*m.gridArcs+a]
}

// classifySectors returns the per-sector eligibility under the given band and
// margins: eligible sectors may hold data.
func (m *disperseToneMap) classifySectors(t1, t2 byte, marginWhiteMm, marginBlackMm float64) []bool {
	whiteInt, blackInt := m.erodedInteriors(marginWhiteMm)
	whiteIntB, blackIntB := m.erodedInteriors(marginBlackMm)
	sectors := len(m.sectorSamples)
	eligible := make([]bool, sectors)
	for sector := 0; sector < sectors; sector++ {
		gray, white, black := m.sectorToneStats(t1, t2, sector)
		total := gray + white + black
		if gray*100 >= 98*total {
			eligible[sector] = true
			continue
		}
		if white == total || black == total {
			wi, bi := m.sectorInterior2(sector, whiteInt, blackInt, whiteIntB, blackIntB)
			if (white == total && wi) || (black == total && bi) {
				eligible[sector] = true
			}
		}
	}
	return eligible
}
