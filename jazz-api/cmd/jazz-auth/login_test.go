package main

import (
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func loginAuth(t *testing.T) auth {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return auth{config{"owner", string(hash), "test-signing-key-at-least-32-characters"}, func() time.Time { return time.Unix(1800000000, 0) }}
}
func TestFormLogin(t *testing.T) {
	a := loginAuth(t)
	next := "/jazz/films/test.notes.html?view=day"
	for _, tc := range []struct {
		origin, password string
		want             int
	}{{"https://evil.example", "test-password", 403}, {"", "test-password", 403}, {"https://zachbednarke.com", "wrong", 401}, {"https://zachbednarke.com", "test-password", 303}} {
		r := httptest.NewRequest("POST", "https://zachbednarke.com/jazz/sign-in", strings.NewReader(url.Values{"username": {"owner"}, "password": {tc.password}, "next": {next}}.Encode()))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
		if w.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("native challenge on form")
		}
		cs := w.Result().Cookies()
		if tc.want != 303 {
			if len(cs) != 0 {
				t.Fatal("invalid login got cookie")
			}
			continue
		}
		if w.Header().Get("Location") != next || len(cs) != 1 {
			t.Fatal("missing destination/session")
		}
		c := cs[0]
		if !c.Secure || !c.HttpOnly || c.Path != "/" || c.MaxAge != 604800 || c.SameSite != http.SameSiteLaxMode {
			t.Fatal("cookie policy")
		}
		r = httptest.NewRequest("GET", "http://localhost/check", nil)
		r.AddCookie(c)
		w = httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("X-Jazz-User") != "owner" {
			t.Fatal("cookie login failed")
		}
	}
}
func TestDocumentRedirectKeepsAPIUnauthorized(t *testing.T) {
	a := loginAuth(t)
	for _, tc := range []struct {
		method, accept, dest string
		want                 int
	}{{"GET", "text/html", "document", 303}, {"GET", "application/json", "", 401}, {"POST", "text/html", "document", 401}} {
		r := httptest.NewRequest("GET", "http://localhost/check", nil)
		r.Header.Set("X-Forwarded-Method", tc.method)
		r.Header.Set("X-Forwarded-Uri", "/jazz/films/map.notes.html")
		r.Header.Set("Accept", tc.accept)
		r.Header.Set("Sec-Fetch-Dest", tc.dest)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
		if tc.want == 303 {
			if w.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("native challenge")
			}
			u, _ := url.Parse(w.Header().Get("Location"))
			if u.Query().Get("next") != "/jazz/films/map.notes.html" {
				t.Fatal("lost destination")
			}
		}
	}
}
func TestReturnDestinationAndEscaping(t *testing.T) {
	for _, bad := range []string{"https://evil.example", "//evil.example", "/jazz/\\evil", "/jazz/%0d%0aLocation:evil", "/other"} {
		if safeNext(bad) != "/jazz/" {
			t.Fatalf("unsafe redirect %q", bad)
		}
	}
	a := loginAuth(t)
	r := httptest.NewRequest("GET", "https://zachbednarke.com/jazz/sign-in?next="+url.QueryEscape(`/jazz/"><script>alert(1)</script>`), nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "<script>") {
		t.Fatal("login HTML unsafe")
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing protections")
	}
}
