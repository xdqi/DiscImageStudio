package bluray

import (
	"context"
	"errors"
	"math"
	"sort"

	"disc-image-studio/internal/core"
)

// LuminanceMapping converts an image sample into a post-scrambler payload
// byte. The built-in 3T/6T mapping is an image convention; optical polarity
// needs physical calibration.
type LuminanceMapping interface {
	Map(luminance byte, position PayloadBytePosition) byte
}

// PayloadBytePosition is a user byte's location after LDC interleaving and
// BIS multiplexing. Channel position is a nominal byte midpoint using the 2:3
// modulation rate, including dc-control insertion, sync and recording-unit
// run-in. Variable-length 17PP codewords mean this is not an exact channel-bit
// boundary or a predicted optical mark.
type PayloadBytePosition struct {
	Sector                    uint32
	ByteOffset                int
	PhysicalSectorNumber      uint32
	RecordingFrame            int
	LdcColumn                 int
	EccColumn                 int
	NominalChannelBitPosition float64
	TrackPoint                TrackPoint
}

// Run-length mapping bytes.
const (
	ThreeTByte = 0xAA
	SixTByte   = 0x44
)

// RunLengthMapping is the binary image mapping favouring the number of 3T or
// 6T runs in equal-length physical clusters. Values are post-scrambler payload
// bytes, not host bytes.
type RunLengthMapping struct {
	Invert bool
}

// NewRunLengthMapping ports the C# constructor.
func NewRunLengthMapping(invert bool) *RunLengthMapping { return &RunLengthMapping{Invert: invert} }

// Map ports BluRayRunLengthMapping.Map.
func (m *RunLengthMapping) Map(luminance byte, _ PayloadBytePosition) byte {
	useThreeT := core.BinaryLevelIndex(luminance) == 0
	if m.Invert {
		useThreeT = !useThreeT
	}
	if useThreeT {
		return ThreeTByte
	}
	return SixTByte
}

// mapLuminances maps a completed luminance span with the same exact 128
// threshold (scalar port of the SIMD loop).
func (m *RunLengthMapping) mapLuminances(luminances []byte) {
	dark := byte(ThreeTByte)
	light := byte(SixTByte)
	if m.Invert {
		dark, light = light, dark
	}
	for index := range luminances {
		if luminances[index] < 128 {
			luminances[index] = dark
		} else {
			luminances[index] = light
		}
	}
}

// payloadLayout holds the precomputed LDC/recording-frame channel layout.
var payloadLayout = createPayloadLayout()

type payloadLayoutData struct {
	channelOffsets [PayloadBytesPerCluster]float64
	physicalOrder  [PayloadBytesPerCluster]int
}

func createPayloadLayout() *payloadLayoutData {
	layout := &payloadLayoutData{}
	for sector := 0; sector < 32; sector++ {
		for index := 0; index < BytesPerSector; index++ {
			serial := sector*2052 + index
			column, row := serial/216, serial%216
			frame := 2*row + column%2
			ldcColumn := (column/2 + 152 - (row*3)%152) % 152
			eccColumn := ldcColumn + ldcColumn/38
			midpoint := eccColumn*8 + 4
			dc := 0
			if midpoint >= 25 {
				dc = 1 + (midpoint-25)/45
			}
			layout.channelOffsets[sector*BytesPerSector+index] =
				2760 + float64(frame*1932) + 30 + float64(midpoint+dc)*1.5
		}
	}
	for i := range layout.physicalOrder {
		layout.physicalOrder[i] = i
	}
	sort.SliceStable(layout.physicalOrder[:], func(a, b int) bool {
		return layout.channelOffsets[layout.physicalOrder[a]] < layout.channelOffsets[layout.physicalOrder[b]]
	})
	return layout
}

// payloadChannelOffsets ports BluRayImagePayloadMapping.PayloadChannelOffsets.
func payloadChannelOffsets(sector uint32, count int) []float64 {
	start := int(sector%SectorsPerCluster) * BytesPerSector
	return payloadLayout.channelOffsets[start : start+count]
}

// ImagePayloadMapping samples the source at each payload byte's nominal
// physical position, then delegates intensity conversion to an explicitly
// injected mapping strategy.
type ImagePayloadMapping struct {
	disc                       *DiscParameters
	sampleLuminance            func(x, y float64) byte
	mapping                    LuminanceMapping
	geometry                   *TrackGeometry
	supportsParallelGeneration bool

	raster      *Gray8Raster
	rasterScale float64

	clipRasterAnnulus    bool
	minimumRadiusSquared float64
	maximumRadiusSquared float64
	minimumRadius        float64
	maximumRadius        float64
}

// newImagePayloadMappingSampler ports the procedural-pattern constructor.
func newImagePayloadMappingSampler(disc *DiscParameters, sampleLuminance func(x, y float64) byte,
	mapping LuminanceMapping, allowParallelSampling bool) (*ImagePayloadMapping, error) {
	if disc == nil {
		return nil, argNull("disc")
	}
	if sampleLuminance == nil {
		return nil, argNull("sampleLuminance")
	}
	if mapping == nil {
		return nil, argNull("mapping")
	}
	if err := disc.Validate(); err != nil {
		return nil, err
	}
	geometry, err := NewTrackGeometry(disc)
	if err != nil {
		return nil, err
	}
	_, parallel := mapping.(*RunLengthMapping)
	return &ImagePayloadMapping{
		disc: disc, sampleLuminance: sampleLuminance, mapping: mapping, geometry: geometry,
		supportsParallelGeneration: allowParallelSampling && parallel,
	}, nil
}

// NewImagePayloadMapping ports the raster constructor with the default outer
// radius (disc.OutputOuterRadiusMm).
func NewImagePayloadMapping(disc *DiscParameters, raster *Gray8Raster,
	mapping LuminanceMapping) (*ImagePayloadMapping, error) {
	return newImagePayloadMappingRaster(disc, raster, mapping, nil)
}

func newImagePayloadMappingRaster(disc *DiscParameters, raster *Gray8Raster,
	mapping LuminanceMapping, imageOuterRadiusMm *float64) (*ImagePayloadMapping, error) {
	if disc == nil {
		return nil, argNull("disc")
	}
	if raster == nil {
		return nil, argNull("raster")
	}
	outerRadiusMm := disc.OutputOuterRadiusMm()
	if imageOuterRadiusMm != nil {
		outerRadiusMm = *imageOuterRadiusMm
	}
	if isNotFinite(outerRadiusMm) || outerRadiusMm <= 0 {
		return nil, argOutOfRange("imageOuterRadiusMm")
	}
	m, err := newImagePayloadMappingSampler(disc,
		func(x, y float64) byte {
			b, _ := raster.SampleDiscPoint(x, y, outerRadiusMm)
			return b
		}, mapping, true)
	if err != nil {
		return nil, err
	}
	m.raster = raster
	m.rasterScale = float64(maxInt(raster.Width, raster.Height)) / (2 * outerRadiusMm)
	return m, nil
}

// newImagePayloadMappingAnnulus ports the calibration constructor that clips
// the raster to a narrower recording annulus.
func newImagePayloadMappingAnnulus(disc *DiscParameters, raster *Gray8Raster,
	mapping LuminanceMapping, minimumRadiusSquared, maximumRadiusSquared float64) (*ImagePayloadMapping, error) {
	outer := disc.OuterRadiusMm
	m, err := newImagePayloadMappingRaster(disc, raster, mapping, &outer)
	if err != nil {
		return nil, err
	}
	if isNotFinite(minimumRadiusSquared) || minimumRadiusSquared < 0 ||
		isNotFinite(maximumRadiusSquared) || maximumRadiusSquared <= minimumRadiusSquared {
		return nil, argOutOfRange("maximumRadiusSquared")
	}
	m.clipRasterAnnulus = true
	m.minimumRadiusSquared = minimumRadiusSquared
	m.maximumRadiusSquared = maximumRadiusSquared
	m.minimumRadius = math.Sqrt(minimumRadiusSquared)
	m.maximumRadius = math.Sqrt(maximumRadiusSquared)
	return m, nil
}

// CanGenerateInParallel ports the internal C# method.
func (m *ImagePayloadMapping) CanGenerateInParallel(disc *DiscParameters) bool {
	return m.supportsParallelGeneration && m.disc == disc
}

// tryFillScrambledCluster ports the pixel-reuse fast path (scalar; the C#
// original is also scalar here — only the SIMD four-point sampling is omitted).
func (m *ImagePayloadMapping) tryFillScrambledCluster(disc *DiscParameters, firstSector uint32,
	destination []byte, ctx context.Context) (bool, error) {
	runLength, isRunLength := m.mapping.(*RunLengthMapping)
	if m.raster == nil || !isRunLength || disc != m.disc || !m.geometry.supportsPixelReuse() ||
		m.geometry.pixelReuseSafetyMarginMm() >= 1/(m.rasterScale*16) {
		return false, nil
	}
	if firstSector%32 != 0 || firstSector >= disc.TotalSectors || len(destination) != PayloadBytesPerCluster {
		return false, errors.New("A complete aligned image cluster is required.")
	}

	clusterBase := float64(int64(firstSector/32) * int64(ChannelBitsPerRecordingUnit))
	margin := m.geometry.pixelReuseSafetyMarginMm()
	mmPerBit := m.geometry.millimetresPerChannelBit()
	curvatureBound := m.geometry.pixelReuseCurvatureBound()
	tangentMargin := m.geometry.pixelReuseTangentMargin() +
		4*margin/m.disc.InnerRadiusMm + 64*2.2204460492503131e-16
	safeUntil := math.Inf(-1)
	luminance := byte(255)
	// LDC payload order jumps around the spiral. Walk physical positions in
	// ascending order, then scatter back to the original payload addresses.
	for index := 0; index < len(payloadLayout.physicalOrder); index++ {
		if (index & 1023) == 0 {
			if err := ctx.Err(); err != nil {
				return false, err
			}
		}
		payload := payloadLayout.physicalOrder[index]
		channel := payloadLayout.channelOffsets[payload]
		if channel >= safeUntil {
			point := m.geometry.atSector((clusterBase + channel) /
				(float64(ChannelBitsPerRecordingUnit) / 32.0))
			x, y := point.Xmm(), point.Ymm()
			tangentX, tangentY := m.geometry.unitTangent(x, y, point.RadiusMm)
			var travelMm float64
			squared := x*x + y*y
			clipped := m.clipRasterAnnulus &&
				(squared < m.minimumRadiusSquared || squared > m.maximumRadiusSquared)
			if clipped {
				luminance = 255
				travelMm = math.Inf(1)
			} else {
				luminance, travelMm = m.raster.sampleWithTravelMargin(x, y, m.rasterScale,
					tangentX, tangentY, curvatureBound, margin, tangentMargin)
			}
			if m.clipRasterAnnulus {
				// Radius is 1-Lipschitz in physical arc length. The fast
				// interpolated curve's speed relative to nominal arc length
				// is bounded by 1 + tangentMargin.
				radius := math.Sqrt(squared)
				var distance float64
				if clipped {
					distance = math.Max(m.minimumRadius-radius, radius-m.maximumRadius)
				} else {
					distance = math.Min(radius-m.minimumRadius, m.maximumRadius-radius)
				}
				travelMm = math.Min(travelMm, math.Max(0, distance-margin)/(1+tangentMargin))
			}
			safeUntil = math.Min(ChannelBitsPerRecordingUnit, channel+travelMm/mmPerBit)
		}
		destination[payload] = luminance
	}
	runLength.mapLuminances(destination)
	return true, nil
}

// GetPosition ports the public C# method.
func (m *ImagePayloadMapping) GetPosition(sector uint32, byteOffset int) (PayloadBytePosition, error) {
	if sector >= m.disc.TotalSectors {
		return PayloadBytePosition{}, argOutOfRange("sector")
	}
	if byteOffset < 0 || byteOffset >= BytesPerSector {
		return PayloadBytePosition{}, argOutOfRange("byteOffset")
	}
	physicalSector, err := m.disc.GetPhysicalSectorNumber(sector)
	if err != nil {
		return PayloadBytePosition{}, err
	}
	return m.getPositionCore(sector, byteOffset, physicalSector), nil
}

func (m *ImagePayloadMapping) getPositionCore(sector uint32, byteOffset int, physicalSector uint32) PayloadBytePosition {
	// EDC adds four bytes after each 2048-byte payload before column-wise LDC encoding.
	serial := int(sector%32)*2052 + byteOffset
	column := serial / 216
	row := serial % 216
	frame := 2*row + column%2
	ldcColumn := (column/2 + 152 - (row*3)%152) % 152
	// BIS inserts one byte after each of the first three groups of 38 LDC bytes.
	eccColumn := ldcColumn + ldcColumn/38
	dataBitMidpoint := eccColumn*8 + 4
	precedingDcBits := 0
	if dataBitMidpoint >= 25 {
		precedingDcBits = 1 + (dataBitMidpoint-25)/45
	}
	channelPosition := float64(int64(sector/32)*int64(ChannelBitsPerRecordingUnit)) +
		2760 + float64(frame*1932) + 30 + float64(dataBitMidpoint+precedingDcBits)*1.5
	sectorPosition := channelPosition / (float64(ChannelBitsPerRecordingUnit) / 32.0)
	return PayloadBytePosition{
		Sector: sector, ByteOffset: byteOffset, PhysicalSectorNumber: physicalSector,
		RecordingFrame: frame, LdcColumn: ldcColumn, EccColumn: eccColumn,
		NominalChannelBitPosition: channelPosition,
		TrackPoint:                m.geometry.atSector(sectorPosition),
	}
}

// FillScrambledPayload ports the payload mapping (scalar path; the C#
// four-point SIMD fast path is documented to preserve the scalar results).
func (m *ImagePayloadMapping) FillScrambledPayload(context SectorContext, destination []byte) error {
	if len(destination) != BytesPerSector {
		return argErr("A Blu-ray payload contains exactly 2048 bytes.", "destination")
	}
	psn, err := m.disc.GetPhysicalSectorNumber(context.Sector)
	if context.Disc != m.disc || context.Sector >= m.disc.TotalSectors || err != nil ||
		context.PhysicalSectorNumber != psn {
		return argErr("Sector context does not match the image mapping's disc and address.", "context")
	}
	if runLength, ok := m.mapping.(*RunLengthMapping); ok {
		// Layout repeats every cluster. Its half-bit offsets and the cluster
		// base are exactly representable doubles throughout supported discs.
		offsets := payloadLayout.channelOffsets[int(context.Sector%32)*BytesPerSector:][:BytesPerSector]
		clusterBase := float64(int64(context.Sector/32) * int64(ChannelBitsPerRecordingUnit))
		for offset := 0; offset < len(destination); offset++ {
			sectorPosition := (clusterBase + offsets[offset]) /
				(float64(ChannelBitsPerRecordingUnit) / 32.0)
			point := m.geometry.atSector(sectorPosition)
			if m.raster != nil {
				destination[offset] = m.sampleRaster(point.Xmm(), point.Ymm())
			} else {
				destination[offset] = m.sampleLuminance(point.Xmm(), point.Ymm())
			}
		}
		runLength.mapLuminances(destination)
		return nil
	}
	for offset := 0; offset < len(destination); offset++ {
		position := m.getPositionCore(context.Sector, offset, context.PhysicalSectorNumber)
		var luminance byte
		if m.clipRasterAnnulus {
			luminance = m.sampleRaster(position.TrackPoint.Xmm(), position.TrackPoint.Ymm())
		} else {
			luminance = m.sampleLuminance(position.TrackPoint.Xmm(), position.TrackPoint.Ymm())
		}
		destination[offset] = m.mapping.Map(luminance, position)
	}
	return nil
}

func (m *ImagePayloadMapping) sampleRaster(x, y float64) byte {
	if m.clipRasterAnnulus {
		squared := x*x + y*y
		if squared < m.minimumRadiusSquared || squared > m.maximumRadiusSquared {
			return 255
		}
	}
	return m.raster.sampleWithScale(x, y, m.rasterScale)
}

// PatternRangeMapping is a calibration pattern confined to one drawing range;
// every other payload byte stays zero so the burn still covers the whole disc
// address space.
type PatternRangeMapping struct {
	disc               *DiscParameters
	image              *ImagePayloadMapping
	bands              []PatternBand
	dark               byte
	light              byte
	switchChannels     []float64
	switchTones        []*byte
	startSector        uint32
	endSectorExclusive uint32
}

// PatternBand is one cluster-aligned calibration range [Start, EndExclusive).
type PatternBand struct {
	Start        int64
	EndExclusive int64
}

// AngleSwitch is one optional sweep transition (fraction of the full physical
// track; nil luminance restores the enclosing band).
type AngleSwitch struct {
	Progress  float64
	Luminance *byte
}

// NewPatternRangeMappingSampler ports the procedural-sampler constructor.
func NewPatternRangeMappingSampler(disc *DiscParameters, sampleLuminance func(x, y float64) byte,
	startSector, endSectorExclusive uint32, invertMapping, allowParallelSampling bool) (*PatternRangeMapping, error) {
	m, err := newPatternRangeMapping(disc, startSector, endSectorExclusive)
	if err != nil {
		return nil, err
	}
	if sampleLuminance == nil {
		return nil, argNull("sampleLuminance")
	}
	image, err := newImagePayloadMappingSampler(disc, sampleLuminance,
		NewRunLengthMapping(invertMapping), allowParallelSampling)
	if err != nil {
		return nil, err
	}
	m.image = image
	return m, nil
}

// NewPatternRangeMappingRaster ports the immutable-raster constructor with
// exact squared-radius clipping.
func NewPatternRangeMappingRaster(disc *DiscParameters, raster *Gray8Raster,
	startSector, endSectorExclusive uint32, invertMapping bool,
	minimumRadiusSquared, maximumRadiusSquared float64) (*PatternRangeMapping, error) {
	m, err := newPatternRangeMapping(disc, startSector, endSectorExclusive)
	if err != nil {
		return nil, err
	}
	image, err := newImagePayloadMappingAnnulus(disc, raster,
		NewRunLengthMapping(invertMapping), minimumRadiusSquared, maximumRadiusSquared)
	if err != nil {
		return nil, err
	}
	m.image = image
	return m, nil
}

// NewPatternRangeMappingBands ports the band/sweep constructor. The broad
// edges align to recording units.
func NewPatternRangeMappingBands(disc *DiscParameters, startSector, endSectorExclusive uint32,
	bands []PatternBand, invertMapping bool, angleSwitches []AngleSwitch) (*PatternRangeMapping, error) {
	m, err := newPatternRangeMapping(disc, startSector, endSectorExclusive)
	if err != nil {
		return nil, err
	}
	if bands == nil {
		return nil, argNull("bands")
	}
	m.bands = append([]PatternBand(nil), bands...)
	previousEnd := int64(startSector)
	for _, b := range m.bands {
		if b.Start < previousEnd || b.Start >= b.EndExclusive || b.EndExclusive > int64(endSectorExclusive) ||
			b.Start%SectorsPerCluster != 0 || b.EndExclusive%SectorsPerCluster != 0 {
			return nil, argErr("Calibration bands must be ordered, cluster-aligned ranges within the drawing area.", "bands")
		}
		previousEnd = b.EndExclusive
	}
	tone := NewRunLengthMapping(invertMapping)
	m.dark = tone.Map(0, PayloadBytePosition{})
	m.light = tone.Map(255, PayloadBytePosition{})
	if angleSwitches == nil {
		return m, nil
	}
	m.switchChannels = make([]float64, len(angleSwitches))
	m.switchTones = make([]*byte, len(angleSwitches))
	channels := float64(int64(disc.TotalSectors/SectorsPerCluster) * int64(ChannelBitsPerRecordingUnit))
	previous := -1.0
	for i, sw := range angleSwitches {
		if isNotFinite(sw.Progress) || sw.Progress <= previous || sw.Progress < 0 || sw.Progress > 1 {
			return nil, argErr("Sweep transitions must be ordered positions on the track.", "angleSwitches")
		}
		m.switchChannels[i] = sw.Progress * channels
		if sw.Luminance != nil {
			v := tone.Map(*sw.Luminance, PayloadBytePosition{})
			m.switchTones[i] = &v
		}
		previous = sw.Progress
	}
	return m, nil
}

func newPatternRangeMapping(disc *DiscParameters, startSector, endSectorExclusive uint32) (*PatternRangeMapping, error) {
	if disc == nil {
		return nil, argNull("disc")
	}
	if err := disc.Validate(); err != nil {
		return nil, err
	}
	if startSector >= endSectorExclusive || endSectorExclusive > disc.TotalSectors ||
		startSector%SectorsPerCluster != 0 || endSectorExclusive%SectorsPerCluster != 0 {
		return nil, argOutOfRangeMsg("endSectorExclusive",
			"The calibration drawing range must be 32-sector aligned and lie inside the disc.")
	}
	return &PatternRangeMapping{disc: disc, startSector: startSector, endSectorExclusive: endSectorExclusive}, nil
}

// CanGenerateInParallel ports the interface implementation.
func (m *PatternRangeMapping) CanGenerateInParallel(disc *DiscParameters) bool {
	if m.bands != nil {
		return disc == m.disc
	}
	return m.image.CanGenerateInParallel(disc)
}

func (m *PatternRangeMapping) tryFillAddressPattern(sector uint32, destination []byte) bool {
	if sector < m.startSector || sector >= m.endSectorExclusive {
		clear(destination)
		return true
	}
	if m.bands == nil {
		return false
	}
	background := m.light
	for _, b := range m.bands {
		if int64(sector) >= b.Start && int64(sector) < b.EndExclusive {
			background = m.dark
			break
		}
	}
	if len(m.switchChannels) == 0 {
		for i := range destination {
			destination[i] = background
		}
	} else {
		m.fillSweep(sector, destination, background)
	}
	return true
}

func (m *PatternRangeMapping) fillSweep(sector uint32, destination []byte, background byte) {
	start := float64(int64(sector/SectorsPerCluster) * int64(ChannelBitsPerRecordingUnit))
	end := start + ChannelBitsPerRecordingUnit
	index := binarySearchFloat64s(m.switchChannels, start)
	next := index + 1
	if index < 0 {
		next = ^index
	}
	initial := background
	if next != 0 && m.switchTones[next-1] != nil {
		initial = *m.switchTones[next-1]
	}
	if next == len(m.switchChannels) || m.switchChannels[next] >= end {
		for i := range destination {
			destination[i] = initial
		}
		return
	}

	// Adjacent host payload bytes need not occupy adjacent track positions.
	// Only a cluster containing a transition needs the per-byte LDC layout.
	offsets := payloadChannelOffsets(sector, len(destination))
	for i := range destination {
		position := start + offsets[i]
		value := initial
		for j := next; j < len(m.switchChannels) && m.switchChannels[j] <= position; j++ {
			if m.switchTones[j] != nil {
				value = *m.switchTones[j]
			} else {
				value = background
			}
		}
		destination[i] = value
	}
}

// tryFillScrambledCluster ports the batch interface implementation.
func (m *PatternRangeMapping) tryFillScrambledCluster(disc *DiscParameters, firstSector uint32,
	destination []byte, ctx context.Context) (bool, error) {
	if disc != m.disc || firstSector%SectorsPerCluster != 0 || firstSector >= disc.TotalSectors ||
		len(destination) != PayloadBytesPerCluster {
		return false, errors.New("A complete aligned pattern cluster must match the source disc.")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Both range edges are cluster-aligned. Outside clusters remain zero in
	// the scrambled domain without invoking the pattern sampler at all.
	if m.tryFillAddressPattern(firstSector, destination) {
		return true, nil
	}
	return m.image.tryFillScrambledCluster(disc, firstSector, destination, ctx)
}

// FillScrambledPayload ports the interface implementation.
func (m *PatternRangeMapping) FillScrambledPayload(context SectorContext, destination []byte) error {
	if !m.tryFillAddressPattern(context.Sector, destination) {
		return m.image.FillScrambledPayload(context, destination)
	}
	return nil
}

// binarySearchFloat64s ports Array.BinarySearch on a sorted double array:
// the index of value when found, otherwise ^insertionPoint.
func binarySearchFloat64s(sorted []float64, value float64) int {
	idx := sort.Search(len(sorted), func(i int) bool { return sorted[i] >= value })
	if idx < len(sorted) && sorted[idx] == value {
		return idx
	}
	return ^idx
}
