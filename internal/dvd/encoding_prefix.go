package dvd

// efmStreamPrefix ports EfmStreamPrefix (immutable record struct).
type efmStreamPrefix struct {
	State         byte
	Dsv           int
	IsLand        bool
	BinaryErrors  int
	GrayError     int
	TrailingZeros int
}

func efmStreamPrefixInitialize(boundary modulationBoundary, sync uint32) efmStreamPrefix {
	result := efmStreamPrefix{
		State:         boundary.State,
		Dsv:           boundary.Dsv,
		IsLand:        boundary.IsLand,
		BinaryErrors:  0,
		GrayError:     0,
		TrailingZeros: 0,
	}
	result = result.appendRaw(uint16(sync >> 16))
	result = result.appendRaw(uint16(sync))
	result.State = 1
	return result
}

func (p efmStreamPrefix) canAppend(word uint16) bool {
	metric := nrziMetric(word, p.IsLand)
	combined := p.TrailingZeros + int(metric.LeadingZeros)
	return combined >= 2 && combined <= 10
}

func (p efmStreamPrefix) append(word uint16, nextState byte, desiredLandCount int8) (efmStreamPrefix, error) {
	metric := nrziMetric(word, p.IsLand)
	combined := p.TrailingZeros + int(metric.LeadingZeros)
	if combined < 2 || combined > 10 {
		return efmStreamPrefix{}, newInvalidOperationError("EFMPlus code word would violate the RLL(2,10) boundary.")
	}
	result := p.apply(metric)
	result.State = nextState
	result.BinaryErrors = p.BinaryErrors + binaryError(metric.LandCount, desiredLandCount)
	gray := 0
	if desiredLandCount >= 0 {
		gray = absInt(int(metric.LandCount) - int(desiredLandCount))
	}
	result.GrayError = p.GrayError + gray
	return result, nil
}

func (p efmStreamPrefix) appendRaw(word uint16) efmStreamPrefix {
	return p.apply(nrziMetric(word, p.IsLand))
}

func (p efmStreamPrefix) apply(metric nrziCodewordMetric) efmStreamPrefix {
	result := p
	result.Dsv = p.Dsv + int(metric.DsvDelta)
	result.IsLand = metric.FinalIsLand
	if metric.TrailingZeros == 16 {
		result.TrailingZeros = p.TrailingZeros + 16
	} else {
		result.TrailingZeros = int(metric.TrailingZeros)
	}
	return result
}

func binaryError(landCount byte, desiredLandCount int8) int {
	switch {
	case desiredLandCount < 0:
		return 0
	case desiredLandCount < 8:
		if landCount < 8 {
			return 0
		}
		return 1
	case desiredLandCount > 8:
		if landCount > 8 {
			return 0
		}
		return 1
	default:
		return 0
	}
}

// efmPlusPrefixState ports EfmPlusPrefixState (pair of streams).
type efmPlusPrefixState struct {
	Stream1 efmStreamPrefix
	Stream2 efmStreamPrefix
}

func efmPlusPrefixStateInitialize(input modulationBoundary, syncCategory int) (efmPlusPrefixState, error) {
	primary, err := efmPlusSync(input.State, syncCategory, true)
	if err != nil {
		return efmPlusPrefixState{}, err
	}
	secondary, err := efmPlusSync(input.State, syncCategory, false)
	if err != nil {
		return efmPlusPrefixState{}, err
	}
	return efmPlusPrefixState{
		Stream1: efmStreamPrefixInitialize(input, primary),
		Stream2: efmStreamPrefixInitialize(input, secondary),
	}, nil
}

func (p efmPlusPrefixState) selected() efmStreamPrefix {
	if absInt(p.Stream1.Dsv) <= absInt(p.Stream2.Dsv) {
		return p.Stream1
	}
	return p.Stream2
}

func (p efmPlusPrefixState) advance(value byte, desiredLandCount int8) (efmPlusPrefixState, error) {
	stream1 := p.Stream1
	stream2 := p.Stream2
	if value <= 87 {
		selected := prefixSelect(stream1, stream2)
		mainWord, mainNext := efmPlusLookupMainMust(value, selected.State)
		subWord, subNext := efmPlusSubstitutionMust(value, selected.State)
		first, err := selected.append(mainWord, mainNext, desiredLandCount)
		if err != nil {
			return efmPlusPrefixState{}, err
		}
		second, err := selected.append(subWord, subNext, desiredLandCount)
		if err != nil {
			return efmPlusPrefixState{}, err
		}
		return efmPlusPrefixState{Stream1: first, Stream2: second}, nil
	}

	firstCanBranch := prefixHasTwoStateOneFourRepresentations(value, stream1)
	secondCanBranch := prefixHasTwoStateOneFourRepresentations(value, stream2)
	if firstCanBranch && secondCanBranch {
		selected := prefixSelect(stream1, stream2)
		return prefixAppendStateOneAndFourAlternatives(value, desiredLandCount, selected, selected)
	}

	if firstCanBranch || secondCanBranch {
		eligible := stream1
		other := stream2
		eligibleIsFirst := true
		if !firstCanBranch {
			eligible = stream2
			other = stream1
			eligibleIsFirst = false
		}
		chooseEligible := absInt(eligible.Dsv) < absInt(other.Dsv) ||
			(absInt(eligible.Dsv) == absInt(other.Dsv) && eligibleIsFirst)
		if chooseEligible {
			return prefixAppendStateOneAndFourAlternatives(value, desiredLandCount, eligible, eligible)
		}
	}

	first, err := prefixAppendMain(value, desiredLandCount, stream1)
	if err != nil {
		return efmPlusPrefixState{}, err
	}
	second, err := prefixAppendMain(value, desiredLandCount, stream2)
	if err != nil {
		return efmPlusPrefixState{}, err
	}
	return efmPlusPrefixState{Stream1: first, Stream2: second}, nil
}

func prefixAppendStateOneAndFourAlternatives(value byte, target int8, stream1, stream2 efmStreamPrefix) (efmPlusPrefixState, error) {
	stateOneWord, stateOneNext := efmPlusLookupMainMust(value, 1)
	stateFourWord, stateFourNext := efmPlusLookupMainMust(value, 4)
	if !stream1.canAppend(stateOneWord) || !stream2.canAppend(stateFourWord) {
		return efmPlusPrefixState{}, newInvalidOperationError("Invalid State 1/4 EFMPlus branch.")
	}
	first, err := stream1.append(stateOneWord, stateOneNext, target)
	if err != nil {
		return efmPlusPrefixState{}, err
	}
	second, err := stream2.append(stateFourWord, stateFourNext, target)
	if err != nil {
		return efmPlusPrefixState{}, err
	}
	return efmPlusPrefixState{Stream1: first, Stream2: second}, nil
}

func prefixHasTwoStateOneFourRepresentations(value byte, stream efmStreamPrefix) bool {
	if stream.State != 1 && stream.State != 4 {
		return false
	}
	stateOneWord, _ := efmPlusLookupMainMust(value, 1)
	stateFourWord, _ := efmPlusLookupMainMust(value, 4)
	return stream.canAppend(stateOneWord) && stream.canAppend(stateFourWord)
}

func prefixAppendMain(value byte, target int8, stream efmStreamPrefix) (efmStreamPrefix, error) {
	word, next := efmPlusLookupMainMust(value, stream.State)
	return stream.append(word, next, target)
}

func prefixSelect(stream1, stream2 efmStreamPrefix) efmStreamPrefix {
	if absInt(stream1.Dsv) <= absInt(stream2.Dsv) {
		return stream1
	}
	return stream2
}
