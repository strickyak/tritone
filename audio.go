package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
)

// AudioFormat represents the audio encoding.
type AudioFormat string

const (
	FormatAuto  AudioFormat = "auto"
	FormatWAV   AudioFormat = "wav"
	FormatU16LE AudioFormat = "u16le"
	FormatS16LE AudioFormat = "s16le"
)

// AudioData holds mono floating-point audio samples and sample rate.
type AudioData struct {
	Samples    []float64
	SampleRate int
}

// ReadAudio reads audio from a reader or file, detecting WAV or raw PCM.
func ReadAudio(r io.Reader, format AudioFormat, defaultSampleRate int) (*AudioData, error) {
	// Read header prefix to detect format if auto
	header := make([]byte, 12)
	n, err := io.ReadFull(r, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, fmt.Errorf("reading audio header: %w", err)
	}

	combinedReader := io.MultiReader(bytes.NewReader(header[:n]), r)

	isWav := false
	if n >= 12 && string(header[0:4]) == "RIFF" && string(header[8:12]) == "WAVE" {
		isWav = true
	}

	if format == FormatWAV || (format == FormatAuto && isWav) {
		return readWAV(combinedReader)
	}

	// Raw PCM
	sampleRate := defaultSampleRate
	if sampleRate <= 0 {
		sampleRate = 48000
	}

	rawFormat := format
	if rawFormat == FormatAuto {
		rawFormat = FormatU16LE // Default raw format is Mono Unsigned 16-bit LE
	}

	return readRawPCM(combinedReader, rawFormat, sampleRate)
}

func readWAV(r io.Reader) (*AudioData, error) {
	// Read 12-byte RIFF header
	var riffHeader struct {
		ChunkID   [4]byte
		ChunkSize uint32
		Format    [4]byte
	}
	if err := binary.Read(r, binary.LittleEndian, &riffHeader); err != nil {
		return nil, fmt.Errorf("reading WAV header: %w", err)
	}
	if string(riffHeader.ChunkID[:]) != "RIFF" || string(riffHeader.Format[:]) != "WAVE" {
		return nil, errors.New("invalid WAV RIFF header")
	}

	var audioFormat uint16
	var numChannels uint16
	var sampleRate uint32
	var bitsPerSample uint16
	var rawData []byte
	fmtParsed := false

	// Iterate chunks
	for {
		var chunkHeader struct {
			SubchunkID   [4]byte
			SubchunkSize uint32
		}
		err := binary.Read(r, binary.LittleEndian, &chunkHeader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("reading chunk header: %w", err)
		}

		chunkID := string(chunkHeader.SubchunkID[:])
		chunkSize := chunkHeader.SubchunkSize

		switch chunkID {
		case "fmt ":
			fmtData := make([]byte, chunkSize)
			if _, err := io.ReadFull(r, fmtData); err != nil {
				return nil, fmt.Errorf("reading fmt chunk: %w", err)
			}
			if len(fmtData) < 16 {
				return nil, errors.New("fmt chunk too small")
			}
			audioFormat = binary.LittleEndian.Uint16(fmtData[0:2])
			numChannels = binary.LittleEndian.Uint16(fmtData[2:4])
			sampleRate = binary.LittleEndian.Uint32(fmtData[4:8])
			bitsPerSample = binary.LittleEndian.Uint16(fmtData[14:16])

			// Support WAVE_FORMAT_EXTENSIBLE (0xFFFE = 65534)
			if audioFormat == 0xFFFE && len(fmtData) >= 40 {
				subFormat := binary.LittleEndian.Uint16(fmtData[24:26])
				audioFormat = subFormat
			}
			fmtParsed = true

		case "data":
			if !fmtParsed {
				return nil, errors.New("data chunk before fmt chunk")
			}
			rawData = make([]byte, chunkSize)
			if _, err := io.ReadFull(r, rawData); err != nil {
				return nil, fmt.Errorf("reading data chunk: %w", err)
			}
			// Reached audio data
			goto decodeData

		default:
			// Skip unrecognized chunk
			if _, err := io.CopyN(io.Discard, r, int64(chunkSize)); err != nil {
				return nil, fmt.Errorf("skipping chunk %s: %w", chunkID, err)
			}
		}

		// WAV chunk sizes are word-padded (must align to even byte boundary)
		if chunkSize%2 != 0 {
			var pad [1]byte
			if _, err := io.ReadFull(r, pad[:]); err != nil && !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("skipping chunk padding: %w", err)
			}
		}
	}

decodeData:
	if rawData == nil {
		return nil, errors.New("no audio data chunk found in WAV")
	}
	if numChannels == 0 {
		return nil, errors.New("invalid channel count: 0")
	}

	bytesPerSample := int(bitsPerSample / 8)
	if bytesPerSample == 0 {
		return nil, fmt.Errorf("invalid bits per sample: %d", bitsPerSample)
	}
	blockAlign := int(numChannels) * bytesPerSample
	numFrames := len(rawData) / blockAlign
	samples := make([]float64, numFrames)

	channels := int(numChannels)
	for i := 0; i < numFrames; i++ {
		offset := i * blockAlign
		var frameSum float64 = 0.0

		for ch := 0; ch < channels; ch++ {
			sampleOffset := offset + ch*bytesPerSample
			var val float64

			switch {
			case audioFormat == 1 && bitsPerSample == 8: // 8-bit unsigned PCM
				u := rawData[sampleOffset]
				val = (float64(u) - 128.0) / 128.0

			case audioFormat == 1 && bitsPerSample == 16: // 16-bit signed PCM
				s := int16(binary.LittleEndian.Uint16(rawData[sampleOffset : sampleOffset+2]))
				val = float64(s) / 32768.0

			case audioFormat == 1 && bitsPerSample == 24: // 24-bit signed PCM
				b0 := rawData[sampleOffset]
				b1 := rawData[sampleOffset+1]
				b2 := rawData[sampleOffset+2]
				s := int32(b0) | (int32(b1) << 8) | (int32(int8(b2)) << 16)
				val = float64(s) / 8388608.0

			case audioFormat == 3 && bitsPerSample == 32: // 32-bit IEEE float
				bits := binary.LittleEndian.Uint32(rawData[sampleOffset : sampleOffset+4])
				val = float64(math.Float32frombits(bits))

			default:
				return nil, fmt.Errorf("unsupported WAV format: audioFormat=%d, bitsPerSample=%d", audioFormat, bitsPerSample)
			}

			frameSum += val
		}
		samples[i] = frameSum / float64(channels)
	}

	return &AudioData{
		Samples:    samples,
		SampleRate: int(sampleRate),
	}, nil
}

func readRawPCM(r io.Reader, format AudioFormat, sampleRate int) (*AudioData, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading raw PCM: %w", err)
	}

	numBytes := len(data)
	if numBytes%2 != 0 {
		data = data[:numBytes-1] // Truncate odd trailing byte
		numBytes = len(data)
	}

	numSamples := numBytes / 2
	samples := make([]float64, numSamples)

	for i := 0; i < numSamples; i++ {
		offset := i * 2
		u := binary.LittleEndian.Uint16(data[offset : offset+2])

		if format == FormatS16LE {
			s := int16(u)
			samples[i] = float64(s) / 32768.0
		} else {
			// FormatU16LE (Mono Unsigned 16-bit LE)
			// Map [0, 65535] with midpoint 32768 to [-1.0, 1.0]
			samples[i] = (float64(u) - 32768.0) / 32768.0
		}
	}

	return &AudioData{
		Samples:    samples,
		SampleRate: sampleRate,
	}, nil
}

// WriteAudio writes mono audio samples to a writer in the specified format.
func WriteAudio(w io.Writer, audio *AudioData, format AudioFormat) error {
	switch format {
	case FormatWAV:
		return writeWAV(w, audio, false)
	case FormatU16LE:
		return writeRawPCM(w, audio, FormatU16LE)
	case FormatS16LE:
		return writeRawPCM(w, audio, FormatS16LE)
	default:
		return writeWAV(w, audio, false)
	}
}

func writeWAV(w io.Writer, audio *AudioData, unsigned16 bool) error {
	numSamples := len(audio.Samples)
	numChannels := uint16(1)
	bitsPerSample := uint16(16)
	bytesPerSample := uint16(2)
	blockAlign := numChannels * bytesPerSample
	byteRate := uint32(audio.SampleRate) * uint32(blockAlign)
	dataSize := uint32(numSamples * int(blockAlign))
	riffSize := 36 + dataSize

	// RIFF header
	if _, err := w.Write([]byte("RIFF")); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, riffSize); err != nil {
		return err
	}
	if _, err := w.Write([]byte("WAVE")); err != nil {
		return err
	}

	// fmt chunk
	if _, err := w.Write([]byte("fmt ")); err != nil {
		return err
	}
	var fmtChunkSize uint32 = 16
	if err := binary.Write(w, binary.LittleEndian, fmtChunkSize); err != nil {
		return err
	}
	var audioFormat uint16 = 1 // PCM
	if err := binary.Write(w, binary.LittleEndian, audioFormat); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, numChannels); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(audio.SampleRate)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, byteRate); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, blockAlign); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, bitsPerSample); err != nil {
		return err
	}

	// data chunk
	if _, err := w.Write([]byte("data")); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, dataSize); err != nil {
		return err
	}

	buf := make([]byte, numSamples*2)
	for i, f := range audio.Samples {
		clamped := math.Max(-1.0, math.Min(1.0, f))
		if unsigned16 {
			u := uint16(math.Round(clamped*32767.0 + 32768.0))
			binary.LittleEndian.PutUint16(buf[i*2:i*2+2], u)
		} else {
			s := int16(math.Round(clamped * 32767.0))
			binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(s))
		}
	}

	_, err := w.Write(buf)
	return err
}

func writeRawPCM(w io.Writer, audio *AudioData, format AudioFormat) error {
	buf := make([]byte, len(audio.Samples)*2)
	for i, f := range audio.Samples {
		clamped := math.Max(-1.0, math.Min(1.0, f))
		if format == FormatS16LE {
			s := int16(math.Round(clamped * 32767.0))
			binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(s))
		} else {
			// FormatU16LE (Mono Unsigned 16-bit LE)
			u := uint16(math.Round(clamped*32767.0 + 32768.0))
			binary.LittleEndian.PutUint16(buf[i*2:i*2+2], u)
		}
	}
	_, err := w.Write(buf)
	return err
}

// DeduceFormat infers format from filename or defaults.
func DeduceFormat(filename string, defaultFmt AudioFormat) AudioFormat {
	lower := strings.ToLower(filename)
	if strings.HasSuffix(lower, ".wav") {
		return FormatWAV
	}
	if strings.HasSuffix(lower, ".u16") || strings.HasSuffix(lower, ".raw") || strings.HasSuffix(lower, ".pcm") {
		return FormatU16LE
	}
	if strings.HasSuffix(lower, ".s16") {
		return FormatS16LE
	}
	return defaultFmt
}

// OpenAudioInput opens a file or stdin for audio reading.
func OpenAudioInput(path string) (io.ReadCloser, error) {
	if path == "" || path == "-" {
		return os.Stdin, nil
	}
	return os.Open(path)
}

// CreateAudioOutput creates a file or stdout for audio writing.
func CreateAudioOutput(path string) (io.WriteCloser, error) {
	if path == "" || path == "-" {
		return os.Stdout, nil
	}
	return os.Create(path)
}
