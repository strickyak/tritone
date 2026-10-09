package main

import (
	"math"
	"sort"

	"github.com/mjibson/go-dsp/dsputils"
	"github.com/mjibson/go-dsp/fft"
)

// Peak represents a detected spectral peak.
type Peak struct {
	Freq     float64 // Frequency in Hz
	Mag      float64 // Spectral magnitude
	RelPower float64 // Normalized relative strength (sums to 1.0 among chunk peaks)
}

// DSPConfig controls FFT and peak picking parameters.
type DSPConfig struct {
	NumTones         int     // Number of peak frequencies to pick (N, default 3)
	MinDistHz        float64 // Minimum frequency distance between peaks in Hz (default 70.0)
	MinFreqHz        float64 // Minimum frequency to consider in Hz (default 50.0)
	MaxFreqHz        float64 // Maximum frequency to consider in Hz (default 12000.0)
	SilenceThreshold float64 // RMS threshold below which chunk is considered silent (default 1e-4)
	FadeMs           float64 // Fade-in / fade-out duration on edges in ms (default 2.0)
}

// DefaultDSPConfig returns sensible defaults.
func DefaultDSPConfig() DSPConfig {
	return DSPConfig{
		NumTones:         3,
		MinDistHz:        70.0,
		MinFreqHz:        50.0,
		MaxFreqHz:        12000.0,
		SilenceThreshold: 1e-4,
		FadeMs:           2.0,
	}
}

// MakeTukeyWindow creates a cosine-tapered window with fade-in and fade-out edges.
func MakeTukeyWindow(length int, fadeSamples int) []float64 {
	w := make([]float64, length)
	if fadeSamples <= 0 {
		for i := range w {
			w[i] = 1.0
		}
		return w
	}
	if fadeSamples > length/2 {
		fadeSamples = length / 2
	}

	for i := 0; i < length; i++ {
		if i < fadeSamples {
			w[i] = 0.5 * (1.0 - math.Cos(math.Pi*float64(i)/float64(fadeSamples)))
		} else if i >= length-fadeSamples {
			w[i] = 0.5 * (1.0 - math.Cos(math.Pi*float64(length-1-i)/float64(fadeSamples)))
		} else {
			w[i] = 1.0
		}
	}
	return w
}

// CalculateRMS calculates the root mean square energy of a slice of samples.
func CalculateRMS(samples []float64) float64 {
	if len(samples) == 0 {
		return 0.0
	}
	var sum float64
	for _, s := range samples {
		sum += s * s
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// AnalyzeChunk performs FFT on a chunk and picks N peak frequencies.
func AnalyzeChunk(chunk []float64, sampleRate int, cfg DSPConfig) ([]Peak, float64) {
	chunkLen := len(chunk)
	if chunkLen == 0 {
		return nil, 0.0
	}

	chunkRMS := CalculateRMS(chunk)
	if chunkRMS < cfg.SilenceThreshold {
		return nil, chunkRMS
	}

	// Fade-in and fade-out on the edges of the chunk for FFT
	fadeSamples := int(math.Round(float64(sampleRate) * cfg.FadeMs / 1000.0))
	if fadeSamples < 2 {
		fadeSamples = 2
	}
	win := MakeTukeyWindow(chunkLen, fadeSamples)

	// Zero-pad to next power of 2 (at least 2048 for high frequency resolution)
	fftLen := 2048
	for fftLen < chunkLen {
		fftLen *= 2
	}

	padded := make([]float64, fftLen)
	for i := 0; i < chunkLen; i++ {
		padded[i] = chunk[i] * win[i]
	}

	// Compute forward FFT using go-dsp/fft
	spectrum := fft.FFTReal(padded)
	numBins := fftLen / 2
	mags := make([]float64, numBins)

	var maxMag float64
	for i := 0; i < numBins; i++ {
		re := real(spectrum[i])
		im := imag(spectrum[i])
		mags[i] = math.Sqrt(re*re + im*im)
		if mags[i] > maxMag {
			maxMag = mags[i]
		}
	}

	if maxMag < 1e-6 {
		return nil, chunkRMS
	}

	// Find local maxima and refine peak frequency with parabolic interpolation
	sr := float64(sampleRate)
	flen := float64(fftLen)
	var candidates []Peak

	for i := 1; i < numBins-1; i++ {
		fCenter := float64(i) * sr / flen
		if fCenter < cfg.MinFreqHz || fCenter > cfg.MaxFreqHz {
			continue
		}

		if mags[i] > mags[i-1] && mags[i] > mags[i+1] {
			y0 := mags[i-1]
			y1 := mags[i]
			y2 := mags[i+1]

			denom := y0 - 2*y1 + y2
			delta := 0.0
			if math.Abs(denom) > 1e-12 {
				delta = 0.5 * (y0 - y2) / denom
				if delta > 0.5 {
					delta = 0.5
				} else if delta < -0.5 {
					delta = -0.5
				}
			}

			refinedFreq := (float64(i) + delta) * sr / flen
			refinedMag := y1 - 0.25*(y0-y2)*delta

			// Ignore peaks below -40 dB relative to chunk peak
			if refinedMag >= 0.01*maxMag {
				candidates = append(candidates, Peak{
					Freq: refinedFreq,
					Mag:  refinedMag,
				})
			}
		}
	}

	// Sort candidates by magnitude descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Mag > candidates[j].Mag
	})

	// Select up to cfg.NumTones peaks, enforcing minimum frequency distance
	var selected []Peak
	for _, cand := range candidates {
		tooClose := false
		for _, s := range selected {
			if math.Abs(cand.Freq-s.Freq) < cfg.MinDistHz {
				tooClose = true
				break
			}
		}
		if !tooClose {
			selected = append(selected, cand)
			if len(selected) == cfg.NumTones {
				break
			}
		}
	}

	// Calculate relative strengths
	var totalMag float64
	for _, p := range selected {
		totalMag += p.Mag
	}
	if totalMag > 0 {
		for i := range selected {
			selected[i].RelPower = selected[i].Mag / totalMag
		}
	}

	return selected, chunkRMS
}

// NextPowerOf2 returns the next power of 2 greater than or equal to n.
func NextPowerOf2(n int) int {
	return dsputils.NextPowerOf2(n)
}
