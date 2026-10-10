package main

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type googleEndpoints struct {
	Auth, Token, JWKS string
}

var googleDefaults = googleEndpoints{
	Auth:  "https://accounts.google.com/o/oauth2/v2/auth",
	Token: "https://oauth2.googleapis.com/token",
	JWKS:  "https://www.googleapis.com/oauth2/v3/certs",
}

const (
	clockSkew       = 2 * time.Minute
	oauthLifetime   = 10 * time.Minute
	jwksMinRefetch  = 30 * time.Second
	jwksDefaultTTL  = time.Hour
	jwksMaxTTL      = 24 * time.Hour
	maxResponseSize = 1 << 20
)

var (
	errNotAllowed = errors.New("email is not allowed")
	errUnknownKey = errors.New("unknown signing key")
)

type googleProvider struct {
	clientID, secret, redirectURI string
	endpoints                     googleEndpoints
	client                        *http.Client
	now                           func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	expires   time.Time
	lastFetch time.Time
}

// --- short-lived sign-in state (state, nonce, PKCE verifier) ---

type oauthState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	Expires  int64  `json:"e"`
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b64.EncodeToString(b)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return b64.EncodeToString(sum[:])
}

// sealState encrypts and authenticates the sign-in state with a key derived
// from the signing key, so the PKCE verifier never appears in clear text.
func (s *server) sealState(st oauthState) string {
	plain, _ := json.Marshal(st)
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return b64.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte(oauthCookie)))
}

func (s *server) openState(value string) (oauthState, bool) {
	var st oauthState
	raw, err := b64.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return st, false
	}
	plain, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], []byte(oauthCookie))
	if err != nil || json.Unmarshal(plain, &st) != nil {
		return st, false
	}
	if st.State == "" || st.Nonce == "" || st.Verifier == "" || !s.now().Before(time.Unix(st.Expires, 0)) {
		return st, false
	}
	return st, true
}

func (s *server) googleStart(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	st := oauthState{State: randomToken(), Nonce: randomToken(), Verifier: randomToken(), Next: safeNext(q.Get("next")), Expires: s.now().Add(oauthLifetime).Unix()}
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: s.sealState(st), Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(oauthLifetime.Seconds())})
	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {s.google.clientID},
		"redirect_uri":          {s.google.redirectURI},
		"scope":                 {"openid email"},
		"state":                 {st.State},
		"nonce":                 {st.Nonce},
		"code_challenge":        {pkceChallenge(st.Verifier)},
		"code_challenge_method": {"S256"},
	}
	// Ask Google to show the account chooser only when the person asked to
	// switch accounts; otherwise the configured hint makes sign-in one tap.
	if q.Get("switch") == "1" {
		params.Set("prompt", "select_account")
	} else if s.cfg.LoginHint != "" {
		params.Set("login_hint", s.cfg.LoginHint)
	}
	http.Redirect(w, r, s.google.endpoints.Auth+"?"+params.Encode(), http.StatusFound)
}

func (s *server) googleCallback(w http.ResponseWriter, r *http.Request) {
	if s.google == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clearCookie(w, oauthCookie)
	fail := func(code, next string) {
		http.Redirect(w, r, "/auth/login?error="+code+"&next="+url.QueryEscape(safeNext(next)), http.StatusSeeOther)
	}
	c, err := r.Cookie(oauthCookie)
	if err != nil {
		s.logf("google sign-in: no sign-in state (expired or another tab)")
		fail("expired", "")
		return
	}
	st, ok := s.openState(c.Value)
	if !ok {
		s.logf("google sign-in: sign-in state invalid or expired")
		fail("expired", "")
		return
	}
	q := r.URL.Query()
	if !hmac.Equal([]byte(q.Get("state")), []byte(st.State)) {
		s.logf("google sign-in: state mismatch")
		fail("expired", st.Next)
		return
	}
	if e := q.Get("error"); e != "" {
		s.logf("google sign-in: provider returned an error")
		if e == "access_denied" {
			fail("denied", st.Next)
		} else {
			fail("failed", st.Next)
		}
		return
	}
	code := q.Get("code")
	if code == "" {
		fail("failed", st.Next)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	idToken, err := s.google.exchange(ctx, code, st.Verifier)
	if err != nil {
		s.logf("google sign-in: code exchange failed: %v", err)
		fail("failed", st.Next)
		return
	}
	email, err := s.verifyIDToken(ctx, idToken, st.Nonce)
	if errors.Is(err, errNotAllowed) {
		s.logf("google sign-in: account not allowed (email %s)", s.emailTag(email)[:8])
		fail("not_allowed", st.Next)
		return
	}
	if err != nil {
		s.logf("google sign-in: ID token rejected: %v", err)
		fail("failed", st.Next)
		return
	}
	tag := s.emailTag(email)
	s.logf("google sign-in accepted (email %s)", tag[:8])
	s.newSession(w, methodGoogle, tag)
	http.Redirect(w, r, st.Next, http.StatusSeeOther)
}

// --- token endpoint ---

func (g *googleProvider) exchange(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {g.redirectURI},
		"client_id":     {g.clientID},
		"client_secret": {g.secret},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoints.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return "", errors.New("token endpoint unreachable")
	}
	defer resp.Body.Close()
	var body struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&body); err != nil {
		return "", fmt.Errorf("token endpoint returned %d with an unreadable body", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		// The error code is a fixed vocabulary (e.g. invalid_grant), safe to log.
		return "", fmt.Errorf("token endpoint returned %d (%.40s)", resp.StatusCode, body.Error)
	}
	if body.IDToken == "" {
		return "", errors.New("token response has no id_token")
	}
	return body.IDToken, nil
}

// --- signing keys ---

func cacheTTL(header string) time.Duration {
	for _, d := range strings.Split(header, ",") {
		d = strings.TrimSpace(strings.ToLower(d))
		if v, ok := strings.CutPrefix(d, "max-age="); ok {
			if n, err := strconv.Atoi(v); err == nil {
				ttl := time.Duration(n) * time.Second
				return min(max(ttl, time.Minute), jwksMaxTTL)
			}
		}
	}
	return jwksDefaultTTL
}

func (g *googleProvider) fetchKeys(ctx context.Context) error {
	g.lastFetch = g.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.endpoints.JWKS, nil)
	if err != nil {
		return err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return errors.New("signing keys unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("signing keys returned %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty, Kid, Use, Alg, N, E string
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&set); err != nil {
		return errors.New("signing keys unreadable")
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		n, errN := b64.DecodeString(k.N)
		e, errE := b64.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < 2048 || pub.E < 3 || pub.E%2 == 0 {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("signing key set is empty")
	}
	g.keys = keys
	g.expires = g.now().Add(cacheTTL(resp.Header.Get("Cache-Control")))
	return nil
}

// key returns the signing key for kid, refreshing the cached set when it has
// expired or when Google has rotated to a key we have not seen yet.
func (g *googleProvider) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	fresh := g.keys != nil && now.Before(g.expires)
	if k, ok := g.keys[kid]; ok && fresh {
		return k, nil
	}
	if !fresh || now.Sub(g.lastFetch) >= jwksMinRefetch {
		if err := g.fetchKeys(ctx); err != nil {
			return nil, err
		}
	}
	if k, ok := g.keys[kid]; ok {
		return k, nil
	}
	return nil, errUnknownKey
}

// --- ID token ---

type idClaims struct {
	Iss           string          `json:"iss"`
	Aud           json.RawMessage `json:"aud"`
	Azp           string          `json:"azp"`
	Exp           json.Number     `json:"exp"`
	Iat           json.Number     `json:"iat"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
}

func (c idClaims) audienceOK(clientID string) bool {
	var one string
	if json.Unmarshal(c.Aud, &one) == nil {
		return one == clientID
	}
	var many []string
	if json.Unmarshal(c.Aud, &many) != nil {
		return false
	}
	for _, a := range many {
		if a == clientID {
			return c.Azp == clientID
		}
	}
	return false
}

func (c idClaims) verified() bool {
	s := strings.Trim(string(c.EmailVerified), `"`)
	return s == "true"
}

func unixClaim(n json.Number) (time.Time, bool) {
	f, err := n.Float64()
	if err != nil || f <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// verifyIDToken checks the RS256 signature and claims and returns the email.
// The email is returned alongside errNotAllowed so the caller can log a tag.
func (s *server) verifyIDToken(ctx context.Context, raw, nonce string) (string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed token")
	}
	var header struct{ Alg, Kid, Typ string }
	hb, err := b64.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &header) != nil {
		return "", errors.New("malformed header")
	}
	if header.Alg != "RS256" || header.Kid == "" {
		return "", errors.New("unexpected signing algorithm")
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("malformed signature")
	}
	pub, err := s.google.key(ctx, header.Kid)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) != nil {
		return "", errors.New("bad signature")
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("malformed payload")
	}
	var c idClaims
	dec := json.NewDecoder(strings.NewReader(string(pb)))
	dec.UseNumber()
	if dec.Decode(&c) != nil {
		return "", errors.New("malformed claims")
	}
	if c.Iss != "https://accounts.google.com" && c.Iss != "accounts.google.com" {
		return "", errors.New("wrong issuer")
	}
	if !c.audienceOK(s.cfg.GoogleClientID) {
		return "", errors.New("wrong audience")
	}
	now := s.now()
	exp, ok := unixClaim(c.Exp)
	if !ok || !now.Before(exp.Add(clockSkew)) {
		return "", errors.New("token expired")
	}
	iat, ok := unixClaim(c.Iat)
	if !ok || iat.After(now.Add(clockSkew)) {
		return "", errors.New("token issued in the future")
	}
	if c.Nonce == "" || !hmac.Equal([]byte(c.Nonce), []byte(nonce)) {
		return "", errors.New("nonce mismatch")
	}
	if !c.verified() {
		return "", errors.New("email not verified")
	}
	email := strings.ToLower(strings.TrimSpace(c.Email))
	if email == "" {
		return "", errors.New("token has no email")
	}
	for _, allowed := range s.cfg.AllowedEmails {
		if strings.EqualFold(allowed, email) {
			return email, nil
		}
	}
	return email, errNotAllowed
}
