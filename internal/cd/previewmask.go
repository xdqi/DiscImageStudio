package cd

// applyPreviewMask ports CdPreviewMask.Apply: pixels outside the disc
// annulus become transparent.
func applyPreviewMask(pixels []byte, size int, actual *DiscParameters) {
	centre := float64(size) / 2.0
	millimetresPerPixel := actual.PreviewImageOuterRadiusMm() / centre
	innerSquared := actual.InnerRadiusMm * actual.InnerRadiusMm
	outerSquared := actual.OutputOuterRadiusMm() * actual.OutputOuterRadiusMm()
	for y := 0; y < size; y++ {
		dy := (float64(y) - centre) * millimetresPerPixel
		dySquared := dy * dy
		for x := 0; x < size; x++ {
			dx := (float64(x) - centre) * millimetresPerPixel
			radiusSquared := dx*dx + dySquared
			if radiusSquared < innerSquared || radiusSquared >= outerSquared {
				pixels[(y*size+x)*4+3] = 0
			}
		}
	}
}
