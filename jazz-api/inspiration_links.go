package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Link classification is mirrored in assets/trumpets/inspiration-model.js and
// both suites share jazz-api/testdata/inspiration_urls.json.
var (
	youtubeIDPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	instagramCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{5,40}$`)
	tiktokIDPattern      = regexp.MustCompile(`^[0-9]{5,30}$`)
	ebayHostPattern      = regexp.MustCompile(`^(?:m\.)?ebay\.[a-z]{2,3}(?:\.[a-z]{2})?$`)
	ebayItemPattern      = regexp.MustCompile(`^[0-9]{6,20}$`)
	reverbItemPattern    = regexp.MustCompile(`^([0-9]+)(?:-.*)?$`)
	currencyPattern      = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Share-sheet tracking parameters. Deliberately not added to
// canonicalTrumpetURL: that would change Observatory listing identities.
var genericShareParams = []string{"si", "is", "igsh", "igshid", "mibextid", "mc_cid", "mc_eid", "_ga", "yclid", "msclkid"}
var tiktokShareParams = []string{"_r", "_t", "is_from_webapp", "sender_device", "is_copy_url"}
var facebookShareParams = []string{"mibextid", "rdid", "share_url"}

func stripParams(u *url.URL, names []string) {
	q := u.Query()
	for k := range q {
		if containsString(names, strings.ToLower(k)) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
}

func pathSegments(p string) []string {
	out := []string{}
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cleanSourceURL keeps the link as entered but without tracking parameters.
func cleanSourceURL(raw string) string {
	base, err := canonicalTrumpetURL(raw)
	if err != nil || base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	names := []string{"img_index", "feature", "pp"}
	names = append(names, genericShareParams...)
	names = append(names, tiktokShareParams...)
	names = append(names, facebookShareParams...)
	stripParams(u, names)
	return u.String()
}

// canonicalInspirationURL returns the dedupe identity of a pasted link.
func canonicalInspirationURL(raw string) (canonical, provider, mediaID, format string, err error) {
	base, err := canonicalTrumpetURL(raw)
	if err != nil {
		return "", "", "", "", errors.New("link must be an http(s) URL")
	}
	if base == "" {
		return "", "", "", "", errors.New("link required")
	}
	if len(base) > 2000 {
		return "", "", "", "", errors.New("link too long")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", "", "", "", errors.New("link must be an http(s) URL")
	}
	host := u.Host
	segs := pathSegments(u.Path)
	switch {
	case host == "youtube.com" || host == "m.youtube.com" || host == "music.youtube.com" || host == "youtube-nocookie.com" || host == "youtu.be":
		id, kind := "", ""
		if host == "youtu.be" && len(segs) >= 1 {
			id, kind = segs[0], "video"
		} else if len(segs) >= 2 && segs[0] == "shorts" {
			id, kind = segs[1], "short"
		} else if len(segs) >= 2 && (segs[0] == "live" || segs[0] == "embed" || segs[0] == "v") {
			id, kind = segs[1], "video"
		} else if len(segs) == 1 && segs[0] == "watch" {
			id, kind = u.Query().Get("v"), "video"
		}
		if kind == "" {
			stripParams(u, genericShareParams)
			return u.String(), "youtube", "", "page", nil
		}
		if !youtubeIDPattern.MatchString(id) {
			return "", "", "", "", errors.New("invalid YouTube video id")
		}
		if kind == "short" {
			return "https://youtube.com/shorts/" + id, "youtube", id, "short", nil
		}
		out := "https://youtube.com/watch?v=" + id
		if t := u.Query().Get("t"); t != "" && len(t) <= 16 {
			out += "&t=" + url.QueryEscape(t)
		}
		return out, "youtube", id, "video", nil
	case host == "instagram.com" || host == "m.instagram.com":
		// /p/<code>, /reel/<code>, /reels/<code>, /tv/<code>, also /<user>/p/<code>.
		for i := 0; i < len(segs)-1 && i < 2; i++ {
			kind := ""
			switch segs[i] {
			case "p":
				kind = "post"
			case "reel", "reels", "tv":
				kind = "reel"
			}
			if kind == "" {
				continue
			}
			code := segs[i+1]
			if !instagramCodePattern.MatchString(code) {
				return "", "", "", "", errors.New("invalid Instagram shortcode")
			}
			p := "p"
			if kind == "reel" {
				p = "reel"
			}
			return "https://instagram.com/" + p + "/" + code, "instagram", code, kind, nil
		}
		u.RawQuery = ""
		return u.String(), "instagram", "", "page", nil
	case host == "tiktok.com" || host == "m.tiktok.com" || host == "vm.tiktok.com" || host == "vt.tiktok.com":
		stripParams(u, append(append([]string{}, tiktokShareParams...), genericShareParams...))
		id, kind := "", "video"
		if len(segs) >= 3 && strings.HasPrefix(segs[0], "@") && (segs[1] == "video" || segs[1] == "photo") {
			if tiktokIDPattern.MatchString(segs[2]) {
				id = segs[2]
			}
			if segs[1] == "photo" {
				kind = "post"
			}
		}
		return u.String(), "tiktok", id, kind, nil
	case host == "facebook.com" || host == "m.facebook.com" || host == "web.facebook.com" || host == "fb.com" || host == "fb.watch":
		stripParams(u, append(append([]string{}, facebookShareParams...), genericShareParams...))
		kind := "post"
		if host == "fb.watch" || strings.Contains(u.Path, "/videos/") || strings.Contains(u.Path, "/reel/") || (len(segs) > 0 && segs[0] == "watch") {
			kind = "video"
		}
		return u.String(), "facebook", "", kind, nil
	case host == "reverb.com" || host == "m.reverb.com":
		if len(segs) >= 2 && segs[0] == "item" {
			if m := reverbItemPattern.FindStringSubmatch(segs[1]); m != nil {
				return "https://reverb.com/item/" + m[1], "reverb", "", "page", nil
			}
		}
		stripParams(u, genericShareParams)
		return u.String(), "reverb", "", "page", nil
	case ebayHostPattern.MatchString(host):
		h := strings.TrimPrefix(host, "m.")
		if len(segs) >= 2 && segs[0] == "itm" {
			for _, s := range segs[1:] {
				if ebayItemPattern.MatchString(s) {
					return "https://" + h + "/itm/" + s, "ebay", "", "page", nil
				}
			}
		}
		stripParams(u, genericShareParams)
		return u.String(), "ebay", "", "page", nil
	}
	stripParams(u, genericShareParams)
	return u.String(), "web", "", "page", nil
}

type inspirationEmbed struct {
	Kind   string `json:"kind"`
	Src    string `json:"src"`
	Aspect string `json:"aspect"`
}

// Derived from the validated media id only, never from fetched HTML.
func inspirationEmbedFor(provider, mediaID, format string) *inspirationEmbed {
	switch {
	case provider == "youtube" && youtubeIDPattern.MatchString(mediaID):
		aspect := "16:9"
		if format == "short" {
			aspect = "9:16"
		}
		return &inspirationEmbed{Kind: "youtube", Src: "https://www.youtube-nocookie.com/embed/" + mediaID, Aspect: aspect}
	case provider == "instagram" && instagramCodePattern.MatchString(mediaID):
		p := "p"
		if format == "reel" {
			p = "reel"
		}
		return &inspirationEmbed{Kind: "instagram", Src: "https://www.instagram.com/" + p + "/" + mediaID + "/embed", Aspect: "4:5"}
	}
	return nil
}

// ---- Safe fetching ---------------------------------------------------------

const (
	inspirationUserAgent    = "zachbednarke.com inspiration preview (+https://zachbednarke.com)"
	inspirationHTMLLimit    = 1 << 20
	inspirationImageLimit   = 5 << 20
	inspirationRequestLimit = 4 * time.Second
	inspirationTotalLimit   = 10 * time.Second
)

var errBlockedAddress = errors.New("blocked address")

type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

var blockedPrefixes = func() []netip.Prefix {
	out := []netip.Prefix{}
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001:db8::/32", "fec0::/10"} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// blockedIP rejects loopback, private, link-local (incl. the metadata server),
// CGNAT, multicast, unspecified and documentation/reserved ranges.
func blockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	if addr.Is4() && addr.As4() == [4]byte{255, 255, 255, 255} {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

type safeDialer struct {
	resolver ipResolver
	// allowLoopbackForTests permits loopback addresses on any port. It is set
	// only by tests that serve fixtures from httptest servers.
	allowLoopbackForTests bool
	dialer                net.Dialer
}

func (d *safeDialer) check(host, port string) error {
	if !d.allowLoopbackForTests && port != "80" && port != "443" {
		return errors.New("blocked port")
	}
	return nil
}

func (d *safeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if err = d.check(host, port); err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := d.resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("no addresses")
	}
	for _, ip := range ips {
		if blockedIP(ip) && !(d.allowLoopbackForTests && ip.IsLoopback()) {
			return nil, errBlockedAddress
		}
	}
	// Dial the checked address, not the name, so DNS cannot rebind in between.
	return d.dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}

func newSafeHTTPClient(resolver ipResolver, allowLoopbackForTests bool) *http.Client {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	d := &safeDialer{resolver: resolver, allowLoopbackForTests: allowLoopbackForTests, dialer: net.Dialer{Timeout: inspirationRequestLimit}}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   inspirationRequestLimit,
		ResponseHeaderTimeout: inspirationRequestLimit,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    false,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   inspirationRequestLimit,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > 3 {
				return errors.New("too many redirects")
			}
			if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
				return errors.New("redirect scheme rejected")
			}
			if p := r.URL.Port(); p != "" && p != "80" && p != "443" && !allowLoopbackForTests {
				return errors.New("blocked port")
			}
			return nil
		},
	}
}

// fetchLimited GETs one URL with the fixed preview identity and a body cap.
func fetchLimited(ctx context.Context, client *http.Client, raw string, limit int64, accept string) ([]byte, string, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, "", nil, errors.New("invalid fetch URL")
	}
	ctx, cancel := context.WithTimeout(ctx, inspirationRequestLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", nil, err
	}
	req.Header.Set("User-Agent", inspirationUserAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en")
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddress) {
			return nil, "", nil, errBlockedAddress
		}
		return nil, "", nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, "", nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, "", nil, err
	}
	if int64(len(body)) > limit {
		return nil, "", nil, errors.New("response too large")
	}
	return body, res.Header.Get("Content-Type"), res.Request.URL, nil
}

// ---- Page metadata ---------------------------------------------------------

type pageMetadata struct {
	Title       string
	OGTitle     string
	SiteName    string
	Description string
	Image       string
	Price       *float64
	Currency    string
}

func parsePageMetadata(body []byte, base *url.URL) pageMetadata {
	var m pageMetadata
	var docTitle, twitterImage, metaDescription, metaPrice, metaCurrency string
	ldBlocks := []string{}
	z := html.NewTokenizer(bytes.NewReader(body))
	inTitle, inLD := false, false
	var ld strings.Builder
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			a := atom.Lookup(name)
			attrs := map[string]string{}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				attrs[strings.ToLower(string(k))] = string(v)
			}
			switch a {
			case atom.Title:
				inTitle = tt == html.StartTagToken && docTitle == ""
			case atom.Script:
				inLD = tt == html.StartTagToken && strings.EqualFold(strings.TrimSpace(attrs["type"]), "application/ld+json")
				ld.Reset()
			case atom.Meta:
				key := strings.ToLower(strings.TrimSpace(attrs["property"]))
				if key == "" {
					key = strings.ToLower(strings.TrimSpace(attrs["name"]))
				}
				content := strings.TrimSpace(attrs["content"])
				if content == "" {
					continue
				}
				switch key {
				case "og:title":
					if m.OGTitle == "" {
						m.OGTitle = content
					}
				case "og:site_name":
					if m.SiteName == "" {
						m.SiteName = content
					}
				case "og:description":
					if m.Description == "" {
						m.Description = content
					}
				case "description":
					if metaDescription == "" {
						metaDescription = content
					}
				case "og:image", "og:image:secure_url", "og:image:url":
					if m.Image == "" {
						m.Image = content
					}
				case "twitter:image", "twitter:image:src":
					if twitterImage == "" {
						twitterImage = content
					}
				case "product:price:amount", "og:price:amount":
					if metaPrice == "" {
						metaPrice = content
					}
				case "product:price:currency", "og:price:currency":
					if metaCurrency == "" {
						metaCurrency = content
					}
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch atom.Lookup(name) {
			case atom.Title:
				inTitle = false
			case atom.Script:
				if inLD {
					ldBlocks = append(ldBlocks, ld.String())
				}
				inLD = false
			}
		case html.TextToken:
			if inTitle {
				docTitle += string(z.Text())
			} else if inLD && ld.Len() < inspirationHTMLLimit {
				ld.Write(z.Text())
			}
		}
	}
	m.Title = collapseSpace(m.OGTitle)
	if m.Title == "" {
		m.Title = collapseSpace(docTitle)
	}
	if m.Description == "" {
		m.Description = metaDescription
	}
	if m.Image == "" {
		m.Image = twitterImage
	}
	m.Image = resolveHTTPURL(base, m.Image)
	if v, ok := parseAmount(metaPrice); ok {
		m.Price, m.Currency = &v, strings.ToUpper(strings.TrimSpace(metaCurrency))
	}
	for _, block := range ldBlocks {
		var doc any
		if json.Unmarshal([]byte(block), &doc) != nil {
			continue
		}
		if p, c, img, ok := findLDProduct(doc); ok {
			if m.Price == nil && p != nil {
				m.Price, m.Currency = p, strings.ToUpper(c)
			}
			if m.Image == "" && img != "" {
				m.Image = resolveHTTPURL(base, img)
			}
			break
		}
	}
	if m.Price != nil && !currencyPattern.MatchString(m.Currency) {
		m.Currency = "USD"
	}
	m.Title = truncateRunes(m.Title, 300)
	m.SiteName = truncateRunes(collapseSpace(m.SiteName), 120)
	m.Description = truncateRunes(collapseSpace(m.Description), 2000)
	return m
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func resolveHTTPURL(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || len(u.String()) > 2000 {
		return ""
	}
	return u.String()
}

func parseAmount(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		s := strings.TrimSpace(strings.ReplaceAll(x, ",", ""))
		if s == "" {
			return 0, false
		}
		var err error
		if f, err = strconv.ParseFloat(s, 64); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f > 1000000 {
		return 0, false
	}
	return math.Round(f*100) / 100, true
}

// findLDProduct walks JSON-LD (object, array or @graph) for a Product and its
// offer price, accepting offers as an object or an array.
func findLDProduct(doc any) (*float64, string, string, bool) {
	switch x := doc.(type) {
	case []any:
		for _, v := range x {
			if p, c, img, ok := findLDProduct(v); ok {
				return p, c, img, ok
			}
		}
	case map[string]any:
		if g, ok := x["@graph"]; ok {
			if p, c, img, ok := findLDProduct(g); ok {
				return p, c, img, ok
			}
		}
		if !ldTypeIs(x["@type"], "Product") {
			return nil, "", "", false
		}
		image := ""
		switch im := x["image"].(type) {
		case string:
			image = im
		case []any:
			if len(im) > 0 {
				if s, ok := im[0].(string); ok {
					image = s
				} else if o, ok := im[0].(map[string]any); ok {
					image, _ = o["url"].(string)
				}
			}
		case map[string]any:
			image, _ = im["url"].(string)
		}
		offers := []any{}
		switch o := x["offers"].(type) {
		case []any:
			offers = o
		case map[string]any:
			offers = []any{o}
		}
		for _, o := range offers {
			offer, ok := o.(map[string]any)
			if !ok {
				continue
			}
			price := offer["price"]
			if price == nil {
				price = offer["lowPrice"]
			}
			if v, ok := parseAmount(price); ok {
				c, _ := offer["priceCurrency"].(string)
				return &v, strings.TrimSpace(c), image, true
			}
		}
		return nil, "", image, true
	}
	return nil, "", "", false
}

func ldTypeIs(v any, want string) bool {
	switch t := v.(type) {
	case string:
		return strings.EqualFold(t, want)
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && strings.EqualFold(s, want) {
				return true
			}
		}
	}
	return false
}

var loginWallTitle = regexp.MustCompile(`(?i)^(log ?in|sign ?in|sign up)\b|\blog ?in\s*[•·|-]|^(instagram|facebook|tiktok)$|^tiktok - make your day$|^(error|page not found)$`)

// isLoginWall reports whether a social page returned a login interstitial
// rather than the post. Its title must never be stored.
func isLoginWall(m pageMetadata) bool {
	return m.OGTitle == "" || loginWallTitle.MatchString(strings.TrimSpace(m.Title))
}

func genericSocialTitle(provider, format string) string {
	name := map[string]string{"instagram": "Instagram", "facebook": "Facebook", "tiktok": "TikTok"}[provider]
	if name == "" {
		return ""
	}
	switch format {
	case "reel":
		return name + " reel"
	case "video", "short":
		return name + " video"
	}
	return name + " post"
}

type oembedResponse struct {
	Title        string `json:"title"`
	AuthorName   string `json:"author_name"`
	ProviderName string `json:"provider_name"`
	ThumbnailURL string `json:"thumbnail_url"`
}

func fetchOEmbed(ctx context.Context, client *http.Client, endpoint, canonical string) (oembedResponse, error) {
	var out oembedResponse
	body, _, _, err := fetchLimited(ctx, client, endpoint+url.QueryEscape(canonical)+"&format=json", inspirationHTMLLimit, "application/json")
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(body, &out); err != nil {
		return out, errors.New("invalid oEmbed response")
	}
	out.Title = truncateRunes(collapseSpace(out.Title), 300)
	out.AuthorName = truncateRunes(collapseSpace(out.AuthorName), 200)
	return out, nil
}

// ---- Image validation --------------------------------------------------------

var inspirationImageTypes = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "image/gif": "gif"}

const (
	inspirationUploadLimit = 10 << 20
	inspirationMaxPixels   = 12000
	inspirationMaxImages   = 20
)

type sniffedImage struct {
	ContentType   string
	Width, Height *int
}

// sniffInspirationImage requires the declared and sniffed types to agree (when
// a type is declared), one of the four allowed types, and sane dimensions.
func sniffInspirationImage(body []byte, declared string) (sniffedImage, error) {
	var out sniffedImage
	if len(body) == 0 {
		return out, errors.New("empty image")
	}
	sniffed := http.DetectContentType(body)
	if _, ok := inspirationImageTypes[sniffed]; !ok {
		return out, errors.New("unsupported image type")
	}
	declared = strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0]))
	if declared != "" && declared != sniffed {
		return out, errors.New("declared image type does not match its contents")
	}
	out.ContentType = sniffed
	if sniffed != "image/webp" {
		w, h, err := decodeImageConfig(body)
		if err != nil {
			return out, errors.New("unreadable image")
		}
		if w <= 0 || h <= 0 || w > inspirationMaxPixels || h > inspirationMaxPixels {
			return out, errors.New("image dimensions exceed 12000px")
		}
		out.Width, out.Height = &w, &h
	}
	return out, nil
}
