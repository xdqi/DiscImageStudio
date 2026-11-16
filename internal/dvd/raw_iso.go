package dvd

import (
	"os"
	"path/filepath"
)

// rawIsoImageWriter ports RawIsoImageWriter.
type rawIsoImageWriter struct {
	output        *os.File
	volumeSectors uint32
	sparseFile    bool
}

func newRawIsoImageWriter(path string, volumeSectors uint32, preserveExisting bool) (*rawIsoImageWriter, error) {
	if volumeSectors == 0 {
		return nil, &argumentOutOfRangeError{param: "volumeSectors"}
	}
	full := fullPath(path)
	if directory := filepath.Dir(full); directory != "" {
		if err := os.MkdirAll(directory, 0o777); err != nil {
			return nil, err
		}
	}

	if preserveExisting {
		expectedLength := int64(volumeSectors) * isoLogicalSectorBytes
		info, err := os.Stat(full)
		if err != nil || info.Size() != expectedLength {
			return nil, newArgumentErrorParam("The existing hybrid ISO size does not match the reported disc size.", "path")
		}
	}

	flags := os.O_RDWR
	if !preserveExisting {
		flags |= os.O_CREATE | os.O_TRUNC
	}
	output, err := os.OpenFile(full, flags, 0o666)
	if err != nil {
		if preserveExisting {
			return nil, translateReadError(err, full)
		}
		return nil, translateWriteError(err, full)
	}
	writer := &rawIsoImageWriter{output: output, volumeSectors: volumeSectors, sparseFile: isoTryMarkSparse()}
	if !preserveExisting {
		if err := output.Truncate(int64(volumeSectors) * isoLogicalSectorBytes); err != nil {
			output.Close()
			return nil, err
		}
	}
	return writer, nil
}

func (w *rawIsoImageWriter) writeBlock(lba uint32, payloads []byte) error {
	if len(payloads) == 0 || len(payloads)%isoLogicalSectorBytes != 0 {
		return newArgumentErrorParam("Raw ISO payload must contain complete 2048-byte sectors.", "payloads")
	}
	sectorCount := uint64(len(payloads) / isoLogicalSectorBytes)
	if uint64(lba)+sectorCount > uint64(w.volumeSectors) {
		return &argumentOutOfRangeError{param: "lba", msg: "Payload exceeds raw ISO size."}
	}
	_, err := w.output.WriteAt(payloads, int64(lba)*isoLogicalSectorBytes)
	return err
}

func (w *rawIsoImageWriter) close() error {
	if w.output == nil {
		return nil
	}
	syncErr := w.output.Sync()
	closeErr := w.output.Close()
	w.output = nil
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
