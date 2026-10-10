package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// Regression tests from the PR review. All data is fictional.

func lineByKey(t *testing.T, full map[string]any, key string) map[string]any {
	t.Helper()
	for _, l := range list(full["lines"]) {
		if obj(l)["key"] == key {
			return obj(l)
		}
	}
	t.Fatalf("line %s not found", key)
	return nil
}

func TestCommonplaceBundleReimportKeepsOwnerEdits(t *testing.T) {
	h := newCommonplaceHarness(t)
	manifest := readFixture(t, "bundle-sample.json")
	shot1, shot2 := syntheticPNG(t, 20, 40, 10), syntheticPNG(t, 20, 40, 90)
	rep := h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: manifest}, {field: "shot1", filename: "a.png", body: shot1}, {field: "shot2", filename: "b.png", body: shot2}}, 201)
	id := rep["momentId"].(string)
	full := h.call("GET", "/v1/commonplace/moments/"+id, nil, 200)

	// The owner corrects a transcript line, renames a person and adds someone.
	l1 := lineByKey(t, full, "l1")
	h.call("PATCH", "/v1/commonplace/lines/"+l1["id"].(string), map[string]any{"expectedRevision": l1["revision"], "text": "owner's corrected transcription"}, 200)
	var friendID string
	for _, p := range list(obj(full["moment"])["people"]) {
		if obj(p)["name"] == "Sample Friend" {
			friendID = obj(p)["id"].(string)
		}
	}
	h.call("PATCH", "/v1/commonplace/people/"+friendID, map[string]any{"name": "Sample Friend (renamed by owner)"}, 200)
	m := obj(full["moment"])
	people := []map[string]any{}
	for _, p := range list(m["people"]) {
		people = append(people, map[string]any{"id": obj(p)["id"], "role": obj(p)["role"]})
	}
	people = append(people, map[string]any{"name": "Owner Added Person", "role": "recipient"})
	h.call("PATCH", "/v1/commonplace/moments/"+id, map[string]any{"expectedRevision": m["revision"], "people": people}, 200)

	// The bundle changes l1 and l2 and renames both people.
	var b map[string]any
	json.Unmarshal(manifest, &b)
	obj(list(b["lines"])[0])["text"] = "importer version of line one"
	obj(list(b["lines"])[1])["text"] = "importer version of line two"
	obj(list(b["people"])[0])["name"] = "Sample Friend (bundle rename)"
	obj(list(b["people"])[1])["name"] = "Example Cousin (bundle rename)"
	revised, _ := json.Marshal(b)
	l2before := lineByKey(t, full, "l2")
	h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: revised}}, 200)
	full = h.call("GET", "/v1/commonplace/moments/"+id, nil, 200)

	if got := lineByKey(t, full, "l1")["text"]; got != "owner's corrected transcription" {
		t.Fatalf("re-import overwrote the owner's line: %v", got)
	}
	l2 := lineByKey(t, full, "l2")
	if l2["text"] != "importer version of line two" || l2["revision"].(float64) <= l2before["revision"].(float64) {
		t.Fatalf("untouched lines follow the bundle and bump their revision: %v", l2)
	}
	names := map[string]bool{}
	for _, p := range list(obj(full["moment"])["people"]) {
		names[obj(p)["name"].(string)] = true
	}
	if !names["Sample Friend (renamed by owner)"] || names["Sample Friend (bundle rename)"] {
		t.Fatalf("the owner's rename must stick: %v", names)
	}
	if !names["Example Cousin (bundle rename)"] {
		t.Fatalf("a person the import named (and nobody renamed) follows the bundle: %v", names)
	}
	if !names["Owner Added Person"] {
		t.Fatalf("re-import dropped a person the owner added: %v", names)
	}

	// A person who matched an existing name is never renamed by an import.
	h.call("POST", "/v1/commonplace/people", map[string]any{"name": "Fixture Neighbour", "aliases": []string{"neighbour-alias"}}, 201)
	b["externalKey"] = "sample:neighbour"
	b["people"] = []any{map[string]any{"key": "neighbour", "name": "neighbour-alias", "role": "sender"}}
	b["lines"] = []any{}
	b["annotations"] = []any{}
	b["threads"] = []any{}
	third, _ := json.Marshal(b)
	h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: third}}, 201)
	b["people"] = []any{map[string]any{"key": "neighbour", "name": "Neighbour Renamed By Import", "role": "sender"}}
	third, _ = json.Marshal(b)
	h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: third}}, 200)
	found := false
	for _, p := range list(h.call("GET", "/v1/commonplace/people", nil, 200)["people"]) {
		if obj(p)["name"] == "Fixture Neighbour" {
			found = true
		}
		if obj(p)["name"] == "Neighbour Renamed By Import" || obj(p)["name"] == "neighbour-alias" {
			t.Fatal("a person matched by name or alias was renamed", p)
		}
	}
	if !found {
		t.Fatal("matched person missing")
	}

	// Turning an image into text deletes its stored file once committed.
	if n := h.objects.count("commonplace/"); n != 2 {
		t.Fatal("objects before", n)
	}
	json.Unmarshal(revised, &b)
	arts := list(b["artifacts"])
	obj(arts[1])["kind"] = "text"
	delete(obj(arts[1]), "file")
	obj(arts[1])["text"] = "now transcribed as text"
	for _, l := range list(b["lines"]) {
		if obj(l)["artifact"] == "shot-2" {
			delete(obj(l), "artifact")
			delete(obj(l), "rect")
		}
	}
	for _, n := range list(b["annotations"]) {
		if a, ok := obj(n)["anchor"].(map[string]any); ok && a["artifact"] == "shot-2" {
			delete(obj(n), "anchor")
		}
	}
	changed, _ := json.Marshal(b)
	h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: changed}}, 200)
	if n := h.objects.count("commonplace/"); n != 1 {
		t.Fatal("the image's stored object must be deleted when it becomes text", n)
	}

	// Ambiguous file names are refused, not guessed.
	amb := h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: manifest},
		{field: "files", filename: "one/shot1", body: shot1}, {field: "files", filename: "two/shot1", body: shot2}}, 422)
	if !strings.Contains(fmt.Sprint(amb["problems"]), "match") {
		t.Fatal(amb)
	}

	// timeLabel agrees with its column (80 characters).
	json.Unmarshal(manifest, &b)
	obj(list(b["lines"])[0])["timeLabel"] = strings.Repeat("x", 100)
	long, _ := json.Marshal(b)
	bad := h.multipart("/v1/commonplace/import/bundle", []cpPart{{field: "manifest", body: long}}, 422)
	if !strings.Contains(fmt.Sprint(bad["problems"]), "timeLabel") {
		t.Fatal(bad)
	}
}

func TestCommonplaceDiscordReimportKeepsOwnerEdits(t *testing.T) {
	h := newCommonplaceHarness(t)
	export := readFixture(t, "discord-dreams.json")
	form := func(body []byte) []cpPart {
		return []cpPart{{field: "export", filename: "dreams.json", body: body}, {field: "me", body: []byte("1000")}}
	}
	h.multipart("/v1/commonplace/import/discord", form(export), 200)
	lineOf := func(ext string) (id string, body string, rev int64) {
		err := h.app.db.QueryRow(context.Background(), `SELECT id::text,body,revision FROM cp_lines WHERE external_id=$1`, "discord:"+ext).Scan(&id, &body, &rev)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	id3, _, rev3 := lineOf("910000000000000003")
	h.call("PATCH", "/v1/commonplace/lines/"+id3, map[string]any{"expectedRevision": rev3, "text": "owner's own correction"}, 200)

	// Discord edits two messages later; the export is taken again.
	var e map[string]any
	json.Unmarshal(export, &e)
	for _, m := range list(e["messages"]) {
		switch obj(m)["id"] {
		case "910000000000000003", "910000000000000006":
			obj(m)["content"] = "edited on Discord afterwards"
			obj(m)["timestampEdited"] = "2026-01-06T09:00:00.000+00:00"
		case "910000000000000008":
			obj(m)["content"] = "changed text without a newer edit time"
		}
	}
	again, _ := json.Marshal(e)
	_, _, rev6 := lineOf("910000000000000006")
	h.multipart("/v1/commonplace/import/discord", form(again), 200)
	if _, body, _ := lineOf("910000000000000003"); body != "owner's own correction" {
		t.Fatalf("re-import overwrote the owner's edit: %q", body)
	}
	if _, body, rev := lineOf("910000000000000006"); body != "edited on Discord afterwards" || rev <= rev6 {
		t.Fatalf("a newer Discord edit updates an untouched line and bumps its revision: %q %d", body, rev)
	}
	if _, body, _ := lineOf("910000000000000008"); body != "Rain clocks would never be late." {
		t.Fatalf("words change only with a newer Discord edit time: %q", body)
	}
	// Re-importing the same export again changes nothing.
	_, _, rev6 = lineOf("910000000000000006")
	h.multipart("/v1/commonplace/import/discord", form(again), 200)
	if _, _, rev := lineOf("910000000000000006"); rev != rev6 {
		t.Fatal("an unchanged re-import must not bump revisions")
	}
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(body)
	}
	zw.Close()
	return buf.Bytes()
}

func TestCommonplaceDiscordZipLimits(t *testing.T) {
	h := newCommonplaceHarness(t)
	export := readFixture(t, "discord-dreams.json")
	archive := zipOf(t, map[string][]byte{"export/dreams.json": export, "export/dreams.json_Files/sample-window-1A2B3.png": syntheticPNG(t, 20, 20, 5)})
	rep := h.multipart("/v1/commonplace/import/discord", []cpPart{{field: "archive", filename: "export.zip", body: archive}, {field: "dryRun", body: []byte("1")}}, 200)
	if obj(list(rep["groups"])[0])["mediaMatched"].(float64) != 1 {
		t.Fatal(rep)
	}
	// A zip that expands past the cap is refused (the cap is lowered for the test).
	saved := cpZipTotalLimit
	cpZipTotalLimit = int64(len(export)) + 10
	defer func() { cpZipTotalLimit = saved }()
	bomb := zipOf(t, map[string][]byte{"export/dreams.json": export, "export/padding.bin": bytes.Repeat([]byte{0}, 1<<20)})
	h.multipart("/v1/commonplace/import/discord", []cpPart{{field: "archive", filename: "export.zip", body: bomb}}, 400)
	if _, _, err := cpUnzipExport(bomb, cpZipTotalLimit); err == nil || !strings.Contains(err.Error(), "expands") {
		t.Fatal(err)
	}
}

func TestCommonplaceSearchAndZoneValidation(t *testing.T) {
	h := newCommonplaceHarness(t)
	h.call("POST", "/v1/commonplace/moments", map[string]any{"clientCaptureId": "00000000-0000-4000-8000-0000000000f1", "text": "Fixture café notes"}, 201)
	// A 70-rune token of 2-byte characters must not be cut mid-character.
	q := url.QueryEscape(strings.Repeat("é", 70))
	h.call("GET", "/v1/commonplace/search?q="+q, nil, 200)
	if res := list(h.call("GET", "/v1/commonplace/search?q=caf%C3%A9", nil, 200)["results"]); len(res) != 1 {
		t.Fatal(res)
	}
	for _, zone := range []string{"Local", "local", "Nowhere", "America/Not_A_City", "../etc"} {
		h.call("POST", "/v1/commonplace/moments", map[string]any{"clientCaptureId": "00000000-0000-4000-8000-0000000000f2", "text": "x", "timezone": zone}, 422)
	}
	m := obj(h.call("POST", "/v1/commonplace/moments", map[string]any{"clientCaptureId": "00000000-0000-4000-8000-0000000000f3", "text": "x", "timezone": "Asia/Tokyo"}, 201)["moment"])
	h.call("PATCH", "/v1/commonplace/moments/"+m["id"].(string), map[string]any{"expectedRevision": 1, "timezone": ""}, 422)
	h.call("PATCH", "/v1/commonplace/moments/"+m["id"].(string), map[string]any{"expectedRevision": 1, "timezone": "Local"}, 422)
	h.call("PATCH", "/v1/commonplace/moments/"+m["id"].(string), map[string]any{"expectedRevision": 1, "timezone": "UTC"}, 200)
	// The list queries work for every stored zone.
	h.call("GET", "/v1/commonplace/moments?year=2026", nil, 200)
}

func TestCommonplaceFindUpload(t *testing.T) {
	files := []*cpUpload{{Field: "shot1", FileName: "a.png"}, {Field: "files", FileName: "x/b.png"}, {Field: "files", FileName: "y/b.png"}, {Field: "files", FileName: "c.png"}}
	if f, err := cpFindUpload(files, "shot1"); err != nil || f.FileName != "a.png" {
		t.Fatal(f, err)
	}
	if f, err := cpFindUpload(files, "c.png"); err != nil || f == nil {
		t.Fatal(f, err)
	}
	if _, err := cpFindUpload(files, "b.png"); err == nil {
		t.Fatal("ambiguous base names must be refused")
	}
	if f, err := cpFindUpload(files, "x/b.png"); err != nil || f.FileName != "x/b.png" {
		t.Fatal("an exact path wins", f, err)
	}
	if f, err := cpFindUpload(files, "none"); err != nil || f != nil {
		t.Fatal(f, err)
	}
}
