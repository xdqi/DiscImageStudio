package bluray

import (
	"fmt"
)

// renderBdInfoJSON reproduces the bd-info anonymous-object JSON byte-for-byte:
// System.Text.Json with WriteIndented on Linux ('\n' newlines, two-space
// indent), the anonymous property names verbatim and the BluRayDiscParameters
// record's PascalCase properties without a naming policy.
func renderBdInfoJSON(disc *DiscParameters, preset *DiscPreset, invert bool) (string, error) {
	geometry, err := NewTrackGeometry(disc)
	if err != nil {
		return "", err
	}
	darkRun, lightRun := "3T", "6T"
	darkByte, lightByte := byte(ThreeTByte), byte(SixTByte)
	if invert {
		darkRun, lightRun = lightRun, darkRun
		darkByte, lightByte = lightByte, darkByte
	}

	w := newJSONWriter()
	w.BeginObject()
	w.Property("standard")
	w.StringValue("ISO/IEC 30190:2021")
	w.Property("preset")
	w.StringValue(preset.ID)
	w.Property("mappingStatus")
	w.StringValue("enabled")
	w.Property("parameters")
	writeDiscParametersJSON(w, disc)
	w.Property("luminanceMapping")
	w.BeginObject()
	w.Property("strategy")
	w.StringValue("3T/6T run-count bias")
	w.Property("threshold")
	w.IntValue(128)
	w.Property("dark")
	w.BeginObject()
	w.Property("run")
	w.StringValue(darkRun)
	w.Property("scrambledByte")
	w.StringValue(fmt.Sprintf("0x%02X", darkByte))
	w.EndObject()
	w.Property("light")
	w.BeginObject()
	w.Property("run")
	w.StringValue(lightRun)
	w.Property("scrambledByte")
	w.StringValue(fmt.Sprintf("0x%02X", lightByte))
	w.EndObject()
	w.Property("invert")
	w.BoolValue(invert)
	w.Property("invertSupported")
	w.BoolValue(true)
	w.Property("note")
	w.StringValue("Run-count bias is not an optical-reflectance calibration.")
	w.EndObject()
	w.Property("payloadBytesPerSector")
	w.IntValue(2048)
	w.Property("sectorsPerCluster")
	w.IntValue(32)
	w.Property("channelBitsPerRecordingUnit")
	w.IntValue(ChannelBitsPerRecordingUnit)
	w.Property("channelBitLengthNm")
	w.DoubleValue(ChannelBitLengthNm)
	w.Property("nominalTrackPitchMicrometers")
	w.DoubleValue(NominalTrackPitchMicrometers)
	w.Property("effectiveTrackPitchMicrometers")
	w.DoubleValue(geometry.EffectiveTrackPitchMicrometers())
	w.Property("addressModel")
	w.StringValue("Nominal data zones; drive spare areas and defect remapping require calibration.")
	w.Property("physicalLayerStarts")
	w.BeginArray()
	for layer := 0; layer < disc.LayerCount; layer++ {
		psn, err := disc.GetPhysicalSectorNumber(uint32(layer) * SectorsPerLayer)
		if err != nil {
			return "", err
		}
		w.Element()
		w.Uint32Value(psn)
	}
	w.EndArray()
	w.EndObject()
	return w.String(), nil
}

// writeDiscParametersJSON serializes the record exactly as System.Text.Json:
// positional properties in declaration order, then the computed properties.
func writeDiscParametersJSON(w *jsonWriter, disc *DiscParameters) {
	w.BeginObject()
	w.Property("TotalSectors")
	w.Uint32Value(disc.TotalSectors)
	w.Property("LayerCount")
	w.IntValue(disc.LayerCount)
	w.Property("InnerRadiusMm")
	w.DoubleValue(disc.InnerRadiusMm)
	w.Property("OuterRadiusMm")
	w.DoubleValue(disc.OuterRadiusMm)
	w.Property("StartAngleDegrees")
	w.DoubleValue(disc.StartAngleDegrees)
	w.Property("PitchLinear")
	w.DoubleValue(disc.PitchLinear)
	w.Property("PitchQuadratic")
	w.DoubleValue(disc.PitchQuadratic)
	w.Property("PitchCubic")
	w.DoubleValue(disc.PitchCubic)
	w.Property("UsableSectors")
	if disc.UsableSectors == nil {
		w.Null()
	} else {
		w.Uint32Value(*disc.UsableSectors)
	}
	w.Property("OutputSectors")
	w.Uint32Value(disc.OutputSectors())
	w.Property("ContentLength")
	w.Int64Value(disc.ContentLength())
	w.Property("OutputOuterRadiusMm")
	w.DoubleValue(disc.OutputOuterRadiusMm())
	w.Property("HasVariablePitch")
	w.BoolValue(disc.HasVariablePitch())
	w.EndObject()
}
