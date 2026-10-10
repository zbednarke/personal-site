package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	testClientID = "test-client.apps.googleusercontent.com"
	testEmail    = "owner@example.com"
	testKey      = "test-only-signing-key-at-least-32-characters"
	testPassword = "test-only-password"
)

var (
	keyOnce sync.Once
	keyA    *rsa.PrivateKey
	keyB    *rsa.PrivateKey
)

func testKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	keyOnce.Do(func() {
		var err error
		if keyA, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if keyB, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return keyA, keyB
}

// fakeGoogle serves the token and JWKS endpoints from httptest.
type fakeGoogle struct {
	mu        sync.Mutex
	published map[string]*rsa.PublicKey
	signKey   *rsa.PrivateKey
	signKid   string
	claims    map[string]any
	challenge string
	jwksHits  int
	tokenForm url.Values
	server    *httptest.Server
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	a, _ := testKeys(t)
	g := &fakeGoogle{published: map[string]*rsa.PublicKey{"kid-a": &a.PublicKey}, signKey: a, signKid: "kid-a"}
	mux := http.NewServeMux()
	mux.HandleFunc("/certs", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.jwksHits++
		var keys []map[string]string
		for kid, k := range g.published {
			keys = append(keys, map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid, "n": b64.EncodeToString(k.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(k.E)).Bytes())})
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		g.mu.Lock()
		defer g.mu.Unlock()
		g.tokenForm = r.PostForm
		f := r.PostForm
		ok := r.Method == "POST" && f.Get("grant_type") == "authorization_code" && f.Get("code") == "good-code" &&
			f.Get("client_id") == testClientID && f.Get("client_secret") == "test-secret" &&
			f.Get("redirect_uri") == "https://zachbednarke.com/auth/google/callback" &&
			pkceChallenge(f.Get("code_verifier")) == g.challenge
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id_token": mint(g.signKey, g.signKid, g.claims), "access_token": "unused"})
	})
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

func (g *fakeGoogle) endpoints() googleEndpoints {
	return googleEndpoints{Auth: "https://accounts.example/auth", Token: g.server.URL + "/token", JWKS: g.server.URL + "/certs"}
}

func mint(key *rsa.PrivateKey, kid string, claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	p, _ := json.Marshal(claims)
	signing := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return signing + "." + b64.EncodeToString(sig)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func baseConfig(t *testing.T) config {
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	return config{User: "zach", Hash: string(hash), Key: testKey, GoogleClientID: testClientID, GoogleClientSecret: "test-secret", AllowedEmails: []string{"Owner@Example.com"}}
}

func newTestServer(t *testing.T, c config, g *fakeGoogle, clk *clock) *server {
	t.Helper()
	ep := googleEndpoints{}
	if g != nil {
		ep = g.endpoints()
	}
	s, err := newServer(c, ep, &http.Client{Timeout: 5 * time.Second}, clk.now)
	if err != nil {
		t.Fatal(err)
	}
	s.logf = func(format string, args ...any) {
		// Logs must never contain the address, secret, code or tokens.
		line := strings.ToLower(format)
		for _, a := range args {
			if v, ok := a.(string); ok {
				line += v
			}
			if e, ok := a.(error); ok {
				line += e.Error()
			}
		}
		for _, secret := range []string{strings.ToLower(testEmail), "test-secret", "good-code", "test-only-password"} {
			if strings.Contains(line, secret) {
				t.Errorf("log leaked a secret: %q", line)
			}
		}
	}
	return s
}

func validClaims(nonce string, now time.Time) map[string]any {
	return map[string]any{
		"iss": "https://accounts.google.com", "aud": testClientID, "azp": testClientID, "sub": "1234",
		"email": testEmail, "email_verified": true, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

type req struct {
	method, path string
	headers      map[string]string
	cookies      []*http.Cookie
	form         url.Values
}

func do(s *server, q req) *httptest.ResponseRecorder {
	if q.method == "" {
		q.method = "GET"
	}
	var r *http.Request
	if q.form != nil {
		r = httptest.NewRequest(q.method, "http://127.0.0.1:8768"+q.path, strings.NewReader(q.form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(q.method, "http://127.0.0.1:8768"+q.path, nil)
	}
	for k, v := range q.headers {
		r.Header.Set(k, v)
	}
	for _, c := range q.cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// signIn runs the whole Google flow and returns the session cookie.
func signIn(t *testing.T, s *server, g *fakeGoogle, clk *clock, next string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	start := do(s, req{path: "/auth/google/start?next=" + url.QueryEscape(next)})
	if start.Code != 302 {
		t.Fatalf("start: %d", start.Code)
	}
	loc, _ := url.Parse(start.Header().Get("Location"))
	p := loc.Query()
	g.mu.Lock()
	g.challenge = p.Get("code_challenge")
	g.claims = validClaims(p.Get("nonce"), clk.now())
	g.mu.Unlock()
	state := cookieNamed(start, oauthCookie)
	cb := do(s, req{path: "/auth/google/callback?state=" + url.QueryEscape(p.Get("state")) + "&code=good-code", cookies: []*http.Cookie{state}})
	return cb, cookieNamed(cb, sessionCookie)
}

func TestConfigValidation(t *testing.T) {
	good := baseConfig(t)
	if err := good.normalize(); err != nil {
		t.Fatal(err)
	}
	if good.SessionDays != 30 || good.PublicOrigin != "https://zachbednarke.com" || good.AllowedEmails[0] != "owner@example.com" || good.LoginHint != "owner@example.com" {
		t.Fatalf("defaults not applied: %+v", good)
	}
	cases := map[string]func(*config){
		"no secret":        func(c *config) { c.GoogleClientSecret = "" },
		"no emails":        func(c *config) { c.AllowedEmails = nil },
		"only secret":      func(c *config) { c.GoogleClientID, c.AllowedEmails = "", nil },
		"bad client":       func(c *config) { c.GoogleClientID = "nope" },
		"bad email":        func(c *config) { c.AllowedEmails = []string{"not-an-email"} },
		"short key":        func(c *config) { c.Key = "short" },
		"no user":          func(c *config) { c.User = "" },
		"http origin":      func(c *config) { c.PublicOrigin = "http://zachbednarke.com" },
		"origin path":      func(c *config) { c.PublicOrigin = "https://zachbednarke.com/x" },
		"session days":     func(c *config) { c.SessionDays = 1000 },
		"nothing enabled":  func(c *config) { c.GoogleClientID, c.GoogleClientSecret, c.AllowedEmails = "", "", nil },
		"password no hash": func(c *config) { c.AllowPassword, c.Hash = true, "" },
	}
	for name, mutate := range cases {
		c := baseConfig(t)
		mutate(&c)
		if c.normalize() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	pw := config{User: "zach", Hash: good.Hash, Key: testKey, AllowPassword: true}
	if err := pw.normalize(); err != nil {
		t.Fatalf("password-only config rejected: %v", err)
	}
	// The file loader matches the struct, and the legacy file (no Google, no
	// AllowPassword) refuses to start rather than silently allowing nothing.
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	os.WriteFile(path, []byte(`{"User":"zach","Hash":"`+good.Hash+`","Key":"`+testKey+`"}`), 0o600)
	if _, err := loadConfig(path); err == nil {
		t.Fatal("legacy config without a sign-in method accepted")
	}
}

func TestGoogleRoundTrip(t *testing.T) {
	g := newFakeGoogle(t)
	clk := &clock{time.Unix(1800000000, 0)}
	s := newTestServer(t, baseConfig(t), g, clk)

	start := do(s, req{path: "/auth/google/start?next=" + url.QueryEscape("/trumpets/?view=all")})
	loc, _ := url.Parse(start.Header().Get("Location"))
	p := loc.Query()
	if loc.Host != "accounts.example" || p.Get("response_type") != "code" || p.Get("client_id") != testClientID ||
		p.Get("scope") != "openid email" || p.Get("code_challenge_method") != "S256" || len(p.Get("code_challenge")) != 43 ||
		p.Get("state") == "" || p.Get("nonce") == "" || p.Get("state") == p.Get("nonce") ||
		p.Get("redirect_uri") != "https://zachbednarke.com/auth/google/callback" || p.Get("login_hint") != "owner@example.com" || p.Has("prompt") {
		t.Fatalf("authorization request: %s", loc)
	}
	st := cookieNamed(start, oauthCookie)
	if st == nil || !st.Secure || !st.HttpOnly || st.SameSite != http.SameSiteLaxMode || st.Path != "/" || st.MaxAge != 600 {
		t.Fatalf("state cookie policy: %+v", st)
	}
	for _, secret := range []string{p.Get("state"), p.Get("nonce")} {
		if strings.Contains(st.Value, secret) {
			t.Fatal("state cookie is not encrypted")
		}
	}
	// The PKCE verifier stays in the encrypted cookie and matches the challenge.
	opened, ok := s.openState(st.Value)
	if !ok || pkceChallenge(opened.Verifier) != p.Get("code_challenge") || opened.State != p.Get("state") || opened.Nonce != p.Get("nonce") {
		t.Fatal("state cookie round trip")
	}
	if sw := do(s, req{path: "/auth/google/start?switch=1"}); !strings.Contains(sw.Header().Get("Location"), "prompt=select_account") || strings.Contains(sw.Header().Get("Location"), "login_hint") {
		t.Fatal("switch account should use the account chooser")
	}

	cb, session := signIn(t, s, g, clk, "/trumpets/?view=all")
	if cb.Code != 303 || cb.Header().Get("Location") != "/trumpets/?view=all" {
		t.Fatalf("callback: %d %s", cb.Code, cb.Header().Get("Location"))
	}
	if g.tokenForm.Get("code_verifier") == "" || g.tokenForm.Get("client_secret") != "test-secret" {
		t.Fatal("token exchange form")
	}
	if c := cookieNamed(cb, oauthCookie); c == nil || c.MaxAge >= 0 {
		t.Fatal("state cookie not cleared")
	}
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Path != "/" || session.MaxAge != 30*86400 {
		t.Fatalf("session cookie policy: %+v", session)
	}
	if strings.Contains(strings.ToLower(session.Value), "owner") {
		t.Fatal("session cookie exposes the email")
	}
	check := do(s, req{path: "/check", headers: map[string]string{"X-Forwarded-Method": "GET", "X-Jazz-User": "attacker"}, cookies: []*http.Cookie{session}})
	if check.Code != 200 || check.Header().Get("X-Jazz-User") != "zach" {
		t.Fatalf("check with Google session: %d %q", check.Code, check.Header().Get("X-Jazz-User"))
	}

	// Replaying the callback, a wrong state, or a missing cookie all fail.
	start = do(s, req{path: "/auth/google/start"})
	stc := cookieNamed(start, oauthCookie)
	bad := do(s, req{path: "/auth/google/callback?state=wrong&code=good-code", cookies: []*http.Cookie{stc}})
	if bad.Code != 303 || !strings.HasPrefix(bad.Header().Get("Location"), "/auth/login?error=expired") || cookieNamed(bad, sessionCookie) != nil {
		t.Fatalf("state mismatch: %d %s", bad.Code, bad.Header().Get("Location"))
	}
	loc, _ = url.Parse(start.Header().Get("Location"))
	if w := do(s, req{path: "/auth/google/callback?state=" + loc.Query().Get("state") + "&code=good-code"}); !strings.Contains(w.Header().Get("Location"), "error=expired") {
		t.Fatal("missing state cookie accepted")
	}
	if w := do(s, req{path: "/auth/google/callback?state=" + loc.Query().Get("state") + "&error=access_denied", cookies: []*http.Cookie{stc}}); !strings.Contains(w.Header().Get("Location"), "error=denied") {
		t.Fatal("cancelled sign-in")
	}
	// State cookies expire after ten minutes.
	clk.t = clk.t.Add(11 * time.Minute)
	if w := do(s, req{path: "/auth/google/callback?state=" + loc.Query().Get("state") + "&code=good-code", cookies: []*http.Cookie{stc}}); !strings.Contains(w.Header().Get("Location"), "error=expired") {
		t.Fatal("expired state accepted")
	}
	tampered := *stc
	tampered.Value = stc.Value[:len(stc.Value)-2] + "AA"
	if _, ok := s.openState(tampered.Value); ok {
		t.Fatal("tampered state accepted")
	}
}

func TestCallbackRejections(t *testing.T) {
	g := newFakeGoogle(t)
	clk := &clock{time.Unix(1800000000, 0)}
	s := newTestServer(t, baseConfig(t), g, clk)
	run := func(mutate func(map[string]any)) string {
		start := do(s, req{path: "/auth/google/start?next=/jazz/"})
		loc, _ := url.Parse(start.Header().Get("Location"))
		p := loc.Query()
		claims := validClaims(p.Get("nonce"), clk.now())
		mutate(claims)
		g.mu.Lock()
		g.challenge, g.claims = p.Get("code_challenge"), claims
		g.mu.Unlock()
		w := do(s, req{path: "/auth/google/callback?state=" + url.QueryEscape(p.Get("state")) + "&code=good-code", cookies: []*http.Cookie{cookieNamed(start, oauthCookie)}})
		if cookieNamed(w, sessionCookie) != nil && w.Header().Get("Location") != "/jazz/" {
			t.Fatal("session issued on failure")
		}
		return w.Header().Get("Location")
	}
	if loc := run(func(map[string]any) {}); loc != "/jazz/" {
		t.Fatalf("baseline: %s", loc)
	}
	if loc := run(func(c map[string]any) { c["email"] = "stranger@example.com" }); !strings.Contains(loc, "error=not_allowed") {
		t.Fatalf("wrong email: %s", loc)
	}
	if loc := run(func(c map[string]any) { c["nonce"] = "other" }); !strings.Contains(loc, "error=failed") {
		t.Fatalf("nonce: %s", loc)
	}
	// A wrong code verifier (PKCE) is refused by the token endpoint.
	start := do(s, req{path: "/auth/google/start"})
	loc, _ := url.Parse(start.Header().Get("Location"))
	g.mu.Lock()
	g.challenge = "not-the-challenge"
	g.mu.Unlock()
	w := do(s, req{path: "/auth/google/callback?state=" + url.QueryEscape(loc.Query().Get("state")) + "&code=good-code", cookies: []*http.Cookie{cookieNamed(start, oauthCookie)}})
	if !strings.Contains(w.Header().Get("Location"), "error=failed") || cookieNamed(w, sessionCookie) != nil {
		t.Fatal("PKCE mismatch accepted")
	}
	// The not-allowed page offers the account chooser.
	page := do(s, req{path: "/auth/login?error=not_allowed&next=/jazz/"})
	if !strings.Contains(page.Body.String(), "switch=1") {
		t.Fatal("no switch-account link")
	}
}

func TestIDTokenValidation(t *testing.T) {
	g := newFakeGoogle(t)
	clk := &clock{time.Unix(1800000000, 0)}
	s := newTestServer(t, baseConfig(t), g, clk)
	a, b := testKeys(t)
	ctx := context.Background()
	now := clk.now()
	verify := func(key *rsa.PrivateKey, kid string, mutate func(map[string]any)) error {
		c := validClaims("n-1", now)
		mutate(c)
		_, err := s.verifyIDToken(ctx, mint(key, kid, c), "n-1")
		return err
	}
	if err := verify(a, "kid-a", func(map[string]any) {}); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"string email_verified": func(c map[string]any) { c["email_verified"] = "true" },
		"short issuer":          func(c map[string]any) { c["iss"] = "accounts.google.com" },
		"uppercase email":       func(c map[string]any) { c["email"] = "OWNER@example.COM" },
		"aud list with azp":     func(c map[string]any) { c["aud"] = []string{testClientID, "other"} },
		"small skew":            func(c map[string]any) { c["exp"] = now.Add(-time.Minute).Unix() },
	} {
		if err := verify(a, "kid-a", mutate); err != nil {
			t.Errorf("%s: rejected: %v", name, err)
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"wrong aud":        func(c map[string]any) { c["aud"] = "other.apps.googleusercontent.com" },
		"aud list, no azp": func(c map[string]any) { c["aud"], c["azp"] = []string{testClientID, "other"}, "other" },
		"wrong iss":        func(c map[string]any) { c["iss"] = "https://evil.example" },
		"expired":          func(c map[string]any) { c["exp"] = now.Add(-10 * time.Minute).Unix() },
		"no exp":           func(c map[string]any) { delete(c, "exp") },
		"future iat":       func(c map[string]any) { c["iat"] = now.Add(time.Hour).Unix() },
		"unverified email": func(c map[string]any) { c["email_verified"] = false },
		"missing verified": func(c map[string]any) { delete(c, "email_verified") },
		"wrong email":      func(c map[string]any) { c["email"] = "owner@example.com.evil" },
		"nonce mismatch":   func(c map[string]any) { c["nonce"] = "n-2" },
		"missing nonce":    func(c map[string]any) { delete(c, "nonce") },
		"no email":         func(c map[string]any) { delete(c, "email") },
	} {
		if err := verify(a, "kid-a", mutate); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := verify(a, "kid-a", func(c map[string]any) { c["email"] = "x@example.com" }); !errors.Is(err, errNotAllowed) {
		t.Errorf("wrong email should be errNotAllowed, got %v", err)
	}
	// A token signed by another key under a published kid fails.
	if err := verify(b, "kid-a", func(map[string]any) {}); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("bad signature: %v", err)
	}
	// Tampered payload with the original signature fails.
	tok := mint(a, "kid-a", validClaims("n-1", now))
	parts := strings.Split(tok, ".")
	evil := validClaims("n-1", now)
	evil["email"] = testEmail
	evil["aud"] = testClientID
	pb, _ := json.Marshal(evil)
	if _, err := s.verifyIDToken(ctx, parts[0]+"."+b64.EncodeToString(append(pb, ' '))+"."+parts[2], "n-1"); err == nil {
		t.Fatal("tampered payload accepted")
	}
	// alg none / HS256 are refused before any key lookup.
	none := b64.EncodeToString([]byte(`{"alg":"none","kid":"kid-a"}`)) + "." + parts[1] + "."
	if _, err := s.verifyIDToken(ctx, none, "n-1"); err == nil {
		t.Fatal("alg none accepted")
	}

	// Unknown kid: the cache is refetched (after the minimum interval), and the
	// rotated key is then accepted.
	hits := g.jwksHits
	g.mu.Lock()
	g.published["kid-b"] = &b.PublicKey
	g.mu.Unlock()
	if err := verify(b, "kid-b", func(map[string]any) {}); err == nil {
		t.Fatal("unknown kid accepted without refetch interval")
	}
	if g.jwksHits != hits {
		t.Fatal("refetched keys within the minimum interval")
	}
	clk.t = clk.t.Add(time.Minute)
	now = clk.now()
	if err := verify(b, "kid-b", func(map[string]any) {}); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
	if g.jwksHits != hits+1 {
		t.Fatalf("expected one refetch, got %d", g.jwksHits-hits)
	}
	// Cached keys are reused until Cache-Control max-age expires.
	verify(a, "kid-a", func(map[string]any) {})
	if g.jwksHits != hits+1 {
		t.Fatal("cache not used")
	}
	clk.t = clk.t.Add(2 * time.Hour)
	now = clk.now()
	verify(a, "kid-a", func(map[string]any) {})
	if g.jwksHits != hits+2 {
		t.Fatal("expired cache not refreshed")
	}
	if err := verify(b, "kid-unknown", func(map[string]any) {}); !errors.Is(err, errUnknownKey) {
		t.Fatalf("unknown kid: %v", err)
	}
	if cacheTTL("public, max-age=19800, must-revalidate") != 19800*time.Second || cacheTTL("") != time.Hour || cacheTTL("max-age=5") != time.Minute || cacheTTL("max-age=999999") != 24*time.Hour {
		t.Fatal("cache ttl")
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/jazz/":                        "/jazz/",
		"/trumpets/?view=all#x":         "/trumpets/?view=all#x",
		"/commonplace/notes":            "/commonplace/notes",
		"":                              "/jazz/",
		"//evil.example/":               "/jazz/",
		"/\\evil.example":               "/jazz/",
		"https://evil.example/":         "/jazz/",
		"javascript:alert(1)":           "/jazz/",
		"jazz/":                         "/jazz/",
		"/jazz/\r\nSet-Cookie:x":        "/jazz/",
		"/jazz/\t":                      "/jazz/",
		"/auth/logout":                  "/jazz/",
		"/auth":                         "/jazz/",
		"/%2F%2Fevil.example":           "/%2F%2Fevil.example",
		"\\\\evil.example":              "/jazz/",
		"/" + strings.Repeat("a", 3000): "/jazz/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
	// End to end: the callback never redirects off site, whatever next was.
	g := newFakeGoogle(t)
	clk := &clock{time.Unix(1800000000, 0)}
	s := newTestServer(t, baseConfig(t), g, clk)
	cb, _ := signIn(t, s, g, clk, "//evil.example/path")
	if cb.Header().Get("Location") != "/jazz/" {
		t.Fatalf("open redirect: %s", cb.Header().Get("Location"))
	}
	// A signed-in visit to the login page also uses the guard.
	_, session := signIn(t, s, g, clk, "/jazz/")
	w := do(s, req{path: "/auth/login?next=https://evil.example", cookies: []*http.Cookie{session}})
	if w.Code != 303 || w.Header().Get("Location") != "/jazz/" {
		t.Fatalf("login redirect: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestCheckResponses(t *testing.T) {
	g := newFakeGoogle(t)
	clk := &clock{time.Unix(1800000000, 0)}
	s := newTestServer(t, baseConfig(t), g, clk)
	check := func(h map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		return do(s, req{path: "/check", headers: h, cookies: cookies})
	}
	nav := check(map[string]string{"X-Forwarded-Method": "GET", "X-Forwarded-Uri": "/trumpets/?view=all", "Sec-Fetch-Mode": "navigate", "Accept": "text/html"})
	if nav.Code != 302 || nav.Header().Get("Location") != "/auth/login?next=%2Ftrumpets%2F%3Fview%3Dall" || nav.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("navigation: %d %s", nav.Code, nav.Header().Get("Location"))
	}
	// Without Fetch Metadata, Accept decides.
	if w := check(map[string]string{"X-Forwarded-Method": "HEAD", "X-Forwarded-Uri": "/jazz/", "Accept": "text/html,application/xhtml+xml"}); w.Code != 302 {
		t.Fatalf("Accept navigation: %d", w.Code)
	}
	if w := check(map[string]string{"X-Forwarded-Method": "GET", "X-Forwarded-Uri": "//evil.example", "Sec-Fetch-Mode": "navigate"}); w.Header().Get("Location") != "/auth/login?next=%2Fjazz%2F" {
		t.Fatalf("navigation next guard: %s", w.Header().Get("Location"))
	}
	apiCases := []map[string]string{
		{"X-Forwarded-Method": "GET", "X-Forwarded-Uri": "/jazz/api/v1/state", "Sec-Fetch-Mode": "cors", "Accept": "*/*"},
		{"X-Forwarded-Method": "GET", "X-Forwarded-Uri": "/assets/jazz/app.js", "Sec-Fetch-Mode": "no-cors"},
		{"X-Forwarded-Method": "GET", "X-Forwarded-Uri": "/jazz/api/v1/state", "Sec-Fetch-Mode": "cors", "Accept": "text/html"},
		{"X-Forwarded-Method": "POST", "X-Forwarded-Uri": "/jazz/", "Sec-Fetch-Mode": "navigate", "Accept": "text/html"},
		{"X-Forwarded-Method": "GET", "X-Forwarded-Uri": "/trumpets/api/v1/trumpets/listings"},
		{"X-Forwarded-Method": "GET", "Authorization": "Basic " + b64.EncodeToString([]byte("zach:"+testPassword))},
	}
	for _, h := range apiCases {
		w := check(h)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("api %v: %d %v", h, w.Code, w.Header())
		}
		var body map[string]string
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["error"] != "signin_required" || body["login"] != "/auth/login" || len(body) != 2 {
			t.Fatalf("api body: %s", w.Body)
		}
	}
	// Cross-site unsafe methods are rejected even with a valid session.
	_, session := signIn(t, s, g, clk, "/jazz/")
	if w := check(map[string]string{"X-Forwarded-Method": "POST", "Origin": "https://attacker.example"}, session); w.Code != 403 {
		t.Fatal("cross-site write accepted")
	}
	if w := check(map[string]string{"X-Forwarded-Method": "PUT", "Sec-Fetch-Site": "cross-site"}, session); w.Code != 403 {
		t.Fatal("cross-site write accepted without Origin")
	}
	if w := check(map[string]string{"X-Forwarded-Method": "POST", "Origin": "https://zachbednarke.com"}, session); w.Code != 200 {
		t.Fatal("same-origin write rejected")
	}
	// Expiry is preserved, not extended, and expired cookies fail.
	clk.t = clk.t.Add(29 * 24 * time.Hour)
	if w := check(map[string]string{"X-Forwarded-Method": "GET"}, session); w.Code != 200 || cookieNamed(w, sessionCookie).MaxAge != 86400 {
		t.Fatal("expiry extended")
	}
	clk.t = clk.t.Add(24 * time.Hour)
	if w := check(map[string]string{"X-Forwarded-Method": "GET"}, session); w.Code != 401 {
		t.Fatal("expired cookie accepted")
	}
	if w := check(map[string]string{"X-Forwarded-Method": "GET"}, &http.Cookie{Name: sessionCookie, Value: "garbage"}); w.Code != 401 {
		t.Fatal("garbage cookie accepted")
	}
	if do(s, req{path: "/elsewhere"}).Code != 404 {
		t.Fatal("unknown path")
	}
}

func TestSessionBinding(t *testing.T) {
	g := newFakeGoogle(t)
	clk := &clock{time.Unix(1800000000, 0)}
	cfg := baseConfig(t)
	cfg.AllowPassword = true
	s := newTestServer(t, cfg, g, clk)
	ok := func(s *server, c *http.Cookie) bool {
		return do(s, req{path: "/check", headers: map[string]string{"X-Forwarded-Method": "GET"}, cookies: []*http.Cookie{c}}).Code == 200
	}
	_, googleSession := signIn(t, s, g, clk, "/jazz/")
	pw := do(s, req{method: "POST", path: "/auth/password", headers: map[string]string{"Origin": "https://zachbednarke.com"}, form: url.Values{"user": {"zach"}, "password": {testPassword}, "next": {"/jazz/"}}})
	passwordSession := cookieNamed(pw, sessionCookie)
	if !ok(s, googleSession) || passwordSession == nil || !ok(s, passwordSession) {
		t.Fatal("baseline sessions")
	}
	// Tampering with the method field breaks the signature.
	parts := strings.Split(googleSession.Value, ".")
	parts[2] = methodPassword
	if ok(s, &http.Cookie{Name: sessionCookie, Value: strings.Join(parts, ".")}) {
		t.Fatal("method swap accepted")
	}
	// Disabling the password method ends password sessions only.
	noPw := cfg
	noPw.AllowPassword = false
	s2 := newTestServer(t, noPw, g, clk)
	if ok(s2, passwordSession) || !ok(s2, googleSession) {
		t.Fatal("disabling password")
	}
	// Disabling Google ends Google sessions only.
	noGoogle := cfg
	noGoogle.GoogleClientID, noGoogle.GoogleClientSecret, noGoogle.AllowedEmails = "", "", nil
	s3 := newTestServer(t, noGoogle, nil, clk)
	if ok(s3, googleSession) || !ok(s3, passwordSession) {
		t.Fatal("disabling Google")
	}
	// Removing the email from the allowlist signs that account out.
	other := cfg
	other.AllowedEmails = []string{"someone-else@example.com"}
	if ok(newTestServer(t, other, g, clk), googleSession) {
		t.Fatal("removed email still signed in")
	}
	// Rotating the key signs everyone out; changing the password hash ends
	// password sessions; changing the client ends Google sessions.
	rotated := cfg
	rotated.Key = strings.Repeat("r", 40)
	s4 := newTestServer(t, rotated, g, clk)
	if ok(s4, googleSession) || ok(s4, passwordSession) {
		t.Fatal("key rotation")
	}
	newHash := cfg
	h, _ := bcrypt.GenerateFromPassword([]byte("another"), bcrypt.MinCost)
	newHash.Hash = string(h)
	s5 := newTestServer(t, newHash, g, clk)
	if ok(s5, passwordSession) || !ok(s5, googleSession) {
		t.Fatal("password change")
	}
	newClient := cfg
	newClient.GoogleClientID = "another.apps.googleusercontent.com"
	if ok(newTestServer(t, newClient, g, clk), googleSession) {
		t.Fatal("client change")
	}
	// Changing the configured user invalidates cookies for the old user.
	renamed := cfg
	renamed.User = "someone"
	if ok(newTestServer(t, renamed, g, clk), googleSession) {
		t.Fatal("user change")
	}
	// Shortening the lifetime invalidates cookies that outlive it.
	short := cfg
	short.SessionDays = 7
	if ok(newTestServer(t, short, g, clk), googleSession) {
		t.Fatal("lifetime shortened")
	}
	// The pre-Google cookie format is not accepted.
	if ok(s, &http.Cookie{Name: sessionCookie, Value: b64.EncodeToString([]byte("zach")) + ".1800600000.sig"}) {
		t.Fatal("legacy cookie accepted")
	}
}

func TestPasswordFallback(t *testing.T) {
	g := newFakeGoogle(t)
	clk := &clock{time.Unix(1800000000, 0)}
	post := func(s *server, h map[string]string, user, pass, next string) *httptest.ResponseRecorder {
		return do(s, req{method: "POST", path: "/auth/password", headers: h, form: url.Values{"user": {user}, "password": {pass}, "next": {next}}})
	}
	same := map[string]string{"Origin": "https://zachbednarke.com"}

	// Off by default: no form, no endpoint, Basic ignored.
	off := newTestServer(t, baseConfig(t), g, clk)
	if w := post(off, same, "zach", testPassword, "/jazz/"); w.Code != 404 || cookieNamed(w, sessionCookie) != nil {
		t.Fatalf("password accepted while disabled: %d", w.Code)
	}
	if page := do(off, req{path: "/auth/login"}); page.Code != 200 || strings.Contains(page.Body.String(), "/auth/password") || !strings.Contains(page.Body.String(), "Sign in with Google") {
		t.Fatal("login page with password off")
	}

	cfg := baseConfig(t)
	cfg.AllowPassword = true
	on := newTestServer(t, cfg, g, clk)
	page := do(on, req{path: "/auth/login?next=/trumpets/"})
	body := page.Body.String()
	if !strings.Contains(body, "Use password") || !strings.Contains(body, `value="/trumpets/"`) || page.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("login page with password on")
	}
	if w := post(on, map[string]string{"Origin": "https://attacker.example"}, "zach", testPassword, "/jazz/"); w.Code != 403 {
		t.Fatal("cross-site password post accepted")
	}
	if w := post(on, map[string]string{}, "zach", testPassword, "/jazz/"); w.Code != 403 {
		t.Fatal("password post without origin accepted")
	}
	if w := do(on, req{path: "/auth/password"}); w.Code != 405 {
		t.Fatal("password GET")
	}
	w := post(on, map[string]string{"Sec-Fetch-Site": "same-origin"}, "zach", testPassword, "//evil.example")
	if w.Code != 303 || w.Header().Get("Location") != "/jazz/" || cookieNamed(w, sessionCookie) == nil {
		t.Fatalf("password sign-in: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Even with password enabled, /check never challenges or accepts Basic.
	if c := do(on, req{path: "/check", headers: map[string]string{"Authorization": "Basic " + b64.EncodeToString([]byte("zach:"+testPassword))}}); c.Code != 401 || c.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("Basic accepted at /check")
	}
	// Wrong passwords redirect back with a message, and are rate limited.
	h := map[string]string{"Origin": "https://zachbednarke.com", "X-Forwarded-For": "203.0.113.9"}
	for i := 0; i < 5; i++ {
		if w := post(on, h, "zach", "wrong", "/jazz/"); w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "error=password") {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := post(on, h, "zach", testPassword, "/jazz/"); w.Code != 429 || cookieNamed(w, sessionCookie) != nil {
		t.Fatalf("rate limit not applied: %d", w.Code)
	}
	if w := post(on, map[string]string{"Origin": "https://zachbednarke.com", "X-Forwarded-For": "203.0.113.10"}, "nobody", testPassword, "/jazz/"); w.Code != 303 || cookieNamed(w, sessionCookie) != nil {
		t.Fatal("wrong user accepted")
	}
	clk.t = clk.t.Add(16 * time.Minute)
	if w := post(on, h, "zach", testPassword, "/jazz/"); w.Code != 303 || cookieNamed(w, sessionCookie) == nil {
		t.Fatal("rate limit did not reset")
	}
	// Global limit across addresses.
	for i := 0; i < 30; i++ {
		post(on, map[string]string{"Origin": "https://zachbednarke.com", "X-Forwarded-For": "198.51.100." + string(rune('0'+i%10)) + string(rune('0'+i/10))}, "zach", "wrong", "/jazz/")
	}
	if w := post(on, map[string]string{"Origin": "https://zachbednarke.com", "X-Forwarded-For": "192.0.2.1"}, "zach", testPassword, "/jazz/"); w.Code != 429 {
		t.Fatal("global limit not applied")
	}
	// Password-only configuration shows the form open and no Google button.
	pwOnly := config{User: "zach", Hash: cfg.Hash, Key: testKey, AllowPassword: true}
	p := newTestServer(t, pwOnly, nil, clk)
	if b := do(p, req{path: "/auth/login"}).Body.String(); strings.Contains(b, "Sign in with Google") || !strings.Contains(b, "<details open>") {
		t.Fatal("password-only login page")
	}
	if do(p, req{path: "/auth/google/start"}).Code != 404 {
		t.Fatal("Google start without Google config")
	}
}

func TestLoginAndLogout(t *testing.T) {
	g := newFakeGoogle(t)
	clk := &clock{time.Unix(1800000000, 0)}
	s := newTestServer(t, baseConfig(t), g, clk)
	page := do(s, req{path: "/auth/login?next=/trumpets/"})
	h := page.Header()
	if page.Code != 200 || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || h.Get("X-Frame-Options") != "DENY" ||
		h.Get("Referrer-Policy") != "no-referrer" || h.Get("Cache-Control") != "no-store" || strings.Contains(page.Body.String(), "<script") {
		t.Fatalf("login page headers: %v", h)
	}
	if !strings.Contains(page.Body.String(), `href="/auth/google/start?next=%2Ftrumpets%2F"`) {
		t.Fatalf("Google link: %s", page.Body.String())
	}
	if do(s, req{method: "HEAD", path: "/auth/login"}).Code != 200 || do(s, req{method: "POST", path: "/auth/login"}).Code != 405 {
		t.Fatal("login methods")
	}
	if b := do(s, req{path: "/auth/login?error=expired"}).Body.String(); !strings.Contains(b, "took too long") || !strings.Contains(b, `role="alert"`) {
		t.Fatal("error message")
	}
	if b := do(s, req{path: "/auth/login?signed_out=1"}).Body.String(); !strings.Contains(b, "signed out") {
		t.Fatal("signed-out message")
	}
	if b := do(s, req{path: "/auth/login?next=%22%3E%3Cscript%3E"}).Body.String(); strings.Contains(b, "<script>") {
		t.Fatal("next is not escaped")
	}

	_, session := signIn(t, s, g, clk, "/jazz/")
	// GET renders a confirmation and does not sign out.
	confirm := do(s, req{path: "/auth/logout?next=/trumpets/", cookies: []*http.Cookie{session}})
	if confirm.Code != 200 || !strings.Contains(confirm.Body.String(), `method="post" action="/auth/logout"`) || cookieNamed(confirm, sessionCookie) != nil {
		t.Fatal("logout GET")
	}
	if b := do(s, req{path: "/auth/logout"}).Body.String(); !strings.Contains(b, "isn't signed in") {
		t.Fatalf("logout GET signed out: %s", b)
	}
	if w := do(s, req{method: "POST", path: "/auth/logout", headers: map[string]string{"Origin": "https://attacker.example"}, cookies: []*http.Cookie{session}}); w.Code != 403 || cookieNamed(w, sessionCookie) != nil {
		t.Fatal("cross-site logout accepted")
	}
	w := do(s, req{method: "POST", path: "/auth/logout", headers: map[string]string{"Origin": "https://zachbednarke.com"}, cookies: []*http.Cookie{session}})
	cleared := cookieNamed(w, sessionCookie)
	if w.Code != 303 || w.Header().Get("Location") != "/auth/login?signed_out=1" || cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" || !cleared.Secure || cleared.Path != "/" {
		t.Fatalf("logout POST: %d %+v", w.Code, cleared)
	}
	if do(s, req{method: "DELETE", path: "/auth/logout"}).Code != 405 {
		t.Fatal("logout method")
	}
}
