package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateRepertoirePDF(t *testing.T) {
	if err := validateRepertoirePDF([]byte("%PDF-1.7\n%%EOF"), "application/pdf; charset=binary"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		body        []byte
		contentType string
	}{
		{nil, "application/pdf"},
		{[]byte("not a pdf"), "application/pdf"},
		{[]byte("%PDF-1.7"), "text/plain"},
		{append([]byte("%PDF-1.7"), make([]byte, maxRepertoireSheetBytes)...), "application/pdf"},
	} {
		if err := validateRepertoirePDF(test.body, test.contentType); err == nil {
			t.Fatalf("accepted invalid PDF (%s, %d bytes)", test.contentType, len(test.body))
		}
	}
}

func TestRepertoirePrivacyRejectsCrossSiteWrites(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := repertoirePrivacy(next)
	for _, test := range []struct {
		method, target, contentType, fetchSite, origin string
		want                                           int
	}{
		{"PATCH", "/v1/repertoire/tunes/skylark", "application/json", "same-origin", "", 204},
		{"POST", "/v1/repertoire/tunes/skylark/sheets", "application/pdf", "same-origin", "", 204},
		{"DELETE", "/v1/repertoire/sheets/id", "", "same-origin", "", 204},
		{"POST", "/v1/repertoire/tunes/skylark/sheets", "text/plain", "same-origin", "", 403},
		{"PATCH", "/v1/repertoire/tunes/skylark", "application/json", "cross-site", "", 403},
		{"PATCH", "/v1/repertoire/tunes/skylark", "application/json", "same-origin", "https://attacker.example", 403},
	} {
		r := httptest.NewRequest(test.method, "https://zachbednarke.com"+test.target, nil)
		r.Header.Set("Content-Type", test.contentType)
		r.Header.Set("Sec-Fetch-Site", test.fetchSite)
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Errorf("%s %s: got %d want %d", test.method, test.target, w.Code, test.want)
		}
	}
}

func TestSheetMetadataValidation(t *testing.T) {
	if got := decodeSheetHeader("B%E2%99%AD%20part"); got != "B♭ part" {
		t.Fatalf("decoded header: %q", got)
	}
	for value, want := range map[string]bool{
		"": true, "https://example.com/chart.pdf": true, "/assets/jazz/sheets/chart.pdf": true,
		"http://example.com/chart.pdf": false, "https://user@example.com/chart.pdf": false,
		strings.Repeat("x", 1001): false,
	} {
		if got := validSheetSourceURL(value); got != want {
			t.Errorf("validSheetSourceURL(%q) = %v want %v", value, got, want)
		}
	}
}
