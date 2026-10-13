package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/rand"
	"testing"
)

func TestLongToneClassifierExactThreshold(t *testing.T) {
	for _, test := range []struct {
		name      string
		duration  int64
		completed bool
	}{
		{name: "29.98 seconds fails", duration: 29980, completed: false},
		{name: "30 seconds succeeds", duration: 30000, completed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			classifier := newLongToneClassifier()
			var frame longToneFrame
			for at := int64(20); at <= test.duration; at += 20 {
				observation := voicedObservation(at, 0, 440)
				observation.final = at == test.duration
				frame = classifier.advance(observation)
			}
			if got := frame.State == "completed"; got != test.completed {
				t.Fatalf("completed=%v, want %v (last frame: %+v)", got, test.completed, frame)
			}
			if frame.HoldMS != test.duration {
				t.Fatalf("hold duration=%d, want %d", frame.HoldMS, test.duration)
			}
		})
	}
}

func TestLongToneClassifierBreathDoesNotAccumulate(t *testing.T) {
	classifier := newLongToneClassifier()
	for at := int64(20); at <= 15000; at += 20 {
		classifier.advance(voicedObservation(at, at-20, 440))
	}
	for at := int64(15020); at <= 15100; at += 20 {
		classifier.advance(longToneObservation{timeMS: at, startMS: at - 20, rmsDB: -120})
	}
	var frame longToneFrame
	for at := int64(15120); at <= 30100; at += 20 {
		frame = classifier.advance(voicedObservation(at, at-20, 440))
	}
	if frame.State == "completed" {
		t.Fatalf("two spans around a breath completed: %+v", frame)
	}
	if frame.HoldMS > 15000 {
		t.Fatalf("post-breath hold=%dms, want no more than 15s", frame.HoldMS)
	}
}

func TestLongToneClassifierLegatoChangeBackdatesNewHold(t *testing.T) {
	classifier := newLongToneClassifier()
	for at := int64(20); at <= 5000; at += 20 {
		classifier.advance(voicedObservation(at, at-20, 440))
	}
	var frame longToneFrame
	for at := int64(5020); at <= 35200; at += 20 {
		frame = classifier.advance(voicedObservation(at, at-20, 466.1637615))
	}
	if frame.State != "completed" || frame.NoteMIDI == nil || *frame.NoteMIDI != 70 {
		t.Fatalf("new B-flat hold did not complete: %+v", frame)
	}
	if frame.HoldStartMS == nil || *frame.HoldStartMS != 5000 {
		t.Fatalf("hold start=%v, want change boundary 5000ms", frame.HoldStartMS)
	}
}

func TestLongToneClassifierVibratoAndOctaveHandling(t *testing.T) {
	t.Run("vibrato retains fixed note", func(t *testing.T) {
		classifier := newLongToneClassifier()
		var frame longToneFrame
		for at := int64(20); at <= 30000; at += 20 {
			cents := 45 * math.Sin(2*math.Pi*5*float64(at)/1000)
			frequency := 440 * math.Pow(2, cents/1200)
			observation := voicedObservation(at, at-20, frequency)
			observation.final = at == 30000
			frame = classifier.advance(observation)
		}
		if frame.State != "completed" || frame.NoteMIDI == nil || *frame.NoteMIDI != 69 {
			t.Fatalf("vibrato reset the hold: %+v", frame)
		}
	})

	t.Run("brief octave estimate is uncertain then conservatively restarts", func(t *testing.T) {
		classifier := newLongToneClassifier()
		for at := int64(20); at <= 10000; at += 20 {
			classifier.advance(voicedObservation(at, at-20, 440))
		}
		for at := int64(10020); at <= 10080; at += 20 {
			frame := classifier.advance(voicedObservation(at, at-20, 880))
			if frame.State != "uncertain" {
				t.Fatalf("octave transient state=%q, want uncertain", frame.State)
			}
		}
		frame := classifier.advance(voicedObservation(10100, 10080, 440))
		if frame.HoldStartMS == nil || *frame.HoldStartMS != 10080 || frame.NoteMIDI == nil || *frame.NoteMIDI != 69 || frame.HoldMS != 20 {
			t.Fatalf("brief octave estimate bridged an unverified interval: %+v", frame)
		}
	})

	t.Run("sustained octave starts a new hold", func(t *testing.T) {
		classifier := newLongToneClassifier()
		for at := int64(20); at <= 10000; at += 20 {
			classifier.advance(voicedObservation(at, at-20, 440))
		}
		var frame longToneFrame
		for at := int64(10020); at <= 10200; at += 20 {
			frame = classifier.advance(voicedObservation(at, at-20, 880))
		}
		if frame.NoteMIDI == nil || *frame.NoteMIDI != 81 || frame.HoldStartMS == nil || *frame.HoldStartMS != 10000 {
			t.Fatalf("sustained octave did not backdate reset: %+v", frame)
		}
	})
}

func TestLongToneClassifierUnknownGapCannotComplete(t *testing.T) {
	classifier := newLongToneClassifier()
	for at := int64(20); at <= 15000; at += 20 {
		classifier.advance(voicedObservation(at, at-20, 440))
	}
	for at := int64(15020); at <= 15160; at += 20 {
		classifier.advance(longToneObservation{
			timeMS: at, startMS: at - 20, rmsDB: -20, audible: true,
		})
	}
	var frame longToneFrame
	for at := int64(15180); at <= 30160; at += 20 {
		frame = classifier.advance(voicedObservation(at, at-20, 440))
	}
	if frame.State == "completed" || frame.HoldMS > 15000 {
		t.Fatalf("unknown gap accumulated two spans: %+v", frame)
	}
}

func TestLongToneClassifierSilenceCannotCompleteAcrossPendingAmbiguity(t *testing.T) {
	classifier := newLongToneClassifier()
	for at := int64(20); at <= 29980; at += 20 {
		classifier.advance(voicedObservation(at, at-20, 440))
	}
	classifier.advance(longToneObservation{timeMS: 30000, startMS: 29980, rmsDB: -20, audible: true})
	frame := classifier.advance(longToneObservation{timeMS: 30020, startMS: 30000, rmsDB: -120})
	if frame.State == "completed" {
		t.Fatalf("silence boundary completed across unresolved ambiguity: %+v", frame)
	}
}

func TestLongToneClassifierRepeatedShortUnknownGapsNeverAccumulate(t *testing.T) {
	classifier := newLongToneClassifier()
	var at int64
	var frame longToneFrame
	for cycle := 0; cycle < 4; cycle++ {
		for end := at + 8000; at < end; {
			at += 20
			frame = classifier.advance(voicedObservation(at, at-20, 440))
		}
		// Each gap is below the 100ms pending-ambiguity timeout. Recovery must
		// still establish a fresh continuity interval.
		for i := 0; i < 3; i++ {
			at += 20
			frame = classifier.advance(longToneObservation{
				timeMS: at, startMS: at - 20, rmsDB: -20, audible: true,
			})
		}
	}
	at += 20
	frame = classifier.advance(voicedObservation(at, at-20, 440))
	if frame.State == "completed" || frame.HoldMS != 20 {
		t.Fatalf("short unknown gaps accumulated progress: %+v", frame)
	}
}

func TestAnalyzeLongTonePCMGeneratedToneCompletesAtSourceThreshold(t *testing.T) {
	for _, test := range []struct {
		name       string
		durationMS int
		completed  bool
	}{{"29980ms", 29980, false}, {"30000ms", 30000, true}, {"aligned 30004ms EOF", 30004, true}} {
		t.Run(test.name, func(t *testing.T) {
			pcm := sinePCM(440, test.durationMS, 0.18)
			frames, batches, duration, err := collectLongTone(context.Background(), bytes.NewReader(pcm))
			if err != nil {
				t.Fatal(err)
			}
			if duration != int64(test.durationMS) {
				t.Fatalf("duration=%d, want %d", duration, test.durationMS)
			}
			if batches == 0 || len(frames) == 0 {
				t.Fatal("analyzer emitted no frames")
			}
			last := frames[len(frames)-1]
			if last.TimeMS != int64(test.durationMS) || (last.State == "completed") != test.completed || last.HoldMS != int64(test.durationMS) {
				t.Fatalf("final frame: %+v", last)
			}
			if math.Abs(last.FrequencyHz-440) > 0.5 || last.Clarity < 0.99 {
				t.Fatalf("pitch result: hz=%f clarity=%f", last.FrequencyHz, last.Clarity)
			}
		})
	}
}

func TestAnalyzeLongTonePCMEnvelopeDoesNotBackdateAcrossSilence(t *testing.T) {
	for _, test := range []struct {
		name      string
		toneMS    int
		completed bool
	}{
		{name: "29980ms tone", toneMS: 29980, completed: false},
		{name: "30000ms tone", toneMS: 30000, completed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pcm := make([]byte, 2000*longToneSampleRate/1000*4)
			pcm = append(pcm, sinePCM(440, test.toneMS, 0.18)...)
			pcm = append(pcm, make([]byte, 200*longToneSampleRate/1000*4)...)
			frames, _, _, err := collectLongTone(context.Background(), bytes.NewReader(pcm))
			if err != nil {
				t.Fatal(err)
			}
			completed := false
			for _, frame := range frames {
				completed = completed || frame.State == "completed"
				if frame.HoldStartMS != nil && *frame.HoldStartMS < 2000 {
					t.Fatalf("hold backdated into leading silence: %+v", frame)
				}
			}
			if completed != test.completed {
				t.Fatalf("completed=%v, want %v", completed, test.completed)
			}
		})
	}
}

func TestAnalyzeLongTonePCMTinyBreathBreaksContinuity(t *testing.T) {
	for _, breathMS := range []int{10, 20, 40} {
		for _, firstToneMS := range []int{14990, 15000, 15010} {
			pcm := sinePCM(440, firstToneMS, 0.18)
			pcm = append(pcm, make([]byte, breathMS*longToneSampleRate/1000*4)...)
			pcm = append(pcm, sinePCM(440, 30000-firstToneMS, 0.18)...)
			frames, _, _, err := collectLongTone(context.Background(), bytes.NewReader(pcm))
			if err != nil {
				t.Fatal(err)
			}
			for _, frame := range frames {
				if frame.State == "completed" {
					t.Fatalf("%dms breath after %dms falsely completed at %dms", breathMS, firstToneMS, frame.TimeMS)
				}
			}
		}
	}
}

func TestAnalyzeLongTonePCMNearGoalTransitionCannotCompleteOldNote(t *testing.T) {
	for _, test := range []struct {
		name string
		tail []byte
	}{
		{name: "legato semitone", tail: sinePCM(466.1637615, 1000, 0.18)},
		{name: "legato octave", tail: sinePCM(880, 1000, 0.18)},
		{name: "silence", tail: make([]byte, longToneSampleRate/1000*1000*4)},
	} {
		t.Run(test.name, func(t *testing.T) {
			pcm := append(sinePCM(440, 29980, 0.18), test.tail...)
			frames, _, _, err := collectLongTone(context.Background(), bytes.NewReader(pcm))
			if err != nil {
				t.Fatal(err)
			}
			for _, frame := range frames {
				if frame.State == "completed" {
					t.Fatalf("old A4 falsely completed after transition: %+v", frame)
				}
			}
		})
	}
}

func TestAnalyzeLongTonePCMReaderChunkBoundaries(t *testing.T) {
	pcm := sinePCM(261.625565, 900, 0.12)
	want, _, _, err := collectLongTone(context.Background(), bytes.NewReader(pcm))
	if err != nil {
		t.Fatal(err)
	}
	got, _, duration, err := collectLongTone(context.Background(), &oddChunkReader{data: pcm, sizes: []int{1, 3, 7, 2, 31}})
	if err != nil {
		t.Fatal(err)
	}
	if duration != 900 || len(got) != len(want) {
		t.Fatalf("duration/frames=(%d,%d), want (900,%d)", duration, len(got), len(want))
	}
	for i := range want {
		if got[i].TimeMS != want[i].TimeMS || math.Abs(got[i].FrequencyHz-want[i].FrequencyHz) > 1e-9 || got[i].State != want[i].State {
			t.Fatalf("frame %d differs across reader boundaries: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAnalyzeLongTonePCMSilenceNoiseCancellationAndEmissionBounds(t *testing.T) {
	t.Run("silence and noise never lock", func(t *testing.T) {
		pcm := make([]byte, longToneSampleRate*4)
		random := rand.New(rand.NewSource(7))
		for i := 0; i < longToneSampleRate; i++ {
			sample := float32((random.Float64()*2 - 1) * 0.05)
			binary.LittleEndian.PutUint32(pcm[i*4:], math.Float32bits(sample))
		}
		frames, _, _, err := collectLongTone(context.Background(), bytes.NewReader(pcm))
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range frames {
			if frame.State == "holding" || frame.State == "completed" || frame.NoteMIDI != nil {
				t.Fatalf("noise produced a hold: %+v", frame)
			}
		}
	})

	t.Run("callback batches cover at most one second", func(t *testing.T) {
		pcm := sinePCM(330, 2200, 0.12)
		calls := 0
		_, err := analyzeLongTonePCM(context.Background(), bytes.NewReader(pcm), func(frames []longToneFrame) error {
			calls++
			if len(frames) > longToneBatchFrames {
				t.Fatalf("batch has %d frames, max %d", len(frames), longToneBatchFrames)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls < 2 {
			t.Fatalf("got %d callback(s), want multiple bounded batches", calls)
		}
	})

	t.Run("cancellation is returned", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := analyzeLongTonePCM(ctx, bytes.NewReader(sinePCM(440, 100, 0.1)), func([]longToneFrame) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v, want context.Canceled", err)
		}
	})

	t.Run("truncated float is rejected", func(t *testing.T) {
		_, err := analyzeLongTonePCM(context.Background(), bytes.NewReader([]byte{0, 0, 0}), func([]longToneFrame) error { return nil })
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("error=%v, want io.ErrUnexpectedEOF", err)
		}
	})
}

func TestAnalyzeLongTonePCMAudibleNoiseDoesNotBackdateLaterPitch(t *testing.T) {
	random := rand.New(rand.NewSource(11))
	pcm := make([]byte, longToneSampleRate*4)
	for i := 0; i < longToneSampleRate; i++ {
		sample := float32((random.Float64()*2 - 1) * 0.05)
		binary.LittleEndian.PutUint32(pcm[i*4:], math.Float32bits(sample))
	}
	pcm = append(pcm, sinePCM(440, 500, 0.18)...)
	frames, _, _, err := collectLongTone(context.Background(), bytes.NewReader(pcm))
	if err != nil {
		t.Fatal(err)
	}
	last := frames[len(frames)-1]
	if last.HoldStartMS == nil || *last.HoldStartMS < 980 {
		t.Fatalf("later pitch was backdated into audible unpitched noise: %+v", last)
	}
}

func voicedObservation(timeMS, startMS int64, frequency float64) longToneObservation {
	return longToneObservation{
		timeMS: timeMS, startMS: startMS, frequency: frequency,
		clarity: 0.99, rmsDB: -15, voiced: true, audible: true,
	}
}

func sinePCM(frequency float64, durationMS int, amplitude float64) []byte {
	sampleCount := durationMS * longToneSampleRate / 1000
	pcm := make([]byte, sampleCount*4)
	for i := 0; i < sampleCount; i++ {
		sample := float32(amplitude * math.Sin(2*math.Pi*frequency*float64(i)/longToneSampleRate))
		binary.LittleEndian.PutUint32(pcm[i*4:], math.Float32bits(sample))
	}
	return pcm
}

func collectLongTone(ctx context.Context, reader io.Reader) ([]longToneFrame, int, int64, error) {
	frames := make([]longToneFrame, 0)
	batches := 0
	duration, err := analyzeLongTonePCM(ctx, reader, func(batch []longToneFrame) error {
		batches++
		frames = append(frames, batch...)
		return nil
	})
	return frames, batches, duration, err
}

type oddChunkReader struct {
	data  []byte
	sizes []int
	at    int
	step  int
}

func (r *oddChunkReader) Read(p []byte) (int, error) {
	if r.at == len(r.data) {
		return 0, io.EOF
	}
	size := r.sizes[r.step%len(r.sizes)]
	r.step++
	if size > len(p) {
		size = len(p)
	}
	if size > len(r.data)-r.at {
		size = len(r.data) - r.at
	}
	copy(p, r.data[r.at:r.at+size])
	r.at += size
	return size, nil
}
