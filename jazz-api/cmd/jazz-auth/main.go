// Command jazz-auth is the loopback sign-in service for the private paths of
// zachbednarke.com. Caddy asks it to /check every private request and proxies
// /auth/* to it directly. People sign in with Google (OpenID Connect,
// authorization code + PKCE); a password form is available only when
// AllowPassword is enabled. A successful sign-in issues a signed session
// cookie for the single configured API user.
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie      = "__Host-jazz-session"
	oauthCookie        = "__Host-jazz-oauth"
	defaultOrigin      = "https://zachbednarke.com"
	defaultSessionDays = 30
	defaultNext        = "/jazz/"
	listenAddr         = "127.0.0.1:8768"
)

// config is /etc/jazz-auth.json. User, Hash and Key predate Google sign-in and
// are preserved by the installers.
type config struct {
	User               string   // API identity forwarded as X-Jazz-User
	Hash               string   `json:",omitempty"` // bcrypt hash; used only when AllowPassword
	Key                string   // session signing key; rotating it signs everyone out
	GoogleClientID     string   `json:",omitempty"`
	GoogleClientSecret string   `json:",omitempty"`
	AllowedEmails      []string `json:",omitempty"`
	LoginHint          string   `json:",omitempty"` // optional; defaults to the only allowed email
	PublicOrigin       string   `json:",omitempty"`
	SessionDays        int      `json:",omitempty"`
	AllowPassword      bool
}

func (c config) googleEnabled() bool { return c.GoogleClientID != "" }

// normalize fills defaults and refuses unsafe or half-finished configuration.
func (c *config) normalize() error {
	if strings.TrimSpace(c.User) == "" {
		return errors.New("User is required")
	}
	if len(c.Key) < 32 {
		return errors.New("Key must be at least 32 characters")
	}
	if c.PublicOrigin == "" {
		c.PublicOrigin = defaultOrigin
	}
	origin, err := url.Parse(c.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
		(origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
		return errors.New("PublicOrigin must be an https origin such as https://zachbednarke.com")
	}
	c.PublicOrigin = "https://" + origin.Host
	switch {
	case c.SessionDays == 0:
		c.SessionDays = defaultSessionDays
	case c.SessionDays < 1 || c.SessionDays > 400:
		return errors.New("SessionDays must be between 1 and 400")
	}
	partial := c.GoogleClientID != "" || c.GoogleClientSecret != "" || len(c.AllowedEmails) > 0
	if partial {
		if c.GoogleClientID == "" || c.GoogleClientSecret == "" || len(c.AllowedEmails) == 0 {
			return errors.New("Google sign-in is half-configured: GoogleClientID, GoogleClientSecret and AllowedEmails are all required")
		}
		if !strings.HasSuffix(c.GoogleClientID, ".apps.googleusercontent.com") {
			return errors.New("GoogleClientID does not look like a Google OAuth client ID")
		}
		seen := map[string]bool{}
		var emails []string
		for _, e := range c.AllowedEmails {
			e = strings.ToLower(strings.TrimSpace(e))
			at := strings.IndexByte(e, '@')
			if at < 1 || at != strings.LastIndexByte(e, '@') || at == len(e)-1 || strings.ContainsAny(e, " \t\r\n") {
				return errors.New("AllowedEmails contains an invalid address")
			}
			if !seen[e] {
				seen[e] = true
				emails = append(emails, e)
			}
		}
		c.AllowedEmails = emails
		if c.LoginHint == "" && len(emails) == 1 {
			c.LoginHint = emails[0]
		}
	}
	if c.AllowPassword {
		if _, err := bcrypt.Cost([]byte(c.Hash)); err != nil {
			return errors.New("AllowPassword requires a valid bcrypt Hash")
		}
	}
	if !c.googleEnabled() && !c.AllowPassword {
		return errors.New("no sign-in method is enabled: configure Google or set AllowPassword")
	}
	return nil
}

func loadConfig(path string) (config, error) {
	var c config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, errors.New("cannot read auth configuration")
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, errors.New("auth configuration is not valid JSON")
	}
	return c, c.normalize()
}

// newServer builds the handler. endpoints and client are injectable for tests.
func newServer(c config, endpoints googleEndpoints, client *http.Client, now func() time.Time) (*server, error) {
	if err := c.normalize(); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(derive(c.Key, "oauth-cookie"))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &server{
		cfg:      c,
		now:      now,
		lifetime: time.Duration(c.SessionDays) * 24 * time.Hour,
		aead:     aead,
		limiter:  newLimiter(now),
		tags:     map[string]bool{},
		logf:     log.Printf,
	}
	if c.googleEnabled() {
		for _, e := range c.AllowedEmails {
			s.tags[s.emailTag(e)] = true
		}
		s.google = &googleProvider{
			clientID:    c.GoogleClientID,
			secret:      c.GoogleClientSecret,
			redirectURI: c.PublicOrigin + "/auth/google/callback",
			endpoints:   endpoints,
			client:      client,
			now:         now,
		}
	}
	return s, nil
}

// derive returns a 32-byte subkey of the signing key for one purpose.
func derive(key, purpose string) []byte {
	sum := sha256.Sum256([]byte("jazz-auth|" + purpose + "|" + key))
	return sum[:]
}

func main() {
	check := flag.Bool("check-config", false, "validate the configuration and exit")
	flag.Parse()
	path := os.Getenv("JAZZ_AUTH_CONFIG")
	if path == "" {
		path = "/etc/jazz-auth.json"
	}
	c, err := loadConfig(path)
	if err != nil {
		log.Fatalf("Invalid auth configuration: %v", err)
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 8 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          4,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	s, err := newServer(c, googleDefaults, client, time.Now)
	if err != nil {
		log.Fatalf("Invalid auth configuration: %v", err)
	}
	if *check {
		fmt.Printf("Configuration valid: google=%t password=%t sessionDays=%d\n", c.googleEnabled(), c.AllowPassword, c.SessionDays)
		return
	}
	server := &http.Server{Addr: listenAddr, Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	log.Printf("jazz-auth listening on loopback: google=%t password=%t sessionDays=%d", c.googleEnabled(), c.AllowPassword, c.SessionDays)
	log.Fatal(server.ListenAndServe())
}
