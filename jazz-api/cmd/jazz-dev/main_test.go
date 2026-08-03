package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDevelopmentHandlerServesJazzWithoutLogin(t *testing.T) {
	root := t.TempDir()
	jazzDir := filepath.Join(root, "jazz")
	if err := os.Mkdir(jazzDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jazzDir, "index.html"), []byte("local jazz"), 0o644); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/state" {
			t.Errorf("proxied path = %q, want /v1/state", request.URL.Path)
		}
		if request.Header.Get("X-Jazz-User") != "zach" {
			t.Errorf("missing development user header")
		}
		if request.Header.Get("X-Jazz-Gateway-Key") != "secret" {
			t.Errorf("missing gateway key")
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"hasState":true}`))
	}))
	defer upstream.Close()

	if _, err := newDevHandler(devConfig{staticRoot: root, apiURL: upstream.URL, gatewayKey: "secret", user: "zach"}); err == nil {
		t.Fatal("expected non-HTTPS upstream to be rejected")
	}

	allowHTTPUpstreamForTests = true
	t.Cleanup(func() { allowHTTPUpstreamForTests = false })
	handler, err := newDevHandler(devConfig{staticRoot: root, apiURL: upstream.URL, gatewayKey: "secret", user: "zach"})
	if err != nil {
		t.Fatal(err)
	}

	staticRequest := httptest.NewRequest(http.MethodGet, "/jazz/", nil)
	staticResponse := httptest.NewRecorder()
	handler.ServeHTTP(staticResponse, staticRequest)
	if staticResponse.Code != http.StatusOK || staticResponse.Body.String() != "local jazz" {
		t.Fatalf("local page response = %d %q", staticResponse.Code, staticResponse.Body.String())
	}
	if staticResponse.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("local page unexpectedly requested authentication")
	}

	apiRequest := httptest.NewRequest(http.MethodGet, "/jazz/api/v1/state", nil)
	apiResponse := httptest.NewRecorder()
	handler.ServeHTTP(apiResponse, apiRequest)
	body, _ := io.ReadAll(apiResponse.Result().Body)
	if apiResponse.Code != http.StatusOK || string(body) != `{"hasState":true}` {
		t.Fatalf("proxied response = %d %q", apiResponse.Code, body)
	}
}
