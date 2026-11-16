package cd

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// WaveHeaderBytes is the RIFF/WAV header size.
const WaveHeaderBytes = 44

// ValidateWaveLength checks the RIFF/WAV size limit.
func ValidateWaveLength(audioBytes int64) error {
	if audioBytes <= 0 || audioBytes > int64(^uint32(0))-36 {
		return fmt.Errorf("Audio exceeds the RIFF/WAV size limit. (Parameter 'audioBytes')")
	}
	return nil
}

// WriteWaveHeader writes a 44-byte PCM stereo 44.1kHz 16-bit header.
func WriteWaveHeader(output io.Writer, audioBytes int64) error {
	if err := ValidateWaveLength(audioBytes); err != nil {
		return err
	}
	header := make([]byte, WaveHeaderBytes)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(audioBytes+36))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 2)
	binary.LittleEndian.PutUint32(header[24:], 44100)
	binary.LittleEndian.PutUint32(header[28:], 176400)
	binary.LittleEndian.PutUint16(header[32:], 4)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(audioBytes))
	_, err := output.Write(header)
	return err
}

// WriteCue writes the CUE sheet next to the WAV track and returns its path.
func WriteCue(wavePath string) (string, error) {
	ext := filepath.Ext(wavePath)
	cuePath := wavePath[:len(wavePath)-len(ext)] + ".cue"
	content := "\ufeff" + "FILE \"" + filepath.Base(wavePath) + "\" WAVE\r\n" +
		"  TRACK 01 AUDIO\r\n" +
		"    INDEX 01 00:00:00\r\n"
	if err := os.WriteFile(cuePath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return cuePath, nil
}

// ReadWaveAudioRange locates the PCM data chunk of a RIFF/WAV stream.
// The reader must support seeking (an io.ReadSeeker at position 0).
func ReadWaveAudioRange(input io.ReadSeeker, length int64) (offset, audioLength int64, err error) {
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return 0, 0, err
	}
	readU32 := func() (uint32, error) {
		var b [4]byte
		if _, err := io.ReadFull(input, b[:]); err != nil {
			return 0, err
		}
		return binary.LittleEndian.Uint32(b[:]), nil
	}
	readU16 := func() (uint16, error) {
		var b [2]byte
		if _, err := io.ReadFull(input, b[:]); err != nil {
			return 0, err
		}
		return binary.LittleEndian.Uint16(b[:]), nil
	}
	invalid := func(msg string) (int64, int64, error) { return 0, 0, fmt.Errorf("%s", msg) }

	if length < 12 {
		return invalid("Expected a RIFF/WAV audio file.")
	}
	magic, err := readU32()
	if err != nil {
		return 0, 0, err
	}
	if magic != 0x46464952 {
		return invalid("Expected a RIFF/WAV audio file.")
	}
	riffSize, err := readU32()
	if err != nil {
		return 0, 0, err
	}
	riffEnd := int64(riffSize) + 8
	wave, err := readU32()
	if err != nil {
		return 0, 0, err
	}
	if wave != 0x45564157 || riffEnd < 12 || riffEnd > length {
		return invalid("The WAV header or declared length is invalid.")
	}

	hasFormat := false
	haveAudio := false
	var audioOffset, audioLen int64
	position := int64(12)
	for position < riffEnd {
		if riffEnd-position < 8 {
			return invalid("Truncated WAV chunk header.")
		}
		chunk, err := readU32()
		if err != nil {
			return 0, 0, err
		}
		chunkLength, err := readU32()
		if err != nil {
			return 0, 0, err
		}
		start := position + 8
		next := start + int64(chunkLength) + int64(chunkLength&1)
		if next > riffEnd {
			return invalid("Truncated WAV chunk data.")
		}
		switch chunk {
		case 0x20746D66: // fmt
			if hasFormat || chunkLength < 16 {
				return invalid("CD WAV audio must be PCM, 44100 Hz, 16-bit stereo.")
			}
			fields := make([]uint32, 0, 4)
			tag, err := readU16()
			if err != nil {
				return 0, 0, err
			}
			channels, err := readU16()
			if err != nil {
				return 0, 0, err
			}
			rate, err := readU32()
			if err != nil {
				return 0, 0, err
			}
			rateB, err := readU32()
			if err != nil {
				return 0, 0, err
			}
			fields = append(fields, rate, rateB)
			blockAlign, err := readU16()
			if err != nil {
				return 0, 0, err
			}
			bits, err := readU16()
			if err != nil {
				return 0, 0, err
			}
			_ = fields
			if tag != 1 || channels != 2 || rate != 44100 || rateB != 176400 || blockAlign != 4 || bits != 16 {
				return invalid("CD WAV audio must be PCM, 44100 Hz, 16-bit stereo.")
			}
			hasFormat = true
		case 0x61746164: // data
			if haveAudio || chunkLength == 0 || chunkLength%4 != 0 {
				return invalid("WAV must contain one data chunk with complete stereo samples.")
			}
			haveAudio = true
			audioOffset = start
			audioLen = int64(chunkLength)
		}
		if _, err := input.Seek(next, io.SeekStart); err != nil {
			return 0, 0, err
		}
		position = next
	}
	if !hasFormat || !haveAudio {
		return invalid("WAV is missing its PCM format or audio data chunk.")
	}
	return audioOffset, audioLen, nil
}

// hasWaveExtension reports whether the path ends in .wav (any case).
func hasWaveExtension(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".wav")
}
