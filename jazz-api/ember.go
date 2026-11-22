package main

import (
	"bufio"
	"bytes"
	"context"
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
	mux.Handle("POST /v1/686/device/turn", app.newEmberDeviceHandler())
}

type emberProviderError struct {
	status  int
	message string
}

func (e *emberProviderError) Error() string { return e.message }

func (app *application) writeEmberError(w http.ResponseWriter, err error) {
	var provider *emberProviderError
	if errors.As(err, &provider) {
		writeError(w, provider.status, provider.message)
	} else {
		app.serverError(w, err)
	}
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
	if address := os.Getenv("EMBER_DEVICE_LAN_ADDR"); address != "" {
		cert, key := os.Getenv("EMBER_DEVICE_LAN_CERT"), os.Getenv("EMBER_DEVICE_LAN_KEY")
		if cert == "" || key == "" || len(strings.TrimSpace(os.Getenv("EMBER_DEVICE_TOKEN_SHA256"))) != 64 {
			return errors.New("LAN device mode requires TLS files and a device token digest")
		}
		mux.Handle("POST /686/device/v1/turn", app.newEmberDeviceHandler())
		server := &http.Server{
			Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 22 * time.Second, IdleTimeout: 25 * time.Second,
		}
		return server.ListenAndServeTLS(cert, key)
	}
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
	if strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")) == "" {
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
	transcript, err := app.transcribeEmber(r.Context(), audio, safeEmberAudioName(header.Filename, contentType))
	if err != nil {
		app.writeEmberError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"transcript": transcript})
}

func (app *application) transcribeEmber(ctx context.Context, audio []byte, filename string) (string, error) {
	key := strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY"))
	if key == "" {
		return "", &emberProviderError{http.StatusServiceUnavailable, "voice transcription is not configured"}
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filename)
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
		return "", err
	}

	endpoint := strings.TrimRight(envOr("EMBER_ELEVENLABS_API_BASE", "https://api.elevenlabs.io/v1"), "/") + "/speech-to-text"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("xi-api-key", key)
	response, err := app.emberHTTPClient().Do(request)
	if err != nil {
		return "", &emberProviderError{http.StatusBadGateway, "voice transcription provider is unavailable"}
	}
	defer response.Body.Close()
	providerBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &emberProviderError{http.StatusBadGateway, "voice transcription failed"}
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(providerBody, &decoded) != nil || strings.TrimSpace(decoded.Text) == "" {
		return "", &emberProviderError{http.StatusBadGateway, "voice transcription returned no speech"}
	}
	return strings.TrimSpace(decoded.Text), nil
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
	if strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) == "" {
		writeError(w, http.StatusServiceUnavailable, "conversation model is not configured")
		return
	}
	var input emberRespondRequest
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	streaming := r.URL.Query().Get("stream") == "1"
	response, err := app.requestEmberResponse(r.Context(), input.Messages, streaming)
	if err != nil {
		app.writeEmberError(w, err)
		return
	}
	defer response.Body.Close()
	if streaming {
		streamEmberResponse(w, response.Body)
		return
	}
	providerBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
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

func (app *application) requestEmberResponse(ctx context.Context, messages []emberMessage, streaming bool) (*http.Response, error) {
	key := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if key == "" {
		return nil, &emberProviderError{http.StatusServiceUnavailable, "conversation model is not configured"}
	}
	contents, err := validateEmberMessages(messages)
	if err != nil {
		return nil, &emberProviderError{http.StatusUnprocessableEntity, err.Error()}
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
	method := "generateContent"
	if streaming {
		method = "streamGenerateContent"
	}
	endpoint := fmt.Sprintf("%s/models/%s:%s?alt=sse&key=%s", base, url.PathEscape(model), method, url.QueryEscape(key))
	if !streaming {
		endpoint = strings.Replace(endpoint, "alt=sse&", "", 1)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := app.emberHTTPClient().Do(request)
	if err != nil {
		return nil, &emberProviderError{http.StatusBadGateway, "conversation provider is unavailable"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, &emberProviderError{http.StatusBadGateway, "conversation generation failed"}
	}
	return response, nil
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
	if strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")) == "" {
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
	format, contentType := "mp3_44100_128", "audio/mpeg"
	if r.URL.Query().Get("format") == "pcm" {
		format, contentType = "pcm_24000", "application/octet-stream"
	}
	response, err := app.requestEmberSpeech(r.Context(), text, format)
	if err != nil {
		app.writeEmberError(w, err)
		return
	}
	defer response.Body.Close()
	// Read only the first chunk before committing headers, then flush each chunk.
	reader := io.LimitReader(response.Body, emberMaxSpeechBytes+1)
	buffer := make([]byte, 8192)
	total := 0
	for {
		n, readErr := reader.Read(buffer)
		total += n
		if total > emberMaxSpeechBytes || (readErr != nil && readErr != io.EOF) {
			if total == n {
				writeError(w, http.StatusBadGateway, "speech synthesis failed")
				return
			}
			panic(http.ErrAbortHandler) // Never append JSON to an audio stream.
		}
		if n > 0 {
			if total == n {
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("Cache-Control", "private, no-store")
				w.Header().Set("X-Accel-Buffering", "no")
			}
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
		}
		if readErr == io.EOF {
			if total == 0 {
				writeError(w, http.StatusBadGateway, "speech synthesis returned no audio")
			}
			return
		}
	}
}

func (app *application) requestEmberSpeech(ctx context.Context, text, format string) (*http.Response, error) {
	key := strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY"))
	if key == "" {
		return nil, &emberProviderError{http.StatusServiceUnavailable, "speech synthesis is not configured"}
	}
	voiceID := envOr("EMBER_ELEVENLABS_VOICE_ID", emberVoiceID)
	body, _ := json.Marshal(map[string]any{
		"text":           text,
		"model_id":       "eleven_v4",
		"voice_settings": map[string]any{"stability": 0.50, "similarity_boost": 0.95},
	})
	base := strings.TrimRight(envOr("EMBER_ELEVENLABS_API_BASE", "https://api.elevenlabs.io/v1"), "/")
	endpoint := fmt.Sprintf("%s/text-to-speech/%s/stream?output_format=%s", base, url.PathEscape(voiceID), format)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("xi-api-key", key)
	response, err := app.emberHTTPClient().Do(request)
	if err != nil {
		return nil, &emberProviderError{http.StatusBadGateway, "speech provider is unavailable"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, &emberProviderError{http.StatusBadGateway, "speech synthesis failed"}
	}
	return response, nil
}

// Translate provider SSE to a small, bounded NDJSON protocol. A terminal event
// lets clients distinguish a complete answer from a dropped connection.
func streamEmberResponse(w http.ResponseWriter, body io.Reader) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event map[string]any) error {
		if err := json.NewEncoder(w).Encode(event); err != nil {
			return err
		}
		return http.NewResponseController(w).Flush()
	}
	err := readEmberTokens(body, func(text string) error { return send(map[string]any{"text": text}) })
	if err != nil {
		_ = send(map[string]any{"error": "conversation stream interrupted"})
		return
	}
	_ = send(map[string]any{"done": true})
}

func readEmberTokens(body io.Reader, emit func(string) error) error {
	scanner := bufio.NewScanner(io.LimitReader(body, 2<<20))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	var data []string
	finished, hasText := false, false
	var streamErr error
	consume := func() bool {
		if len(data) == 0 {
			return true
		}
		payload := strings.Join(data, "\n")
		data = nil
		var event struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text    string
						Thought bool
					}
				}
				FinishReason string
			}
		}
		if json.Unmarshal([]byte(payload), &event) != nil {
			streamErr = errors.New("invalid conversation event")
			return false
		}
		if len(event.Candidates) == 0 {
			return true
		}
		candidate := event.Candidates[0]
		for _, part := range candidate.Content.Parts {
			if !part.Thought && part.Text != "" {
				hasText = true
				if streamErr = emit(part.Text); streamErr != nil {
					return false
				}
			}
		}
		if candidate.FinishReason != "" {
			finished = candidate.FinishReason == "STOP" || candidate.FinishReason == "MAX_TOKENS"
			if !finished {
				streamErr = errors.New("unexpected conversation finish reason")
				return false
			}
		}
		return true
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !consume() {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if streamErr == nil && len(data) > 0 {
		consume()
	}
	if streamErr != nil {
		return streamErr
	}
	if scanner.Err() != nil || !finished || !hasText {
		return errors.New("conversation stream interrupted")
	}
	return nil
}
