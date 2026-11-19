package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestEmberGuestSessionScope(t *testing.T) {
	c := baseConfig(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("guest-fixture"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	c.EmberGuestHash = string(hash)
	clk := &clock{time.Now().UTC()}
	s := newTestServer(t, c, nil, clk)
	for _, tc := range []struct {
		next string
		show bool
	}{{"/686/", true}, {"/jazz/", false}, {"/686/../jazz/", false}} {
		w := do(s, req{path: loginURL(tc.next)})
		if strings.Contains(w.Body.String(), "Guest login") != tc.show {
			t.Fatalf("unexpected guest form for %s", tc.next)
		}
	}
	login := req{method: "POST", path: "/auth/686/guest", headers: map[string]string{"Origin": s.cfg.PublicOrigin},
		form: url.Values{"password": {"guest-fixture"}, "next": {"/jazz/"}}}
	foreign := login
	foreign.headers = map[string]string{"Origin": "https://unrelated.example"}
	if w := do(s, foreign); w.Code != http.StatusForbidden {
		t.Fatalf("cross-site guest login: %d", w.Code)
	}
	foreign.headers = map[string]string{"Origin": "null", "Sec-Fetch-Site": "cross-site"}
	if w := do(s, foreign); w.Code != http.StatusForbidden {
		t.Fatalf("cross-site opaque origin: %d", w.Code)
	}
	login.headers = map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"}
	w := do(s, login)
	guest := cookieNamed(w, emberGuestCookie)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/686/" ||
		guest == nil || !guest.Secure || !guest.HttpOnly || cookieNamed(w, sessionCookie) != nil {
		t.Fatalf("unexpected guest login response: %d", w.Code)
	}
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/686/?view=demo", true},
		{"GET", "/assets/686/vendor/three.module.js", true},
		{"POST", "/686/api/v1/686/speak", true},
		{"GET", "/jazz/", false},
		{"GET", "/assets/jazz/signin-guard.js", false},
		{"GET", "/686/../jazz/", false},
		{"GET", "/assets/686/%2e%2e/jazz/signin-guard.js", false},
		{"POST", "/jazz/api/v1/686/speak", false},
		{"POST", "/686/api/v1/state", false},
		{"GET", "/commonplace/", false},
		{"GET", "/workbench/", false},
	} {
		request := req{path: "/check", cookies: []*http.Cookie{guest},
			headers: map[string]string{"X-Forwarded-Method": tc.method, "X-Forwarded-Uri": tc.path}}
		w := do(s, request)
		if tc.allowed && (w.Code != http.StatusOK || w.Header().Get("X-Jazz-User") != "ember-guest") ||
			!tc.allowed && w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	forged := *guest
	forged.Name = sessionCookie
	if w := do(s, req{path: "/check", cookies: []*http.Cookie{&forged}, headers: map[string]string{"X-Forwarded-Uri": "/jazz/"}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("renamed guest cookie granted owner access: %d", w.Code)
	}
	clk.t = clk.t.Add(12 * time.Hour)
	if w := do(s, req{path: "/check", cookies: []*http.Cookie{guest}, headers: map[string]string{"X-Forwarded-Uri": "/686/"}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("expired guest cookie granted access: %d", w.Code)
	}
}
