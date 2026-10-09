package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type urlFixture struct {
	Input     string `json:"input"`
	Canonical string `json:"canonical"`
	Provider  string `json:"provider"`
	MediaID   string `json:"mediaId"`
	Format    string `json:"format"`
	Error     bool   `json:"error"`
}

// Shared with assets/trumpets/inspiration-model.test.js for Go/JS parity.
func loadURLFixtures(t *testing.T) []urlFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/inspiration_urls.json")
	if err != nil {
		t.Fatal(err)
	}
	var out []urlFixture
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCanonicalInspirationURLFixtures(t *testing.T) {
	for _, f := range loadURLFixtures(t) {
		canonical, provider, mediaID, format, err := canonicalInspirationURL(f.Input)
		if f.Error {
			if err == nil {
				t.Errorf("%s: expected error, got %s", f.Input, canonical)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", f.Input, err)
			continue
		}
		if canonical != f.Canonical || provider != f.Provider || mediaID != f.MediaID || format != f.Format {
			t.Errorf("%s: got %s %s %s %s want %s %s %s %s", f.Input, canonical, provider, mediaID, format, f.Canonical, f.Provider, f.MediaID, f.Format)
		}
	}
}

func TestCanonicalInspirationKeepsObservatoryIdentity(t *testing.T) {
	// The share-param strips must not leak into Observatory listing identities.
	got, _ := canonicalTrumpetURL("https://dealer.test/x?si=1&is=2")
	if got != "https://dealer.test/x?is=2&si=1" {
		t.Fatal(got)
	}
}

func TestInspirationMediaIDValidators(t *testing.T) {
	for id, ok := range map[string]bool{"qHetQ-t4Wi0": true, "dQw4w9WgXcQ": true, "short": false, "qHetQ-t4Wi0x": false, "qHetQ t4Wi0": false, "<script>abc": false} {
		if youtubeIDPattern.MatchString(id) != ok {
			t.Errorf("youtube %q", id)
		}
	}
	for code, ok := range map[string]bool{"C9xYz12AbCd": true, "abcde": true, "abcd": false, strings.Repeat("a", 41): false, "ab/cd": false} {
		if instagramCodePattern.MatchString(code) != ok {
			t.Errorf("instagram %q", code)
		}
	}
	if e := inspirationEmbedFor("youtube", "qHetQ-t4Wi0", "short"); e == nil || e.Src != "https://www.youtube-nocookie.com/embed/qHetQ-t4Wi0" || e.Aspect != "9:16" {
		t.Fatal(e)
	}
	if e := inspirationEmbedFor("youtube", "dQw4w9WgXcQ", "video"); e.Aspect != "16:9" {
		t.Fatal(e)
	}
	if e := inspirationEmbedFor("instagram", "C9xYz12AbCd", "reel"); e == nil || e.Src != "https://www.instagram.com/reel/C9xYz12AbCd/embed" {
		t.Fatal(e)
	}
	if inspirationEmbedFor("youtube", "bad\"id", "video") != nil || inspirationEmbedFor("reverb", "123", "page") != nil {
		t.Fatal("embed from unvalidated id")
	}
}

func TestNormalizeInspirationTags(t *testing.T) {
	got, err := normalizeInspirationTags([]string{" Raw Brass ", "raw  brass", "", "MBS Technology", "upswept bell"})
	if err != nil || strings.Join(got, "|") != "raw brass|mbs technology|upswept bell" {
		t.Fatal(got, err)
	}
	if _, err = normalizeInspirationTags([]string{strings.Repeat("x", 41)}); err == nil {
		t.Fatal("long tag accepted")
	}
	many := []string{}
	for i := 0; i < 21; i++ {
		many = append(many, "tag"+string(rune('a'+i)))
	}
	if _, err = normalizeInspirationTags(many); err == nil {
		t.Fatal("21 tags accepted")
	}
}

func TestValidateInspirationPatch(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	decode := func(raw string) (inspirationPatch, error) {
		var p inspirationPatch
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return p, err
		}
		return p, validateInspirationPatch(&p, now)
	}
	p, err := decode(`{"expectedRevision":3,"maker":"  Harrelson ","tags":["MBS Technology"],"priceSeen":4200.456,"priceCurrency":"usd","priceSeenOn":"2026-03-14","hornId":null,"priority":"hunting"}`)
	if err != nil || p.Maker.Value != "Harrelson" || p.Tags.Value[0] != "mbs technology" || p.PriceSeen.Value != 4200.46 || p.PriceCurrency.Value != "USD" || !p.HornID.Set || !p.HornID.Null || p.Why.Set {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = decode(`{"expectedRevision":1,"priceSeen":null}`)
	if err != nil || !p.PriceSeen.Set || !p.PriceSeen.Null {
		t.Fatal("null price must clear", err)
	}
	for _, bad := range []string{
		`{"maker":"x"}`,
		`{"expectedRevision":1,"priority":"someday want"}`,
		`{"expectedRevision":1,"priceCurrency":"US"}`,
		`{"expectedRevision":1,"priceCurrency":"usd1"}`,
		`{"expectedRevision":1,"priceSeen":-1}`,
		`{"expectedRevision":1,"priceSeen":1000000.01}`,
		`{"expectedRevision":1,"priceSeenOn":"2026-10-14"}`,
		`{"expectedRevision":1,"priceSeenOn":"2026-02-30"}`,
		`{"expectedRevision":1,"maker":"` + strings.Repeat("m", 81) + `"}`,
		`{"expectedRevision":1,"model":"` + strings.Repeat("m", 121) + `"}`,
		`{"expectedRevision":1,"why":"` + strings.Repeat("w", 4001) + `"}`,
		`{"expectedRevision":1,"url":"javascript:alert(1)"}`,
		`{"expectedRevision":1,"hornId":"not-a-uuid"}`,
		`{"expectedRevision":1,"unknown":true}`,
	} {
		if _, err := decode(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	p, err = decode(`{"expectedRevision":1,"url":"https://youtu.be/qHetQ-t4Wi0?si=x"}`)
	if err != nil || p.canonical != "https://youtube.com/watch?v=qHetQ-t4Wi0" || p.mediaID != "qHetQ-t4Wi0" {
		t.Fatal(p.canonical, err)
	}
	if _, err = decode(`{"expectedRevision":1,"priceSeenOn":"2026-10-10"}`); err != nil {
		t.Fatal("local-date slack", err)
	}
}

const productFixture = `<!doctype html><html><head>
<title>Fallback &amp; title</title>
<meta property="og:title" content="Harrelson MUSE &lt;b&gt;raw&lt;/b&gt; brass">
<meta property="og:site_name" content="Test Dealer">
<meta property="og:description" content="A   one-off build.">
<meta property="og:image" content="/img/muse.png">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebPage"},{"@type":["Product"],"name":"MUSE","offers":[{"@type":"Offer","price":"4,250.00","priceCurrency":"usd"}]}]}</script>
</head><body><script>var x = "<meta property='og:title' content='nope'>";</script></body></html>`

func TestParsePageMetadata(t *testing.T) {
	base, _ := url.Parse("https://dealer.test/horns/muse")
	m := parsePageMetadata([]byte(productFixture), base)
	if m.Title != "Harrelson MUSE <b>raw</b> brass" || m.SiteName != "Test Dealer" || m.Description != "A one-off build." || m.Image != "https://dealer.test/img/muse.png" {
		t.Fatalf("%+v", m)
	}
	if m.Price == nil || *m.Price != 4250 || m.Currency != "USD" {
		t.Fatalf("price %+v", m)
	}
	// Offers as an object; Twitter image fallback; <title> fallback.
	m = parsePageMetadata([]byte(`<title> Plain   title </title><meta name="twitter:image" content="https://cdn.test/a.jpg">
<script type="application/ld+json">[{"@type":"Product","offers":{"price":3100,"priceCurrency":"EUR"},"image":["https://cdn.test/b.jpg"]}]</script>`), base)
	if m.Title != "Plain title" || m.Image != "https://cdn.test/a.jpg" || m.Price == nil || *m.Price != 3100 || m.Currency != "EUR" {
		t.Fatalf("%+v", m)
	}
	// product:price meta tags.
	m = parsePageMetadata([]byte(`<meta property="og:title" content="Horn"><meta property="product:price:amount" content="999.5"><meta property="product:price:currency" content="GBP"><meta property="og:image" content="javascript:alert(1)">`), base)
	if m.Price == nil || *m.Price != 999.5 || m.Currency != "GBP" || m.Image != "" {
		t.Fatalf("%+v", m)
	}
	// Login walls never yield their titles.
	for _, page := range []string{
		`<title>Login • Instagram</title>`,
		`<meta property="og:title" content="Instagram"><title>Instagram</title>`,
		`<meta property="og:title" content="Log in to Facebook">`,
	} {
		if !isLoginWall(parsePageMetadata([]byte(page), base)) {
			t.Errorf("login wall missed: %s", page)
		}
	}
	if isLoginWall(parsePageMetadata([]byte(`<meta property="og:title" content="Harrelson Trumpets on Instagram: &quot;MUSE&quot;">`), base)) {
		t.Error("real post treated as login wall")
	}
}

type fakeResolver map[string][]string

func (f fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	out := []net.IPAddr{}
	for _, s := range f[host] {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	if len(out) == 0 {
		return nil, errors.New("no such host")
	}
	return out, nil
}

func TestSafeDialerRejectsInternalAddresses(t *testing.T) {
	resolver := fakeResolver{}
	for i, ip := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.5.4", "172.31.255.255", "192.168.1.1", "169.254.169.254", "100.64.0.1", "100.127.255.254", "fc00::1", "fd12::1", "fe80::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1"} {
		host := "internal" + string(rune('a'+i)) + ".test"
		resolver[host] = []string{ip}
		d := &safeDialer{resolver: resolver}
		if _, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort(host, "443")); !errors.Is(err, errBlockedAddress) {
			t.Errorf("%s (%s): %v", host, ip, err)
		}
		if _, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort(ip, "80")); !errors.Is(err, errBlockedAddress) {
			t.Errorf("literal %s: %v", ip, err)
		}
	}
	// Mixed answers are rejected outright (no fallback to a private address).
	resolver["mixed.test"] = []string{"93.184.216.34", "10.0.0.1"}
	d := &safeDialer{resolver: resolver}
	if _, err := d.DialContext(context.Background(), "tcp", "mixed.test:443"); !errors.Is(err, errBlockedAddress) {
		t.Error(err)
	}
	if _, err := d.DialContext(context.Background(), "tcp", "public.test:8080"); err == nil || !strings.Contains(err.Error(), "blocked port") {
		t.Error("non-web port allowed", err)
	}
	for _, ip := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "8.8.8.8"} {
		if blockedIP(net.ParseIP(ip)) {
			t.Errorf("public %s blocked", ip)
		}
	}
}

func TestSafeClientRevalidatesRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
		switch r.URL.Path {
		case "/to-private":
			http.Redirect(w, r, "http://private.test:"+port+"/ok", http.StatusFound)
		case "/to-metadata":
			http.Redirect(w, r, "http://169.254.169.254/computeMetadata/v1/", http.StatusFound)
		case "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/two":
			http.Redirect(w, r, "/one", http.StatusFound)
		case "/one":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/big":
			w.Write(bytes.Repeat([]byte("a"), inspirationHTMLLimit+1))
		case "/exact":
			w.Write(bytes.Repeat([]byte("a"), inspirationHTMLLimit))
		default:
			if r.Header.Get("User-Agent") != inspirationUserAgent || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
				w.WriteHeader(400)
			}
			io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	client := newSafeHTTPClient(fakeResolver{"public.test": {"127.0.0.1"}, "private.test": {"10.9.8.7"}}, true)
	base := "http://public.test:" + port
	ctx := context.Background()
	if body, _, _, err := fetchLimited(ctx, client, base+"/two", inspirationHTMLLimit, "*/*"); err != nil || string(body) != "ok" {
		t.Fatal("redirects within limit", err)
	}
	for _, path := range []string{"/to-private", "/to-metadata"} {
		if _, _, _, err := fetchLimited(ctx, client, base+path, inspirationHTMLLimit, "*/*"); !errors.Is(err, errBlockedAddress) {
			t.Errorf("%s: %v", path, err)
		}
	}
	for _, path := range []string{"/to-file", "/loop", "/big"} {
		if _, _, _, err := fetchLimited(ctx, client, base+path, inspirationHTMLLimit, "*/*"); err == nil {
			t.Errorf("%s allowed", path)
		}
	}
	if _, _, _, err := fetchLimited(ctx, client, base+"/exact", inspirationHTMLLimit, "*/*"); err != nil {
		t.Error("1 MB body rejected", err)
	}
	if _, _, _, err := fetchLimited(ctx, client, base+"/big", inspirationImageLimit, "*/*"); err != nil {
		t.Error("image cap is 5 MB", err)
	}
	// Production policy: loopback and non-web ports are refused.
	strict := newSafeHTTPClient(fakeResolver{"public.test": {"127.0.0.1"}}, false)
	if _, _, _, err := fetchLimited(ctx, strict, base+"/ok", inspirationHTMLLimit, "*/*"); err == nil {
		t.Error("strict client reached loopback test server")
	}
	if _, _, _, err := fetchLimited(ctx, strict, "http://localhost/", inspirationHTMLLimit, "*/*"); err == nil {
		t.Error("localhost allowed")
	}
}

func pngFixture(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(150 + x%80), uint8(110 + y%60), 60, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSniffInspirationImage(t *testing.T) {
	p := pngFixture(t, 4, 3)
	img, err := sniffInspirationImage(p, "image/png")
	if err != nil || img.ContentType != "image/png" || *img.Width != 4 || *img.Height != 3 {
		t.Fatal(img, err)
	}
	if _, err = sniffInspirationImage(p, "image/jpeg"); err == nil {
		t.Fatal("declared/sniffed mismatch accepted")
	}
	if _, err = sniffInspirationImage([]byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>"), "image/svg+xml"); err == nil {
		t.Fatal("svg accepted")
	}
	if _, err = sniffInspirationImage([]byte("<html><script>alert(1)</script>"), "image/png"); err == nil {
		t.Fatal("html accepted")
	}
	// 13000 x 1 GIF header: the dimension cap is enforced before decoding pixels.
	huge := append([]byte("GIF89a"), 0xC8, 0x32, 0x01, 0x00, 0x00, 0x00, 0x00, 0x3B)
	if _, err = sniffInspirationImage(huge, "image/gif"); err == nil || !strings.Contains(err.Error(), "12000") {
		t.Fatal("oversized image accepted", err)
	}
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 24)...)
	if img, err = sniffInspirationImage(webp, "image/webp"); err != nil || img.Width != nil {
		t.Fatal("webp dimensions come from the client", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func cannedClient(pages map[string]string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		key := r.URL.Scheme + "://" + r.URL.Host + r.URL.Path
		body, ok := pages[key]
		if !ok {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"text/html"}}, Request: r}, nil
	})}
}

func TestEnrichLinkProviders(t *testing.T) {
	ctx := context.Background()
	out := enrichLink(ctx, cannedClient(map[string]string{
		"https://www.youtube.com/oembed": `{"title":"1st Harrelson Muse with MBS Technology","author_name":"Harrelson Trumpets + Rumors & Dreams","thumbnail_url":"https://i.ytimg.com/vi/qHetQ-t4Wi0/hq2.jpg"}`,
	}), "https://youtube.com/shorts/qHetQ-t4Wi0", "youtube", "qHetQ-t4Wi0", "short")
	if out.Status != "fetched" || out.Title != "1st Harrelson Muse with MBS Technology" || out.AuthorName != "Harrelson Trumpets + Rumors & Dreams" || out.SiteName != "YouTube" || out.ImageURL != "https://i.ytimg.com/vi/qHetQ-t4Wi0/hq2.jpg" {
		t.Fatalf("%+v", out)
	}
	// Private/deleted video: oEmbed fails, fall back to hqdefault.jpg.
	out = enrichLink(ctx, cannedClient(nil), "https://youtube.com/watch?v=dQw4w9WgXcQ", "youtube", "dQw4w9WgXcQ", "video")
	if out.Status != "partial" || out.ImageURL != "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg" || out.Title != "" {
		t.Fatalf("%+v", out)
	}
	out = enrichLink(ctx, cannedClient(map[string]string{"https://instagram.com/reel/C9xYz12AbCd": `<html><head><title>Login • Instagram</title></head></html>`}), "https://instagram.com/reel/C9xYz12AbCd", "instagram", "C9xYz12AbCd", "reel")
	if out.Status != "partial" || out.Title != "Instagram reel" || strings.Contains(strings.ToLower(out.Title), "login") {
		t.Fatalf("%+v", out)
	}
	out = enrichLink(ctx, cannedClient(map[string]string{"https://dealer.test/muse": productFixture}), "https://dealer.test/muse", "web", "", "page")
	if out.Status != "fetched" || out.Price == nil || *out.Price != 4250 || out.ImageURL != "https://dealer.test/img/muse.png" {
		t.Fatalf("%+v", out)
	}
	blocked := newSafeHTTPClient(fakeResolver{"intranet.test": {"10.0.0.5"}}, false)
	out = enrichLink(ctx, blocked, "https://intranet.test/x", "web", "", "page")
	if out.Status != "failed" || out.Error != "blocked address" {
		t.Fatalf("%+v", out)
	}
}

func TestTrumpetBodyTypeAllowsOnlyImageUploads(t *testing.T) {
	for _, c := range []struct {
		method, path, ct string
		ok               bool
	}{
		{"POST", "/v1/trumpets/inspiration", "application/json", true},
		{"POST", "/v1/trumpets/inspiration/x/images", "image/png", true},
		{"POST", "/v1/trumpets/inspiration/x/images", "image/svg+xml", false},
		{"POST", "/v1/trumpets/inspiration", "image/png", false},
		{"POST", "/v1/trumpets/seed", "text/plain", false},
		{"PATCH", "/v1/trumpets/inspiration/x/images", "image/png", false},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Header.Set("Content-Type", c.ct)
		if trumpetBodyTypeAllowed(r) != c.ok {
			t.Errorf("%+v", c)
		}
	}
}

func TestTrumpetSearchProfileInspirations(t *testing.T) {
	p := trumpetSearchProfile(nil, []map[string]any{{"maker": "Harrelson", "model": "MUSE", "tags": []string{"mbs technology"}, "priority": "someday", "why": "Some day"}})
	feed := p["inspirations"].([]map[string]any)
	if len(feed) != 1 || feed[0]["maker"] != "Harrelson" {
		t.Fatal(feed)
	}
	if _, ok := feed[0]["url"]; ok {
		t.Fatal("URLs must not reach the profile")
	}
	if len(trumpetSearchProfile(nil)["inspirations"].([]map[string]any)) != 0 {
		t.Fatal("default feed should be empty")
	}
}
