package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	emberDeviceMaxInput    = 12 * 16000 * 2
	emberDeviceMaxOutput   = 24 * 24000 * 2
	emberDeviceContentType = "application/vnd.ember.turn.v1"
)

type emberDeviceHandler struct {
	app       *application
	tokenHash []byte
	mu        sync.Mutex
	busy      bool
	lastTurn  time.Time
	recent    map[string]time.Time
	history   []emberMessage
}

func (app *application) newEmberDeviceHandler() http.Handler {
	digest, _ := hex.DecodeString(strings.TrimSpace(os.Getenv("EMBER_DEVICE_TOKEN_SHA256")))
	return &emberDeviceHandler{app: app, tokenHash: digest, recent: make(map[string]time.Time)}
}

func (d *emberDeviceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := d.app.cfg
	if len(d.tokenHash) != sha256.Size {
		http.NotFound(w, r)
		return
	}
	token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	digest := sha256.Sum256([]byte(token))
	if !bearer || len(token) != 64 || subtle.ConstantTimeCompare(digest[:], d.tokenHash) != 1 ||
		(!cfg.AllowInsecureLocal && (cfg.GatewayKey == "" || r.Header.Get("X-Jazz-User") != "" ||
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Jazz-Gateway-Key")), []byte(cfg.GatewayKey)) != 1)) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		writeError(w, http.StatusUnsupportedMediaType, "16 kHz mono PCM16 required")
		return
	}
	id := strings.ToLower(r.Header.Get("X-Ember-Turn-ID"))
	if b, err := hex.DecodeString(id); err != nil || len(b) != 16 {
		writeError(w, http.StatusBadRequest, "invalid turn ID")
		return
	}

	now := time.Now()
	d.mu.Lock()
	for key, stamp := range d.recent {
		if now.Sub(stamp) > 2*time.Minute {
			delete(d.recent, key)
		}
	}
	if _, duplicate := d.recent[id]; duplicate || d.busy {
		d.mu.Unlock()
		writeError(w, http.StatusConflict, "turn already active or used")
		return
	}
	if now.Sub(d.lastTurn) < time.Second {
		d.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "wait before starting another turn")
		return
	}
	if now.Sub(d.lastTurn) > 15*time.Minute {
		d.history = nil
	}
	d.lastTurn, d.busy, d.recent[id] = now, true, now
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.busy = false
		d.mu.Unlock()
	}()

	defer r.Body.Close()
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(now.Add(19 * time.Second))
	pcm, err := io.ReadAll(http.MaxBytesReader(w, r.Body, emberDeviceMaxInput))
	_ = rc.SetReadDeadline(time.Time{})
	if err != nil || len(pcm) < 8000 || len(pcm)%2 != 0 {
		writeError(w, http.StatusBadRequest, "invalid or incomplete microphone audio")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	transcript, err := d.app.transcribeEmber(ctx, emberDeviceWAV(pcm), "utterance.wav")
	if err != nil {
		d.app.writeEmberError(w, err)
		return
	}
	if len(transcript) > 1000 {
		writeError(w, http.StatusUnprocessableEntity, "transcript too long")
		return
	}
	user := emberMessage{Role: "user", Content: transcript}
	messages := append(d.history, user)
	response, err := d.app.requestEmberResponse(ctx, messages, true)
	if err != nil {
		d.app.writeEmberError(w, err)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", emberDeviceContentType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(kind byte, payload []byte) error {
		frame := []byte{kind, byte(len(payload) >> 8), byte(len(payload))}
		if _, err := w.Write(frame); err != nil {
			return err
		}
		if _, err := w.Write(payload); err != nil {
			return err
		}
		return rc.Flush()
	}
	var gain emberDeviceGain
	var answer, pending string
	outputBytes := 0
	speak := func(sentence string) error {
		if len(sentence) > 1200 {
			return errors.New("sentence too long")
		}
		audio, err := d.app.requestEmberSpeech(ctx, sentence, "pcm_24000")
		if err != nil {
			return err
		}
		defer audio.Body.Close()
		block := make([]byte, 3840)
		total := 0
		for {
			n, err := io.ReadFull(audio.Body, block)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return err
			}
			if n%2 != 0 || outputBytes+n > emberDeviceMaxOutput {
				return errors.New("invalid reply audio")
			}
			if n > 0 {
				gain.normalize(block[:n])
				if err := send('A', block[:n]); err != nil {
					return err
				}
				total += n
				outputBytes += n
			}
			if err != nil {
				if total == 0 {
					return errors.New("empty reply audio")
				}
				return nil
			}
		}
	}
	err = readEmberTokens(response.Body, func(text string) error {
		answer += text
		pending += text
		if len(answer) > 2000 {
			return errors.New("reply too long")
		}
		for i := 0; i+1 < len(pending); i++ {
			if strings.ContainsRune(".!?", rune(pending[i])) {
				next, _ := utf8.DecodeRuneInString(pending[i+1:])
				if unicode.IsSpace(next) {
					if err := speak(strings.TrimSpace(pending[:i+1])); err != nil {
						return err
					}
					pending = strings.TrimSpace(pending[i+1:])
					i = -1
				}
			}
		}
		return nil
	})
	if err == nil && strings.TrimSpace(pending) != "" {
		err = speak(strings.TrimSpace(pending))
	}
	if err != nil || outputBytes == 0 {
		_ = send('E', []byte("reply failed"))
		return
	}
	if send('D', nil) == nil {
		d.history = append(messages, emberMessage{Role: "assistant", Content: strings.TrimSpace(answer)})
		if len(d.history) > 6 {
			d.history = d.history[len(d.history)-6:]
		}
	}
}

func emberDeviceWAV(pcm []byte) []byte {
	wav := make([]byte, 44+len(pcm))
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(pcm)))
	var sum float64
	for i := 0; i < len(pcm); i += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += math.Min(math.Abs(sample), 512) * math.Min(math.Abs(sample), 512)
	}
	gain := math.Min(120, math.Max(1, 1200/math.Max(1, math.Sqrt(sum/float64(len(pcm)/2)))))
	for i := 0; i < len(pcm); i += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		scaled := math.Max(-16000, math.Min(16000, math.Round(sample*gain)))
		binary.LittleEndian.PutUint16(wav[44+i:], uint16(int16(scaled)))
	}
	return wav
}

// Matches the web player's gradual boost and soft peak limit.
type emberDeviceGain struct{ gain float64 }

func (g *emberDeviceGain) normalize(pcm []byte) {
	if g.gain == 0 {
		g.gain = 1
	}
	frames := float64(len(pcm) / 2)
	var sum float64
	for i := 0; i < len(pcm); i += 2 {
		sample := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) / 32768
		sum += sample * sample
	}
	rms := math.Sqrt(sum / frames)
	target := 1.0
	if rms >= .012 {
		target = math.Max(1, math.Min(1.6, .11/rms))
	}
	rate := .18
	if target > g.gain {
		rate = .45
	}
	end := g.gain + (target-g.gain)*(1-math.Exp(-frames/24000/rate))
	step := (end - g.gain) / frames
	for i := 0; i < len(pcm); i += 2 {
		g.gain += step
		sample := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) * g.gain / 32768
		const knee, headroom = .84, .105
		if magnitude := math.Abs(sample); magnitude > knee {
			sample = math.Copysign(knee-headroom*math.Expm1(-(magnitude-knee)/headroom), sample)
		}
		binary.LittleEndian.PutUint16(pcm[i:], uint16(int16(math.Round(sample*32768))))
	}
	g.gain = end
}
