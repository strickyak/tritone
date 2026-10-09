package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func main() {
	var (
		inputPath      string
		outputPath     string
		numTones       int
		chunkMs        float64
		fadeMs         float64
		minDistHz      float64
		minFreqHz      float64
		maxFreqHz      float64
		sampleRate     int
		inFormatStr    string
		outFormatStr   string
		overlapPercent float64
		smooth         bool
		verbose        bool
		quiet          bool
	)

	flag.StringVar(&inputPath, "i", "", "Input audio file path (or '-' for stdin)")
	flag.StringVar(&inputPath, "input", "", "Input audio file path (or '-' for stdin)")
	flag.StringVar(&outputPath, "o", "", "Output audio file path (or '-' for stdout)")
	flag.StringVar(&outputPath, "output", "", "Output audio file path (or '-' for stdout)")
	flag.IntVar(&numTones, "n", 3, "Number of peak frequencies per chunk (N)")
	flag.IntVar(&numTones, "tones", 3, "Number of peak frequencies per chunk (N)")
	flag.Float64Var(&chunkMs, "chunk-ms", 20.0, "Chunk duration in milliseconds")
	flag.Float64Var(&fadeMs, "fade-ms", 2.0, "Fade-in and fade-out duration on edges in milliseconds")
	flag.Float64Var(&minDistHz, "min-dist", 70.0, "Minimum frequency separation between peaks in Hz")
	flag.Float64Var(&minFreqHz, "min-freq", 50.0, "Minimum frequency to consider in Hz")
	flag.Float64Var(&maxFreqHz, "max-freq", 12000.0, "Maximum frequency to consider in Hz")
	flag.IntVar(&sampleRate, "rate", 48000, "Sample rate for raw PCM audio (samples per second)")
	flag.StringVar(&inFormatStr, "in-format", "auto", "Input format: auto, wav, u16le, s16le")
	flag.StringVar(&outFormatStr, "out-format", "auto", "Output format: auto, wav, u16le, s16le")
	flag.Float64Var(&overlapPercent, "overlap", 0.0, "Overlap fraction between chunks (0.0 to 0.75)")
	flag.BoolVar(&smooth, "smooth", true, "Enable continuous smooth voice transitions across chunks")
	flag.BoolVar(&verbose, "v", false, "Verbose mode: print detailed chunk frequency analysis")
	flag.BoolVar(&verbose, "verbose", false, "Verbose mode: print detailed chunk frequency analysis")
	flag.BoolVar(&quiet, "quiet", false, "Quiet mode: suppress progress messages")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: tritone [options] [input_file] [output_file]\n\n")
		fmt.Fprintf(os.Stderr, "Tritone analyzes audio chunks, identifies peak frequencies using FFT,\n")
		fmt.Fprintf(os.Stderr, "and resynthesizes the sound using N continuous tones per chunk.\n\n")
		fmt.Fprintf(os.Stderr, "If input_file does not end with .raw or .wav, ffmpeg is used to convert it to temporary WAV.\n")
		fmt.Fprintf(os.Stderr, "If output_file is not specified, output is played via mplayer from a temporary WAV.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// Parse positional arguments if not provided by flags
	args := flag.Args()
	if inputPath == "" && len(args) > 0 {
		inputPath = args[0]
		args = args[1:]
	}
	if outputPath == "" && len(args) > 0 {
		outputPath = args[0]
	}

	// Handle default input from stdin if nothing specified and stdin is not a terminal
	if inputPath == "" {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			inputPath = "-" // Reading from pipe
		} else {
			flag.Usage()
			os.Exit(1)
		}
	}

	actualInputPath := inputPath
	if inputPath != "" && inputPath != "-" {
		lowerIn := strings.ToLower(inputPath)
		if !strings.HasSuffix(lowerIn, ".raw") && !strings.HasSuffix(lowerIn, ".wav") {
			if !quiet {
				fmt.Fprintf(os.Stderr, "Converting '%s' to temporary WAV using ffmpeg...\n", inputPath)
			}
			tempWav, cleanup, err := convertToWAVWithFFmpeg(inputPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error converting input with ffmpeg: %v\n", err)
				os.Exit(1)
			}
			defer cleanup()
			actualInputPath = tempWav
		}
	}

	inFmt := parseAudioFormat(inFormatStr)
	outFmt := parseAudioFormat(outFormatStr)

	// Open input
	inReader, err := OpenAudioInput(actualInputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening input '%s': %v\n", inputPath, err)
		os.Exit(1)
	}
	defer inReader.Close()

	if !quiet && outputPath != "-" {
		fmt.Fprintf(os.Stderr, "Tritone: reading %s...\n", inputDesc(inputPath))
	}

	// Read audio
	audioData, err := ReadAudio(inReader, inFmt, sampleRate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading audio: %v\n", err)
		os.Exit(1)
	}

	if !quiet && outputPath != "-" {
		durationSec := float64(len(audioData.Samples)) / float64(audioData.SampleRate)
		fmt.Fprintf(os.Stderr, "Loaded %d samples (%.2f s) at %d Hz\n",
			len(audioData.Samples), durationSec, audioData.SampleRate)
	}

	// Configure DSP and Synthesis
	dspCfg := DSPConfig{
		NumTones:         numTones,
		MinDistHz:        minDistHz,
		MinFreqHz:        minFreqHz,
		MaxFreqHz:        maxFreqHz,
		SilenceThreshold: 1e-4,
		FadeMs:           fadeMs,
	}

	synthCfg := SynthConfig{
		ChunkMs:           chunkMs,
		FadeMs:            fadeMs,
		OverlapPercent:    overlapPercent,
		SmoothTransitions: smooth,
	}

	if !quiet && outputPath != "-" {
		fmt.Fprintf(os.Stderr, "Processing with N=%d tones, %.1f ms chunks, %.1f ms fade...\n",
			numTones, chunkMs, fadeMs)
	}

	// Process
	outAudio, chunkInfos := ProcessAudio(audioData, dspCfg, synthCfg)

	if verbose && outputPath != "-" {
		fmt.Fprintf(os.Stderr, "\n--- Chunk Analysis Details ---\n")
		for _, ci := range chunkInfos {
			if len(ci.Peaks) == 0 {
				fmt.Fprintf(os.Stderr, "Chunk %4d [%6.3fs]: (silence)\n", ci.Index, ci.StartSec)
				continue
			}
			var pStrs []string
			for _, p := range ci.Peaks {
				pStrs = append(pStrs, fmt.Sprintf("%6.1fHz (rel: %4.1f%%, mag: %5.1f)",
					p.Freq, p.RelPower*100.0, p.Mag))
			}
			fmt.Fprintf(os.Stderr, "Chunk %4d [%6.3fs, RMS %6.4f]: %s\n",
				ci.Index, ci.StartSec, ci.RMS, strings.Join(pStrs, " | "))
		}
		fmt.Fprintf(os.Stderr, "------------------------------\n\n")
	}

	// If output is not specified, play it from a temporary .wav file with mplayer command
	if outputPath == "" {
		tempOut, err := os.CreateTemp("", "tritone_out_*.wav")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating temporary output WAV: %v\n", err)
			os.Exit(1)
		}
		tempOutPath := tempOut.Name()
		defer os.Remove(tempOutPath)

		if err := WriteAudio(tempOut, outAudio, FormatWAV); err != nil {
			tempOut.Close()
			fmt.Fprintf(os.Stderr, "Error writing temporary output WAV: %v\n", err)
			os.Exit(1)
		}
		tempOut.Close()

		if !quiet {
			fmt.Fprintf(os.Stderr, "Playing synthesized audio with mplayer...\n")
		}

		if err := playWithMPlayer(tempOutPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error running mplayer: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Determine output format
	resolvedOutFmt := outFmt
	if resolvedOutFmt == FormatAuto {
		if outputPath != "-" {
			resolvedOutFmt = DeduceFormat(outputPath, FormatWAV)
		} else {
			// For stdout, if inFormat was raw or input was raw, default to u16le
			if inFmt == FormatU16LE || (!strings.HasSuffix(strings.ToLower(inputPath), ".wav") && inputPath != "") {
				resolvedOutFmt = FormatU16LE
			} else {
				resolvedOutFmt = FormatWAV
			}
		}
	}

	// Open output
	var outWriter io.WriteCloser
	outWriter, err = CreateAudioOutput(outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output '%s': %v\n", outputPath, err)
		os.Exit(1)
	}
	defer outWriter.Close()

	if err := WriteAudio(outWriter, outAudio, resolvedOutFmt); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	if !quiet && outputPath != "-" {
		fmt.Fprintf(os.Stderr, "Successfully wrote output to %s (format: %s)\n", outputPath, resolvedOutFmt)
	}
}

func convertToWAVWithFFmpeg(inputPath string) (string, func(), error) {
	tempFile, err := os.CreateTemp("", "tritone_in_*.wav")
	if err != nil {
		return "", nil, fmt.Errorf("creating temp wav file: %w", err)
	}
	tempPath := tempFile.Name()
	tempFile.Close()

	cleanup := func() {
		os.Remove(tempPath)
	}

	cmd := exec.Command("ffmpeg", "-y", "-i", inputPath, "-vn", "-ar", "48000", "-ac", "1", tempPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}

	return tempPath, cleanup, nil
}

func playWithMPlayer(wavPath string) error {
	cmd := exec.Command("mplayer", wavPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func parseAudioFormat(s string) AudioFormat {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "wav":
		return FormatWAV
	case "u16le", "u16", "uint16":
		return FormatU16LE
	case "s16le", "s16", "int16":
		return FormatS16LE
	default:
		return FormatAuto
	}
}

func inputDesc(path string) string {
	if path == "-" {
		return "stdin"
	}
	return "'" + path + "'"
}
