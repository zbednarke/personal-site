package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type devConfig struct {
	addr       string
	staticRoot string
	apiURL     string
	gatewayKey string
	user       string
}

var allowHTTPUpstreamForTests bool

func main() {
	cfg, err := loadDevConfig()
	if err != nil {
		log.Fatal(err)
	}
	handler, err := newDevHandler(cfg)
	if err != nil {
		log.Fatal(err)
	}

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("Jazz development site ready at http://%s/jazz/", cfg.addr)
	log.Printf("Local login is disabled; data and recordings use the private production account for %q.", cfg.user)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func loadDevConfig() (devConfig, error) {
	cfg := devConfig{
		addr:       strings.TrimSpace(os.Getenv("JAZZ_DEV_ADDR")),
		staticRoot: strings.TrimSpace(os.Getenv("JAZZ_DEV_STATIC_ROOT")),
		apiURL:     strings.TrimSpace(os.Getenv("JAZZ_DEV_API_URL")),
		gatewayKey: strings.TrimSpace(os.Getenv("JAZZ_DEV_GATEWAY_KEY")),
		user:       strings.TrimSpace(os.Getenv("JAZZ_DEV_USER")),
	}
	if cfg.addr == "" {
		cfg.addr = "localhost:4173"
	}
	if cfg.user == "" {
		cfg.user = "zach"
	}
	if cfg.staticRoot == "" || cfg.apiURL == "" || cfg.gatewayKey == "" {
		return devConfig{}, errors.New("JAZZ_DEV_STATIC_ROOT, JAZZ_DEV_API_URL, and JAZZ_DEV_GATEWAY_KEY are required")
	}
	absRoot, err := filepath.Abs(cfg.staticRoot)
	if err != nil {
		return devConfig{}, fmt.Errorf("resolve static root: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil || !info.IsDir() {
		return devConfig{}, errors.New("JAZZ_DEV_STATIC_ROOT must be an existing directory")
	}
	cfg.staticRoot = absRoot
	return cfg, nil
}

func newDevHandler(cfg devConfig) (http.Handler, error) {
	upstream, err := url.Parse(cfg.apiURL)
	if err != nil || (upstream.Scheme != "https" && !(allowHTTPUpstreamForTests && upstream.Scheme == "http")) || upstream.Host == "" {
		return nil, errors.New("JAZZ_DEV_API_URL must be a valid HTTPS URL")
	}

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.URL.Path = strings.TrimPrefix(request.URL.Path, "/jazz/api")
		if request.URL.Path == "" {
			request.URL.Path = "/"
		}
		request.Host = upstream.Host
		request.Header.Set("X-Jazz-User", cfg.user)
		request.Header.Set("X-Jazz-Gateway-Key", cfg.gatewayKey)
		request.Header.Del("Authorization")
	}
	proxy.ErrorHandler = func(response http.ResponseWriter, _ *http.Request, proxyErr error) {
		log.Printf("Jazz API proxy error: %v", proxyErr)
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusBadGateway)
		_, _ = response.Write([]byte(`{"error":"the private Jazz data service is unavailable"}`))
	}

	files := http.FileServer(http.Dir(cfg.staticRoot))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Permissions-Policy", "camera=(self), microphone=(self), geolocation=()")
		if strings.HasPrefix(request.URL.Path, "/jazz/api/") {
			proxy.ServeHTTP(response, request)
			return
		}
		files.ServeHTTP(response, request)
	}), nil
}
