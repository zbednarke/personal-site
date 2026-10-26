package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMagicFilmFailureClassification(t *testing.T) {
	for _, tt := range []struct {
		name, raw, message, code string
		retry                    bool
	}{
		{"historical credits", `Astra returned 429: {"error":{"code":"credit_balance_exhausted","message":"private provider detail"}}`, "Magic Film failed", "provider_quota", false},
		{"historical quota type", `Astra returned 429: {"error":{"type":"insufficient_quota"}}`, "", "provider_quota", false},
		{"new credits", "Magic Film provider HTTP 429: insufficient_quota", "", "provider_quota", false},
		{"rate limit", `Astra returned 429: {"error":{"code":"rate_limit_exceeded","message":"not insufficient_quota"}}`, "", "provider_rate_limit", true},
		{"non json rate limit", "Astra returned 429: <html>private proxy error</html>", "", "provider_rate_limit", true},
		{"authentication", "Magic Film provider HTTP 401: provider_error", "", "provider_auth", false},
		{"permissions", "Magic Film provider HTTP 403: provider_error", "", "provider_auth", false},
		{"configuration", "OPENAI_API_KEY is not configured for the Magic Film worker", "", "provider_configuration", false},
		{"outage", "Magic Film provider HTTP 503: provider_error", "", "provider_unavailable", true},
		{"timeout", "request: context deadline exceeded", "", "timeout", true},
		{"worker", "private infrastructure response", "Cloud worker could not start", "worker_start", true},
		{"unknown", "ffmpeg failed https://private.example/?secret=123", "", "unknown", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			job := magicFilmJob{Status: "failed", Error: tt.raw, Message: tt.message}
			job.sanitizeFailure()
			if job.Failure.Code != tt.code || job.Failure.Retryable != tt.retry {
				t.Fatalf("unexpected failure: %+v", job.Failure)
			}
			body, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "private") || strings.Contains(string(body), "secret=123") || strings.Contains(string(body), "Astra returned") {
				t.Fatalf("raw error exposed: %s", body)
			}
			if job.Error == "" || job.Message != job.Failure.Title || job.Failure.Action == "" {
				t.Fatal("missing actionable failure")
			}
		})
	}
}

func TestMagicFilmProviderHTTPFailureIsSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"credit_balance_exhausted","message":"private provider detail"}}`))
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "test-only")
	t.Setenv("OPENAI_API_BASE", server.URL)
	_, err := requestMagicFilmPlan(context.Background(), "test-model", "medium", testMagicManifest(), "", t.TempDir())
	if err == nil || err.Error() != "Magic Film provider HTTP 429: insufficient_quota" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMagicFilmSuccessfulJobHasNoFailure(t *testing.T) {
	job := magicFilmJob{Status: "complete", Error: "private stale error"}
	job.sanitizeFailure()
	if job.Error != "" || job.Failure != nil {
		t.Fatal("stale failure exposed")
	}
}
