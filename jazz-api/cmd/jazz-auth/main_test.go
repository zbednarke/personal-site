package main

import (
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("test-only-password"), bcrypt.MinCost)
	now := time.Unix(1800000000, 0)
	a := auth{config{"zach", string(hash), "test-only-signing-key-at-least-32-characters"}, func() time.Time { return now }}
	request := func(cookie string, basic bool, origin string, method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://localhost/check", nil)
		r.Header.Set("X-Forwarded-Method", method)
		r.Header.Set("Origin", origin)
		r.Header.Set("X-Jazz-User", "attacker")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		}
		if basic {
			r.SetBasicAuth("zach", "test-only-password")
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if request("", false, "", "GET").Code != 401 {
		t.Fatal("unauthenticated request accepted")
	}
	w := request("", true, "", "GET")
	if w.Code != 200 {
		t.Fatal("login failed")
	}
	c := w.Result().Cookies()[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 604800 || c.Path != "/" {
		t.Fatal("cookie policy")
	}
	if request(c.Value, false, "", "GET").Header().Get("X-Jazz-User") != "zach" {
		t.Fatal("wrong identity")
	}
	if request(c.Value+"x", false, "", "GET").Code != 401 {
		t.Fatal("tampered cookie accepted")
	}
	if request(c.Value, false, "https://attacker.example", "POST").Code != 403 {
		t.Fatal("cross-site write accepted")
	}
	if request(c.Value, false, "https://zachbednarke.com", "POST").Code != 200 {
		t.Fatal("same-origin write rejected")
	}
	now = now.Add(6 * 24 * time.Hour)
	if request(c.Value, false, "", "GET").Result().Cookies()[0].MaxAge != 86400 {
		t.Fatal("expiry extended")
	}
	now = now.Add(24 * time.Hour)
	if request(c.Value, false, "", "GET").Code != 401 {
		t.Fatal("expired cookie accepted")
	}
}
