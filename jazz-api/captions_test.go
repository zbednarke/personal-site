package main

import (
	"encoding/json"
	"testing"
)

func TestParseSpeechDuration(t *testing.T) {
	for input, want := range map[string]int64{"": 0, "0s": 0, "1.250s": 1250, "62.003000000s": 62003} {
		if got := parseSpeechDuration(input); got != want {
			t.Fatalf("%q: got %d want %d", input, got, want)
		}
	}
}

func TestSpeechOperationNameAcceptsNumericProjectAndRejectsOtherPaths(t *testing.T) {
	for _, name := range []string{"projects/123456789012/locations/global/operations/v2-abc_123", "projects/parabolio-prod/locations/global/operations/op-1"} {
		if !speechOperationName.MatchString(name) {
			t.Fatalf("rejected %q", name)
		}
	}
	for _, name := range []string{"https://evil.test/op", "projects/x/locations/us/operations/one", "projects/x/locations/global/operations/a/b"} {
		if speechOperationName.MatchString(name) {
			t.Fatalf("accepted %q", name)
		}
	}
}

func TestCaptionSegmentsReadInlineResultsAndPreferWordTimes(t *testing.T) {
	raw := []byte(`{"done":true,"response":{"results":{"gs://bucket/take.wav":{"inlineResult":{"transcript":{"results":[{"alternatives":[{"transcript":" Warm-up begins. ","confidence":0.91,"words":[{"startOffset":"2.100s","endOffset":"2.400s"},{"startOffset":"2.450s","endOffset":"3.200s"}]}],"resultEndOffset":"3.300s"},{"alternatives":[{"transcript":"Sounds great."}],"resultEndOffset":"7.500s"}]}}}}}}`)
	var operation captionOperation
	if err := json.Unmarshal(raw, &operation); err != nil {
		t.Fatal(err)
	}
	segments := captionSegments(operation)
	if len(segments) != 2 {
		t.Fatalf("got %d segments", len(segments))
	}
	if segments[0].StartMS != 2100 || segments[0].EndMS != 3200 || segments[0].Text != "Warm-up begins." {
		t.Fatalf("unexpected first segment: %+v", segments[0])
	}
	if segments[1].StartMS != 3200 || segments[1].EndMS != 7500 {
		t.Fatalf("unexpected fallback timing: %+v", segments[1])
	}
}

func TestSpeechAudioObjectNameTracksSourceGeneration(t *testing.T) {
	got := speechAudioObjectName("users/me/take/audio-master.wav", 42)
	if got != "users/me/take/audio-master.caption-42.flac" {
		t.Fatalf("speechAudioObjectName() = %q", got)
	}
}
