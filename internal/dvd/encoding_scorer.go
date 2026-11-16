package dvd

// efmPlusWordScore ports EfmPlusWordScore.
type efmPlusWordScore struct {
	BinaryErrors int
	GrayError    int
	Boundary     modulationBoundary
}

type scorerStreamCandidate struct {
	words         [efmPlusBytesPerSyncFrame]uint16
	length        int
	state         byte
	dsv           int
	isLand        bool
	syncPrimary   bool
	binaryErrors  int
	grayError     int
	trailingZeros int
}

func (s *scorerStreamCandidate) initialize(boundary modulationBoundary, sync uint32, primary bool, firstSyncDesiredLandCount, secondSyncDesiredLandCount int8) {
	s.length = 0
	s.state = boundary.State
	s.dsv = boundary.Dsv
	s.isLand = boundary.IsLand
	s.syncPrimary = primary
	s.binaryErrors = 0
	s.grayError = 0
	s.trailingZeros = 0
	s.appendRaw(uint16(sync>>16), firstSyncDesiredLandCount)
	s.appendRaw(uint16(sync), secondSyncDesiredLandCount)
	s.state = 1
}

func (s *scorerStreamCandidate) copyFrom(source *scorerStreamCandidate) {
	copy(s.words[:], source.words[:source.length])
	s.length = source.length
	s.state = source.state
	s.dsv = source.dsv
	s.isLand = source.isLand
	s.syncPrimary = source.syncPrimary
	s.binaryErrors = source.binaryErrors
	s.grayError = source.grayError
	s.trailingZeros = source.trailingZeros
}

func (s *scorerStreamCandidate) append(word uint16, nextState byte, desiredLandCount int8) error {
	metric := nrziMetric(word, s.isLand)
	combined := s.trailingZeros + int(metric.LeadingZeros)
	if combined < 2 || combined > 10 {
		return newInvalidOperationError("EFMPlus code word would violate the RLL(2,10) boundary.")
	}
	s.words[s.length] = word
	s.length++
	s.apply(metric)
	if desiredLandCount >= 0 {
		s.binaryErrors += binaryError(metric.LandCount, desiredLandCount)
		s.grayError += absInt(int(metric.LandCount) - int(desiredLandCount))
	}
	s.state = nextState
	return nil
}

func (s *scorerStreamCandidate) canAppend(word uint16) bool {
	metric := nrziMetric(word, s.isLand)
	combined := s.trailingZeros + int(metric.LeadingZeros)
	return combined >= 2 && combined <= 10
}

func (s *scorerStreamCandidate) appendRaw(word uint16, desiredLandCount int8) {
	metric := nrziMetric(word, s.isLand)
	s.apply(metric)
	if desiredLandCount >= 0 {
		s.binaryErrors += binaryError(metric.LandCount, desiredLandCount)
		s.grayError += absInt(int(metric.LandCount) - int(desiredLandCount))
	}
}

func (s *scorerStreamCandidate) apply(metric nrziCodewordMetric) {
	s.dsv += int(metric.DsvDelta)
	s.isLand = metric.FinalIsLand
	if metric.TrailingZeros == 16 {
		s.trailingZeros += 16
	} else {
		s.trailingZeros = int(metric.TrailingZeros)
	}
}

type efmPlusWordScorer struct {
	stream1 scorerStreamCandidate
	stream2 scorerStreamCandidate
}

// scoreSyncFrame ports EfmPlusWordScorer.ScoreSyncFrame.
func (e *efmPlusWordScorer) scoreSyncFrame(bytes []byte, syncCategory int, input modulationBoundary, desiredLandCounts []int8, firstSyncDesiredLandCount, secondSyncDesiredLandCount int8) (efmPlusWordScore, error) {
	if len(bytes) != efmPlusBytesPerSyncFrame || len(desiredLandCounts) != efmPlusBytesPerSyncFrame {
		return efmPlusWordScore{}, newArgumentError("A word-scored Sync Frame needs 91 bytes and 91 gray targets.")
	}
	syncPrimary, err := efmPlusSync(input.State, syncCategory, true)
	if err != nil {
		return efmPlusWordScore{}, err
	}
	syncSecondary, err := efmPlusSync(input.State, syncCategory, false)
	if err != nil {
		return efmPlusWordScore{}, err
	}
	stream1 := &e.stream1
	stream2 := &e.stream2
	stream1.initialize(input, syncPrimary, true, firstSyncDesiredLandCount, secondSyncDesiredLandCount)
	stream2.initialize(input, syncSecondary, false, firstSyncDesiredLandCount, secondSyncDesiredLandCount)

	for index := 0; index < len(bytes); index++ {
		value := bytes[index]
		target := desiredLandCounts[index]
		if value <= 87 {
			selected := scorerSelect(stream1, stream2)
			scorerCopySelectedToOther(selected, stream1, stream2)
			mainWord, mainNext := efmPlusLookupMainMust(value, selected.state)
			subWord, subNext := efmPlusSubstitutionMust(value, selected.state)
			if err := stream1.append(mainWord, mainNext, target); err != nil {
				return efmPlusWordScore{}, err
			}
			if err := stream2.append(subWord, subNext, target); err != nil {
				return efmPlusWordScore{}, err
			}
			continue
		}

		firstCanBranch := scorerHasTwoStateOneFourRepresentations(value, stream1)
		secondCanBranch := scorerHasTwoStateOneFourRepresentations(value, stream2)
		if firstCanBranch && secondCanBranch {
			selected := scorerSelect(stream1, stream2)
			scorerCopySelectedToOther(selected, stream1, stream2)
			if err := scorerAppendStateOneAndFourAlternatives(value, target, stream1, stream2); err != nil {
				return efmPlusWordScore{}, err
			}
		} else if firstCanBranch || secondCanBranch {
			eligible := stream1
			other := stream2
			eligibleIsFirst := true
			if !firstCanBranch {
				eligible = stream2
				other = stream1
				eligibleIsFirst = false
			}
			chooseEligible := absInt(eligible.dsv) < absInt(other.dsv) ||
				(absInt(eligible.dsv) == absInt(other.dsv) && eligibleIsFirst)
			if chooseEligible {
				scorerCopySelectedToOther(eligible, stream1, stream2)
				if err := scorerAppendStateOneAndFourAlternatives(value, target, stream1, stream2); err != nil {
					return efmPlusWordScore{}, err
				}
			} else {
				if err := scorerAppendMain(value, target, stream1); err != nil {
					return efmPlusWordScore{}, err
				}
				if err := scorerAppendMain(value, target, stream2); err != nil {
					return efmPlusWordScore{}, err
				}
			}
		} else {
			if err := scorerAppendMain(value, target, stream1); err != nil {
				return efmPlusWordScore{}, err
			}
			if err := scorerAppendMain(value, target, stream2); err != nil {
				return efmPlusWordScore{}, err
			}
		}
	}

	winner := scorerSelect(stream1, stream2)
	boundary := modulationBoundary{State: winner.state, Dsv: winner.dsv, IsLand: winner.isLand}
	binaryErrors := winner.binaryErrors
	grayError := winner.grayError
	if winner.dsv > 63 || winner.dsv < -64 {
		alternatePrimary := !winner.syncPrimary
		alternateSync, err := efmPlusSync(input.State, syncCategory, alternatePrimary)
		if err != nil {
			return efmPlusWordScore{}, err
		}
		alternate := scorerReplay(winner, input, alternateSync, desiredLandCounts, firstSyncDesiredLandCount, secondSyncDesiredLandCount)
		if absInt(alternate.Boundary.Dsv) < absInt(winner.dsv) {
			boundary = alternate.Boundary
			binaryErrors = alternate.BinaryErrors
			grayError = alternate.GrayError
		}
	}

	return efmPlusWordScore{BinaryErrors: binaryErrors, GrayError: grayError, Boundary: boundary}, nil
}

func scorerCopySelectedToOther(selected, stream1, stream2 *scorerStreamCandidate) {
	if selected == stream1 {
		stream2.copyFrom(stream1)
	} else {
		stream1.copyFrom(stream2)
	}
}

func scorerReplay(candidate *scorerStreamCandidate, input modulationBoundary, sync uint32, desiredLandCounts []int8, firstSyncDesiredLandCount, secondSyncDesiredLandCount int8) efmPlusWordScore {
	isLand := input.IsLand
	dsv := input.Dsv
	binaryErrors := 0
	grayError := 0
	scorerApplyWord(uint16(sync>>16), &isLand, &dsv, firstSyncDesiredLandCount, &binaryErrors, &grayError)
	scorerApplyWord(uint16(sync), &isLand, &dsv, secondSyncDesiredLandCount, &binaryErrors, &grayError)
	for index := 0; index < candidate.length; index++ {
		scorerApplyWord(candidate.words[index], &isLand, &dsv, desiredLandCounts[index], &binaryErrors, &grayError)
	}
	return efmPlusWordScore{
		BinaryErrors: binaryErrors,
		GrayError:    grayError,
		Boundary:     modulationBoundary{State: candidate.state, Dsv: dsv, IsLand: isLand},
	}
}

func scorerApplyWord(word uint16, isLand *bool, dsv *int, desiredLandCount int8, binaryErrors, grayError *int) {
	metric := nrziMetric(word, *isLand)
	*dsv += int(metric.DsvDelta)
	*isLand = metric.FinalIsLand
	if desiredLandCount >= 0 {
		*binaryErrors += binaryError(metric.LandCount, desiredLandCount)
		*grayError += absInt(int(metric.LandCount) - int(desiredLandCount))
	}
}

func scorerAppendStateOneAndFourAlternatives(value byte, target int8, stream1, stream2 *scorerStreamCandidate) error {
	stateOneWord, stateOneNext := efmPlusLookupMainMust(value, 1)
	stateFourWord, stateFourNext := efmPlusLookupMainMust(value, 4)
	if !stream1.canAppend(stateOneWord) || !stream2.canAppend(stateFourWord) {
		return newInvalidOperationError("Invalid State 1/4 EFMPlus branch.")
	}
	if err := stream1.append(stateOneWord, stateOneNext, target); err != nil {
		return err
	}
	return stream2.append(stateFourWord, stateFourNext, target)
}

func scorerHasTwoStateOneFourRepresentations(value byte, stream *scorerStreamCandidate) bool {
	if stream.state != 1 && stream.state != 4 {
		return false
	}
	stateOneWord, _ := efmPlusLookupMainMust(value, 1)
	stateFourWord, _ := efmPlusLookupMainMust(value, 4)
	return stream.canAppend(stateOneWord) && stream.canAppend(stateFourWord)
}

func scorerAppendMain(value byte, target int8, stream *scorerStreamCandidate) error {
	word, next := efmPlusLookupMainMust(value, stream.state)
	return stream.append(word, next, target)
}

func scorerSelect(stream1, stream2 *scorerStreamCandidate) *scorerStreamCandidate {
	if absInt(stream1.dsv) <= absInt(stream2.dsv) {
		return stream1
	}
	return stream2
}
