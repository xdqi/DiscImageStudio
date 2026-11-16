package dvd

import (
	"fmt"
	"math"
	"slices"
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

	// Per-sector luminance samples along the whole arc (~one per image
	// pixel), sorted ascending for binary-search statistics. Flat storage,
	// sectorSampleStride per sector.
	sectorSamples      []byte
	sectorSampleStride int
	alphaThreshold     byte
	lumThreshold       byte

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
	samplingOuter := options.samplingOuterRadiusMm()
	// Sample along each sector's arc at twice the image-pixel rate (Nyquist):
	// a 1-px stroke crossed at any angle occupies >= 1 px of arc length and
	// must always catch a sample — a sparser walk lets data sectors slip onto
	// thin picture features (measured: 262 leaked sectors at ~1 px spacing).
	pxPerMm := (float64(minInt(image.width, image.height)) - 1) / (2 * samplingOuter)
	steps := int(math.Ceil(2*m.sectorArcMm*pxPerMm)) + 1
	m.sectorSampleStride = steps
	m.sectorSamples = make([]byte, sectors*steps)
	for sector := 0; sector < sectors; sector++ {
		row := m.sectorSamples[sector*steps : (sector+1)*steps]
		arc0 := float64(sector) * m.sectorArcMm
		for k := 0; k < steps; k++ {
			arcMm := arc0 + float64(k)*m.sectorArcMm/float64(steps)
			radius, trackAngle := spiral.atArcLengthFast(arcMm)
			// The same mapping the writer uses (CW, start angle 0):
			// sampleDisc's y-down flip turns math y-up into image rows.
			polarAngle := -trackAngle
			x := radius * math.Cos(polarAngle)
			y := radius * math.Sin(polarAngle)
			sample := image.sampleDisc(x, y, samplingOuter, true)
			row[k] = sample.Luminance
			if sample.Alpha < m.alphaThreshold {
				row[k] = 255 // unconstrained reads as white
			}
		}
		slices.Sort(row)
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
			// Cell center angle within the turn is converted with the
			// writer's mapping (CW, start angle 0), same as the arcs.
			x := r * math.Cos(-angle)
			y := r * math.Sin(-angle)
			sample := m.image.sampleDisc(x, y, samplingOuter, true)
			lum := sample.Luminance
			if sample.Alpha < m.alphaThreshold {
				lum = 255
			}
			m.gridLum[t*erosionArcBins+a] = lum
		}
	}
	return nil
}

// sectorToneStats computes the per-cell tone class under a gray band [t1,t2]:
// gray = in-band, white = above the band and at/above the luminance threshold,
// black = the rest. The per-sector samples are sorted, so counts are binary
// searches.
func (m *disperseToneMap) sectorToneStats(t1, t2 byte, sector int) (gray, white, black int) {
	row := m.sectorSamples[sector*m.sectorSampleStride : (sector+1)*m.sectorSampleStride]
	// White means "not gray and at/above the luminance threshold" (the gray
	// band wins for in-band tones).
	whiteLow := int(m.lumThreshold)
	if int(t2)+1 > whiteLow {
		whiteLow = int(t2) + 1
	}
	search := func(v int) int {
		if v > 255 {
			return len(row)
		}
		i, _ := slices.BinarySearch(row, byte(v))
		return i
	}
	lo := search(int(t1))
	hi := search(int(t2) + 1)
	ws := search(whiteLow)
	gray = hi - lo
	white = len(row) - ws
	black = len(row) - gray - white
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
	sectors := len(m.sectorSamples) / m.sectorSampleStride
	eligible := make([]bool, sectors)
	for sector := 0; sector < sectors; sector++ {
		gray, white, black := m.sectorToneStats(t1, t2, sector)
		total := gray + white + black
		// A single off-band sample means the sector's arc grazes a picture
		// feature: protect it. (Tolerating even 1 in N samples visibly
		// frays thin strokes.)
		if gray == total {
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
