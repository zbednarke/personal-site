package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// silentWAV is a synthetic 0.1 s mono WAV (not a recording of anyone).
func silentWAV() []byte {
	samples := make([]byte, 1600)
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(samples)))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(8000), uint32(16000), uint16(2), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(samples)))
	b.Write(samples)
	return b.Bytes()
}

func TestTrumpetTastePersistence(t *testing.T) {
	dsn := os.Getenv("TRUMPETS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TRUMPETS_TEST_DATABASE_URL to disposable Postgres")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := "taste_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = db.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	// Migrations replay on every jazz-api and workbench startup.
	for i := 0; i < 2; i++ {
		if err = migrate(ctx, isolated); err != nil {
			t.Fatalf("migration pass %d: %v", i, err)
		}
	}
	objects := newMemoryObjects("https://signed.invalid/")
	app := &application{db: isolated, cfg: config{GatewayKey: "gateway"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), objects: objects}
	routes := app.routes()
	request := func(method, path, contentType string, body []byte, subject string, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		r.Header.Set("X-Jazz-Gateway-Key", "gateway")
		r.Header.Set("X-Jazz-User", subject)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w
	}
	call := func(method, path string, input any, subject string, want int) map[string]any {
		t.Helper()
		body, _ := json.Marshal(input)
		if input == nil {
			body = nil
		}
		w := request(method, path, "application/json", body, subject, nil)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	call("POST", "/v1/trumpets/seed", map[string]any{}, "owner", 200)
	board := call("GET", "/v1/trumpets/listings", nil, "owner", 200)
	listings := board["listings"].([]any)
	if len(listings) < 4 {
		t.Fatal("seed missing")
	}
	id := func(i int) string { return listings[i].(map[string]any)["id"].(string) }
	if _, ok := listings[0].(map[string]any)["tasteScore"]; !ok {
		t.Fatal("board listings carry the learned score")
	}

	// Only stale historical seeds: nothing worth comparing, so no pairs (the
	// page explains why) rather than weak ones.
	seedOnly := call("GET", "/v1/trumpets/taste", nil, "owner", 200)
	if len(seedOnly["queue"].([]any)) == 0 || len(seedOnly["pairs"].([]any)) != 0 || seedOnly["pairEligible"].(float64) != 0 || seedOnly["pairMinimum"].(float64) != tastePairMinimum {
		t.Fatalf("seed-only queue/pairs: queue %d, pairs %v, eligible %v", len(seedOnly["queue"].([]any)), seedOnly["pairs"], seedOnly["pairEligible"])
	}
	// Six comparable fictional listings (active, verified, priced, with a
	// photo, checked now), plus weak ones that must never be paired: an
	// unverified lead, a photo-less and a price-less listing.
	owner, err := app.userID(context.WithValue(ctx, userSubjectKey, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	comparableIDs := map[string]bool{}
	tx, err := isolated.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for i := 0; i < 9; i++ {
		c := perfCandidate(i, float64(2500+100*i))
		switch i {
		case 6:
			c.VerificationState, c.Status, c.Price, c.SerialNumber = "candidate", "stale", nil, ""
		case 7:
			c.Images = nil
		case 8:
			c.Price = nil
		}
		lid, err := upsertTrumpet(ctx, tx, owner, c, nil)
		if err != nil {
			t.Fatalf("fixture %d: %v", i, err)
		}
		if i < 6 {
			comparableIDs[lid.String()] = true
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	taste := call("GET", "/v1/trumpets/taste", nil, "owner", 200)
	if len(taste["queue"].([]any)) == 0 || len(taste["pairs"].([]any)) == 0 {
		t.Fatalf("empty queue/pairs: %v", taste)
	}
	if taste["pairEligible"].(float64) != 6 {
		t.Fatalf("pairEligible %v", taste["pairEligible"])
	}
	for _, p := range taste["pairs"].([]any) {
		pair := p.(map[string]any)
		if !comparableIDs[pair["a"].(string)] || !comparableIDs[pair["b"].(string)] {
			t.Fatalf("pair with an ineligible side: %v", pair)
		}
	}
	// The comparison graph: six never-compared horns are six islands. One
	// choice joins two; the next pair bridges that component to an island;
	// undoing the choice splits it again.
	graph := func(state map[string]any) (components, largest float64) {
		g := state["pairGraph"].(map[string]any)
		if g["eligible"].(float64) != 6 {
			t.Fatalf("pairGraph %v", g)
		}
		return g["components"].(float64), g["largest"].(float64)
	}
	if c, l := graph(taste); c != 6 || l != 1 {
		t.Fatalf("before any choice: %v components, largest %v", c, l)
	}
	top := taste["pairs"].([]any)[0].(map[string]any)
	chose := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "graph-0001", "kind": "pair", "mode": "pair", "listingId": top["a"], "otherListingId": top["b"], "action": "neither"}, "owner", 201)
	if c, l := graph(chose); c != 5 || l != 2 {
		t.Fatalf("after one choice: %v components, largest %v", c, l)
	}
	next := chose["pairs"].([]any)[0].(map[string]any)
	joined := map[any]bool{top["a"]: true, top["b"]: true}
	if joined[next["a"]] == joined[next["b"]] {
		t.Fatalf("next pair %v does not bridge the joined pair %v to an island", next, top)
	}
	reverted := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "graph-0002", "kind": "undo", "undoes": chose["event"].(map[string]any)["id"]}, "owner", 201)
	if c, _ := graph(reverted); c != 6 {
		t.Fatalf("an undone choice still joins horns: %v components", c)
	}
	// The watermark is the newest event the response was computed from.
	watermark := func(state map[string]any) float64 {
		return state["watermark"].(map[string]any)["events"].(float64)
	}
	if watermark(chose) != chose["event"].(map[string]any)["id"].(float64) || watermark(reverted) != reverted["event"].(map[string]any)["id"].(float64) || watermark(reverted) <= watermark(chose) || watermark(taste) >= watermark(chose) {
		t.Fatalf("watermarks: taste %v, choice %v, undo %v", taste["watermark"], chose["watermark"], reverted["watermark"])
	}
	if conf := taste["model"].(map[string]any)["confidence"].(float64); conf != 0 {
		t.Fatalf("confidence before any reaction = %v", conf)
	}

	// Swipe, then replay the same clientId: one event, 200 replayed.
	swipe := map[string]any{"clientId": "swipe-0001", "kind": "swipe", "mode": "swipe", "listingId": id(0), "action": "right"}
	first := call("POST", "/v1/trumpets/taste/events", swipe, "owner", 201)
	again := call("POST", "/v1/trumpets/taste/events", swipe, "owner", 200)
	if again["replayed"] != true || first["event"].(map[string]any)["id"] != again["event"].(map[string]any)["id"] {
		t.Fatal("clientId replay added history")
	}
	swipeID := first["event"].(map[string]any)["id"].(float64)
	for _, q := range first["queue"].([]any) {
		if q.(map[string]any)["listingId"] == id(0) {
			t.Fatal("just-swiped horn is still at the front of the queue")
		}
	}
	if meta := first["event"].(map[string]any)["meta"].(map[string]any); len(meta) != 0 {
		t.Fatalf("a swipe without metadata stored %v", meta)
	}
	// Whether the card showed a photo is stored with the reaction and returned.
	paired := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "pair-0001", "kind": "pair", "mode": "pair", "listingId": id(1), "otherListingId": id(2), "action": "b", "meta": map[string]any{"hadPhoto": false}}, "owner", 201)
	if meta := paired["event"].(map[string]any)["meta"].(map[string]any); meta["hadPhoto"] != false {
		t.Fatalf("pair meta %v", meta)
	}
	var storedMeta string
	if err := isolated.QueryRow(ctx, `SELECT meta::text FROM trumpet_taste_events WHERE client_id='pair-0001'`).Scan(&storedMeta); err != nil || storedMeta != `{"hadPhoto": false}` {
		t.Fatalf("stored meta %q %v", storedMeta, err)
	}
	call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "meta-bad-1", "kind": "swipe", "mode": "swipe", "listingId": id(3), "action": "left", "meta": map[string]any{"hadPhoto": false, "url": "https://x.invalid"}}, "owner", 400)
	call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "tag-00001", "kind": "tag", "listingId": id(1), "tag": "raw brass", "action": "add"}, "owner", 201)
	noted := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "note-0001", "kind": "note", "listingId": id(1), "note": "Love the raw brass, but too expensive"}, "owner", 201)
	if tags := noted["tags"].(map[string]any)[id(1)].([]any); len(tags) != 1 || tags[0] != "raw brass" {
		t.Fatalf("tag state %v", noted["tags"])
	}
	if fav := noted["noteSignals"].(map[string]any)["favoredAttributes"].(map[string]any); fav["raw brass"] == nil {
		t.Fatalf("note statements %v", noted["noteSignals"])
	}
	if model := noted["model"].(map[string]any); model["swipes"].(float64) != 1 || model["pairs"].(float64) != 1 {
		t.Fatalf("counts %v", model)
	}

	// Undo once; a second undo of the same event conflicts.
	undone := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "undo-0001", "kind": "undo", "undoes": swipeID}, "owner", 201)
	if undone["model"].(map[string]any)["swipes"].(float64) != 0 {
		t.Fatal("undone swipe still counted")
	}
	call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "undo-0002", "kind": "undo", "undoes": swipeID}, "owner", 409)
	call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "undo-0003", "kind": "undo", "undoes": 999999}, "owner", 404)

	// Owner scoping: another user cannot react to, undo or read these.
	call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "swipe-x001", "kind": "swipe", "listingId": id(0), "action": "left"}, "intruder", 404)
	call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "undo-x001", "kind": "undo", "undoes": swipeID}, "intruder", 404)
	if other := call("GET", "/v1/trumpets/taste", nil, "intruder", 200); len(other["recent"].([]any)) != 0 {
		t.Fatal("events leaked across owners")
	}
	// Writes keep the JSON, same-site rules of the other trumpet writes.
	body, _ := json.Marshal(swipe)
	if w := request("POST", "/v1/trumpets/taste/events", "text/plain", body, "owner", nil); w.Code != 403 {
		t.Fatalf("text/plain write: %d", w.Code)
	}
	if w := request("POST", "/v1/trumpets/taste/events", "application/json", body, "owner", map[string]string{"Sec-Fetch-Site": "cross-site"}); w.Code != 403 {
		t.Fatalf("cross-site write: %d", w.Code)
	}
	if w := request("POST", "/v1/trumpets/taste/events", "application/json", body, "owner", map[string]string{"Origin": "https://evil.invalid"}); w.Code != 403 {
		t.Fatalf("foreign origin: %d", w.Code)
	}
	if w := request("POST", "/v1/trumpets/taste/events", "application/json", body, "", nil); w.Code != 401 {
		t.Fatalf("anonymous write: %d", w.Code)
	}

	// Append-only: the table refuses updates.
	if _, err := isolated.Exec(ctx, `UPDATE trumpet_taste_events SET action='left' WHERE id=$1`, int64(swipeID)); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update allowed: %v", err)
	}

	// Hold-to-talk voice note: stored privately, served by a signed redirect.
	voicePath := "/v1/trumpets/taste/voice?clientId=voice-0001&mode=swipe&durationMs=1200&listingId=" + id(1)
	if w := request("POST", voicePath, "image/png", silentWAV(), "owner", nil); w.Code != 403 {
		t.Fatalf("image content type accepted for voice: %d", w.Code)
	}
	if w := request("POST", voicePath, "audio/wav", []byte("not audio at all"), "owner", nil); w.Code != 400 {
		t.Fatalf("non-audio accepted: %d", w.Code)
	}
	w := request("POST", voicePath, "audio/wav", silentWAV(), "owner", nil)
	if w.Code != 201 {
		t.Fatalf("voice: %d %s", w.Code, w.Body.String())
	}
	var voice struct {
		Event tasteEvent `json:"event"`
	}
	json.Unmarshal(w.Body.Bytes(), &voice)
	if !voice.Event.HasAudio || voice.Event.AudioMs == nil || *voice.Event.AudioMs != 1200 || objects.count("trumpet-taste/") != 1 {
		t.Fatalf("voice event %+v objects=%d", voice.Event, objects.count("trumpet-taste/"))
	}
	if w := request("POST", voicePath, "audio/wav", silentWAV(), "owner", nil); w.Code != 200 || objects.count("trumpet-taste/") != 1 {
		t.Fatalf("voice replay: %d objects=%d", w.Code, objects.count("trumpet-taste/"))
	}
	media := request("GET", "/v1/trumpets/taste/voice/"+jsonNumber(voice.Event.ID), "", nil, "owner", nil)
	if media.Code != 302 || !strings.HasPrefix(media.Header().Get("Location"), "https://signed.invalid/") {
		t.Fatalf("voice redirect: %d %v", media.Code, media.Header())
	}
	if other := request("GET", "/v1/trumpets/taste/voice/"+jsonNumber(voice.Event.ID), "", nil, "intruder", nil); other.Code != 404 {
		t.Fatalf("voice leaked: %d", other.Code)
	}
	var stored int
	if err := isolated.QueryRow(ctx, `SELECT count(*) FROM trumpet_taste_events WHERE user_id=(SELECT id FROM app_users WHERE auth_subject='owner')`).Scan(&stored); err != nil || stored != 8 { // 6 + the graph check's choice and its undo
		t.Fatalf("stored events = %d, %v", stored, err)
	}

	// Review fix: a client that disconnects after the commit must not lose the
	// stored voice note's audio (the response uses a non-cancelled context).
	cancelCtx, cancel := context.WithCancel(ctx)
	tasteAfterCommitHook = cancel
	dropped := httptest.NewRequest("POST", "/v1/trumpets/taste/voice?clientId=voice-0002&listingId="+id(2), bytes.NewReader(silentWAV())).WithContext(cancelCtx)
	dropped.Header.Set("Content-Type", "audio/wav")
	dropped.Header.Set("X-Jazz-Gateway-Key", "gateway")
	dropped.Header.Set("X-Jazz-User", "owner")
	dw := httptest.NewRecorder()
	routes.ServeHTTP(dw, dropped)
	tasteAfterCommitHook = nil
	if dw.Code != 201 || objects.count("trumpet-taste/") != 2 {
		t.Fatalf("committed voice note lost its audio after a disconnect: %d objects=%d %s", dw.Code, objects.count("trumpet-taste/"), dw.Body.String())
	}
	// Review fix: an undone voice note is no longer served and its audio is removed.
	call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "undo-voice", "kind": "undo", "undoes": voice.Event.ID}, "owner", 201)
	if gone := request("GET", "/v1/trumpets/taste/voice/"+jsonNumber(voice.Event.ID), "", nil, "owner", nil); gone.Code != 404 {
		t.Fatalf("undone voice note still served: %d", gone.Code)
	}
	if objects.count("trumpet-taste/") != 1 {
		t.Fatalf("undone voice audio not deleted: %d", objects.count("trumpet-taste/"))
	}

	// Owner preference statements: versioned, owner-scoped, same write rules.
	if empty := call("GET", "/v1/trumpets/taste/preferences", nil, "owner", 200); empty["revision"].(float64) != 0 {
		t.Fatalf("preferences before any save %v", empty)
	}
	prefs := map[string]any{"statements": []string{"Cool horn, not dark horn (fixture)"}, "likedMakers": []string{"Taylor"}, "dislikedMakers": []string{}, "likedTraits": []string{"raw brass"}, "dislikedTraits": []string{"gold plate"}, "priceGuidance": "Fixture: value matters", "expectedRevision": 0}
	saved := call("PUT", "/v1/trumpets/taste/preferences", prefs, "owner", 200)
	if saved["revision"].(float64) != 1 || saved["likedTraitKeys"].([]any)[0] != "finish:raw brass" {
		t.Fatalf("saved %v", saved)
	}
	call("PUT", "/v1/trumpets/taste/preferences", prefs, "owner", 409) // stale expectedRevision
	prefs["expectedRevision"] = 1
	prefs["statements"] = []string{"Cool horn, not dark horn (fixture)", "Unusual engineering (fixture)"}
	if again := call("PUT", "/v1/trumpets/taste/preferences", prefs, "owner", 200); again["revision"].(float64) != 2 {
		t.Fatal("second version")
	}
	if w := call("GET", "/v1/trumpets/taste", nil, "owner", 200)["watermark"].(map[string]any); w["preferences"].(float64) != 2 {
		t.Fatalf("watermark after two preference versions: %v", w)
	}
	if other := call("GET", "/v1/trumpets/taste/preferences", nil, "intruder", 200); other["revision"].(float64) != 0 {
		t.Fatal("preferences leaked across owners")
	}
	pbody, _ := json.Marshal(prefs)
	if w := request("PUT", "/v1/trumpets/taste/preferences", "text/plain", pbody, "owner", nil); w.Code != 403 {
		t.Fatalf("text/plain preferences write: %d", w.Code)
	}
	if w := request("PUT", "/v1/trumpets/taste/preferences", "application/json", pbody, "owner", map[string]string{"Sec-Fetch-Site": "cross-site"}); w.Code != 403 {
		t.Fatalf("cross-site preferences write: %d", w.Code)
	}
	if panel := call("GET", "/v1/trumpets/taste", nil, "owner", 200); len(panel["preferences"].(map[string]any)["statements"].([]any)) != 2 {
		t.Fatalf("taste panel preferences %v", panel["preferences"])
	}

	// Machine API: the runner reads weights and uncertainties.
	token := strings.Repeat("t", 40)
	setMachineTestToken(t, token, "owner")
	machine := func(path string) map[string]any {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		out := map[string]any{}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	profile := machine("/v1/trumpets/machine/profile")
	summary, ok := profile["taste"].(map[string]any)
	if !ok || len(summary["weights"].([]any)) == 0 {
		t.Fatalf("profile taste %v", profile["taste"])
	}
	weight := summary["weights"].([]any)[0].(map[string]any)
	for _, key := range []string{"feature", "kind", "label", "weight", "sd", "n"} {
		if _, ok := weight[key]; !ok {
			t.Fatalf("weight lacks %s: %v", key, weight)
		}
	}
	encoded, _ := json.Marshal(summary)
	if strings.Contains(string(encoded), "too expensive") || strings.Contains(string(encoded), id(1)) {
		t.Fatal("machine summary carries notes or listing ids")
	}
	if direct := machine("/v1/trumpets/machine/taste"); direct["pairs"].(float64) != 1 || direct["statedPreferences"].(float64) != 3 {
		t.Fatalf("machine taste %v", direct)
	}
	if stated := profile["preferences"].(map[string]any); stated["priceGuidance"] != "Fixture: value matters" || len(stated["dislikedTraitKeys"].([]any)) != 1 {
		t.Fatalf("profile preferences %v", stated)
	}
	// Verification provenance is stored per observation and shown on the listing.
	post := func(body any) int {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/v1/trumpets/machine/runs", bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w.Code
	}
	offer := map[string]any{"maker": "Fixturecraft", "model": "Zephyr", "title": "Fixturecraft Zephyr · FIXTURE", "url": "https://ebay.com/itm/900000000003", "source": "eBay", "price": 4950, "currency": "USD", "status": "active", "searchScore": 70, "verifiedVia": "search-provider", "evidence": "Search-provider verification (fixture)"}
	if code := post(map[string]any{"externalId": "provenance-1", "kind": "search", "status": "succeeded", "sources": []any{map[string]any{"source": "eBay via web search", "status": "checked", "candidates": 1}}, "listings": []any{offer}}); code != 200 {
		t.Fatalf("provenance run: %d", code)
	}
	offer["verifiedVia"] = "rumour"
	if code := post(map[string]any{"externalId": "provenance-2", "kind": "search", "status": "partial", "listings": []any{offer}}); code != 400 {
		t.Fatalf("invalid provenance accepted: %d", code)
	}
	// A page the runner fetched and a model read (grounded in the page text).
	read := map[string]any{"maker": "Fixturecraft", "model": "Mistral", "title": "Fixturecraft Mistral · FIXTURE", "url": "https://dealer.fixtures.invalid/horn/mistral", "source": "Fixture dealer", "price": 3100, "currency": "EUR", "status": "active", "searchScore": 66, "verifiedVia": "page-llm", "evidence": "LLM page reading, grounded in the fetched page (fixture)"}
	if code := post(map[string]any{"externalId": "provenance-3", "kind": "recheck", "status": "succeeded", "sources": []any{map[string]any{"source": "Live pages / dealer.fixtures.invalid", "domain": "dealer.fixtures.invalid", "status": "checked", "candidates": 1}}, "listings": []any{read}}); code != 200 {
		t.Fatalf("page-llm provenance run: %d", code)
	}
	pageRead := false
	found := false
	for _, l := range call("GET", "/v1/trumpets/listings", nil, "owner", 200)["listings"].([]any) {
		m := l.(map[string]any)
		if m["url"] == "https://ebay.com/itm/900000000003" {
			found = true
			history := m["priceHistory"].([]any)
			if m["verifiedVia"] != "search-provider" || history[len(history)-1].(map[string]any)["verifiedVia"] != "search-provider" {
				t.Fatalf("provenance not stored: %v", m)
			}
		} else if m["url"] == "https://dealer.fixtures.invalid/horn/mistral" {
			pageRead = m["verifiedVia"] == "page-llm"
		} else if m["verifiedVia"] == "search-provider" {
			t.Fatal("provenance leaked to another listing")
		}
	}
	if !found {
		t.Fatal("provenance listing missing")
	}
	if !pageRead {
		t.Fatal("page-llm provenance not stored")
	}

	// An operator can seed the owner's preferences with the machine token.
	seed, _ := json.Marshal(map[string]any{"statements": []string{"Seeded fixture line"}})
	r := httptest.NewRequest("PUT", "/v1/trumpets/machine/preferences", bytes.NewReader(seed))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	sw := httptest.NewRecorder()
	routes.ServeHTTP(sw, r)
	if sw.Code != 200 || !strings.Contains(sw.Body.String(), `"revision":3`) {
		t.Fatalf("machine seed: %d %s", sw.Code, sw.Body.String())
	}
}

func jsonNumber(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
