package trackgeom

import (
	"fmt"
	"os"
	"testing"
)

// TestGoldenDump prints geometry samples for cross-checking against the JS port.
func TestGoldenDump(t *testing.T) {
	if os.Getenv("TRACKGEOM_GOLDEN") == "" {
		t.Skip("set TRACKGEOM_GOLDEN to dump")
	}
	g, err := New(24, 58, 2297888*38688*133.3e-6, 0.08, -0.04, 0.02)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []float64{0.013, 0.1, 0.37, 0.5, 0.83, 0.999} {
		arc := q * g.TrackLengthMm()
		r := g.RadiusAtArcLengthFast(arc)
		fmt.Printf("q=%v radius=%.17g angle=%.17g\n", q, r, g.AngleAtRadius(r))
	}
	g2, _ := New(24, 58, 2297888*38688*133.3e-6, 0, 0, 0)
	for _, q := range []float64{0.013, 0.5, 0.999} {
		r := g2.RadiusAtArcLengthFast(q * g2.TrackLengthMm())
		fmt.Printf("const q=%v radius=%.17g angle=%.17g growth=%.17g\n", q, r, g2.AngleAtRadius(r), g2.RadialGrowthPerRadianMm())
	}
}
