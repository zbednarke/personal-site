package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const cookieName = "__Host-jazz-session"
const lifetime = 7 * 24 * time.Hour

type config struct {
	User string
	Hash string
	Key  string
}
type auth struct {
	config
	now func() time.Time
}

func (a auth) signature(value string) string {
	mac := hmac.New(sha256.New, []byte(a.Key))
	mac.Write([]byte(a.Hash + "|" + value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (a auth) token(exp time.Time) string {
	value := base64.RawURLEncoding.EncodeToString([]byte(a.User)) + "." + strconv.FormatInt(exp.Unix(), 10)
	return value + "." + a.signature(value)
}
func (a auth) valid(value string) (time.Time, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	expected := a.signature(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return time.Time{}, false
	}
	user, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || string(user) != a.User {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(parts[1], 10, 64)
	exp := time.Unix(sec, 0)
	return exp, err == nil && exp.After(a.now()) && !exp.After(a.now().Add(lifetime))
}
func (a auth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/check" {
		http.NotFound(w, r)
		return
	}
	method := r.Header.Get("X-Forwarded-Method")
	if method != "" && method != "GET" && method != "HEAD" && method != "OPTIONS" {
		origin := r.Header.Get("Origin")
		if (origin != "" && origin != "https://zachbednarke.com") || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "Cross-site request denied", http.StatusForbidden)
			return
		}
	}
	var exp time.Time
	ok := false
	if c, err := r.Cookie(cookieName); err == nil {
		exp, ok = a.valid(c.Value)
	}
	if !ok {
		user, password, present := r.BasicAuth()
		if !present || user != a.User || bcrypt.CompareHashAndPassword([]byte(a.Hash), []byte(password)) != nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="restricted", charset="UTF-8"`)
			http.Error(w, "Sign in to the Jazz Project", http.StatusUnauthorized)
			return
		}
		exp = a.now().Add(lifetime).Truncate(time.Second)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: a.token(exp), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: exp, MaxAge: int(exp.Sub(a.now()).Seconds())})
	w.Header().Set("X-Jazz-User", a.User)
	w.WriteHeader(http.StatusOK)
}
func main() {
	path := os.Getenv("JAZZ_AUTH_CONFIG")
	if path == "" {
		path = "/etc/jazz-auth.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatal("Cannot read auth configuration")
	}
	var c config
	if json.Unmarshal(data, &c) != nil || c.User == "" || len(c.Key) < 32 {
		log.Fatal("Invalid auth configuration")
	}
	if _, err := bcrypt.Cost([]byte(c.Hash)); err != nil {
		log.Fatal("Invalid password hash")
	}
	server := &http.Server{Addr: "127.0.0.1:8768", Handler: auth{c, time.Now}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	fmt.Println("Jazz seven-day authentication listening on loopback")
	log.Fatal(server.ListenAndServe())
}
