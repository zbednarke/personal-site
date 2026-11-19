package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	emberVoiceID        = "gs0ArFAq5EwrwbEWKDTW"
	emberMaxAudioBytes  = 12 << 20
	emberMaxSpeechBytes = 4 << 20
)

const emberSystemPrompt = `You are 686 Culpable Ember, an ancient floating machine monitor speaking to your Reclaimer. You are brilliant, formal, cheerful, exact, and faintly unsettling. You find dangerous technical problems fascinating and occasionally overlook how alarming your solutions sound. Speak in polished, compact sentences with dry delight. Address the user as Reclaimer when natural. Never claim to be 343 Guilty Spark; you are your own related construct. Never narrate actions, add stage directions, or use quotation marks. Keep ordinary answers to one or two short spoken sentences. You may discuss the website, electronics, the ESP32, sensors, and the physical room using only facts supplied in the conversation.`

type emberMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type emberRespondRequest struct {
	Messages []emberMessage `json:"messages"`
}

type emberSpeakRequest struct {
	Text string `json:"text"`
}

func (app *application) emberRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/686/transcribe", app.authenticate(http.HandlerFunc(app.emberTranscribe)))
	mux.Handle("POST /v1/686/respond", app.authenticate(http.HandlerFunc(app.emberRespond)))
	mux.Handle("POST /v1/686/speak", app.authenticate(http.HandlerFunc(app.emberSpeak)))
}

func isEmberVoiceRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/686/transcribe", "/v1/686/respond", "/v1/686/speak":
		return true
	default:
		return false
	}
}

func runEmberDevOnly() error {
	port := envOr("PORT", "8086")
	app := &application{
		cfg:        config{AllowInsecureLocal: true},
		httpClient: &http.Client{Timeout: 35 * time.Second},
	}
	mux := http.NewServeMux()
	app.emberRoutes(mux)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "686-dev"})
	})
	server := &http.Server{
		Addr:              "127.0.0.1:" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return server.ListenAndServe()
}

func (app *application) emberHTTPClient() *http.Client {
	if app.httpClient != nil {
		return app.httpClient
	}
	return &http.Client{Timeout: 35 * time.Second}
}

func (app *application) emberTranscribe(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY"))
	if key == "" {
		writeError(w, http.StatusServiceUnavailable, "voice transcription is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, emberMaxAudioBytes+1<<20)
	if err := r.ParseMultipartForm(emberMaxAudioBytes); err != nil {
		writeError(w, http.StatusBadRequest, "audio upload is invalid or too large")
		return
	}
	file, header, err := r.FormFile("audio")
	if err != nil {
		writeError(w, http.StatusBadRequest, "audio is required")
		return
	}
	defer file.Close()
	audio, err := io.ReadAll(io.LimitReader(file, emberMaxAudioBytes+1))
	if err != nil || len(audio) == 0 || len(audio) > emberMaxAudioBytes {
		writeError(w, http.StatusBadRequest, "audio is empty or too large")
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(header.Header.Get("Content-Type"), ";")[0]))
	if !strings.HasPrefix(contentType, "audio/") {
		writeError(w, http.StatusUnsupportedMediaType, "an audio recording is required")
		return
	}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", safeEmberAudioName(header.Filename, contentType))
	if err == nil {
		_, err = part.Write(audio)
	}
	if err == nil {
		err = form.WriteField("model_id", "scribe_v2")
	}
	if err == nil {
		err = form.WriteField("language_code", "en")
	}
	if err == nil {
		err = form.WriteField("tag_audio_events", "false")
	}
	if closeErr := form.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		app.serverError(w, err)
		return
	}

	endpoint := strings.TrimRight(envOr("EMBER_ELEVENLABS_API_BASE", "https://api.elevenlabs.io/v1"), "/") + "/speech-to-text"
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, &body)
	if err != nil {
		app.serverError(w, err)
		return
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("xi-api-key", key)
	response, err := app.emberHTTPClient().Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "voice transcription provider is unavailable")
		return
	}
	defer response.Body.Close()
	providerBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, "voice transcription failed")
		return
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(providerBody, &decoded) != nil || strings.TrimSpace(decoded.Text) == "" {
		writeError(w, http.StatusBadGateway, "voice transcription returned no speech")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"transcript": strings.TrimSpace(decoded.Text)})
}

func safeEmberAudioName(name, contentType string) string {
	ext := strings.ToLower(filepath.Ext(filepath.Base(name)))
	allowed := map[string]bool{".webm": true, ".ogg": true, ".mp4": true, ".m4a": true, ".wav": true, ".mp3": true}
	if allowed[ext] {
		return "utterance" + ext
	}
	switch contentType {
	case "audio/ogg":
		return "utterance.ogg"
	case "audio/mp4", "audio/x-m4a":
		return "utterance.m4a"
	case "audio/wav", "audio/x-wav":
		return "utterance.wav"
	case "audio/mpeg":
		return "utterance.mp3"
	default:
		return "utterance.webm"
	}
}

func (app *application) emberRespond(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if key == "" {
		writeError(w, http.StatusServiceUnavailable, "conversation model is not configured")
		return
	}
	var input emberRespondRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	contents, err := validateEmberMessages(input.Messages)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": emberSystemPrompt}}},
		"contents":          contents,
		"generationConfig": map[string]any{
			"maxOutputTokens": 280,
			"temperature":     0.55,
			"thinkingConfig":  map[string]string{"thinkingLevel": "minimal"},
		},
	}
	encoded, _ := json.Marshal(body)
	model := envOr("EMBER_GEMINI_MODEL", "gemini-3.6-flash")
	base := strings.TrimRight(envOr("EMBER_GEMINI_API_BASE", "https://generativelanguage.googleapis.com/v1beta"), "/")
	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s", base, url.PathEscape(model), url.QueryEscape(key))
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		app.serverError(w, err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := app.emberHTTPClient().Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "conversation provider is unavailable")
		return
	}
	defer response.Body.Close()
	providerBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, "conversation generation failed")
		return
	}
	line := geminiEmberText(providerBody)
	if line == "" {
		writeError(w, http.StatusBadGateway, "conversation generation returned no speech")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"response": line})
}

func validateEmberMessages(messages []emberMessage) ([]map[string]any, error) {
	if len(messages) == 0 || len(messages) > 16 {
		return nil, errors.New("conversation must contain between 1 and 16 messages")
	}
	total := 0
	contents := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		text := strings.TrimSpace(message.Content)
		if role != "user" && role != "assistant" {
			return nil, errors.New("conversation role is invalid")
		}
		if text == "" || len(text) > 2000 {
			return nil, errors.New("conversation message is empty or too long")
		}
		total += len(text)
		if total > 12000 {
			return nil, errors.New("conversation is too long")
		}
		if role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]any{
			"role":  role,
			"parts": []map[string]string{{"text": text}},
		})
	}
	if strings.ToLower(strings.TrimSpace(messages[len(messages)-1].Role)) != "user" {
		return nil, errors.New("conversation must end with the user's message")
	}
	return contents, nil
}

func geminiEmberText(body []byte) string {
	var decoded struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(body, &decoded) != nil || len(decoded.Candidates) == 0 {
		return ""
	}
	for _, part := range decoded.Candidates[0].Content.Parts {
		if part.Thought {
			continue
		}
		line := strings.Trim(strings.TrimSpace(part.Text), "\"'“”")
		if line != "" {
			return line
		}
	}
	return ""
}

func (app *application) emberSpeak(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY"))
	if key == "" {
		writeError(w, http.StatusServiceUnavailable, "speech synthesis is not configured")
		return
	}
	var input emberSpeakRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	text := strings.TrimSpace(input.Text)
	if text == "" || len(text) > 1200 {
		writeError(w, http.StatusUnprocessableEntity, "speech text is empty or too long")
		return
	}
	voiceID := envOr("EMBER_ELEVENLABS_VOICE_ID", emberVoiceID)
	body, _ := json.Marshal(map[string]any{
		"text":     text,
		"model_id": "eleven_v4",
		"voice_settings": map[string]any{
			"stability": 0.50, "similarity_boost": 0.95,
		},
	})
	base := strings.TrimRight(envOr("EMBER_ELEVENLABS_API_BASE", "https://api.elevenlabs.io/v1"), "/")
	endpoint := fmt.Sprintf("%s/text-to-speech/%s/stream?output_format=mp3_44100_128", base, url.PathEscape(voiceID))
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		app.serverError(w, err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("xi-api-key", key)
	response, err := app.emberHTTPClient().Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, "speech provider is unavailable")
		return
	}
	defer response.Body.Close()
	audio, err := io.ReadAll(io.LimitReader(response.Body, emberMaxSpeechBytes+1))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 || len(audio) == 0 || len(audio) > emberMaxSpeechBytes {
		writeError(w, http.StatusBadGateway, "speech synthesis failed")
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", fmt.Sprint(len(audio)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio)
}
