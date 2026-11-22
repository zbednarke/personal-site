package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestEmberDeviceTurn(t *testing.T) {
	const (
		token = "0123456789012345678901234567890123456789012345678901234567890123"
		id    = "aabbccddeeff00112233445566778899"
	)
	digest := sha256.Sum256([]byte(token))
	t.Setenv("EMBER_DEVICE_TOKEN_SHA256", hex.EncodeToString(digest[:]))
	t.Setenv("ELEVENLABS_API_KEY", "test-provider")
	t.Setenv("GEMINI_API_KEY", "test-provider")
	audio := bytes.Repeat([]byte{0, 1}, 4800)
	var spoken []string
	var spokenLock sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/speech-to-text"):
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer file.Close()
			wav, _ := io.ReadAll(file)
			if len(wav) != 44+len(audio) || string(wav[:4]) != "RIFF" || binary.LittleEndian.Uint32(wav[24:]) != 16000 {
				t.Errorf("invalid WAV with %d bytes", len(wav))
				w.WriteHeader(400)
				return
			}
			fmt.Fprint(w, `{"text":"Can you hear me?"}`)
		case strings.HasSuffix(r.URL.Path, ":streamGenerateContent"):
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Greetings. Channel open.\"}]},\"finishReason\":\"STOP\"}]}\n\n")
		case strings.Contains(r.URL.Path, "/text-to-speech/"):
			body, _ := io.ReadAll(r.Body)
			spokenLock.Lock()
			for _, sentence := range []string{"Greetings.", "Channel open."} {
				if bytes.Contains(body, []byte(sentence)) {
					spoken = append(spoken, sentence)
				}
			}
			spokenLock.Unlock()
			w.Write([]byte{0xff, 0x7f, 0x00, 0x80})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("EMBER_ELEVENLABS_API_BASE", upstream.URL)
	t.Setenv("EMBER_GEMINI_API_BASE", upstream.URL)
	app := &application{cfg: config{GatewayKey: "internal-gateway"}, httpClient: upstream.Client()}
	mux := http.NewServeMux()
	app.emberRoutes(mux)
	request := func(bearer, gateway, browserUser string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/686/device/turn", bytes.NewReader(audio))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Authorization", bearer)
		req.Header.Set("X-Ember-Turn-ID", id)
		req.Header.Set("X-Jazz-Gateway-Key", gateway)
		req.Header.Set("X-Jazz-User", browserUser)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, req)
		return recorder
	}
	for _, rec := range []*httptest.ResponseRecorder{
		request("", "internal-gateway", ""), request("Bearer "+token, "", ""),
		request("Bearer "+token, "internal-gateway", "ember-guest"),
	} {
		if rec.Code != 401 {
			t.Errorf("unauthenticated status %d", rec.Code)
		}
	}
	rec := request("Bearer "+token, "internal-gateway", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != emberDeviceContentType {
		t.Fatalf("turn status %d: %s", rec.Code, rec.Body.String())
	}
	for _, tag := range []byte{'A', 'A', 'D'} {
		header := rec.Body.Next(3)
		if len(header) != 3 || header[0] != tag {
			t.Fatalf("frame header %v; wanted %c", header, tag)
		}
		payload := rec.Body.Next(int(binary.BigEndian.Uint16(header[1:])))
		if tag == 'D' {
			if len(payload) != 0 || rec.Body.Len() != 0 {
				t.Fatal("invalid done frame")
			}
		} else if len(payload) != 4 || int16(binary.LittleEndian.Uint16(payload)) >= 31000 {
			t.Fatalf("expected normalized sample; got %v", payload)
		}
	}
	spokenLock.Lock()
	sentences := strings.Join(spoken, " ")
	spokenLock.Unlock()
	if sentences != "Greetings. Channel open." {
		t.Fatalf("phrases %q", sentences)
	}
	if rec = request("Bearer "+token, "internal-gateway", ""); rec.Code != 409 {
		t.Fatalf("duplicate turn status %d", rec.Code)
	}
}
