package main

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

const (
	longToneAnalysisVersion = "long-tone-yin-v1"
	longToneSampleRate      = 16000

	longToneWindowSamples = 1024 // 64 ms at 16 kHz.
	longToneHopSamples    = 320  // 20 ms at 16 kHz.
	longToneBatchFrames   = 50   // At most one second per callback.
)

type longToneFrame struct {
	TimeMS      int64   `json:"tMs"`
	FrequencyHz float64 `json:"hz"`
	Clarity     float64 `json:"clarity"`
	RMSDB       float64 `json:"db"`
	State       string  `json:"state"`
	NoteMIDI    *int    `json:"noteMidi,omitempty"`
	HoldStartMS *int64  `json:"holdStartMs,omitempty"`
	HoldMS      int64   `json:"holdMs"`
}

type longToneObservation struct {
	timeMS        int64
	startMS       int64
	frequency     float64
	clarity       float64
	rmsDB         float64
	voiced        bool
	audible       bool
	energyRestart bool
	final         bool
}

type longToneClassifier struct {
	state string

	lockedMIDI int
	hasLock    bool
	holdStart  int64
	holdMS     int64

	acquireStart        int64
	pendingStart        int64
	pendingKind         string
	pendingMIDI         int
	completed           bool
	completionPendingMS int64
}

func newLongToneClassifier() *longToneClassifier {
	return &longToneClassifier{state: "listening", completionPendingMS: -1}
}

func (c *longToneClassifier) advance(o longToneObservation) longToneFrame {
	frame := longToneFrame{
		TimeMS:      o.timeMS,
		FrequencyHz: o.frequency,
		Clarity:     o.clarity,
		RMSDB:       o.rmsDB,
		State:       c.state,
	}
	if o.energyRestart && c.hasLock {
		c.reset()
	}

	if !o.audible {
		c.advanceBreak(o, "silence", 40)
	} else if !o.voiced {
		c.advanceBreak(o, "ambiguity", 100)
	} else {
		midiFloat := 69 + 12*math.Log2(o.frequency/440)
		midi := int(math.Round(midiFloat))
		if !c.hasLock {
			c.beginAcquisition(o.startMS, midi)
		} else {
			centsFromLock := math.Abs((midiFloat - float64(c.lockedMIDI)) * 100)
			if centsFromLock <= 70 {
				c.continueLocked(o)
			} else {
				c.advanceChange(o, midi)
			}
		}
	}

	frame.State = c.state
	if c.hasLock {
		midi := c.lockedMIDI
		start := c.holdStart
		frame.NoteMIDI = &midi
		frame.HoldStartMS = &start
		frame.HoldMS = c.holdMS
	}
	return frame
}

func (c *longToneClassifier) beginAcquisition(startMS int64, midi int) {
	c.state = "acquiring"
	c.hasLock = true
	c.lockedMIDI = midi
	c.acquireStart = startMS
	c.holdStart = startMS
	c.holdMS = 0
	c.pendingKind = ""
	c.completed = false
	c.completionPendingMS = -1
}

func (c *longToneClassifier) continueLocked(o longToneObservation) {
	if c.pendingKind != "" {
		// Missing pitch, low energy, and conflicting pitch do not prove
		// continuity. Conservatively start again on recovery, even below a
		// debounce timeout. A later calibrated detector could preserve true
		// isolated octave errors by proving the locked harmonic in the raw
		// window; this version intentionally does not infer that evidence.
		midi := c.lockedMIDI
		c.beginAcquisition(o.startMS, midi)
		c.holdMS = o.timeMS - o.startMS
		return
	}
	verified := o.timeMS - c.holdStart
	if verified < 0 {
		verified = 0
	}
	c.holdMS = verified
	if !c.completed && c.holdMS >= 30000 {
		if o.final {
			c.completed = true
		} else if c.completionPendingMS < 0 {
			c.completionPendingMS = o.timeMS
		} else if o.timeMS-c.completionPendingMS >= 120 {
			c.completed = true
		}
	}
	if c.completed {
		c.state = "completed"
	} else if o.timeMS-c.acquireStart >= 200 {
		c.state = "holding"
	} else {
		c.state = "acquiring"
	}
}

func (c *longToneClassifier) advanceBreak(o longToneObservation, kind string, confirmMS int64) {
	if !c.hasLock {
		c.state = "listening"
		return
	}
	previousKind := c.pendingKind
	if previousKind != kind {
		c.pendingKind = kind
		c.pendingStart = o.startMS
		if kind == "silence" && previousKind == "" && c.pendingStart-c.holdStart >= 30000 {
			// The measured energy boundary proves the note reached the goal,
			// even though the overlapping pitch window reports it one hop later.
			c.holdMS = c.pendingStart - c.holdStart
			c.completed = true
			c.state = "completed"
			return
		}
	}
	c.state = "uncertain"
	if o.timeMS-c.pendingStart >= confirmMS {
		c.reset()
	}
}

func (c *longToneClassifier) advanceChange(o longToneObservation, midi int) {
	if c.pendingKind != "change" || c.pendingMIDI != midi {
		c.pendingKind = "change"
		c.pendingMIDI = midi
		c.pendingStart = o.startMS
	}
	c.state = "uncertain"
	if o.timeMS-c.pendingStart >= 120 {
		start := c.pendingStart
		c.beginAcquisition(start, midi)
		// The confirmation frame is verified evidence for the new note.
		c.holdMS = o.timeMS - start
	}
}

func (c *longToneClassifier) reset() {
	c.state = "listening"
	c.hasLock = false
	c.holdMS = 0
	c.pendingKind = ""
	c.completed = false
	c.completionPendingMS = -1
}

// analyzeLongTonePCM analyzes a little-endian, mono, normalized float32 stream.
// It retains one analysis window and one callback batch regardless of input size.
func analyzeLongTonePCM(ctx context.Context, input io.Reader, emit func([]longToneFrame) error) (int64, error) {
	if input == nil {
		return 0, errors.New("long-tone analysis: nil input")
	}
	if emit == nil {
		return 0, errors.New("long-tone analysis: nil emitter")
	}

	classifier := newLongToneClassifier()
	ring := make([]float32, longToneWindowSamples)
	window := make([]float32, longToneWindowSamples)
	difference := make([]float64, int(math.Ceil(longToneSampleRate/65.0))+1)
	batch := make([]longToneFrame, 0, longToneBatchFrames)
	readBuffer := make([]byte, 32*1024)
	var partial [4]byte
	partialN := 0
	ringAt := 0
	totalSamples := int64(0)
	lastAnalyzedSample := int64(-1)
	nextAnalysisSample := int64(longToneWindowSamples)
	noiseRMS := 0.001
	envelopeEnergy := 0.0
	envelopeSamples := 0
	envelopeAudible := false
	energyStartMS := int64(0)
	energyBreakMS := int64(0)
	energyGeneration := int64(0)
	analyzedEnergyGeneration := int64(0)
	var lastObservation longToneObservation

	emitBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := emit(batch); err != nil {
			return err
		}
		batch = make([]longToneFrame, 0, longToneBatchFrames)
		return nil
	}
	analyze := func(endSample int64, final bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		copy(window, ring[ringAt:])
		copy(window[longToneWindowSamples-ringAt:], ring[:ringAt])
		frequency, clarity, rmsDB, voiced, _ := detectLongTonePitch(window, difference)
		audible := envelopeAudible
		if !audible {
			frequency, voiced = 0, false
		}
		frameStartSample := endSample - longToneHopSamples
		if frameStartSample < 0 {
			frameStartSample = 0
		}
		startMS := samplesToMS(frameStartSample)
		windowStartSample := endSample - longToneWindowSamples
		if windowStartSample < 0 {
			windowStartSample = 0
		}
		boundedEnergyStartMS := energyStartMS
		if windowStartMS := samplesToMS(windowStartSample); boundedEnergyStartMS < windowStartMS {
			// Audible but unpitched sound before this YIN window is not onset
			// evidence for the detected note.
			boundedEnergyStartMS = windowStartMS
		}
		energyRestart := energyGeneration != analyzedEnergyGeneration && classifier.hasLock
		if !audible {
			startMS = energyBreakMS
		} else if !classifier.hasLock || energyRestart {
			startMS = boundedEnergyStartMS
		} else if voiced && energyStartMS > classifier.holdStart && (classifier.pendingKind == "silence" || classifier.pendingKind == "ambiguity") {
			midiFloat := 69 + 12*math.Log2(frequency/440)
			if math.Abs((midiFloat-float64(classifier.lockedMIDI))*100) <= 70 {
				startMS = boundedEnergyStartMS
			}
		}
		observation := longToneObservation{
			timeMS:        samplesToMS(endSample),
			startMS:       startMS,
			frequency:     frequency,
			clarity:       clarity,
			rmsDB:         rmsDB,
			voiced:        voiced,
			audible:       audible,
			energyRestart: energyRestart,
			final:         final,
		}
		frame := classifier.advance(observation)
		lastObservation = observation
		analyzedEnergyGeneration = energyGeneration
		batch = append(batch, frame)
		lastAnalyzedSample = endSample
		if len(batch) == longToneBatchFrames {
			// Retain the newest frame until another observation or EOF proves
			// whether a threshold crossing is stable/final.
			last := batch[len(batch)-1]
			if err := emit(batch[:len(batch)-1]); err != nil {
				return err
			}
			batch = make([]longToneFrame, 1, longToneBatchFrames)
			batch[0] = last
		}
		return nil
	}
	consume := func(sample float32) error {
		if math.IsNaN(float64(sample)) || math.IsInf(float64(sample), 0) {
			sample = 0
		}
		ring[ringAt] = sample
		ringAt = (ringAt + 1) % len(ring)
		totalSamples++
		envelopeEnergy += float64(sample) * float64(sample)
		envelopeSamples++
		if envelopeSamples == longToneSampleRate/100 { // 10 ms envelope interval.
			rms := math.Sqrt(envelopeEnergy / float64(envelopeSamples))
			releaseRMS := math.Max(0.008, noiseRMS*2)
			onsetRMS := math.Max(0.012, noiseRMS*3)
			blockStartMS := samplesToMS(totalSamples - int64(envelopeSamples))
			if envelopeAudible {
				if rms < releaseRMS {
					envelopeAudible = false
					energyBreakMS = blockStartMS
				}
			} else if rms >= onsetRMS {
				envelopeAudible = true
				energyStartMS = blockStartMS
				energyGeneration++
			} else {
				// Update the floor only from intervals already classified as
				// silence; a note cannot raise its own onset threshold.
				noiseRMS = 0.995*noiseRMS + 0.005*rms
			}
			envelopeEnergy = 0
			envelopeSamples = 0
		}
		if totalSamples == nextAnalysisSample {
			if err := analyze(totalSamples, false); err != nil {
				return err
			}
			nextAnalysisSample += longToneHopSamples
		}
		return nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return samplesToMS(totalSamples), err
		}
		n, readErr := input.Read(readBuffer)
		for i := 0; i < n; i++ {
			partial[partialN] = readBuffer[i]
			partialN++
			if partialN == 4 {
				bits := binary.LittleEndian.Uint32(partial[:])
				if err := consume(math.Float32frombits(bits)); err != nil {
					return samplesToMS(totalSamples), err
				}
				partialN = 0
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return samplesToMS(totalSamples), readErr
			}
			break
		}
		if n == 0 {
			return samplesToMS(totalSamples), io.ErrNoProgress
		}
	}
	if partialN != 0 {
		return samplesToMS(totalSamples), io.ErrUnexpectedEOF
	}
	if totalSamples >= longToneWindowSamples && lastAnalyzedSample != totalSamples {
		if err := analyze(totalSamples, true); err != nil {
			return samplesToMS(totalSamples), err
		}
	} else if lastAnalyzedSample == totalSamples && len(batch) > 0 {
		lastObservation.final = true
		lastObservation.energyRestart = false
		batch[len(batch)-1] = classifier.advance(lastObservation)
	}
	if err := emitBatch(); err != nil {
		return samplesToMS(totalSamples), err
	}
	return samplesToMS(totalSamples), nil
}

func samplesToMS(samples int64) int64 {
	return samples * 1000 / longToneSampleRate
}

func detectLongTonePitch(samples []float32, difference []float64) (frequency, clarity, rmsDB float64, voiced, audible bool) {
	energy := 0.0
	for _, sample := range samples {
		energy += float64(sample) * float64(sample)
	}
	rms := math.Sqrt(energy / float64(len(samples)))
	rmsDB = -120
	if rms > 0 {
		rmsDB = 20 * math.Log10(rms)
	}
	audible = rms >= 0.008
	if rms < 0.012 {
		return 0, 0, rmsDB, false, audible
	}

	minLag := longToneSampleRate / 1400
	if minLag < 2 {
		minLag = 2
	}
	maxLag := int(math.Ceil(longToneSampleRate / 65.0))
	if maxLag > len(samples)-2 {
		maxLag = len(samples) - 2
	}
	compareLength := len(samples) - maxLag
	for lag := 1; lag <= maxLag; lag++ {
		sum := 0.0
		for i := 0; i < compareLength; i++ {
			delta := float64(samples[i] - samples[i+lag])
			sum += delta * delta
		}
		difference[lag] = sum
	}
	cumulative := 0.0
	bestLag := -1
	for lag := 1; lag <= maxLag; lag++ {
		cumulative += difference[lag]
		normalized := 1.0
		if cumulative != 0 {
			normalized = difference[lag] * float64(lag) / cumulative
		}
		difference[lag] = normalized
		if lag >= minLag && bestLag < 0 && normalized < 0.10 {
			bestLag = lag
		}
	}
	if bestLag < 0 {
		return 0, 0, rmsDB, false, audible
	}
	for bestLag+1 <= maxLag && difference[bestLag+1] < difference[bestLag] {
		bestLag++
	}
	left, center, right := difference[bestLag-1], difference[bestLag], difference[bestLag]
	if bestLag < maxLag {
		right = difference[bestLag+1]
	}
	refinedLag := float64(bestLag)
	denominator := 2 * (2*center - right - left)
	if denominator != 0 {
		refinedLag += (right - left) / denominator
	}
	frequency = longToneSampleRate / refinedLag
	clarity = math.Max(0, math.Min(1, 1-center))
	voiced = frequency >= 65 && frequency <= 1400 && clarity >= 0.90
	if !voiced {
		frequency = 0
	}
	return frequency, clarity, rmsDB, voiced, audible
}
