package dvd

// EfmPlusEncoder port.
const (
	efmPlusBytesPerSyncFrame = 91
	efmPlusBitsPerSyncFrame  = 1488
)

type efmStreamCandidate struct {
	nrz         []byte
	length      int
	state       byte
	dsv         int
	isLand      bool
	syncPrimary bool
}

func newEfmStreamCandidate() *efmStreamCandidate {
	return &efmStreamCandidate{nrz: make([]byte, efmPlusBitsPerSyncFrame)}
}

func (s *efmStreamCandidate) initialize(boundary modulationBoundary, sync uint32, primary bool) {
	s.length = 0
	s.state = boundary.State
	s.dsv = boundary.Dsv
	s.isLand = boundary.IsLand
	s.syncPrimary = primary
	s.appendBits(sync, 32)
	s.state = 1 // Every SYNC code ends in a ONE, therefore the next state is State 1.
}

func (s *efmStreamCandidate) copyFrom(source *efmStreamCandidate) {
	copy(s.nrz, source.nrz[:source.length])
	s.length = source.length
	s.state = source.state
	s.dsv = source.dsv
	s.isLand = source.isLand
	s.syncPrimary = source.syncPrimary
}

func (s *efmStreamCandidate) append(word uint16, nextState byte) error {
	if !s.canAppend(word) {
		return newInvalidOperationError("EFMPlus code word would violate the RLL(2,10) boundary.")
	}
	s.appendBits(uint32(word), 16)
	s.state = nextState
	return nil
}

func (s *efmStreamCandidate) canAppend(word uint16) bool {
	trailingZeros := 0
	for index := s.length - 1; index >= 0 && s.nrz[index] == 0; index-- {
		trailingZeros++
	}
	leadingZeros := 0
	for bit := 15; bit >= 0 && (word>>uint(bit))&1 == 0; bit-- {
		leadingZeros++
	}
	combined := trailingZeros + leadingZeros
	return combined >= 2 && combined <= 10
}

func (s *efmStreamCandidate) appendBits(bits uint32, count int) {
	for bit := count - 1; bit >= 0; bit-- {
		transition := byte((bits >> uint(bit)) & 1)
		s.nrz[s.length] = transition
		s.length++
		if transition != 0 {
			s.isLand = !s.isLand
		}
		if s.isLand {
			s.dsv++
		} else {
			s.dsv--
		}
	}
}

type efmPlusEncoder struct {
	stream1      *efmStreamCandidate
	stream2      *efmStreamCandidate
	alternateNrz []byte
}

func newEfmPlusEncoder() *efmPlusEncoder {
	return &efmPlusEncoder{
		stream1:      newEfmStreamCandidate(),
		stream2:      newEfmStreamCandidate(),
		alternateNrz: make([]byte, efmPlusBitsPerSyncFrame),
	}
}

// encodeSyncFrame ports EfmPlusEncoder.EncodeSyncFrame. physicalLevels may be
// nil (the C# Span<byte> default) or must hold at least BitsPerSyncFrame bytes.
func (e *efmPlusEncoder) encodeSyncFrame(bytes []byte, syncCategory int, input modulationBoundary, physicalLevels []byte) (modulationBoundary, error) {
	if len(bytes) != efmPlusBytesPerSyncFrame {
		return modulationBoundary{}, newArgumentErrorParam("A Sync Frame contains 91 bytes.", "bytes")
	}
	if len(physicalLevels) > 0 && len(physicalLevels) < efmPlusBitsPerSyncFrame {
		return modulationBoundary{}, newArgumentErrorParam("Output needs 1488 level entries.", "physicalLevels")
	}
	syncPrimary, err := efmPlusSync(input.State, syncCategory, true)
	if err != nil {
		return modulationBoundary{}, err
	}
	syncSecondary, err := efmPlusSync(input.State, syncCategory, false)
	if err != nil {
		return modulationBoundary{}, err
	}
	stream1 := e.stream1
	stream2 := e.stream2
	stream1.initialize(input, syncPrimary, true)
	stream2.initialize(input, syncSecondary, false)

	for _, value := range bytes {
		if value <= 87 {
			var selected *efmStreamCandidate
			if absInt(stream1.dsv) <= absInt(stream2.dsv) {
				selected = stream1
			} else {
				selected = stream2
			}
			stream1.copyFrom(selected)
			stream2.copyFrom(selected)
			mainWord, mainNext := efmPlusLookupMainMust(value, selected.state)
			subWord, subNext := efmPlusSubstitutionMust(value, selected.state)
			if err := stream1.append(mainWord, mainNext); err != nil {
				return modulationBoundary{}, err
			}
			if err := stream2.append(subWord, subNext); err != nil {
				return modulationBoundary{}, err
			}
			continue
		}

		firstCanBranch := hasTwoStateOneFourRepresentations(value, stream1)
		secondCanBranch := hasTwoStateOneFourRepresentations(value, stream2)
		if firstCanBranch && secondCanBranch {
			var selected *efmStreamCandidate
			if absInt(stream1.dsv) <= absInt(stream2.dsv) {
				selected = stream1
			} else {
				selected = stream2
			}
			stream1.copyFrom(selected)
			stream2.copyFrom(selected)
			if err := appendStateOneAndFourAlternatives(value, stream1, stream2); err != nil {
				return modulationBoundary{}, err
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
				stream1.copyFrom(eligible)
				stream2.copyFrom(eligible)
				if err := appendStateOneAndFourAlternatives(value, stream1, stream2); err != nil {
					return modulationBoundary{}, err
				}
			} else {
				if err := efmAppendMain(value, stream1); err != nil {
					return modulationBoundary{}, err
				}
				if err := efmAppendMain(value, stream2); err != nil {
					return modulationBoundary{}, err
				}
			}
		} else {
			if err := efmAppendMain(value, stream1); err != nil {
				return modulationBoundary{}, err
			}
			if err := efmAppendMain(value, stream2); err != nil {
				return modulationBoundary{}, err
			}
		}
	}

	var winner *efmStreamCandidate
	if absInt(stream1.dsv) <= absInt(stream2.dsv) {
		winner = stream1
	} else {
		winner = stream2
	}
	if winner.dsv > 63 || winner.dsv < -64 {
		alternatePrimary := !winner.syncPrimary
		copy(e.alternateNrz, winner.nrz)
		alternateSync, err := efmPlusSync(input.State, syncCategory, alternatePrimary)
		if err != nil {
			return modulationBoundary{}, err
		}
		writeBits(alternateSync, 32, e.alternateNrz)
		alternateBoundary := efmReplay(e.alternateNrz, input, nil)
		if absInt(alternateBoundary.Dsv) < absInt(winner.dsv) {
			copy(winner.nrz, e.alternateNrz)
			winner.dsv = alternateBoundary.Dsv
			winner.isLand = alternateBoundary.IsLand
			winner.syncPrimary = alternatePrimary
		}
	}

	output := efmReplay(winner.nrz, input, physicalLevels)
	output.State = winner.state
	return output, nil
}

func appendStateOneAndFourAlternatives(value byte, stream1, stream2 *efmStreamCandidate) error {
	stateOneWord, stateOneNext := efmPlusLookupMainMust(value, 1)
	stateFourWord, stateFourNext := efmPlusLookupMainMust(value, 4)
	if !stream1.canAppend(stateOneWord) || !stream2.canAppend(stateFourWord) {
		return newInvalidOperationError("Invalid State 1/4 EFMPlus branch.")
	}
	if err := stream1.append(stateOneWord, stateOneNext); err != nil {
		return err
	}
	return stream2.append(stateFourWord, stateFourNext)
}

func hasTwoStateOneFourRepresentations(value byte, stream *efmStreamCandidate) bool {
	if stream.state != 1 && stream.state != 4 {
		return false
	}
	stateOneWord, _ := efmPlusLookupMainMust(value, 1)
	stateFourWord, _ := efmPlusLookupMainMust(value, 4)
	return stream.canAppend(stateOneWord) && stream.canAppend(stateFourWord)
}

func efmAppendMain(value byte, stream *efmStreamCandidate) error {
	word, next := efmPlusLookupMainMust(value, stream.state)
	return stream.append(word, next)
}

func writeBits(bits uint32, count int, destination []byte) {
	index := 0
	for bit := count - 1; bit >= 0; bit-- {
		destination[index] = byte((bits >> uint(bit)) & 1)
		index++
	}
}

// efmReplay ports EfmPlusEncoder.Replay.
func efmReplay(nrz []byte, input modulationBoundary, physicalLevels []byte) modulationBoundary {
	level := input.IsLand
	dsv := input.Dsv
	for index := 0; index < len(nrz); index++ {
		if nrz[index] != 0 {
			level = !level
		}
		if len(physicalLevels) > 0 {
			if level {
				physicalLevels[index] = 1
			} else {
				physicalLevels[index] = 0
			}
		}
		if level {
			dsv++
		} else {
			dsv--
		}
	}
	return modulationBoundary{State: input.State, Dsv: dsv, IsLand: level}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func absLong(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
