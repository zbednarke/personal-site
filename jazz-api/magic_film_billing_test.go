package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMagicFilmBillingFailure(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		status           int
	}{
		{"credit", `{"error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"private account detail"}}`, "separately billed API access", 429},
		{"quota", `{"error":{"code":"insufficient_quota"}}`, "billing and limits before retrying", 429},
		{"rate", `{"error":{"code":"rate_limit_exceeded"}}`, "temporarily rate-limited", 429},
		{"auth", `{"error":{"message":"private account detail"}}`, "key and model permissions", 401},
		{"upstream", `private account detail`, "HTTP 503", 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/responses" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			t.Setenv("OPENAI_API_KEY", "test-only")
			t.Setenv("OPENAI_API_BASE", server.URL)
			_, err := requestMagicFilmPlan(context.Background(), "test-model", "medium", magicFilmManifest{TargetSeconds: 60}, "reflective", t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
			if strings.Contains(err.Error(), "private account detail") {
				t.Fatal("raw provider detail persisted")
			}
			if calls != 1 {
				t.Fatalf("billing failure retried %d requests", calls)
			}
		})
	}
}
