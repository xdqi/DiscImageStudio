package dvd

// modulationBoundary ports ModulationBoundary (record struct).
type modulationBoundary struct {
	State  byte
	Dsv    int
	IsLand bool
}

func validateModulationBoundary(state byte, dsv int, isLand bool) (modulationBoundary, error) {
	if state < 1 || state > 4 {
		return modulationBoundary{}, &argumentOutOfRangeError{param: "state", msg: "EFMPlus state must be 1..4."}
	}
	return modulationBoundary{State: state, Dsv: dsv, IsLand: isLand}, nil
}

func (b modulationBoundary) LevelName() string {
	if b.IsLand {
		return "land"
	}
	return "pit"
}

// packMsbFirst ports BitPacking.PackMsbFirst.
func packMsbFirst(bits []byte) []byte {
	packed := make([]byte, (len(bits)+7)/8)
	for index := 0; index < len(bits); index++ {
		if bits[index] != 0 {
			packed[index/8] |= 0x80 >> uint(index&7)
		}
	}
	return packed
}
