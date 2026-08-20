package main

import (
	"math"
	"sort"
)

type clipSuggestion struct {
	StartMS    int
	EndMS      int
	Score      float64
	Coverage   float64
	Continuity float64
	Reasons    []string
}

func percentile(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	index := int(math.Round(float64(len(copyValues)-1) * fraction))
	return copyValues[index]
}

func scanWaveformForClips(peaks []float64, durationMS int) []clipSuggestion {
	if len(peaks) < 2 || durationMS < 8000 {
		return nil
	}
	low := percentile(peaks, .20)
	high := percentile(peaks, .85)
	threshold := math.Max(.018, low+(high-low)*.28)
	stepMS := float64(durationMS) / float64(len(peaks))
	bridgeBins := int(math.Ceil(1800 / stepMS))
	if bridgeBins < 1 {
		bridgeBins = 1
	}
	active := make([]bool, len(peaks))
	for index, peak := range peaks {
		active[index] = peak >= threshold
	}
	for index := 0; index < len(active); {
		if active[index] {
			index++
			continue
		}
		start := index
		for index < len(active) && !active[index] {
			index++
		}
		if start > 0 && index < len(active) && index-start <= bridgeBins {
			for fill := start; fill < index; fill++ {
				active[fill] = true
			}
		}
	}

	var suggestions []clipSuggestion
	for index := 0; index < len(active); {
		if !active[index] {
			index++
			continue
		}
		startBin := index
		for index < len(active) && active[index] {
			index++
		}
		endBin := index
		startMS := max(0, int(float64(startBin)*stepMS)-1500)
		endMS := min(durationMS, int(float64(endBin)*stepMS)+1500)
		if endMS-startMS < 8000 {
			continue
		}
		for windowStart := startMS; windowStart < endMS; {
			windowEnd := min(endMS, windowStart+60000)
			if windowEnd-windowStart < 8000 {
				break
			}
			first := max(0, int(float64(windowStart)/stepMS))
			last := min(len(peaks), int(math.Ceil(float64(windowEnd)/stepMS)))
			activeCount := 0
			longest, run := 0, 0
			for cursor := first; cursor < last; cursor++ {
				if peaks[cursor] >= threshold {
					activeCount++
					run++
					longest = max(longest, run)
				} else {
					run = 0
				}
			}
			bins := max(1, last-first)
			coverage := float64(activeCount) / float64(bins)
			continuity := float64(longest) / float64(bins)
			score := math.Min(1, .18+.52*coverage+.30*continuity)
			reasons := []string{"sustained musical activity"}
			if continuity >= .65 {
				reasons = append(reasons, "continuous phrase")
			}
			if windowEnd-windowStart >= 20000 {
				reasons = append(reasons, "useful clip length")
			}
			suggestions = append(suggestions, clipSuggestion{windowStart, windowEnd, score, coverage, continuity, reasons})
			if windowEnd == endMS {
				break
			}
			windowStart = windowEnd - 3000
		}
	}
	sort.Slice(suggestions, func(i, j int) bool { return suggestions[i].Score > suggestions[j].Score })
	if len(suggestions) > 8 {
		suggestions = suggestions[:8]
	}
	return suggestions
}
