package dvd

// DvdEccBlockEncoder port.
const (
	dvdEccSectorCount        = 16
	dvdPayloadBytesPerSector = 2048
	dvdPayloadBytesPerBlock  = dvdEccSectorCount * dvdPayloadBytesPerSector
	dvdChannelBitsPerSector  = 38688
	dvdChannelBitsPerBlock   = dvdEccSectorCount * dvdChannelBitsPerSector
	dvdSyncFramesPerSector   = 26
	dvdSyncFrameCount        = dvdEccSectorCount * dvdSyncFramesPerSector
	dvdEccRows               = 208
	dvdEccDataRows           = 192
	dvdEccDataColumns        = 172
	dvdEccColumns            = 182
)

var eccFirstSyncCategories = []int{0, 1, 2, 3, 4, 1, 2, 3, 4, 1, 2, 3, 4}
var eccSecondSyncCategories = []int{5, 5, 5, 5, 5, 6, 6, 6, 6, 7, 7, 7, 7}

// encodedEccBlock ports EncodedEccBlock.
type encodedEccBlock struct {
	ChannelLevels             []byte
	FinalBoundary             modulationBoundary
	FirstPhysicalSectorNumber uint32
}

type dvdEccBlockEncoder struct {
	modulator *efmPlusEncoder
}

func newDvdEccBlockEncoder() *dvdEccBlockEncoder {
	return &dvdEccBlockEncoder{modulator: newEfmPlusEncoder()}
}

func (e *dvdEccBlockEncoder) encode(payloads []byte, initialLba, psnOffset uint32, initialBoundary modulationBoundary) (encodedEccBlock, error) {
	if len(payloads) != dvdPayloadBytesPerBlock {
		return encodedEccBlock{}, newArgumentErrorParam("An ECC Block needs exactly 32768 payload bytes.", "payloads")
	}
	firstPsn, err := checkedAddU32(initialLba, psnOffset)
	if err != nil {
		return encodedEccBlock{}, err
	}
	if firstPsn&0xF != 0 {
		return encodedEccBlock{}, newArgumentError("The first PSN 0x" + formatHexUpper(uint64(firstPsn), 6) + " is not aligned to a 16-sector ECC Block.")
	}
	if firstPsn > 0x00FFFFF0 {
		return encodedEccBlock{}, &argumentOutOfRangeError{param: "initialLba", msg: "The ECC Block exceeds the 24-bit PSN range."}
	}
	recordingFrames, err := buildRecordingFrames(payloads, firstPsn)
	if err != nil {
		return encodedEccBlock{}, err
	}
	return e.encodeRecordingFrames(recordingFrames, firstPsn, initialBoundary)
}

func (e *dvdEccBlockEncoder) encodeRecordingFrames(recordingFrames []byte, firstPsn uint32, initialBoundary modulationBoundary) (encodedEccBlock, error) {
	if len(recordingFrames) != dvdEccRows*dvdEccColumns {
		return encodedEccBlock{}, newArgumentErrorParam("Unexpected recording-frame byte count.", "recordingFrames")
	}
	channelLevels := make([]byte, dvdChannelBitsPerBlock)
	boundary, err := validateModulationBoundary(initialBoundary.State, initialBoundary.Dsv, initialBoundary.IsLand)
	if err != nil {
		return encodedEccBlock{}, err
	}
	outputOffset := 0
	for sector := 0; sector < dvdEccSectorCount; sector++ {
		frameOffset := sector * 13 * dvdEccColumns
		for row := 0; row < 13; row++ {
			rowBytes := recordingFrames[frameOffset+row*dvdEccColumns : frameOffset+row*dvdEccColumns+dvdEccColumns]
			boundary, err = e.modulator.encodeSyncFrame(
				rowBytes[:91],
				eccFirstSyncCategories[row],
				boundary,
				channelLevels[outputOffset:outputOffset+efmPlusBitsPerSyncFrame])
			if err != nil {
				return encodedEccBlock{}, err
			}
			outputOffset += efmPlusBitsPerSyncFrame
			boundary, err = e.modulator.encodeSyncFrame(
				rowBytes[91:],
				eccSecondSyncCategories[row],
				boundary,
				channelLevels[outputOffset:outputOffset+efmPlusBitsPerSyncFrame])
			if err != nil {
				return encodedEccBlock{}, err
			}
			outputOffset += efmPlusBitsPerSyncFrame
		}
	}
	return encodedEccBlock{ChannelLevels: channelLevels, FinalBoundary: boundary, FirstPhysicalSectorNumber: firstPsn}, nil
}

func replayRecordingFrameBoundary(recordingFrames []byte, initialBoundary modulationBoundary) (modulationBoundary, error) {
	if len(recordingFrames) != dvdEccRows*dvdEccColumns {
		return modulationBoundary{}, newArgumentErrorParam("Unexpected recording-frame byte count.", "recordingFrames")
	}
	unconstrained := make([]int8, efmPlusBytesPerSyncFrame)
	for i := range unconstrained {
		unconstrained[i] = -1
	}
	scorer := &efmPlusWordScorer{}
	boundary, err := validateModulationBoundary(initialBoundary.State, initialBoundary.Dsv, initialBoundary.IsLand)
	if err != nil {
		return modulationBoundary{}, err
	}
	for syncFrame := 0; syncFrame < dvdSyncFrameCount; syncFrame++ {
		offset := syncFrame * efmPlusBytesPerSyncFrame
		category, err := syncCategory(syncFrame)
		if err != nil {
			return modulationBoundary{}, err
		}
		score, err := scorer.scoreSyncFrame(
			recordingFrames[offset:offset+efmPlusBytesPerSyncFrame],
			category,
			boundary,
			unconstrained,
			-1, -1)
		if err != nil {
			return modulationBoundary{}, err
		}
		boundary = score.Boundary
	}
	return boundary, nil
}

func buildRecordingFrames(payloads []byte, firstPsn uint32) ([]byte, error) {
	ecc, err := buildEccBytes(payloads, firstPsn)
	if err != nil {
		return nil, err
	}
	return interleaveRecordingFrames(ecc)
}

func updateRecordingFrames(currentPayloads, updatedPayloads, currentRecordingFrames []byte, firstPsn uint32) ([]byte, error) {
	if len(currentPayloads) != dvdPayloadBytesPerBlock || len(updatedPayloads) != dvdPayloadBytesPerBlock {
		return nil, newArgumentError("An ECC Block needs exactly 32768 payload bytes.")
	}
	if len(currentRecordingFrames) != dvdEccRows*dvdEccColumns {
		return nil, newArgumentErrorParam("Unexpected recording-frame byte count.", "currentRecordingFrames")
	}

	frames := append([]byte(nil), currentRecordingFrames...)
	var changedColumns [dvdEccDataColumns]bool
	var changedRows [dvdEccRows]bool
	for sector := 0; sector < dvdEccSectorCount; sector++ {
		payloadOffset := sector * dvdPayloadBytesPerSector
		updatedSector := updatedPayloads[payloadOffset : payloadOffset+dvdPayloadBytesPerSector]
		if bytesEqual(updatedSector, currentPayloads[payloadOffset:payloadOffset+dvdPayloadBytesPerSector]) {
			continue
		}
		dataFrame, err := buildScrambledFrame(updatedSector, firstPsn+uint32(sector))
		if err != nil {
			return nil, err
		}
		for row := 0; row < 12; row++ {
			eccRow := sector*12 + row
			recordingOffset, err := recordingFrameIndex(eccRow, 0)
			if err != nil {
				return nil, err
			}
			source := dataFrame[row*dvdDataFrameDataColumns : row*dvdDataFrameDataColumns+dvdDataFrameDataColumns]
			destination := frames[recordingOffset : recordingOffset+dvdDataFrameDataColumns]
			rowChanged := false
			for column := 0; column < dvdDataFrameDataColumns; column++ {
				if source[column] != destination[column] {
					changedColumns[column] = true
					rowChanged = true
				}
			}
			if rowChanged {
				copy(destination, source)
				changedRows[eccRow] = true
			}
		}
	}

	columnBytes := make([]byte, dvdEccDataRows)
	poParity := make([]byte, 16)
	for column := 0; column < dvdEccDataColumns; column++ {
		if !changedColumns[column] {
			continue
		}
		for row := 0; row < dvdEccDataRows; row++ {
			idx, err := recordingFrameIndex(row, column)
			if err != nil {
				return nil, err
			}
			columnBytes[row] = frames[idx]
		}
		rsAppendPo(columnBytes, poParity)
		for parityRow := 0; parityRow < len(poParity); parityRow++ {
			eccRow := dvdEccDataRows + parityRow
			recordingIndex, err := recordingFrameIndex(eccRow, column)
			if err != nil {
				return nil, err
			}
			if frames[recordingIndex] != poParity[parityRow] {
				frames[recordingIndex] = poParity[parityRow]
				changedRows[eccRow] = true
			}
		}
	}

	for row := 0; row < dvdEccRows; row++ {
		if !changedRows[row] {
			continue
		}
		recordingOffset, err := recordingFrameIndex(row, 0)
		if err != nil {
			return nil, err
		}
		rsAppendPi(
			frames[recordingOffset:recordingOffset+dvdEccDataColumns],
			frames[recordingOffset+dvdEccDataColumns:recordingOffset+dvdEccDataColumns+10])
	}

	return frames, nil
}

func buildEccBytes(payloads []byte, firstPsn uint32) ([]byte, error) {
	ecc := make([]byte, dvdEccRows*dvdEccColumns)
	for sector := 0; sector < dvdEccSectorCount; sector++ {
		if len(payloads) < (sector+1)*dvdPayloadBytesPerSector {
			return nil, newArgumentError("An ECC Block needs exactly 32768 payload bytes.")
		}
		frame, err := buildScrambledFrame(
			payloads[sector*dvdPayloadBytesPerSector:sector*dvdPayloadBytesPerSector+dvdPayloadBytesPerSector],
			firstPsn+uint32(sector))
		if err != nil {
			return nil, err
		}
		for row := 0; row < 12; row++ {
			copy(ecc[(sector*12+row)*dvdEccColumns:(sector*12+row)*dvdEccColumns+dvdEccDataColumns],
				frame[row*dvdDataFrameDataColumns:row*dvdDataFrameDataColumns+dvdEccDataColumns])
		}
	}

	column := make([]byte, dvdEccDataRows)
	parity := make([]byte, 16)
	for columnIndex := 0; columnIndex < dvdEccDataColumns; columnIndex++ {
		for row := 0; row < dvdEccDataRows; row++ {
			column[row] = ecc[row*dvdEccColumns+columnIndex]
		}
		rsAppendPo(column, parity)
		for row := 0; row < len(parity); row++ {
			ecc[(dvdEccDataRows+row)*dvdEccColumns+columnIndex] = parity[row]
		}
	}

	for row := 0; row < dvdEccRows; row++ {
		rsAppendPi(
			ecc[row*dvdEccColumns:row*dvdEccColumns+dvdEccDataColumns],
			ecc[row*dvdEccColumns+dvdEccDataColumns:row*dvdEccColumns+dvdEccDataColumns+10])
	}

	return ecc, nil
}

func interleaveRecordingFrames(ecc []byte) ([]byte, error) {
	if len(ecc) != dvdEccRows*dvdEccColumns {
		return nil, newArgumentErrorParam("Unexpected ECC Block size.", "ecc")
	}
	frames := make([]byte, len(ecc))
	for sourceRow := 0; sourceRow < dvdEccDataRows; sourceRow++ {
		destinationRow := sourceRow + sourceRow/12
		copy(frames[destinationRow*dvdEccColumns:destinationRow*dvdEccColumns+dvdEccColumns],
			ecc[sourceRow*dvdEccColumns:sourceRow*dvdEccColumns+dvdEccColumns])
	}
	for sourceRow := dvdEccDataRows; sourceRow < dvdEccRows; sourceRow++ {
		destinationRow := 13*(sourceRow-191) - 1
		copy(frames[destinationRow*dvdEccColumns:destinationRow*dvdEccColumns+dvdEccColumns],
			ecc[sourceRow*dvdEccColumns:sourceRow*dvdEccColumns+dvdEccColumns])
	}
	return frames, nil
}

func recordingFrameIndex(eccRow, eccColumn int) (int, error) {
	if uint(eccRow) >= dvdEccRows || uint(eccColumn) >= dvdEccColumns {
		return 0, &argumentOutOfRangeError{param: "eccRow"}
	}
	var recordingRow int
	if eccRow < dvdEccDataRows {
		recordingRow = eccRow + eccRow/12
	} else {
		recordingRow = 13*(eccRow-191) - 1
	}
	return recordingRow*dvdEccColumns + eccColumn, nil
}

func payloadDirectChannelOffset(payloadIndex int) (int, error) {
	if uint(payloadIndex) >= dvdPayloadBytesPerBlock {
		return 0, &argumentOutOfRangeError{param: "payloadIndex"}
	}
	recordingIndex, err := payloadRecordingFrameIndex(payloadIndex)
	if err != nil {
		return 0, err
	}
	syncFrame := recordingIndex / efmPlusBytesPerSyncFrame
	byteInSyncFrame := recordingIndex % efmPlusBytesPerSyncFrame
	return syncFrame*efmPlusBitsPerSyncFrame + 32 + byteInSyncFrame*16, nil
}

func payloadRecordingFrameIndex(payloadIndex int) (int, error) {
	if uint(payloadIndex) >= dvdPayloadBytesPerBlock {
		return 0, &argumentOutOfRangeError{param: "payloadIndex"}
	}
	sector := payloadIndex / dvdPayloadBytesPerSector
	frameIndex := 12 + (payloadIndex % dvdPayloadBytesPerSector)
	eccRow := sector*12 + frameIndex/dvdEccDataColumns
	eccColumn := frameIndex % dvdEccDataColumns
	return recordingFrameIndex(eccRow, eccColumn)
}

func syncCategory(syncFrameIndex int) (int, error) {
	if uint(syncFrameIndex) >= dvdSyncFrameCount {
		return 0, &argumentOutOfRangeError{param: "syncFrameIndex"}
	}
	withinSector := syncFrameIndex % dvdSyncFramesPerSector
	row := withinSector / 2
	if withinSector&1 == 0 {
		return eccFirstSyncCategories[row], nil
	}
	return eccSecondSyncCategories[row], nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
