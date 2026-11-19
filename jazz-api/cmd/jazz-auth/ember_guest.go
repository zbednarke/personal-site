package main

import (
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	methodEmberGuest  = "e"
	emberGuestCookie  = "__Host-ember-guest"
	emberGuestPageURL = "/686/"
)

func emberGuestPath(uri string) string {
	u, err := url.ParseRequestURI(uri)
	if err != nil || u.IsAbs() || u.RawPath != "" || strings.ContainsAny(u.Path, "\\%") {
		return ""
	}
	if strings.TrimSuffix(u.Path, "/") != path.Clean(u.Path) {
		return ""
	}
	return u.Path
}

func emberGuestPage(uri string) bool {
	p := emberGuestPath(uri)
	return p == "/686" || p == emberGuestPageURL
}

func emberGuestAccess(method, uri string) bool {
	p := emberGuestPath(uri)
	switch method {
	case http.MethodGet, http.MethodHead:
		return emberGuestPage(uri) || strings.HasPrefix(p, "/assets/686/")
	case http.MethodPost:
		switch p {
		case "/686/api/v1/686/transcribe", "/686/api/v1/686/respond", "/686/api/v1/686/speak":
			return true
		}
	}
	return false
}

func (s *server) emberGuestLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EmberGuestHash == "" {
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
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		w.Header().Set("Retry-After", "900")
		s.render(w, http.StatusTooManyRequests, pageData{Title: "Too many attempts", Message: "Too many password attempts. Wait a few minutes, then try again.", Next: emberGuestPageURL, Back: true})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(s.cfg.EmberGuestHash), []byte(r.PostForm.Get("password"))) != nil {
		s.limiter.fail(ip)
		http.Redirect(w, r, "/auth/login?error=guest&next=%2F686%2F", http.StatusSeeOther)
		return
	}
	const lifetime = 12 * time.Hour
	exp := s.now().Add(lifetime).Truncate(time.Second)
	http.SetCookie(w, &http.Cookie{Name: emberGuestCookie, Value: s.token(methodEmberGuest, "-", exp), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: exp, MaxAge: int(lifetime.Seconds())})
	http.Redirect(w, r, emberGuestPageURL, http.StatusSeeOther)
}
