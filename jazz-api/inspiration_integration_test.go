package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func isolatedTrumpetDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TRUMPETS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TRUMPETS_TEST_DATABASE_URL to disposable Postgres")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	schema := "inspiration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(isolated.Close)
	for i := 0; i < 2; i++ {
		if err = migrate(ctx, isolated); err != nil {
			t.Fatalf("migration pass %d: %v", i, err)
		}
	}
	return isolated
}

func TestInspirationPersistence(t *testing.T) {
	db := isolatedTrumpetDB(t)
	ctx := context.Background()
	objects := newMemoryObjects("https://signed.test/")
	app := &application{db: db, cfg: config{GatewayKey: "gateway"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), objects: objects}
	routes := app.routes()
	do := func(method, path string, body []byte, contentType, subject string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		r.Header.Set("X-Jazz-Gateway-Key", "gateway")
		r.Header.Set("X-Jazz-User", subject)
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w
	}
	call := func(method, path string, input any, want int) map[string]any {
		t.Helper()
		body, _ := json.Marshal(input)
		w := do(method, path, body, "application/json", "owner")
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		for header, value := range map[string]string{"Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow, noarchive", "Referrer-Policy": "no-referrer"} {
			if w.Header().Get(header) != value {
				t.Fatalf("%s %s: %s=%q", method, path, header, w.Header().Get(header))
			}
		}
		out := map[string]any{}
		if w.Body.Len() > 0 {
			json.Unmarshal(w.Body.Bytes(), &out)
		}
		return out
	}
	board := func(archived bool) []map[string]any {
		path := "/v1/trumpets/inspiration"
		if archived {
			path += "?archived=1"
		}
		out := []map[string]any{}
		for _, v := range call("GET", path, nil, 200)["inspirations"].([]any) {
			out = append(out, v.(map[string]any))
		}
		return out
	}
	user, err := app.userID(context.WithValue(ctx, userSubjectKey, "owner"))
	if err != nil {
		t.Fatal(err)
	}

	// Idempotent capture.
	capture := uuid.NewString()
	first := call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": capture, "url": "https://youtube.com/shorts/qHetQ-t4Wi0?is=abc", "why": "Some day"}, 201)
	id := first["id"].(string)
	if first["canonicalUrl"] != "https://youtube.com/shorts/qHetQ-t4Wi0" || first["metadataStatus"] != "pending" || first["kind"] != "link" || first["priority"] != "someday" || first["sourceUrl"] != "https://youtube.com/shorts/qHetQ-t4Wi0" {
		t.Fatal(first)
	}
	if embed := first["embed"].(map[string]any); embed["src"] != "https://www.youtube-nocookie.com/embed/qHetQ-t4Wi0" || embed["aspect"] != "9:16" {
		t.Fatal(embed)
	}
	if again := call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": capture, "url": "https://youtube.com/shorts/qHetQ-t4Wi0"}, 200); again["id"] != id {
		t.Fatal("same capture id must return the original row", again)
	}
	// Duplicates by canonical URL and by provider media id.
	for _, link := range []string{"https://youtu.be/qHetQ-t4Wi0?si=x", "https://m.youtube.com/shorts/qHetQ-t4Wi0"} {
		dup := call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": uuid.NewString(), "url": link}, 409)
		if dup["duplicate"] != true || dup["inspiration"].(map[string]any)["id"] != id || dup["archived"] != nil {
			t.Fatal(link, dup)
		}
	}
	call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": uuid.NewString(), "url": "javascript:alert(1)"}, 400)
	call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": "nope", "url": "https://example.com"}, 400)
	call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": uuid.NewString(), "kind": "link"}, 400)

	// Archive, duplicate of archived entry, restore.
	archived := call("PATCH", "/v1/trumpets/inspiration/"+id, map[string]any{"expectedRevision": 1, "archived": true}, 200)
	if archived["archivedAt"] == nil || archived["revision"].(float64) != 2 {
		t.Fatal(archived)
	}
	if len(board(false)) != 0 || len(board(true)) != 1 {
		t.Fatal("archive hides but keeps")
	}
	dup := call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": uuid.NewString(), "url": "https://youtu.be/qHetQ-t4Wi0"}, 409)
	if dup["archived"] != true || dup["inspiration"].(map[string]any)["id"] != id {
		t.Fatal(dup)
	}
	restored := call("PATCH", "/v1/trumpets/inspiration/"+id, map[string]any{"expectedRevision": 2, "archived": false}, 200)
	if restored["archivedAt"] != nil {
		t.Fatal(restored)
	}

	// Stale revisions conflict; typing a price clears price_auto.
	stale := call("PATCH", "/v1/trumpets/inspiration/"+id, map[string]any{"expectedRevision": 1, "maker": "Harrelson"}, 409)
	if stale["inspiration"].(map[string]any)["revision"].(float64) != 3 {
		t.Fatal(stale)
	}
	if _, err = db.Exec(ctx, `UPDATE horn_inspirations SET price_seen=100,price_auto=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	typed := call("PATCH", "/v1/trumpets/inspiration/"+id, map[string]any{"expectedRevision": 3, "priceSeen": 4200, "priceCurrency": "usd", "maker": "Harrelson", "tags": []string{"MBS Technology", "mbs technology"}}, 200)
	if typed["priceAuto"] != false || typed["priceSeen"].(float64) != 4200 || typed["priceCurrency"] != "USD" || typed["priceSeenOn"] == "" || len(typed["tags"].([]any)) != 1 {
		t.Fatal(typed)
	}
	cleared := call("PATCH", "/v1/trumpets/inspiration/"+id, map[string]any{"expectedRevision": 4, "priceSeen": nil}, 200)
	if cleared["priceSeen"] != nil {
		t.Fatal(cleared)
	}

	// Observatory link: other users' horns are rejected; the count appears on listings.
	call("POST", "/v1/trumpets/seed", map[string]any{}, 200)
	var muse, foreign uuid.UUID
	if err = db.QueryRow(ctx, `SELECT id FROM trumpet_horns WHERE user_id=$1 AND maker='Harrelson' AND model='MUSE'`, user).Scan(&muse); err != nil {
		t.Fatal(err)
	}
	other, _ := app.userID(context.WithValue(ctx, userSubjectKey, "someone-else"))
	foreign = uuid.New()
	if _, err = db.Exec(ctx, `INSERT INTO trumpet_horns(id,user_id,maker,model) VALUES($1,$2,'Harrelson','MUSE')`, foreign, other); err != nil {
		t.Fatal(err)
	}
	call("PATCH", "/v1/trumpets/inspiration/"+id, map[string]any{"expectedRevision": 5, "hornId": foreign.String()}, 400)
	linked := call("PATCH", "/v1/trumpets/inspiration/"+id, map[string]any{"expectedRevision": 5, "hornId": muse.String(), "model": "MUSE"}, 200)
	horn, _ := linked["horn"].(map[string]any)
	if linked["hornId"] != muse.String() || horn == nil || horn["maker"] != "Harrelson" || horn["activeOffers"].(float64) != 0 || horn["acquired"] != false {
		t.Fatal(linked)
	}
	counted := false
	for _, l := range call("GET", "/v1/trumpets/listings", nil, 200)["listings"].([]any) {
		m := l.(map[string]any)
		if m["hornId"] == muse.String() && m["inspirationCount"].(float64) == 1 {
			counted = true
		} else if m["inspirationCount"].(float64) != 0 {
			t.Fatal("count leaked to another horn", m["model"])
		}
	}
	if !counted {
		t.Fatal("inspirationCount missing on the linked horn")
	}
	profile := call("GET", "/v1/trumpets/profile", nil, 200)
	feed := profile["inspirations"].([]any)
	if len(feed) != 1 || feed[0].(map[string]any)["maker"] != "Harrelson" || feed[0].(map[string]any)["why"] != "Some day" {
		t.Fatal(feed)
	}
	if raw, _ := json.Marshal(feed); strings.Contains(string(raw), "youtube") || strings.Contains(string(raw), "http") {
		t.Fatal("profile feed must not include URLs", string(raw))
	}

	// Board order: pinned first, then newest.
	second := call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": uuid.NewString(), "url": "https://reverb.com/item/81234567-harrelson-muse", "priority": "hunting", "maker": "Harrelson"}, 201)
	photo := call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": uuid.NewString()}, 201)
	if photo["kind"] != "image" || photo["provider"] != "upload" || photo["metadataStatus"] != "skipped" {
		t.Fatal(photo)
	}
	call("PATCH", "/v1/trumpets/inspiration/"+id, map[string]any{"expectedRevision": 6, "pinned": true}, 200)
	order := []string{}
	for _, x := range board(false) {
		order = append(order, x["id"].(string))
	}
	if strings.Join(order, ",") != strings.Join([]string{id, photo["id"].(string), second["id"].(string)}, ",") {
		t.Fatal("board order", order)
	}
	vocab := call("GET", "/v1/trumpets/inspiration", nil, 200)["vocab"].(map[string]any)
	if !strings.Contains(strings.Join(anyStrings(vocab["makers"]), ","), "Harrelson") || !strings.Contains(strings.Join(anyStrings(vocab["tags"]), ","), "mbs technology") || !strings.Contains(strings.Join(anyStrings(vocab["tags"]), ","), "raw brass") {
		t.Fatal(vocab)
	}

	// Raw image uploads: sniffed, stored privately, served by a short signed redirect.
	photoID := photo["id"].(string)
	png := pngFixture(t, 40, 30)
	if w := do("POST", "/v1/trumpets/inspiration/"+photoID+"/images", png, "image/jpeg", "owner"); w.Code != 400 {
		t.Fatal("declared/sniffed mismatch", w.Code)
	}
	if w := do("POST", "/v1/trumpets/inspiration/"+photoID+"/images", png, "text/plain", "owner"); w.Code != 403 {
		t.Fatal("non-image body type", w.Code)
	}
	if w := do("POST", "/v1/trumpets/inspiration/"+photoID+"/images", png, "image/png", "someone-else"); w.Code != 404 {
		t.Fatal("upload to another user's entry", w.Code)
	}
	w := do("POST", "/v1/trumpets/inspiration/"+photoID+"/images", png, "image/png", "owner")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var uploaded struct{ Image inspirationImage }
	json.Unmarshal(w.Body.Bytes(), &uploaded)
	if uploaded.Image.Width == nil || *uploaded.Image.Width != 40 || objects.count("inspiration/"+user.String()+"/"+photoID+"/") != 1 {
		t.Fatal(w.Body.String())
	}
	for name, meta := range objects.metadata {
		if strings.Contains(name, photoID) && (meta["role"] != "upload" || meta["inspirationId"] != photoID || meta["userId"] != user.String()) {
			t.Fatal(meta)
		}
	}
	w2 := do("POST", "/v1/trumpets/inspiration/"+photoID+"/images", pngFixture(t, 8, 8), "image/png", "owner")
	var second2 struct{ Image inspirationImage }
	json.Unmarshal(w2.Body.Bytes(), &second2)
	reordered := call("PATCH", "/v1/trumpets/inspiration/"+photoID, map[string]any{"expectedRevision": 1, "imageOrder": []string{second2.Image.ID.String(), uploaded.Image.ID.String()}}, 200)
	if imgs := reordered["images"].([]any); len(imgs) != 2 || imgs[0].(map[string]any)["id"] != second2.Image.ID.String() {
		t.Fatal(imgs)
	}
	redirect := do("GET", "/v1/trumpets/inspiration/images/"+uploaded.Image.ID.String(), nil, "", "owner")
	if redirect.Code != 302 || !strings.HasPrefix(redirect.Header().Get("Location"), "https://signed.test/inspiration/"+user.String()+"/") || redirect.Header().Get("Cache-Control") != "private, max-age=300" || redirect.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal(redirect.Code, redirect.Header())
	}
	if w := do("GET", "/v1/trumpets/inspiration/images/"+uploaded.Image.ID.String(), nil, "", "someone-else"); w.Code != 404 {
		t.Fatal("image visible to another user", w.Code)
	}
	call("DELETE", "/v1/trumpets/inspiration/"+photoID+"/images/"+second2.Image.ID.String(), nil, 200)
	if objects.count("inspiration/"+user.String()+"/"+photoID+"/") != 1 {
		t.Fatal("image object not deleted")
	}

	// Enrichment against local fixtures; loopback is allowed only in this test.
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/product":
			io.WriteString(w, strings.Replace(productFixture, "/img/muse.png", "/thumb.png", 1))
		case "/thumb.png":
			w.Write(pngFixture(t, 12, 9))
		case "/oembed":
			if r.URL.Query().Get("url") != "https://youtube.com/shorts/qHetQ-t4Wi0" {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"title": "1st Harrelson Muse with MBS Technology", "author_name": "Harrelson Trumpets + Rumors & Dreams", "thumbnail_url": srv.URL + "/thumb.png"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	client := newSafeHTTPClient(nil, true)
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	app.inspirationHTTP = client
	oldOEmbed := youtubeOEmbedEndpoint
	youtubeOEmbedEndpoint = srv.URL + "/oembed?url="
	defer func() { youtubeOEmbedEndpoint = oldOEmbed }()

	product := call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": uuid.NewString(), "url": srv.URL + "/product?utm_source=x"}, 201)
	enriched := call("POST", "/v1/trumpets/inspiration/"+product["id"].(string)+"/enrich", map[string]any{}, 200)
	if enriched["metadataStatus"] != "fetched" || enriched["title"] != "Harrelson MUSE <b>raw</b> brass" || enriched["priceAuto"] != true || enriched["priceSeen"].(float64) != 4250 || enriched["priceSeenOn"] == "" || len(enriched["images"].([]any)) != 1 {
		t.Fatal(enriched)
	}
	if objects.count("inspiration/"+user.String()+"/"+product["id"].(string)+"/") != 1 {
		t.Fatal("thumbnail not copied into the private bucket")
	}
	// A typed price is never overwritten, and a forced refresh keeps one thumbnail.
	typedPrice := call("PATCH", "/v1/trumpets/inspiration/"+product["id"].(string), map[string]any{"expectedRevision": 1, "priceSeen": 3900}, 200)
	refreshed := call("POST", "/v1/trumpets/inspiration/"+product["id"].(string)+"/enrich?force=1", map[string]any{}, 200)
	if refreshed["priceSeen"].(float64) != 3900 || refreshed["priceAuto"] != false || len(refreshed["images"].([]any)) != 1 || refreshed["revision"] != typedPrice["revision"] {
		t.Fatal(refreshed)
	}
	if noop := call("POST", "/v1/trumpets/inspiration/"+product["id"].(string)+"/enrich", map[string]any{}, 200); noop["updatedAt"] != refreshed["updatedAt"] {
		t.Fatal("fetched entries are not re-enriched without force")
	}
	// SSRF: the production client refuses loopback; the entry stays a plain link.
	app.inspirationHTTP = newSafeHTTPClient(nil, false)
	local := call("POST", "/v1/trumpets/inspiration", map[string]any{"clientCaptureId": uuid.NewString(), "url": "http://localhost/admin"}, 201)
	refused := call("POST", "/v1/trumpets/inspiration/"+local["id"].(string)+"/enrich", map[string]any{}, 200)
	if refused["metadataStatus"] != "failed" || refused["metadataError"] != "blocked address" {
		t.Fatal(refused)
	}
	app.inspirationHTTP = client

	// Seed: created once, enriched from the title, never recreated after delete.
	call("DELETE", "/v1/trumpets/inspiration/"+id, nil, 204)
	if seeded := call("POST", "/v1/trumpets/inspiration/seed", map[string]any{}, 200); seeded["added"].(float64) != 1 {
		t.Fatal(seeded)
	}
	if seeded := call("POST", "/v1/trumpets/inspiration/seed", map[string]any{}, 200); seeded["added"].(float64) != 0 {
		t.Fatal("seed not idempotent")
	}
	var seed map[string]any
	for _, x := range board(false) {
		if x["clientCaptureId"] == inspirationSeedCaptureID {
			seed = x
		}
	}
	if seed == nil || seed["why"] != "Some day I may want a Harrelson like this one." || seed["maker"] != "Harrelson" || seed["model"] != nil || seed["hornId"] != nil || seed["metadataStatus"] != "pending" {
		t.Fatal(seed)
	}
	seedEnriched := call("POST", "/v1/trumpets/inspiration/"+seed["id"].(string)+"/enrich", map[string]any{}, 200)
	if seedEnriched["title"] != "1st Harrelson Muse with MBS Technology" || seedEnriched["authorName"] != "Harrelson Trumpets + Rumors & Dreams" || seedEnriched["siteName"] != "YouTube" || seedEnriched["model"] != "MUSE" || anyStrings(seedEnriched["tags"])[0] != "mbs technology" || len(seedEnriched["images"].([]any)) != 1 {
		t.Fatal(seedEnriched)
	}
	objects.failDelete = true // permanent delete still removes rows when storage fails
	call("DELETE", "/v1/trumpets/inspiration/"+seed["id"].(string), nil, 204)
	objects.failDelete = false
	if seeded := call("POST", "/v1/trumpets/inspiration/seed", map[string]any{}, 200); seeded["added"].(float64) != 0 {
		t.Fatal("seed recreated after delete")
	}
	var rows int
	db.QueryRow(ctx, `SELECT count(*) FROM horn_inspiration_images WHERE inspiration_id=$1`, seed["id"]).Scan(&rows)
	if rows != 0 {
		t.Fatal("image rows survived delete")
	}
	call("DELETE", "/v1/trumpets/inspiration/"+photoID, nil, 204)
	if objects.count("inspiration/"+user.String()+"/"+photoID+"/") != 0 {
		t.Fatal("permanent delete left objects")
	}
	call("DELETE", "/v1/trumpets/inspiration/"+photoID, nil, 404)
	if w := do("GET", "/v1/trumpets/inspiration", nil, "", ""); w.Code != 401 {
		t.Fatal("anonymous request", w.Code)
	}
}

func anyStrings(v any) []string {
	out := []string{}
	if list, ok := v.([]any); ok {
		for _, x := range list {
			out = append(out, x.(string))
		}
	}
	return out
}
