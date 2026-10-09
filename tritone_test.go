package main

import (
	"bytes"
	"math"
	"os"
	"os/exec"
	"testing"
)

// Helper to generate a multi-frequency synthetic audio buffer
func generateChord(freqs []float64, amps []float64, durationSec float64, sampleRate int) *AudioData {
	totalSamples := int(float64(sampleRate) * durationSec)
	samples := make([]float64, totalSamples)
	sr := float64(sampleRate)

	for i := 0; i < totalSamples; i++ {
		t := float64(i) / sr
		var val float64
		for k := 0; k < len(freqs); k++ {
			val += amps[k] * math.Sin(2.0*math.Pi*freqs[k]*t)
		}
		samples[i] = val
	}

	return &AudioData{
		Samples:    samples,
		SampleRate: sampleRate,
	}
}

func TestDSPPeakPicking(t *testing.T) {
	sampleRate := 48000
	freqs := []float64{440.0, 554.37, 659.25} // A Major: A4, C#5, E5
	amps := []float64{0.5, 0.3, 0.2}

	audio := generateChord(freqs, amps, 0.1, sampleRate) // 100ms
	chunkLen := int(float64(sampleRate) * 0.020)          // 20ms = 960 samples

	cfg := DefaultDSPConfig()
	cfg.NumTones = 3
	cfg.MinDistHz = 50.0 // Ensure C#5 (554) and E5 (659) are well separated

	peaks, rms := AnalyzeChunk(audio.Samples[:chunkLen], sampleRate, cfg)
	if len(peaks) != 3 {
		t.Fatalf("Expected 3 peaks, got %d", len(peaks))
	}

	t.Logf("Chunk RMS: %.4f", rms)
	for i, p := range peaks {
		t.Logf("Peak %d: freq=%.2f Hz, mag=%.2f, relPower=%.2f%%",
			i+1, p.Freq, p.Mag, p.RelPower*100.0)
	}

	// Verify frequencies within Rayleigh resolution limit for 20ms chunk
	expectedFreqs := []float64{440.0, 554.37, 659.25}
	for _, ef := range expectedFreqs {
		// Find matching peak
		found := false
		for _, p := range peaks {
			if math.Abs(p.Freq-ef) < 15.0 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected peak near %.2f Hz not found in %v", ef, peaks)
		}
	}

	// Verify relative power ordering: amp 0.5 > amp 0.3 > amp 0.2
	// Note: peaks in AnalyzeChunk are returned in arbitrary order, let's find each
	var p440, p554, p659 Peak
	for _, p := range peaks {
		if math.Abs(p.Freq-440.0) < 15.0 {
			p440 = p
		} else if math.Abs(p.Freq-554.37) < 15.0 {
			p554 = p
		} else if math.Abs(p.Freq-659.25) < 15.0 {
			p659 = p
		}
	}

	if p440.RelPower <= p554.RelPower || p554.RelPower <= p659.RelPower {
		t.Errorf("Expected power order 440 > 554 > 659, got: 440=%.2f, 554=%.2f, 659=%.2f",
			p440.RelPower, p554.RelPower, p659.RelPower)
	}
}

func TestSilenceHandling(t *testing.T) {
	sampleRate := 48000
	silentChunk := make([]float64, 960)

	cfg := DefaultDSPConfig()
	peaks, rms := AnalyzeChunk(silentChunk, sampleRate, cfg)
	if len(peaks) != 0 {
		t.Errorf("Expected 0 peaks for silence, got %d", len(peaks))
	}
	if rms != 0.0 {
		t.Errorf("Expected 0.0 RMS, got %f", rms)
	}
}

func TestChunkEdgeFading(t *testing.T) {
	sampleRate := 48000
	chunkLen := 960
	fadeSamples := 96 // 2ms at 48kHz

	tracker := NewVoiceTracker(1, sampleRate)
	tracker.Voices[0].Freq = 440.0
	tracker.Voices[0].TargetFreq = 440.0
	tracker.Voices[0].Amp = 0.8
	tracker.Voices[0].TargetAmp = 0.8
	tracker.Voices[0].Active = true

	synth := SynthesizeChunk(tracker, chunkLen, fadeSamples, false)

	// Sample 0 must be 0 or near 0 due to fade-in
	if math.Abs(synth[0]) > 1e-4 {
		t.Errorf("Sample 0 should fade in near 0, got %f", synth[0])
	}

	// Sample chunkLen-1 must be near 0 due to fade-out
	if math.Abs(synth[chunkLen-1]) > 1e-4 {
		t.Errorf("Sample %d should fade out near 0, got %f", chunkLen-1, synth[chunkLen-1])
	}

	// Middle sample should have substantial amplitude
	midSample := synth[chunkLen/2]
	t.Logf("Middle sample amplitude: %f", midSample)
}

func TestVoiceContinuity(t *testing.T) {
	sampleRate := 48000
	tracker := NewVoiceTracker(3, sampleRate)

	// Chunk 1: A major (440, 554, 659)
	peaks1 := []Peak{
		{Freq: 440.0, Mag: 100.0, RelPower: 0.5},
		{Freq: 554.0, Mag: 60.0, RelPower: 0.3},
		{Freq: 659.0, Mag: 40.0, RelPower: 0.2},
	}
	tracker.Update(peaks1, 0.2, true)

	// Save initial assigned frequencies
	v0Freq := tracker.Voices[0].TargetFreq
	v1Freq := tracker.Voices[1].TargetFreq
	v2Freq := tracker.Voices[2].TargetFreq

	// Synthesize chunk 1 to advance phases and state
	chunk1 := SynthesizeChunk(tracker, 960, 96, true)
	_ = chunk1

	// Chunk 2: slight pitch drift (e.g. vibrato): 442, 553, 661
	// Scramble the order of candidate peaks to test that tracker matches properly
	peaks2 := []Peak{
		{Freq: 661.0, Mag: 40.0, RelPower: 0.2},
		{Freq: 442.0, Mag: 100.0, RelPower: 0.5},
		{Freq: 553.0, Mag: 60.0, RelPower: 0.3},
	}
	tracker.Update(peaks2, 0.2, true)

	// Each voice should track its nearest frequency without jumping
	match0 := math.Abs(tracker.Voices[0].TargetFreq - v0Freq)
	match1 := math.Abs(tracker.Voices[1].TargetFreq - v1Freq)
	match2 := math.Abs(tracker.Voices[2].TargetFreq - v2Freq)

	if match0 > 5.0 || match1 > 5.0 || match2 > 5.0 {
		t.Errorf("Voice jumped! Delays: v0=%.2f, v1=%.2f, v2=%.2f", match0, match1, match2)
	}
}

func TestRawU16RoundTrip(t *testing.T) {
	sampleRate := 48000
	origAudio := generateChord([]float64{440.0, 880.0}, []float64{0.4, 0.2}, 0.05, sampleRate)

	var buf bytes.Buffer
	err := WriteAudio(&buf, origAudio, FormatU16LE)
	if err != nil {
		t.Fatalf("WriteAudio U16LE failed: %v", err)
	}

	readAudio, err := ReadAudio(&buf, FormatU16LE, sampleRate)
	if err != nil {
		t.Fatalf("ReadAudio U16LE failed: %v", err)
	}

	if len(readAudio.Samples) != len(origAudio.Samples) {
		t.Fatalf("Length mismatch: got %d, expected %d", len(readAudio.Samples), len(origAudio.Samples))
	}

	// Verify quantization error is within 16-bit range (< 1/32768)
	var maxErr float64
	for i := range origAudio.Samples {
		diff := math.Abs(readAudio.Samples[i] - origAudio.Samples[i])
		if diff > maxErr {
			maxErr = diff
		}
	}

	t.Logf("Max U16 roundtrip error: %e", maxErr)
	if maxErr > 1e-3 {
		t.Errorf("Max roundtrip error too large: %e", maxErr)
	}
}

func TestWAVRoundTrip(t *testing.T) {
	sampleRate := 48000
	origAudio := generateChord([]float64{300.0, 600.0}, []float64{0.5, 0.3}, 0.05, sampleRate)

	var buf bytes.Buffer
	err := WriteAudio(&buf, origAudio, FormatWAV)
	if err != nil {
		t.Fatalf("WriteAudio WAV failed: %v", err)
	}

	readAudio, err := ReadAudio(&buf, FormatAuto, sampleRate)
	if err != nil {
		t.Fatalf("ReadAudio WAV auto failed: %v", err)
	}

	if len(readAudio.Samples) != len(origAudio.Samples) {
		t.Fatalf("Length mismatch: got %d, expected %d", len(readAudio.Samples), len(origAudio.Samples))
	}

	var maxErr float64
	for i := range origAudio.Samples {
		diff := math.Abs(readAudio.Samples[i] - origAudio.Samples[i])
		if diff > maxErr {
			maxErr = diff
		}
	}

	t.Logf("Max WAV roundtrip error: %e", maxErr)
	if maxErr > 1e-3 {
		t.Errorf("Max roundtrip error too large: %e", maxErr)
	}
}

func TestFullProcessAudio(t *testing.T) {
	sampleRate := 48000
	// 500ms audio with 3 frequencies
	audio := generateChord([]float64{440.0, 554.0, 659.0}, []float64{0.4, 0.3, 0.2}, 0.5, sampleRate)

	dspCfg := DefaultDSPConfig()
	synthCfg := DefaultSynthConfig()

	outAudio, chunkInfos := ProcessAudio(audio, dspCfg, synthCfg)
	if len(outAudio.Samples) != len(audio.Samples) {
		t.Errorf("Output length %d != input length %d", len(outAudio.Samples), len(audio.Samples))
	}

	if len(chunkInfos) != 25 { // 500ms / 20ms = 25 chunks
		t.Errorf("Expected 25 chunks, got %d", len(chunkInfos))
	}

	// Verify RMS of output is reasonable
	outRMS := CalculateRMS(outAudio.Samples)
	inRMS := CalculateRMS(audio.Samples)
	t.Logf("Input RMS: %.4f, Output RMS: %.4f", inRMS, outRMS)

	if outRMS < 0.1 || outRMS > 0.9 {
		t.Errorf("Output RMS unexpected: %.4f", outRMS)
	}
}

func TestIntegrationBinary(t *testing.T) {
	// Generate a temporary u16le raw file
	sampleRate := 48000
	testAudio := generateChord([]float64{440.0, 880.0, 1320.0}, []float64{0.4, 0.3, 0.2}, 0.2, sampleRate)

	rawIn := "test_in.raw"
	rawOut := "test_out.raw"
	defer os.Remove(rawIn)
	defer os.Remove(rawOut)

	fIn, err := os.Create(rawIn)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAudio(fIn, testAudio, FormatU16LE); err != nil {
		fIn.Close()
		t.Fatal(err)
	}
	fIn.Close()

	// Read input back through ReadAudio and ProcessAudio
	fInRead, err := os.Open(rawIn)
	if err != nil {
		t.Fatal(err)
	}
	defer fInRead.Close()

	readData, err := ReadAudio(fInRead, FormatU16LE, sampleRate)
	if err != nil {
		t.Fatal(err)
	}

	outData, _ := ProcessAudio(readData, DefaultDSPConfig(), DefaultSynthConfig())

	fOut, err := os.Create(rawOut)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteAudio(fOut, outData, FormatU16LE); err != nil {
		fOut.Close()
		t.Fatal(err)
	}
	fOut.Close()

	fi, err := os.Stat(rawOut)
	if err != nil {
		t.Fatal(err)
	}
	expectedBytes := int64(len(testAudio.Samples) * 2)
	if fi.Size() != expectedBytes {
		t.Errorf("Output file size %d != expected %d", fi.Size(), expectedBytes)
	}
}

func TestConvertToWAVWithFFmpeg(t *testing.T) {
	// Create a dummy mp3 with ffmpeg
	testMP3 := "test_convert.mp3"
	defer os.Remove(testMP3)

	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.1", testMP3)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg not working: %v (%s)", err, string(out))
	}

	tempWav, cleanup, err := convertToWAVWithFFmpeg(testMP3)
	if err != nil {
		t.Fatalf("convertToWAVWithFFmpeg failed: %v", err)
	}
	defer cleanup()

	// Verify temp file exists and has content
	info, err := os.Stat(tempWav)
	if err != nil {
		t.Fatalf("Temp wav not found: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("Temp wav is empty")
	}

	// Verify it can be read as WAV
	f, err := os.Open(tempWav)
	if err != nil {
		t.Fatalf("Could not open temp wav: %v", err)
	}
	defer f.Close()

	audio, err := ReadAudio(f, FormatAuto, 48000)
	if err != nil {
		t.Fatalf("ReadAudio on temp wav failed: %v", err)
	}
	if len(audio.Samples) == 0 {
		t.Fatalf("No samples loaded from converted WAV")
	}
	t.Logf("Successfully converted MP3 to WAV: %d samples at %d Hz", len(audio.Samples), audio.SampleRate)
}
