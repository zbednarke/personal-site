package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fictional fixtures only (perfCandidate: fictional makers' models on .test
// dealers).
func TestTrumpetTasteSkipInvestigation(t *testing.T) {
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
	schema := "skip_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	for i := 0; i < 2; i++ { // migrations replay on every startup
		if err = migrate(ctx, isolated); err != nil {
			t.Fatalf("migration pass %d: %v", i, err)
		}
	}
	token := strings.Repeat("s", 40)
	sum := sha256.Sum256([]byte(token))
	t.Setenv("TRUMPETS_MACHINE_TOKEN_SHA256", hex.EncodeToString(sum[:]))
	t.Setenv("TRUMPETS_MACHINE_USER", "owner")
	app := &application{db: isolated, cfg: config{GatewayKey: "gateway"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), objects: newMemoryObjects("https://signed.invalid/")}
	routes := app.routes()
	call := func(method, path string, input any, machine string, want int) map[string]any {
		t.Helper()
		var body io.Reader
		if input != nil {
			raw, _ := json.Marshal(input)
			body = bytes.NewReader(raw)
		}
		r := httptest.NewRequest(method, path, body)
		if input != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		if machine != "" {
			r.Header.Set("Authorization", "Bearer "+machine)
		} else {
			r.Header.Set("X-Jazz-Gateway-Key", "gateway")
			r.Header.Set("X-Jazz-User", "owner")
		}
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	owner, err := app.userID(context.WithValue(ctx, userSubjectKey, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	tx, err := isolated.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		c := perfCandidate(i, float64(2500+100*i))
		if i == 7 {
			p := 9000.0
			c.Price = &p
		}
		lid, err := upsertTrumpet(ctx, tx, owner, c, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, lid.String())
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	str := func(m map[string]any, keys ...string) any {
		var v any = m
		for _, k := range keys {
			v = v.(map[string]any)[k]
		}
		return v
	}
	pairsWith := func(state map[string]any, id string) int {
		n := 0
		for _, p := range state["pairs"].([]any) {
			if p.(map[string]any)["a"] == id || p.(map[string]any)["b"] == id {
				n++
			}
		}
		return n
	}
	queued := func(state map[string]any, id string) bool {
		for _, q := range state["queue"].([]any) {
			if q.(map[string]any)["listingId"] == id {
				return true
			}
		}
		return false
	}
	taste := call("GET", "/v1/trumpets/taste", nil, "", 200)
	if taste["pairEligible"].(float64) != 8 {
		t.Fatalf("eligible %v", taste["pairEligible"])
	}
	// The same state gives the same pairs and queue (the page relies on a
	// stable order). Which pair comes first depends on the listings' random
	// ids, so the test takes its pair from the answer, and the listing
	// outside it from what is left.
	if again := call("GET", "/v1/trumpets/taste", nil, "", 200); fmt.Sprint(again["pairs"], again["queue"]) != fmt.Sprint(taste["pairs"], taste["queue"]) {
		t.Fatalf("pairs differ for the same state:\n%v\n%v", taste["pairs"], again["pairs"])
	}
	top := taste["pairs"].([]any)[0].(map[string]any)
	a, b := top["a"].(string), top["b"].(string)
	outside := ""
	for i := len(ids) - 1; i >= 0 && outside == ""; i-- {
		if ids[i] != a && ids[i] != b {
			outside = ids[i]
		}
	}
	// A note saved for the pair just before the skip belongs to it.
	call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "skip-note-01", "kind": "note", "mode": "pair", "listingId": a, "otherListingId": b, "note": "One of these is a cornet?"}, "", 201)
	skip := map[string]any{"clientId": "skip-00001", "kind": "pair", "mode": "pair", "listingId": a, "otherListingId": b, "action": "skip", "note": "Can't\x00 compare", "meta": map[string]any{"hadPhoto": true, "skipReasons": []string{"not_bb"}}}
	skipped := call("POST", "/v1/trumpets/taste/events", skip, "", 201)
	if again := call("POST", "/v1/trumpets/taste/events", skip, "", 200); again["replayed"] != true {
		t.Fatal("skip replay")
	}
	skipID := str(skipped, "event", "id").(float64)
	if str(skipped, "event", "action") != "skip" || str(skipped, "event", "note") != "Can't compare" {
		t.Fatalf("skip event %v", skipped["event"])
	}
	if str(skipped, "model", "pairs").(float64) != 0 || str(skipped, "model", "confidence").(float64) != 0 || str(skipped, "pairGraph", "components").(float64) != 8 {
		t.Fatalf("a skip is not a reaction or an edge: model %v graph %v", skipped["model"], skipped["pairGraph"])
	}
	for _, p := range skipped["pairs"].([]any) {
		pp := p.(map[string]any)
		if (pp["a"] == a && pp["b"] == b) || (pp["a"] == b && pp["b"] == a) {
			t.Fatal("skipped pair offered again")
		}
	}
	if str(skipped, "watermark", "events").(float64) != skipID {
		t.Fatalf("watermark %v", skipped["watermark"])
	}
	if v := skipped["skips"].(map[string]any); v["pending"].(float64) != 1 || v["items"].([]any)[0].(map[string]any)["finding"] != nil {
		t.Fatalf("skips view %v", v)
	}

	// Machine endpoints are guarded by the machine token.
	call("GET", "/v1/trumpets/machine/taste/skips", nil, strings.Repeat("x", 40), 401)
	call("POST", "/v1/trumpets/machine/taste/skips/findings", map[string]any{"eventId": skipID, "cause": "unclear"}, strings.Repeat("x", 40), 401)
	listed := call("GET", "/v1/trumpets/machine/taste/skips", nil, token, 200)
	items := listed["skips"].([]any)
	if len(items) != 1 || listed["pending"].(float64) != 1 {
		t.Fatalf("listed %v", listed)
	}
	item := items[0].(map[string]any)
	if item["note"] != "Can't compare" || item["reasons"].([]any)[0] != "not_bb" || len(item["notes"].([]any)) != 1 || str(item, "a", "id") != a || str(item, "b", "images").(float64) != 1 || str(item, "b", "url") == "" {
		t.Fatalf("skip for the runner %v", item)
	}

	// A finding: b is not a Bb trumpet. Excluded everywhere, reversibly.
	finding := map[string]any{"eventId": skipID, "cause": "wrong_category", "side": "b", "llmCause": "wrong_category", "llmValid": true, "mode": "explain",
		"explanation": "The right listing\x00 is a cornet.", "checks": []map[string]any{{"code": "wrong_category", "side": "b", "detail": "title says cornet"}},
		"exclude": []map[string]any{{"listingId": b, "scope": "all", "reason": "wrong_category", "detail": "cornet"}, {"listingId": outside, "scope": "all", "reason": "wrong_category"}}}
	// A listing outside the pair, or not a listing id at all, is refused.
	call("POST", "/v1/trumpets/machine/taste/skips/findings", finding, token, 400)
	call("POST", "/v1/trumpets/machine/taste/skips/findings", map[string]any{"eventId": skipID, "cause": "wrong_category", "exclude": []map[string]any{{"listingId": "not-a-uuid", "scope": "all", "reason": "wrong_category"}}}, token, 400)
	call("POST", "/v1/trumpets/machine/taste/skips/findings", map[string]any{"eventId": skipID, "cause": "stale", "recheck": []string{outside}}, token, 400)
	finding["exclude"] = finding["exclude"].([]map[string]any)[:1]
	saved := call("POST", "/v1/trumpets/machine/taste/skips/findings", finding, token, 201)
	f := saved["finding"].(map[string]any)
	if f["explanation"] != "The right listing is a cornet." {
		t.Fatalf("explanation %q", f["explanation"])
	}
	actions := f["actions"].([]any)
	if len(actions) != 1 || actions[0].(map[string]any)["kind"] != "exclude" {
		t.Fatalf("actions %v", actions)
	}
	if replay := call("POST", "/v1/trumpets/machine/taste/skips/findings", finding, token, 200); replay["replayed"] != true {
		t.Fatal("finding replay")
	}
	if left := call("GET", "/v1/trumpets/machine/taste/skips", nil, token, 200); len(left["skips"].([]any)) != 0 {
		t.Fatal("an investigated skip is listed again")
	}
	var title string
	var priceAfter float64
	if err := isolated.QueryRow(ctx, `SELECT title,price FROM trumpet_listings WHERE id=$1`, b).Scan(&title, &priceAfter); err != nil || !strings.HasPrefix(title, "Fictional") || priceAfter == 0 {
		t.Fatalf("a finding changed listing facts: %q %v %v", title, priceAfter, err)
	}
	state := call("GET", "/v1/trumpets/taste", nil, "", 200)
	if state["pairEligible"].(float64) != 7 || pairsWith(state, b) != 0 || queued(state, b) {
		t.Fatalf("excluded listing still offered: eligible %v pairs %d queued %v", state["pairEligible"], pairsWith(state, b), queued(state, b))
	}
	view := state["skips"].(map[string]any)
	if view["items"].([]any)[0].(map[string]any)["finding"].(map[string]any)["cause"] != "wrong_category" {
		t.Fatalf("view %v", view)
	}
	exclusion := view["exclusions"].([]any)[0].(map[string]any)
	if exclusion["active"] != true || exclusion["inForce"] != true || exclusion["listingId"] != b || exclusion["scope"] != "all" {
		t.Fatalf("exclusion %v", exclusion)
	}
	// Undoing the skip takes its exclusion with it: b is back in Head to
	// head and Swipe (the row stays; it no longer applies).
	undoSkip := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "skip-undo-0", "kind": "undo", "undoes": skipID}, "", 201)
	if undoSkip["pairEligible"].(float64) != 8 || !queued(undoSkip, b) {
		t.Fatalf("an undone skip's exclusion still applies: eligible %v queued %v", undoSkip["pairEligible"], queued(undoSkip, b))
	}
	if x := str(undoSkip, "skips").(map[string]any)["exclusions"].([]any)[0].(map[string]any); x["inForce"] != false || x["lifted"] != "its skip was undone" {
		t.Fatalf("exclusion after undo %v", x)
	}
	// Skip the pair again (not_bb) and investigate: excluded again, as a
	// new exclusion of the live skip.
	reskip := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "skip-00001b", "kind": "pair", "mode": "pair", "listingId": a, "otherListingId": b, "action": "skip", "meta": map[string]any{"skipReasons": []string{"not_bb"}}}, "", 201)
	finding["eventId"] = str(reskip, "event", "id")
	call("POST", "/v1/trumpets/machine/taste/skips/findings", finding, token, 201)
	state = call("GET", "/v1/trumpets/taste", nil, "", 200)
	if state["pairEligible"].(float64) != 7 || queued(state, b) {
		t.Fatal("the re-skipped listing is not excluded again")
	}
	var inForce map[string]any
	for _, x := range str(state, "skips").(map[string]any)["exclusions"].([]any) {
		if x.(map[string]any)["inForce"] == true {
			inForce = x.(map[string]any)
		}
	}
	exclusion = inForce
	// The owner puts it back: offered again, and a later finding does not
	// re-exclude it.
	restored := call("PUT", fmt.Sprintf("/v1/trumpets/taste/exclusions/%v", exclusion["id"]), map[string]any{"active": false}, "", 200)
	if restored["pairEligible"].(float64) != 8 || !queued(restored, b) {
		t.Fatalf("restored listing not offered: %v", restored["pairEligible"])
	}
	call("PUT", "/v1/trumpets/taste/exclusions/99999", map[string]any{"active": false}, "", 404)
	call("PUT", fmt.Sprintf("/v1/trumpets/taste/exclusions/%v", exclusion["id"]), map[string]any{}, "", 400)
	other := ids[0]
	if other == a || other == b {
		other = ids[1]
	}
	if other == a || other == b {
		other = ids[2]
	}
	second := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "skip-00002", "kind": "pair", "mode": "pair", "listingId": other, "otherListingId": b, "action": "skip", "meta": map[string]any{"skipReasons": []string{"not_bb"}}}, "", 201)
	again := call("POST", "/v1/trumpets/machine/taste/skips/findings", map[string]any{"eventId": str(second, "event", "id"), "cause": "wrong_category",
		"exclude": []map[string]any{{"listingId": b, "scope": "all", "reason": "wrong_category"}}}, token, 201)
	if k := again["finding"].(map[string]any)["actions"].([]any)[0].(map[string]any)["kind"]; k != "kept" {
		t.Fatalf("an owner-restored listing was excluded again: %v", k)
	}
	// Without the owner's own "Not a Bb trumpet" or "Duplicate", a wrong
	// category or duplicate finding excludes nothing.
	third := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "skip-00002b", "kind": "pair", "mode": "pair", "listingId": other, "otherListingId": ids[6], "action": "skip"}, "", 201)
	plain := call("POST", "/v1/trumpets/machine/taste/skips/findings", map[string]any{"eventId": str(third, "event", "id"), "cause": "duplicate",
		"exclude": []map[string]any{{"listingId": ids[6], "scope": "all", "reason": "duplicate"}}, "fixText": tasteSkipFixLines[3] + "\nFictional"}, token, 201)
	if act := plain["finding"].(map[string]any)["actions"].([]any); act[0].(map[string]any)["kind"] != "note" || act[1].(map[string]any)["kind"] != "fix" {
		t.Fatalf("unconfirmed duplicate: %v", act)
	}

	// Lessons: one incomparable price gap is recorded; pair selection uses it
	// only once a second one agrees.
	lessonSkip := func(client, x string) (float64, map[string]any) {
		e := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": client, "kind": "pair", "mode": "pair", "listingId": x, "otherListingId": ids[7], "action": "skip", "meta": map[string]any{"skipReasons": []string{"not_comparable"}}}, "", 201)
		id := str(e, "event", "id").(float64)
		out := call("POST", "/v1/trumpets/machine/taste/skips/findings", map[string]any{"eventId": id, "cause": "incomparable", "dimension": "price", "lesson": true}, token, 201)
		return id, out["finding"].(map[string]any)
	}
	l1, f1 := lessonSkip("skip-00003", ids[3])
	if f1["actions"].([]any)[0].(map[string]any)["kind"] != "lesson" {
		t.Fatalf("lesson actions %v", f1["actions"])
	}
	state = call("GET", "/v1/trumpets/taste", nil, "", 200)
	if rules := str(state, "skips", "rules").([]any); len(rules) != 0 {
		t.Fatalf("one lesson is already a rule: %v", rules)
	}
	_, _ = lessonSkip("skip-00004", ids[4])
	state = call("GET", "/v1/trumpets/taste", nil, "", 200)
	rules := str(state, "skips", "rules").([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["dimension"] != "price" || rules[0].(map[string]any)["lessons"].(float64) != 2 {
		t.Fatalf("rules %v", rules)
	}
	lessons := str(state, "skips", "lessons").([]any)
	if len(lessons) != 2 || lessons[0].(map[string]any)["inEffect"] != true {
		t.Fatalf("lessons %v", lessons)
	}
	// Undoing one of the skips takes its lesson out of effect.
	call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "skip-undo-1", "kind": "undo", "undoes": l1}, "", 201)
	state = call("GET", "/v1/trumpets/taste", nil, "", 200)
	if rules := str(state, "skips", "rules").([]any); len(rules) != 0 {
		t.Fatalf("an undone skip still teaches: %v", rules)
	}
	// Switching a lesson off works the same way.
	off := call("PUT", fmt.Sprintf("/v1/trumpets/taste/lessons/%v", lessons[1].(map[string]any)["id"]), map[string]any{"active": false}, "", 200)
	if l := str(off, "skips", "lessons").([]any)[1].(map[string]any); l["active"] != false {
		t.Fatalf("lesson %v", l)
	}
	// A finding for an undone skip is refused.
	call("POST", "/v1/trumpets/machine/taste/skips/findings", map[string]any{"eventId": l1, "cause": "unclear"}, token, 404)

	// A stale-looking listing gets a recheck: due again although checked today.
	due := func() map[string]bool {
		out := map[string]bool{}
		for _, l := range call("GET", "/v1/trumpets/machine/due", nil, token, 200)["listings"].([]any) {
			out[l.(map[string]any)["id"].(string)] = true
		}
		return out
	}
	if _, err := isolated.Exec(ctx, `UPDATE trumpet_listings SET last_checked=now() WHERE user_id=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if due()[ids[5]] {
		t.Fatal("checked today, yet due")
	}
	stale := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "skip-00005", "kind": "pair", "mode": "pair", "listingId": ids[5], "otherListingId": ids[6], "action": "skip"}, "", 201)
	call("POST", "/v1/trumpets/machine/taste/skips/findings", map[string]any{"eventId": str(stale, "event", "id"), "cause": "data_problem", "side": "a",
		"recheck": []string{ids[5]}, "fixText": tasteSkipFixLines[1] + "\nFictional listing (dealer5.test)"}, token, 201)
	if !due()[ids[5]] || due()[ids[6]] {
		t.Fatal("the requested recheck is not due (or the other side is)")
	}
	// The data problem is on today's fix list, grouped by its fixed first line.
	digest, err := app.computeFixDigest(ctx, owner, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range digest.Items {
		if it.Key == "checks:skips:"+tasteSkipFixLines[1] && it.Count == 1 && it.Label == tasteSkipFixLines[1] {
			found = true
		}
	}
	if !found {
		t.Fatalf("fix list items %+v", digest.Items)
	}
	// NUL in any string never fails the request.
	nul := call("POST", "/v1/trumpets/taste/events", map[string]any{"clientId": "skip-00006", "kind": "pair", "mode": "pair", "listingId": ids[0], "otherListingId": ids[6], "action": "skip"}, "", 201)
	clean := call("POST", "/v1/trumpets/machine/taste/skips/findings", map[string]any{"eventId": str(nul, "event", "id"), "cause": "unclear", "mode": "ex\x00plain",
		"explanation": strings.Repeat("\x00é", 900), "checks": []map[string]any{{"code": "note", "detail": "a\x00b"}}}, token, 201)
	cf := clean["finding"].(map[string]any)
	if strings.Contains(fmt.Sprint(cf), "\x00") || len(cf["explanation"].(string)) > 1000 || cf["mode"] != "explain" {
		t.Fatalf("not bounded: %v", cf)
	}
}
