package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateCampaignState(t *testing.T) {
	valid := json.RawMessage(`{"version":1,"skillLevels":{"sound":2},"objectives":{"0":true},"repertoire":{"blue-bossa":3},"bosses":{},"scene":{},"practice":[{"id":"p1","date":"2026-08-02","minutes":30,"track":"trumpet","note":"long tones"}],"peopleCanCall":2}`)
	if err := validateCampaignState(valid); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}

	invalid := json.RawMessage(`{"version":1,"skillLevels":{"sound":9},"objectives":{},"repertoire":{},"bosses":{},"scene":{},"practice":[],"peopleCanCall":0}`)
	if err := validateCampaignState(invalid); err == nil {
		t.Fatal("invalid skill level was accepted")
	}
}

func TestAuthenticationRejectsMissingGatewayKey(t *testing.T) {
	app := &application{cfg: config{GatewayKey: "secret"}}
	handler := app.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/state", nil)
	request.Header.Set("X-Jazz-User", "zach")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestAuthenticationAcceptsGatewayAndUser(t *testing.T) {
	app := &application{cfg: config{GatewayKey: "secret"}}
	handler := app.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/state", nil)
	request.Header.Set("X-Jazz-Gateway-Key", "secret")
	request.Header.Set("X-Jazz-User", "zach")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestAllowedUploadOrigin(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{name: "production", origin: "https://zachbednarke.com", want: "https://zachbednarke.com"},
		{name: "local host", origin: "http://localhost:4173", want: "http://localhost:4173"},
		{name: "local IP", origin: "http://127.0.0.1:4173", want: "http://127.0.0.1:4173"},
		{name: "trims whitespace", origin: "  http://localhost:4173  ", want: "http://localhost:4173"},
		{name: "rejects arbitrary origin", origin: "https://example.com", want: ""},
		{name: "rejects lookalike origin", origin: "https://zachbednarke.com.example.com", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := allowedUploadOrigin(test.origin); got != test.want {
				t.Fatalf("allowedUploadOrigin(%q) = %q, want %q", test.origin, got, test.want)
			}
		})
	}
}

func TestValidateRecordingMedia(t *testing.T) {
	audio := recordingInitRequest{ContentType: "audio/wav", SizeBytes: 1024, DurationMS: maxDurationMS}
	kind, audioType, videoType, err := validateRecordingMedia(audio)
	if err != nil || kind != "audio" || audioType != "audio/wav" || videoType != "" {
		t.Fatalf("valid audio rejected: kind=%q audio=%q video=%q err=%v", kind, audioType, videoType, err)
	}

	video := recordingInitRequest{
		MediaKind: "video", ContentType: "audio/wav", SizeBytes: 2048, DurationMS: 60000,
		VideoContentType: "video/webm;codecs=vp9,opus", VideoSizeBytes: 4096,
		VideoWidth: 1920, VideoHeight: 1080, VideoFrameRate: 30,
	}
	kind, audioType, videoType, err = validateRecordingMedia(video)
	if err != nil || kind != "video" || audioType != "audio/wav" || videoType != "video/webm" {
		t.Fatalf("valid video rejected: kind=%q audio=%q video=%q err=%v", kind, audioType, videoType, err)
	}

	video.VideoSizeBytes = maxVideoBytes + 1
	if _, _, _, err = validateRecordingMedia(video); err == nil {
		t.Fatal("oversized video was accepted")
	}
}

func TestVideoExtensions(t *testing.T) {
	if got := extensionFor("video/webm"); got != "webm" {
		t.Fatalf("video/webm extension = %q", got)
	}
	if got := extensionFor("video/mp4"); got != "mp4" {
		t.Fatalf("video/mp4 extension = %q", got)
	}
}

func TestRecordingSectionLimits(t *testing.T) {
	if maxTakesPerBlock != 20 {
		t.Fatalf("max takes per block = %d, want 20", maxTakesPerBlock)
	}
	note := "  clean attacks today  "
	got, err := normalizeRecordingNote(&note)
	if err != nil || got != "clean attacks today" {
		t.Fatalf("valid note normalized to %q with error %v", got, err)
	}
	tooLong := strings.Repeat("x", maxTakeNoteBytes+1)
	if _, err := normalizeRecordingNote(&tooLong); err == nil {
		t.Fatal("oversized take note was accepted")
	}
	if _, err := normalizeRecordingNote(nil); err == nil {
		t.Fatal("missing take note was accepted")
	}
}
