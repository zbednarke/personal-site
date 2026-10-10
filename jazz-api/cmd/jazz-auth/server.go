package main

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Session methods are part of the signed cookie value, so disabling a method
// in the configuration invalidates every cookie issued through it.
const (
	methodGoogle   = "g"
	methodPassword = "p"
)

type server struct {
	cfg      config
	now      func() time.Time
	lifetime time.Duration
	aead     cipher.AEAD
	google   *googleProvider
	limiter  *limiter
	tags     map[string]bool // keyed hashes of AllowedEmails
	logf     func(format string, args ...any)
}

var b64 = base64.RawURLEncoding

func (s *server) mac(parts ...string) []byte {
	m := hmac.New(sha256.New, []byte(s.cfg.Key))
	m.Write([]byte(strings.Join(parts, "|")))
	return m.Sum(nil)
}

// emailTag is a keyed, non-reversible handle for an email address. It binds a
// Google session to the address that signed in (removing the address from
// AllowedEmails signs it out) and is the only form in which emails are logged.
func (s *server) emailTag(email string) string {
	return b64.EncodeToString(s.mac("jazz-email", strings.ToLower(strings.TrimSpace(email))))[:22]
}

// binding ties a cookie to the configuration of the method that issued it:
// changing the password hash ends password sessions, and changing the Google
// client ends Google sessions.
func (s *server) binding(method string) string {
	if method == methodPassword {
		return s.cfg.Hash
	}
	return s.cfg.GoogleClientID
}

// token format: v2.<b64 user>.<method>.<subject tag>.<expiry unix>.<b64 hmac>
func (s *server) token(method, tag string, exp time.Time) string {
	value := "v2." + b64.EncodeToString([]byte(s.cfg.User)) + "." + method + "." + tag + "." + strconv.FormatInt(exp.Unix(), 10)
	return value + "." + b64.EncodeToString(s.mac("jazz-session-v2", method, s.binding(method), value))
}

func (s *server) session(r *http.Request) (time.Time, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return time.Time{}, false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 6 || parts[0] != "v2" {
		return time.Time{}, false
	}
	method, tag := parts[2], parts[3]
	value := strings.Join(parts[:5], ".")
	sig, err := b64.DecodeString(parts[5])
	if err != nil || !hmac.Equal(sig, s.mac("jazz-session-v2", method, s.binding(method), value)) {
		return time.Time{}, false
	}
	switch method {
	case methodGoogle:
		if s.google == nil || !s.tags[tag] {
			return time.Time{}, false
		}
	case methodPassword:
		if !s.cfg.AllowPassword || tag != "-" {
			return time.Time{}, false
		}
	default:
		return time.Time{}, false
	}
	user, err := b64.DecodeString(parts[1])
	if err != nil || string(user) != s.cfg.User {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	exp := time.Unix(sec, 0)
	now := s.now()
	return exp, exp.After(now) && !exp.After(now.Add(s.lifetime))
}

func (s *server) setSession(w http.ResponseWriter, method, tag string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: s.token(method, tag, exp), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: exp, MaxAge: int(exp.Sub(s.now()).Seconds())})
}

func (s *server) newSession(w http.ResponseWriter, method, tag string) {
	s.setSession(w, method, tag, s.now().Add(s.lifetime).Truncate(time.Second))
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// safeNext accepts only same-site relative paths, so a sign-in can never send
// the browser to another site.
func safeNext(next string) string {
	if next == "" || len(next) > 2048 || next[0] != '/' || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\") {
		return defaultNext
	}
	for _, r := range next {
		if r < 0x20 || r == 0x7f {
			return defaultNext
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, "/") ||
		u.Path == "/auth" || strings.HasPrefix(u.Path, "/auth/") {
		return defaultNext
	}
	return next
}

func loginURL(next string) string {
	return "/auth/login?next=" + url.QueryEscape(safeNext(next))
}

// sameOrigin accepts unsafe requests only from this site's own pages.
func (s *server) sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return origin == s.cfg.PublicOrigin
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

func clientIP(r *http.Request) string {
	// Caddy replaces X-Forwarded-For from untrusted clients with the peer
	// address, so its last entry is the browser's address.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/check":
		s.check(w, r)
	case "/auth/login":
		s.loginPage(w, r)
	case "/auth/google/start":
		s.googleStart(w, r)
	case "/auth/google/callback":
		s.googleCallback(w, r)
	case "/auth/password":
		s.password(w, r)
	case "/auth/logout":
		s.logout(w, r)
	default:
		http.NotFound(w, r)
	}
}

// navigation reports whether the protected request was a top-level page load,
// which should go to the sign-in page rather than receive a JSON error.
func navigation(r *http.Request, method string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (s *server) check(w http.ResponseWriter, r *http.Request) {
	method := r.Header.Get("X-Forwarded-Method")
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		origin := r.Header.Get("Origin")
		if (origin != "" && origin != s.cfg.PublicOrigin) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Cross-site request denied", http.StatusForbidden)
			return
		}
	}
	if exp, ok := s.session(r); ok {
		// Re-sending the cookie keeps its original expiry.
		c, _ := r.Cookie(sessionCookie)
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: c.Value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: exp, MaxAge: int(exp.Sub(s.now()).Seconds())})
		w.Header().Set("X-Jazz-User", s.cfg.User)
		w.WriteHeader(http.StatusOK)
		return
	}
	// Never send WWW-Authenticate: browsers must not show the Basic dialog.
	if navigation(r, method) {
		http.Redirect(w, r, loginURL(r.Header.Get("X-Forwarded-Uri")), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":"signin_required","login":"/auth/login"}` + "\n"))
}

func (s *server) password(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowPassword {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.sameOrigin(r) {
		http.Error(w, "Cross-site request denied", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	next := safeNext(r.PostForm.Get("next"))
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		s.logf("password sign-in rate limited")
		w.Header().Set("Retry-After", "900")
		s.render(w, http.StatusTooManyRequests, pageData{Title: "Too many attempts", Message: "Too many password attempts. Wait a few minutes, then try again.", Next: next, Back: true})
		return
	}
	user, pass := r.PostForm.Get("user"), r.PostForm.Get("password")
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.User)) == 1
	passOK := bcrypt.CompareHashAndPassword([]byte(s.cfg.Hash), []byte(pass)) == nil
	if !userOK || !passOK {
		s.limiter.fail(ip)
		s.logf("password sign-in rejected")
		http.Redirect(w, r, "/auth/login?error=password&next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	s.logf("password sign-in accepted")
	s.newSession(w, methodPassword, "-")
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		_, signedIn := s.session(r)
		s.render(w, http.StatusOK, pageData{Title: "Sign out", Logout: true, SignedIn: signedIn, Next: safeNext(r.URL.Query().Get("next"))})
	case http.MethodPost:
		if !s.sameOrigin(r) {
			http.Error(w, "Cross-site request denied", http.StatusForbidden)
			return
		}
		clearCookie(w, sessionCookie)
		clearCookie(w, oauthCookie)
		http.Redirect(w, r, "/auth/login?signed_out=1", http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
