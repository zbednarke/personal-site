package main

import "testing"

func TestScanWaveformForClipsFindsAndRanksActivity(t *testing.T) {
	peaks := make([]float64, 120)
	for index := 20; index < 65; index++ {
		peaks[index] = .5
	}
	for index := 85; index < 96; index++ {
		peaks[index] = .3
	}
	clips := scanWaveformForClips(peaks, 120000)
	if len(clips) < 2 {
		t.Fatalf("expected at least two clips, got %d", len(clips))
	}
	if clips[0].StartMS > 20000 || clips[0].EndMS < 60000 {
		t.Fatalf("expected primary activity near 20s-65s, got %d-%d", clips[0].StartMS, clips[0].EndMS)
	}
	if clips[0].Score < clips[1].Score {
		t.Fatal("clips are not score sorted")
	}
}

func TestScanWaveformForClipsRejectsShortOrEmptyAudio(t *testing.T) {
	if got := scanWaveformForClips([]float64{.5, .5}, 7000); len(got) != 0 {
		t.Fatalf("expected no short clips, got %d", len(got))
	}
	if got := scanWaveformForClips(make([]float64, 100), 60000); len(got) != 0 {
		t.Fatalf("expected no silent clips, got %d", len(got))
	}
}
