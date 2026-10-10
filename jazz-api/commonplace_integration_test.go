package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// All data here is fictional (see testdata/commonplace).

type cpHarness struct {
	t       *testing.T
	routes  http.Handler
	objects *memoryObjects
	app     *application
}

func newCommonplaceHarness(t *testing.T) *cpHarness {
	db := isolatedTrumpetDB(t) // migrates twice: every migration must be replay-safe
	objects := newMemoryObjects("https://signed.test/")
	app := &application{db: db, cfg: config{GatewayKey: "gateway"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), objects: objects}
	return &cpHarness{t: t, routes: app.routes(), objects: objects, app: app}
}

func (h *cpHarness) raw(method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("X-Jazz-Gateway-Key", "gateway")
	r.Header.Set("X-Jazz-User", "owner")
	for k, v := range headers {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.routes.ServeHTTP(w, r)
	return w
}

func (h *cpHarness) call(method, path string, input any, want int) map[string]any {
	h.t.Helper()
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	w := h.raw(method, path, body, map[string]string{"Content-Type": "application/json"})
	if w.Code != want {
		h.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	for header, value := range map[string]string{"Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow, noarchive", "Referrer-Policy": "no-referrer"} {
		if w.Header().Get(header) != value {
			h.t.Fatalf("%s %s: %s=%q", method, path, header, w.Header().Get(header))
		}
	}
	out := map[string]any{}
	if w.Body.Len() > 0 {
		json.Unmarshal(w.Body.Bytes(), &out)
	}
	return out
}

type cpPart struct {
	field, filename, contentType string
	body                         []byte
}

func (h *cpHarness) multipart(path string, parts []cpPart, want int) map[string]any {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		hdr := textproto.MIMEHeader{}
		disp := fmt.Sprintf(`form-data; name="%s"`, p.field)
		if p.filename != "" {
			disp += fmt.Sprintf(`; filename="%s"`, p.filename)
		}
		hdr.Set("Content-Disposition", disp)
		if p.contentType != "" {
			hdr.Set("Content-Type", p.contentType)
		}
		w, _ := mw.CreatePart(hdr)
		w.Write(p.body)
	}
	mw.Close()
	w := h.raw("POST", path, buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType(), "X-Commonplace-Upload": "1"})
	if w.Code != want {
		h.t.Fatalf("POST %s: %d %s", path, w.Code, w.Body.String())
	}
	out := map[string]any{}
	json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func list(v any) []any {
	if v == nil {
		return nil
	}
	return v.([]any)
}

func obj(v any) map[string]any { return v.(map[string]any) }

func TestCommonplaceAuthAndPrivacy(t *testing.T) {
	h := newCommonplaceHarness(t)
	base := "/v1/commonplace/moments"
	if w := h.raw("GET", base, nil, map[string]string{"X-Jazz-Gateway-Key": ""}); w.Code != 401 {
		t.Fatal("missing gateway key must be rejected", w.Code)
	}
	if w := h.raw("GET", base, nil, map[string]string{"X-Jazz-User": ""}); w.Code != 401 {
		t.Fatal("missing user must be rejected", w.Code)
	}
	if w := h.raw("POST", base, []byte(`{}`), map[string]string{"Content-Type": "text/plain"}); w.Code != 403 {
		t.Fatal("simple (non-preflighted) bodies must be rejected", w.Code)
	}
	if w := h.raw("POST", base, []byte(`{}`), map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); w.Code != 403 {
		t.Fatal("cross-site writes must be rejected", w.Code)
	}
	if w := h.raw("POST", base, []byte(`{}`), map[string]string{"Content-Type": "application/json", "Origin": "https://attacker.example"}); w.Code != 403 {
		t.Fatal("foreign origins must be rejected", w.Code)
	}
	if w := h.raw("POST", "/v1/commonplace/import/bundle", []byte("--x--"), map[string]string{"Content-Type": "multipart/form-data; boundary=x"}); w.Code != 403 {
		t.Fatal("multipart imports need the X-Commonplace-Upload header (forces a CORS preflight)", w.Code)
	}
	if w := h.raw("POST", base+"/"+uuid.NewString()+"/artifacts", []byte("x"), map[string]string{"Content-Type": "text/html"}); w.Code != 403 {
		t.Fatal("raw uploads must be media types", w.Code)
	}
	// Another user's data is invisible.
	moment := h.call("POST", base, map[string]any{"clientCaptureId": uuid.NewString(), "text": "Fixture note for the owner only."}, 201)
	id := obj(moment["moment"])["id"].(string)
	if w := h.raw("GET", base+"/"+id, nil, map[string]string{"X-Jazz-User": "someone-else"}); w.Code != 404 {
		t.Fatal("other users must not read a Moment", w.Code)
	}
}

func TestCommonplaceCaptureEditAndMargin(t *testing.T) {
	h := newCommonplaceHarness(t)
	base := "/v1/commonplace"
	capture := uuid.NewString()
	first := h.call("POST", base+"/moments", map[string]any{"clientCaptureId": capture, "text": "Sample Friend said the moon is a lantern someone forgot.", "timezone": "Europe/Lisbon"}, 201)
	m := obj(first["moment"])
	id := m["id"].(string)
	if m["source"] != "text" || m["kind"] != "conversation" || m["timezone"] != "Europe/Lisbon" || len(list(first["artifacts"])) != 1 {
		t.Fatal(first)
	}
	again := h.call("POST", base+"/moments", map[string]any{"clientCaptureId": capture, "text": "different"}, 200)
	if obj(again["moment"])["id"] != id || obj(list(again["artifacts"])[0])["text"] != "Sample Friend said the moon is a lantern someone forgot." {
		t.Fatal("same capture id must return the original Moment unchanged", again)
	}
	link := h.call("POST", base+"/moments", map[string]any{"clientCaptureId": uuid.NewString(), "url": "https://example.com/fixture"}, 201)
	if obj(link["moment"])["source"] != "link" || obj(list(link["artifacts"])[0])["url"] != "https://example.com/fixture" {
		t.Fatal(link)
	}
	h.call("POST", base+"/moments", map[string]any{"clientCaptureId": uuid.NewString(), "url": "javascript:alert(1)"}, 422)
	h.call("POST", base+"/moments", map[string]any{"clientCaptureId": uuid.NewString(), "timezone": "Not/AZone"}, 422)

	// Raw image upload (validated like inspiration images), then the signed redirect.
	png := syntheticPNG(t, 40, 80, 120)
	w := h.raw("POST", base+"/moments/"+id+"/artifacts", png, map[string]string{"Content-Type": "image/png", "X-File-Name": "fixture%20shot.png"})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var up struct{ Artifact cpArtifact }
	json.Unmarshal(w.Body.Bytes(), &up)
	if up.Artifact.Kind != "image" || !up.Artifact.Stored || *up.Artifact.Width != 40 || up.Artifact.OriginalName != "fixture shot.png" || !strings.HasPrefix(up.Artifact.Key, "upload-") {
		t.Fatalf("%+v", up.Artifact)
	}
	if h.objects.count("commonplace/") != 1 {
		t.Fatal("object not stored under commonplace/")
	}
	if w := h.raw("POST", base+"/moments/"+id+"/artifacts", []byte("not an image"), map[string]string{"Content-Type": "image/png"}); w.Code != 400 {
		t.Fatal("bad image accepted", w.Code)
	}
	media := h.raw("GET", base+"/media/"+up.Artifact.ID.String(), nil, nil)
	if media.Code != 302 || !strings.HasPrefix(media.Header().Get("Location"), "https://signed.test/commonplace/") {
		t.Fatal(media.Code, media.Header())
	}
	if w := h.raw("GET", base+"/media/"+up.Artifact.ID.String(), nil, map[string]string{"X-Jazz-User": "someone-else"}); w.Code != 404 {
		t.Fatal("media of another user", w.Code)
	}

	// Metadata afterwards, with revision checks.
	patched := h.call("PATCH", base+"/moments/"+id, map[string]any{"expectedRevision": 1, "kind": "dream", "why": "Fixture why", "occurredAt": "2026-02-01T03:12:00Z",
		"people": []map[string]any{{"name": "Sample Friend", "role": "sender"}, {"name": "Example Cousin", "role": "mentioned"}}}, 200)
	pm := obj(patched["moment"])
	if pm["revision"].(float64) != 2 || pm["kind"] != "dream" || len(list(pm["people"])) != 2 {
		t.Fatal(patched)
	}
	conflict := h.call("PATCH", base+"/moments/"+id, map[string]any{"expectedRevision": 1, "title": "stale"}, 409)
	if obj(obj(conflict["current"])["moment"])["revision"].(float64) != 2 {
		t.Fatal(conflict)
	}
	h.call("PATCH", base+"/moments/"+id, map[string]any{"expectedRevision": 2, "endedAt": "2026-01-01T00:00:00Z"}, 422)
	// Same name resolves to the same person.
	other := h.call("POST", base+"/moments", map[string]any{"clientCaptureId": uuid.NewString(), "text": "Second fixture"}, 201)
	otherID := obj(other["moment"])["id"].(string)
	h.call("PATCH", base+"/moments/"+otherID, map[string]any{"expectedRevision": 1, "people": []map[string]any{{"name": "sample friend"}}}, 200)
	people := list(h.call("GET", base+"/people", nil, 200)["people"])
	if len(people) != 2 || obj(people[0])["moments"].(float64) != 2 {
		t.Fatal(people)
	}
	friend := obj(people[0])["id"].(string)
	if p := obj(h.call("PATCH", base+"/people/"+friend, map[string]any{"special": true, "aliases": []string{"SF"}}, 200)["person"]); p["special"] != true {
		t.Fatal(p)
	}
	filtered := list(h.call("GET", base+"/moments?person="+friend+"&kind=dream", nil, 200)["moments"])
	if len(filtered) != 1 || obj(filtered[0])["id"] != id {
		t.Fatal(filtered)
	}

	// Lines and the owner's own notes in ink.
	line := obj(h.call("POST", base+"/moments/"+id+"/lines", map[string]any{"speaker": friend, "text": "the moon is a lantern someone forgot", "artifactId": up.Artifact.ID, "rect": []float64{5, 10, 60, 8}}, 201)["line"])
	if line["speaker"] != friend || line["speakerName"] != "Sample Friend" || len(list(line["rect"])) != 4 {
		t.Fatal(line)
	}
	lineID := line["id"].(string)
	edited := obj(h.call("PATCH", base+"/lines/"+lineID, map[string]any{"expectedRevision": 1, "dayLabel": "Sunday"}, 200)["line"])
	if edited["dayLabel"] != "Sunday" || edited["revision"].(float64) != 2 {
		t.Fatal(edited)
	}
	h.call("PATCH", base+"/lines/"+lineID, map[string]any{"expectedRevision": 1, "text": "stale"}, 409)
	note := obj(h.call("POST", base+"/moments/"+id+"/annotations", map[string]any{"title": "Lanterns again", "lineId": lineID}, 201)["annotation"])
	if note["state"] != "ink" || note["author"] != "owner" {
		t.Fatal(note)
	}
	box := obj(h.call("POST", base+"/moments/"+id+"/annotations", map[string]any{"title": "This corner", "artifactId": up.Artifact.ID, "rect": []float64{50, 50, 20, 10}}, 201)["annotation"])
	if len(list(box["rect"])) != 4 {
		t.Fatal(box)
	}
	h.call("POST", base+"/moments/"+id+"/annotations", map[string]any{"title": "x", "rect": []float64{1, 1, 1, 1}}, 422)
	h.call("POST", base+"/moments/"+id+"/annotations", map[string]any{"title": " "}, 422)
	noteID := note["id"].(string)
	erased := obj(h.call("PATCH", base+"/annotations/"+noteID, map[string]any{"expectedRevision": 1, "state": "erased"}, 200)["annotation"])
	if erased["state"] != "erased" || erased["revision"].(float64) != 2 {
		t.Fatal(erased)
	}
	undone := obj(h.call("PATCH", base+"/annotations/"+noteID, map[string]any{"expectedRevision": 2, "state": "ink"}, 200)["annotation"])
	if undone["state"] != "ink" {
		t.Fatal(undone)
	}
	h.call("PATCH", base+"/annotations/"+noteID, map[string]any{"expectedRevision": 2, "state": "pencil"}, 409)

	// Threads with knots; doorways from shared threads and people.
	thread := obj(h.call("POST", base+"/threads", map[string]any{}, 201)["thread"])
	if thread["title"] != "" {
		t.Fatal("threads stay nameless until the owner names them", thread)
	}
	tid := thread["id"].(string)
	h.call("POST", base+"/threads/"+tid+"/knots", map[string]any{"momentId": id, "lineId": lineID, "label": "lantern", "style": "fire"}, 201)
	knotted := obj(h.call("POST", base+"/threads/"+tid+"/knots", map[string]any{"momentId": otherID, "label": "second"}, 201)["thread"])
	if len(list(knotted["knots"])) != 2 {
		t.Fatal(knotted)
	}
	named := obj(h.call("PATCH", base+"/threads/"+tid, map[string]any{"expectedRevision": 1, "title": "Lanterns"}, 200)["thread"])
	if named["title"] != "Lanterns" {
		t.Fatal(named)
	}
	full := h.call("GET", base+"/moments/"+id, nil, 200)
	if len(list(full["threads"])) != 1 || len(list(full["doorways"])) != 1 || obj(list(full["doorways"])[0])["momentId"] != otherID {
		t.Fatal(full["threads"], full["doorways"])
	}
	if len(list(full["lines"])) != 1 || len(list(full["annotations"])) != 2 {
		t.Fatal(full)
	}

	// The Idea space payload and an owner-named region.
	h.call("PUT", base+"/regions/person:"+friend, map[string]any{"name": "Fixture region"}, 200)
	h.call("PUT", base+"/regions/bad%20key", map[string]any{"name": "x"}, 400)
	space := h.call("GET", base+"/space", nil, 200)
	if len(list(space["moments"])) != 3 || len(list(space["threads"])) != 1 || len(list(space["regions"])) != 1 || obj(list(space["people"])[0])["special"] != true {
		t.Fatal(space)
	}

	// On this day uses each Moment's own zone.
	otd := list(h.call("GET", base+"/on-this-day?today=2027-02-01", nil, 200)["moments"])
	if len(otd) != 1 || obj(otd[0])["id"] != id {
		t.Fatal(otd)
	}

	// Delete removes the stored objects.
	h.call("DELETE", base+"/moments/"+id, nil, 200)
	if h.objects.count("commonplace/") != 0 {
		t.Fatal("objects left behind")
	}
}

func readFixture(t *testing.T, name string) []byte {
	b, err := os.ReadFile("testdata/commonplace/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCommonplaceBundleImport(t *testing.T) {
	h := newCommonplaceHarness(t)
	manifest := readFixture(t, "bundle-sample.json")
	shot1, shot2 := syntheticPNG(t, 64, 138, 40), syntheticPNG(t, 64, 138, 200)
	parts := []cpPart{{field: "manifest", body: manifest}, {field: "shot1", filename: "Screen 1.png", contentType: "image/png", body: shot1}, {field: "shot2", filename: "Screen 2.png", contentType: "image/png", body: shot2}}

	// Dry run: a full report, nothing kept.
	dry := h.multipart("/v1/commonplace/import/bundle", append(parts, cpPart{field: "dryRun", body: []byte("1")}), 200)
	if dry["dryRun"] != true || dry["created"] != true || obj(dry["counts"])["lines"].(float64) != 4 || dry["filesStored"].(float64) != 2 {
		t.Fatal(dry)
	}
	if n := len(list(h.call("GET", "/v1/commonplace/moments", nil, 200)["moments"])); n != 0 || h.objects.count("commonplace/") != 0 {
		t.Fatal("dry run persisted something", n)
	}

	rep := h.multipart("/v1/commonplace/import/bundle", parts, 201)
	momentID := rep["momentId"].(string)
	if rep["filesStored"].(float64) != 2 || len(list(rep["missingFiles"])) != 0 || obj(rep["counts"])["knots"].(float64) != 2 {
		t.Fatal(rep)
	}
	full := h.call("GET", "/v1/commonplace/moments/"+momentID, nil, 200)
	m := obj(full["moment"])
	if m["kind"] != "conversation" || m["timezone"] != "America/Los_Angeles" || m["source"] != "imessage" || m["externalKey"] != "sample:2026-03-14-lighthouse" || len(list(m["people"])) != 2 {
		t.Fatal(m)
	}
	arts := list(full["artifacts"])
	if len(arts) != 3 || obj(arts[0])["key"] != "shot-1" || obj(arts[0])["originalName"] != "Screen 1.png" || obj(arts[2])["capturedText"] == "" {
		t.Fatal(arts)
	}
	lines := list(full["lines"])
	if len(lines) != 4 || obj(lines[1])["speaker"] != "me" || obj(obj(lines[1])["meta"])["edited"] != true || obj(lines[0])["artifactId"] != obj(arts[0])["id"] || len(list(obj(lines[0])["rect"])) != 4 {
		t.Fatal(lines)
	}
	notes := list(full["annotations"])
	if len(notes) != 3 || obj(notes[0])["state"] != "pencil" || obj(notes[0])["author"] != "import" || obj(notes[2])["state"] != "ink" {
		t.Fatal(notes)
	}
	threads := list(full["threads"])
	if len(threads) != 1 || obj(threads[0])["title"] != "Lighthouses" || obj(list(obj(threads[0])["knots"])[0])["style"] != "fire" {
		t.Fatal(threads)
	}
	if doors := list(full["doorways"]); len(doors) != 1 || obj(doors[0])["url"] != "https://example.com/lighthouses" {
		t.Fatal(doors)
	}

	// The owner keeps one note and edits another; a re-import must not undo that.
	n1 := obj(notes[0])
	h.call("PATCH", "/v1/commonplace/annotations/"+n1["id"].(string), map[string]any{"expectedRevision": n1["revision"], "state": "ink"}, 200)
	ownerLine := obj(h.call("POST", "/v1/commonplace/moments/"+momentID+"/lines", map[string]any{"speaker": "me", "text": "owner addition"}, 201)["line"])

	// Re-import: a changed title, a changed second image, one line removed, no shot-1 file (kept).
	var b map[string]any
	json.Unmarshal(manifest, &b)
	obj(b["moment"])["title"] = "The lighthouse keeper idea (revised)"
	b["lines"] = list(b["lines"])[:3]
	obj(list(b["annotations"])[0])["title"] = "importer rewrite that must not apply"
	b["threads"] = []any{map[string]any{"key": "sample-lighthouse", "knots": []any{map[string]any{"line": "l1", "label": "the diary", "style": "fire"}}}}
	revised, _ := json.Marshal(b)
	newShot2 := syntheticPNG(t, 64, 138, 220)
	rep2 := h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: revised}, {field: "shot2", filename: "Screen 2.png", body: newShot2}}, 200)
	if rep2["momentId"] != momentID || rep2["created"] != false || rep2["filesKept"].(float64) != 1 || rep2["filesStored"].(float64) != 1 || obj(rep2["removed"])["lines"].(float64) != 1 {
		t.Fatal(rep2)
	}
	if h.objects.count("commonplace/") != 2 {
		t.Fatal("the superseded image must be deleted", h.objects.count("commonplace/"))
	}
	full = h.call("GET", "/v1/commonplace/moments/"+momentID, nil, 200)
	if obj(full["moment"])["title"] != "The lighthouse keeper idea (revised)" || len(list(full["lines"])) != 4 { // 3 imported + the owner's line
		t.Fatal(full["moment"], len(list(full["lines"])))
	}
	keptOwner := false
	for _, l := range list(full["lines"]) {
		if obj(l)["id"] == ownerLine["id"] {
			keptOwner = true
		}
	}
	if !keptOwner {
		t.Fatal("re-import removed the owner's line")
	}
	for _, n := range list(full["annotations"]) {
		if obj(n)["key"] == "n1" && (obj(n)["state"] != "ink" || obj(n)["title"] != "A diary kept by a building") {
			t.Fatal("re-import overwrote the owner's decision", n)
		}
	}
	if knots := list(obj(list(full["threads"])[0])["knots"]); len(knots) != 1 || obj(list(full["threads"])[0])["title"] != "Lighthouses" {
		t.Fatal("re-import must replace only this bundle's knots and keep the thread name", knots)
	}
	if n := len(list(h.call("GET", "/v1/commonplace/moments", nil, 200)["moments"])); n != 1 {
		t.Fatal("re-import duplicated the Moment", n)
	}

	// A second bundle with no files yet: the images are reported missing, then filled in.
	b["externalKey"] = "sample:second"
	b["threads"] = []any{map[string]any{"key": "sample-lighthouse", "knots": []any{map[string]any{"moment": "self", "label": "again"}, map[string]any{"moment": "sample:2026-03-14-lighthouse", "line": "l2", "label": "back"}}}}
	obj(list(b["annotations"])[0])["link"] = map[string]any{"moment": "sample:2026-03-14-lighthouse", "label": "the first one"}
	second, _ := json.Marshal(b)
	rep3 := h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", filename: "manifest.json", contentType: "application/json", body: second}}, 201)
	if len(list(rep3["missingFiles"])) != 2 || obj(rep3["counts"])["knots"].(float64) != 2 {
		t.Fatal(rep3)
	}
	rep4 := h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: second}, {field: "files", filename: "shot1", body: shot1}, {field: "files", filename: "folder/shot2", body: shot2}}, 200)
	if len(list(rep4["missingFiles"])) != 0 || rep4["filesStored"].(float64) != 2 {
		t.Fatal(rep4)
	}
	thread := list(h.call("GET", "/v1/commonplace/moments/"+momentID, nil, 200)["threads"])
	if len(list(obj(thread[0])["knots"])) != 3 {
		t.Fatal("knots from both bundles share the thread", thread)
	}

	// Invalid manifests and files are rejected with precise problems; nothing is written.
	bad := h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: []byte(`{"version":1,"externalKey":"x","moment":{"kind":"party"},"lines":[{"key":"a","speaker":"ghost"}]}`)}}, 422)
	if !strings.Contains(fmt.Sprint(bad["problems"]), "moment.kind") || !strings.Contains(fmt.Sprint(bad["problems"]), "lines[0].speaker") {
		t.Fatal(bad)
	}
	h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: []byte(`{"version":1,"externalKey":"x","moment":{},"typo":1}`)}}, 422)
	notImage := h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: manifest}, {field: "shot1", filename: "a.png", body: []byte("nope")}}, 422)
	if !strings.Contains(fmt.Sprint(notImage["problems"]), "shot-1") {
		t.Fatal(notImage)
	}

	// Search finds lines, notes and link text with snippets and anchors.
	res := list(h.call("GET", "/v1/commonplace/search?q=lighthouse+diary", nil, 200)["results"])
	if len(res) == 0 {
		t.Fatal("no results")
	}
	hits := list(obj(res[0])["hits"])
	foundLine := false
	for _, hit := range hits {
		if obj(hit)["kind"] == "line" && obj(hit)["anchorId"] != nil && strings.Contains(obj(hit)["snippet"].(string), "") {
			foundLine = true
		}
	}
	if !foundLine {
		t.Fatal("line hit with highlight markers expected", hits)
	}
	if res := list(h.call("GET", "/v1/commonplace/search?q=keepers+logs", nil, 200)["results"]); len(res) == 0 || obj(list(obj(res[0])["hits"])[0])["kind"] != "artifact" {
		t.Fatal("link captured text must be searchable", res)
	}
	if res := list(h.call("GET", "/v1/commonplace/search?q=examp+cous", nil, 200)["results"]); len(res) == 0 {
		t.Fatal("prefix search on people")
	}
	if res := list(h.call("GET", "/v1/commonplace/search?q=%27%3B+drop", nil, 200)["results"]); len(res) != 0 {
		t.Fatal(res)
	}
}

func TestCommonplaceDiscordImport(t *testing.T) {
	h := newCommonplaceHarness(t)
	export := readFixture(t, "discord-dreams.json")
	window := syntheticPNG(t, 30, 30, 10)
	form := func(dry bool, extra ...cpPart) []cpPart {
		parts := []cpPart{{field: "export", filename: "export/dreams.json", contentType: "application/json", body: export}, {field: "kind", body: []byte("dream")},
			{field: "me", body: []byte("1000")}, {field: "timezone", body: []byte("America/New_York")}}
		if dry {
			parts = append(parts, cpPart{field: "dryRun", body: []byte("1")})
		}
		return append(parts, extra...)
	}
	// Dry run with only the names of the media files the browser holds.
	dry := h.multipart("/v1/commonplace/import/discord", form(true, cpPart{field: "mediaManifest", body: []byte(`["export/dreams.json_Files/sample-window-1A2B3.png"]`)}), 200)
	groups := list(dry["groups"])
	if dry["dryRun"] != true || len(groups) != 3 || obj(groups[0])["messages"].(float64) != 3 || obj(groups[0])["mediaMatched"].(float64) != 1 || obj(dry["totals"])["skippedSystem"].(float64) != 1 {
		t.Fatal(dry)
	}
	authors := list(dry["authors"])
	if len(authors) != 3 || obj(authors[1])["me"] != true {
		t.Fatal(authors)
	}
	if n := len(list(h.call("GET", "/v1/commonplace/moments", nil, 200)["moments"])); n != 0 {
		t.Fatal("dry run persisted", n)
	}

	// The real import, in two batches: first the export alone, then with media.
	rep := h.multipart("/v1/commonplace/import/discord", form(false), 200)
	if obj(rep["totals"])["momentsCreated"].(float64) != 3 || obj(rep["totals"])["messagesNew"].(float64) != 7 || obj(rep["totals"])["mediaMissing"].(float64) != 1 || obj(rep["totals"])["attachmentsSkipped"].(float64) != 1 {
		t.Fatal(rep)
	}
	rep2 := h.multipart("/v1/commonplace/import/discord", form(false, cpPart{field: "media", filename: "export/dreams.json_Files/sample-window-1A2B3.png", body: window}), 200)
	if obj(rep2["totals"])["messagesNew"] != nil || obj(rep2["totals"])["mediaStored"].(float64) != 1 || obj(rep2["totals"])["momentsUpdated"].(float64) != 3 {
		t.Fatal(rep2)
	}
	rep3 := h.multipart("/v1/commonplace/import/discord", form(false), 200)
	if obj(rep3["totals"])["mediaKept"].(float64) != 1 || obj(rep3["totals"])["momentsCreated"] != nil {
		t.Fatal(rep3)
	}
	moments := list(h.call("GET", "/v1/commonplace/moments?source=discord", nil, 200)["moments"])
	if len(moments) != 3 {
		t.Fatal(moments)
	}
	oldest := obj(moments[2])
	if oldest["kind"] != "dream" || oldest["sourceDetail"] != "#dreams · Sample Guild" || oldest["timezone"] != "America/New_York" || obj(oldest["counts"])["lines"].(float64) != 3 || oldest["coverArtifactId"] == nil {
		t.Fatal(oldest)
	}
	full := h.call("GET", "/v1/commonplace/moments/"+oldest["id"].(string), nil, 200)
	lines := list(full["lines"])
	if obj(lines[0])["speakerName"] != "Sample Friend" || obj(lines[1])["speaker"] != "me" || obj(obj(lines[1])["meta"])["replyTo"] != "910000000000000001" || obj(obj(lines[1])["meta"])["edited"] != true {
		t.Fatal(lines)
	}
	if obj(obj(lines[0])["meta"])["pinned"] != true || len(list(obj(obj(lines[0])["meta"])["reactions"])) != 1 {
		t.Fatal(lines[0])
	}
	people := list(h.call("GET", "/v1/commonplace/people", nil, 200)["people"])
	if len(people) != 2 {
		t.Fatal("people come from authors (the owner is \"me\")", people)
	}
	// A different gap re-groups nothing that already exists: no duplicates.
	again := h.multipart("/v1/commonplace/import/discord", append(form(false), cpPart{field: "gapHours", body: []byte("0.1")}), 200)
	if obj(again["totals"])["messagesNew"] != nil || obj(again["totals"])["momentsCreated"] != nil {
		t.Fatal(again)
	}
	var lineCount int
	if err := h.app.db.QueryRow(context.Background(), `SELECT count(*) FROM cp_lines`).Scan(&lineCount); err != nil || lineCount != 7 {
		t.Fatal(lineCount, err)
	}
	h.multipart("/v1/commonplace/import/discord", []cpPart{{field: "export", filename: "x.json", body: []byte(`{"hello":1}`)}}, 422)
}
