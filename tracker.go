package main

import (
	"math"
	"sort"
)

// VoiceState represents the ongoing state of a synthesized tone voice.
type VoiceState struct {
	Freq      float64 // Current frequency in Hz
	TargetFreq float64 // Target frequency in Hz for the current chunk
	Amp       float64 // Current amplitude
	TargetAmp float64 // Target amplitude for the current chunk
	Phase     float64 // Running phase in radians [0, 2pi)
	Active    bool    // Whether the voice is currently producing sound
}

// VoiceTracker manages N voices across consecutive chunks for continuity.
type VoiceTracker struct {
	Voices     []VoiceState
	NumVoices  int
	SampleRate int
	MaxJumpOct float64 // Max octave jump to consider same voice track (default 1.5)
}

// NewVoiceTracker creates a new tracker with N voices.
func NewVoiceTracker(numVoices int, sampleRate int) *VoiceTracker {
	voices := make([]VoiceState, numVoices)
	return &VoiceTracker{
		Voices:     voices,
		NumVoices:  numVoices,
		SampleRate: sampleRate,
		MaxJumpOct: 1.5,
	}
}

// Update processes detected peaks for a chunk and assigns them to voices.
func (vt *VoiceTracker) Update(peaks []Peak, chunkRMS float64, smoothTransitions bool) {
	numPeaks := len(peaks)
	numVoices := vt.NumVoices

	// Determine any active voice currently
	hasActiveVoices := false
	for _, v := range vt.Voices {
		if v.Active && v.Amp > 1e-4 {
			hasActiveVoices = true
			break
		}
	}

	// Calculate target amplitudes based on relative strength and chunk RMS
	peakAmps := make([]float64, numPeaks)
	if numPeaks > 0 && chunkRMS > 1e-5 {
		// Target total peak amplitude proportional to chunk RMS
		// For sum of N sines, scale = sqrt(2) * RMS
		targetTotalAmp := math.Min(0.95, math.Sqrt(2.0)*chunkRMS)
		for i, p := range peaks {
			// Proportional to relative power
			peakAmps[i] = targetTotalAmp * p.RelPower
		}
		// Limit individual amplitude to avoid clipping
		var sumAmp float64
		for _, a := range peakAmps {
			sumAmp += a
		}
		if sumAmp > 0.95 {
			scale := 0.95 / sumAmp
			for i := range peakAmps {
				peakAmps[i] *= scale
			}
		}
	}

	// If no previous active voices, simply assign sorted by frequency ascending
	if !hasActiveVoices {
		// Sort peaks by frequency
		type peakItem struct {
			Peak Peak
			Amp  float64
		}
		items := make([]peakItem, numPeaks)
		for i := 0; i < numPeaks; i++ {
			items[i] = peakItem{Peak: peaks[i], Amp: peakAmps[i]}
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].Peak.Freq < items[j].Peak.Freq
		})

		for i := 0; i < numVoices; i++ {
			if i < len(items) {
				vt.Voices[i].Freq = items[i].Peak.Freq
				vt.Voices[i].TargetFreq = items[i].Peak.Freq
				if smoothTransitions {
					vt.Voices[i].Amp = 0.0 // Ramp up from 0
					vt.Voices[i].TargetAmp = items[i].Amp
				} else {
					vt.Voices[i].Amp = items[i].Amp
					vt.Voices[i].TargetAmp = items[i].Amp
				}
				vt.Voices[i].Active = true
			} else {
				vt.Voices[i].TargetAmp = 0.0
				vt.Voices[i].Active = false
			}
		}
		return
	}

	// Previous voices exist: find optimal assignment minimizing frequency shift
	// Distance between voice frequency and candidate peak frequency
	costMatrix := make([][]float64, numVoices)
	for i := 0; i < numVoices; i++ {
		costMatrix[i] = make([]float64, numPeaks)
		for j := 0; j < numPeaks; j++ {
			fVoice := vt.Voices[i].Freq
			fPeak := peaks[j].Freq
			if fVoice <= 0 || fPeak <= 0 {
				costMatrix[i][j] = 100.0
				continue
			}
			octDiff := math.Abs(math.Log2(fPeak / fVoice))
			if octDiff > vt.MaxJumpOct {
				costMatrix[i][j] = 10.0 + octDiff // Penalty for large frequency jump
			} else {
				costMatrix[i][j] = octDiff
			}
		}
	}

	// Solve assignment for small N (N=3 has 6 permutations; general N <= 8 by permutation)
	bestAssignment := findBestMatching(costMatrix, numVoices, numPeaks)

	// Update voices with assignment
	assignedPeaks := make(map[int]bool)
	for vIdx, pIdx := range bestAssignment {
		if pIdx >= 0 {
			assignedPeaks[pIdx] = true
			p := peaks[pIdx]
			amp := peakAmps[pIdx]

			if smoothTransitions {
				vt.Voices[vIdx].TargetFreq = p.Freq
				vt.Voices[vIdx].TargetAmp = amp
			} else {
				vt.Voices[vIdx].Freq = p.Freq
				vt.Voices[vIdx].TargetFreq = p.Freq
				vt.Voices[vIdx].Amp = amp
				vt.Voices[vIdx].TargetAmp = amp
			}
			vt.Voices[vIdx].Active = true
		} else {
			// Unmatched voice fades out
			vt.Voices[vIdx].TargetAmp = 0.0
			if !smoothTransitions {
				vt.Voices[vIdx].Amp = 0.0
				vt.Voices[vIdx].Active = false
			}
		}
	}

	// If there are unassigned peaks and inactive voices, assign them
	for pIdx := 0; pIdx < numPeaks; pIdx++ {
		if !assignedPeaks[pIdx] {
			// Find an inactive voice
			for vIdx := 0; vIdx < numVoices; vIdx++ {
				if !vt.Voices[vIdx].Active || vt.Voices[vIdx].TargetAmp == 0.0 {
					p := peaks[pIdx]
					amp := peakAmps[pIdx]
					vt.Voices[vIdx].Freq = p.Freq
					vt.Voices[vIdx].TargetFreq = p.Freq
					vt.Voices[vIdx].Amp = 0.0
					vt.Voices[vIdx].TargetAmp = amp
					vt.Voices[vIdx].Active = true
					break
				}
			}
		}
	}
}

// findBestMatching finds assignment of voices to peaks that minimizes total cost.
// Returns slice of size numVoices where val is peakIndex (or -1 if unassigned).
func findBestMatching(costMatrix [][]float64, numVoices, numPeaks int) []int {
	bestAssignment := make([]int, numVoices)
	for i := range bestAssignment {
		bestAssignment[i] = -1
	}

	if numPeaks == 0 {
		return bestAssignment
	}

	minCost := math.MaxFloat64
	current := make([]int, numVoices)
	for i := range current {
		current[i] = -1
	}
	usedPeaks := make([]bool, numPeaks)

	var search func(vIdx int, currentCost float64)
	search = func(vIdx int, currentCost float64) {
		if vIdx == numVoices {
			if currentCost < minCost {
				minCost = currentCost
				copy(bestAssignment, current)
			}
			return
		}

		// Option 1: Unassigned voice (penalty cost 1.5)
		current[vIdx] = -1
		search(vIdx+1, currentCost+1.5)

		// Option 2: Try assigning available peaks
		for pIdx := 0; pIdx < numPeaks; pIdx++ {
			if !usedPeaks[pIdx] {
				cost := costMatrix[vIdx][pIdx]
				if cost < 5.0 { // Only consider plausible match
					usedPeaks[pIdx] = true
					current[vIdx] = pIdx
					search(vIdx+1, currentCost+cost)
					usedPeaks[pIdx] = false
					current[vIdx] = -1
				}
			}
		}
	}

	search(0, 0.0)
	return bestAssignment
}
