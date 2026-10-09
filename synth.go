package main

import (
	"math"
)

// ChunkInfo holds metadata and analysis results for a single chunk.
type ChunkInfo struct {
	Index    int
	StartSec float64
	RMS      float64
	Peaks    []Peak
}

// SynthConfig controls synthesis parameters.
type SynthConfig struct {
	ChunkMs           float64 // Duration of each chunk in ms (default 20.0)
	FadeMs            float64 // Fade-in / fade-out duration on edges in ms (default 2.0)
	OverlapPercent    float64 // Overlap between chunks: 0.0 for sequential, 0.5 for 50% overlap
	SmoothTransitions bool    // Whether to smoothly interpolate frequency and amplitude across chunks
}

// DefaultSynthConfig returns default synthesis parameters.
func DefaultSynthConfig() SynthConfig {
	return SynthConfig{
		ChunkMs:           20.0,
		FadeMs:            2.0,
		OverlapPercent:    0.0,
		SmoothTransitions: true,
	}
}

// SynthesizeChunk produces audio samples for one chunk from voice tracker,
// applying fade-in and fade-out on the edges.
func SynthesizeChunk(vt *VoiceTracker, chunkLen int, fadeSamples int, smooth bool) []float64 {
	out := make([]float64, chunkLen)
	sr := float64(vt.SampleRate)
	chunkLenF := float64(chunkLen)

	for s := 0; s < chunkLen; s++ {
		tFrac := float64(s) / chunkLenF
		var sampleVal float64

		for vIdx := range vt.Voices {
			v := &vt.Voices[vIdx]
			if !v.Active && v.Amp < 1e-5 && v.TargetAmp < 1e-5 {
				continue
			}

			var curFreq float64
			var curAmp float64

			if smooth {
				curFreq = v.Freq + (v.TargetFreq-v.Freq)*tFrac
				curAmp = v.Amp + (v.TargetAmp-v.Amp)*tFrac
			} else {
				curFreq = v.TargetFreq
				curAmp = v.TargetAmp
			}

			if curFreq > 0 {
				deltaPhase := 2.0 * math.Pi * curFreq / sr
				v.Phase += deltaPhase
				if v.Phase >= 2.0*math.Pi {
					v.Phase = math.Mod(v.Phase, 2.0*math.Pi)
				}
				sampleVal += curAmp * math.Sin(v.Phase)
			}
		}

		out[s] = sampleVal
	}

	// Update voice end states for next chunk
	for vIdx := range vt.Voices {
		v := &vt.Voices[vIdx]
		v.Freq = v.TargetFreq
		v.Amp = v.TargetAmp
		if v.Amp < 1e-4 {
			v.Active = false
		}
	}

	// Apply fade-in and fade-out on the edges of the chunk
	if fadeSamples > 0 {
		if fadeSamples > chunkLen/2 {
			fadeSamples = chunkLen / 2
		}
		fadeF := float64(fadeSamples)
		for s := 0; s < chunkLen; s++ {
			env := 1.0
			if s < fadeSamples {
				env = 0.5 * (1.0 - math.Cos(math.Pi*float64(s)/fadeF))
			} else if s >= chunkLen-fadeSamples {
				env = 0.5 * (1.0 - math.Cos(math.Pi*float64(chunkLen-1-s)/fadeF))
			}
			out[s] *= env
		}
	}

	return out
}

// ProcessAudio takes input audio, divides it into chunks, performs FFT peak analysis,
// tracks continuity, and synthesizes output chunks with edge fade-in/fade-out.
func ProcessAudio(input *AudioData, dspCfg DSPConfig, synthCfg SynthConfig) (*AudioData, []ChunkInfo) {
	totalSamples := len(input.Samples)
	sr := input.SampleRate
	chunkLen := int(math.Round(float64(sr) * synthCfg.ChunkMs / 1000.0))
	if chunkLen < 16 {
		chunkLen = 16
	}

	fadeSamples := int(math.Round(float64(sr) * synthCfg.FadeMs / 1000.0))
	if fadeSamples < 0 {
		fadeSamples = 0
	}

	var hopSize int
	if synthCfg.OverlapPercent > 0.0 && synthCfg.OverlapPercent < 0.9 {
		hopSize = int(math.Round(float64(chunkLen) * (1.0 - synthCfg.OverlapPercent)))
		if hopSize < 1 {
			hopSize = 1
		}
	} else {
		hopSize = chunkLen // Sequential non-overlapping chunks
	}

	tracker := NewVoiceTracker(dspCfg.NumTones, sr)

	// Calculate output length
	outLen := totalSamples
	if outLen < chunkLen {
		outLen = chunkLen
	}
	outSamples := make([]float64, outLen+chunkLen) // extra padding to avoid index overflow

	var chunkInfos []ChunkInfo
	chunkIdx := 0

	for pos := 0; pos < totalSamples; pos += hopSize {
		end := pos + chunkLen
		var inChunk []float64

		if end <= totalSamples {
			inChunk = input.Samples[pos:end]
		} else {
			// Pad final chunk with zeros if needed
			inChunk = make([]float64, chunkLen)
			copy(inChunk, input.Samples[pos:totalSamples])
		}

		// Perform FFT and pick N strong peaks
		peaks, chunkRMS := AnalyzeChunk(inChunk, sr, dspCfg)

		// Record chunk info
		info := ChunkInfo{
			Index:    chunkIdx,
			StartSec: float64(pos) / float64(sr),
			RMS:      chunkRMS,
			Peaks:    peaks,
		}
		chunkInfos = append(chunkInfos, info)

		// Update voice tracker with detected peaks
		tracker.Update(peaks, chunkRMS, synthCfg.SmoothTransitions)

		// Synthesize tone chunk with fade-in and fade-out on the edges
		synthChunk := SynthesizeChunk(tracker, chunkLen, fadeSamples, synthCfg.SmoothTransitions)

		// Add into output buffer
		for s := 0; s < chunkLen; s++ {
			if pos+s < len(outSamples) {
				outSamples[pos+s] += synthChunk[s]
			}
		}

		chunkIdx++
	}

	// Trim output to match total input length
	finalSamples := outSamples[:totalSamples]

	// Clamp to [-1.0, 1.0] to prevent any digital clipping
	for i := range finalSamples {
		if finalSamples[i] > 1.0 {
			finalSamples[i] = 1.0
		} else if finalSamples[i] < -1.0 {
			finalSamples[i] = -1.0
		}
	}

	return &AudioData{
		Samples:    finalSamples,
		SampleRate: sr,
	}, chunkInfos
}
