package bluray

// ScramblerMask ports BluRayScrambler.CreateMask. PSN bits 19..5 seed s14..s0;
// s15 is one. All 32 frames in a cluster share a mask.
func ScramblerMask(physicalSectorNumber uint32, length int) ([]byte, error) {
	if length < 0 || length > EdcDataFrameBytes {
		return nil, argOutOfRange("length")
	}
	mask := make([]byte, length)
	if err := FillScrambleMask(physicalSectorNumber, mask); err != nil {
		return nil, err
	}
	return mask, nil
}

// FillScrambleMask ports BluRayScrambler.FillMask.
func FillScrambleMask(physicalSectorNumber uint32, destination []byte) error {
	if len(destination) > EdcDataFrameBytes {
		return argErr("A scrambling sequence cannot exceed one 2052-byte data frame.", "destination")
	}
	state := uint16(0x8000 | ((physicalSectorNumber >> 5) & 0x7fff))
	for index := range destination {
		destination[index] = byte(state)
		for bit := 0; bit < 8; bit++ {
			feedback := ((state >> 15) ^ (state >> 14) ^ (state >> 12) ^ (state >> 3)) & 1
			state = (state << 1) | feedback
		}
	}
	return nil
}

// ScrambleTransform ports BluRayScrambler.Transform: scrambling and
// descrambling are the same XOR operation, starting at frame byte zero.
func ScrambleTransform(data []byte, physicalSectorNumber uint32) error {
	if len(data) > EdcDataFrameBytes {
		return argErr("Transform one data frame at a time.", "data")
	}
	var mask [EdcDataFrameBytes]byte
	if err := FillScrambleMask(physicalSectorNumber, mask[:len(data)]); err != nil {
		return err
	}
	for i := range data {
		data[i] ^= mask[i]
	}
	return nil
}
