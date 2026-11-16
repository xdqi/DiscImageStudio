package dvd

// EfmPlusTables port; the lookup data lives in efmplus_tables_generated.go.

// ECMA-267, 3rd edition, Table 4.
// Indexed by [state group (1/2 or 3/4), primary/secondary, category].
var efmPlusSyncCodes = [2][2][8]uint32{
	{
		{
			0x12440011, 0x04040011, 0x10040011, 0x08040011,
			0x20040011, 0x22440011, 0x24840011, 0x24440011,
		},
		{
			0x12040011, 0x04440011, 0x10440011, 0x08440011,
			0x20440011, 0x22040011, 0x20840011, 0x24040011,
		},
	},
	{
		{
			0x92040011, 0x84440011, 0x90440011, 0x82440011,
			0x88440011, 0x89040011, 0x90840011, 0x88840011,
		},
		{
			0x92440011, 0x84040011, 0x90040011, 0x82040011,
			0x88040011, 0x81040011, 0x80440011, 0x80840011,
		},
	},
}

func efmPlusStateIndex(state byte) (int, error) {
	if state < 1 || state > 4 {
		return 0, &argumentOutOfRangeError{param: "state", msg: "EFMPlus state must be 1..4."}
	}
	return int(state - 1), nil
}

// efmPlusLookupMain ports EfmPlusTables.LookupMain.
func efmPlusLookupMain(value, state byte) (word uint16, nextState byte, err error) {
	index, err := efmPlusStateIndex(state)
	if err != nil {
		return 0, 0, err
	}
	i := int(value)*4 + index
	return efmPlusMainWords[i], efmPlusMainNextStates[i], nil
}

// efmPlusSubstitution ports EfmPlusTables.Substitution.
func efmPlusSubstitution(value, state byte) (word uint16, nextState byte, err error) {
	if value > 87 {
		return 0, 0, &argumentOutOfRangeError{param: "value", msg: "The substitution table only covers bytes 0..87."}
	}
	index, err := efmPlusStateIndex(state)
	if err != nil {
		return 0, 0, err
	}
	i := int(value)*4 + index
	return efmPlusSubstitutionWords[i], efmPlusSubstitutionNextStates[i], nil
}

// efmPlusSync ports EfmPlusTables.Sync. The C# original validates only the
// category; the state is always validated upstream by ModulationBoundary.Validate.
func efmPlusSync(state byte, category int, primary bool) (uint32, error) {
	if uint(category) > 7 {
		return 0, &argumentOutOfRangeError{param: "category"}
	}
	group := 0
	if state > 2 {
		group = 1
	}
	p := 0
	if !primary {
		p = 1
	}
	return efmPlusSyncCodes[group][p][category], nil
}

// efmPlusLookupMainMust is used where state validity is guaranteed by construction.
func efmPlusLookupMainMust(value, state byte) (uint16, byte) {
	i := int(value)*4 + int(state-1)
	return efmPlusMainWords[i], efmPlusMainNextStates[i]
}

func efmPlusSubstitutionMust(value, state byte) (uint16, byte) {
	i := int(value)*4 + int(state-1)
	return efmPlusSubstitutionWords[i], efmPlusSubstitutionNextStates[i]
}

func efmPlusSyncMust(state byte, category int, primary bool) uint32 {
	group := 0
	if state > 2 {
		group = 1
	}
	p := 0
	if !primary {
		p = 1
	}
	return efmPlusSyncCodes[group][p][category]
}
