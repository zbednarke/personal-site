package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
