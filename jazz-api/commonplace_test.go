package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"
)

// syntheticPNG is a flat gradient, clearly not a photograph or screenshot.
func syntheticPNG(t testing.TB, w, h int, tone uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{tone, uint8(40 + y*120/h), uint8(80 + x*100/w), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCommonplaceDiscordGrouping(t *testing.T) {
	raw, err := os.ReadFile("testdata/commonplace/discord-dreams.json")
	if err != nil {
		t.Fatal(err)
	}
	var export dceExport
	if err := json.Unmarshal(raw, &export); err != nil {
		t.Fatal(err)
	}
	var msgs []dceMessage
	for _, m := range export.Messages {
		if m.Type == "Default" || m.Type == "Reply" {
			msgs = append(msgs, m)
		}
	}
	groups := cpGroupDiscord(msgs, 3*time.Hour)
	ids := [][]string{}
	for _, g := range groups {
		var row []string
		for _, i := range g {
			row = append(row, msgs[i].ID[len(msgs[i].ID)-1:])
		}
		ids = append(ids, row)
	}
	// 1 starts; 2 replies; 3 follows up; 5 is a long post by someone else; 6
	// follows it; 7 is two days later and 8 replies to it six hours on.
	want := [][]string{{"1", "2", "3"}, {"5", "6"}, {"7", "8"}}
	if got, _ := json.Marshal(ids); string(got) != mustJSON(want) {
		t.Fatalf("groups %s, want %s", got, mustJSON(want))
	}
	// A one-hour gap splits the follow-up into its own Moment.
	if n := len(cpGroupDiscord(msgs, 20*time.Minute)); n != 4 {
		t.Fatalf("short gap: %d groups", n)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCommonplaceHelpers(t *testing.T) {
	if q := cpTSQuery(`lantern's "tide" & | ! :*`); q != "'lantern':* & 's':* & 'tide':*" {
		t.Fatalf("tsquery %q", q)
	}
	if cpTSQuery("  !!  ") != "" {
		t.Fatal("punctuation-only queries must be empty")
	}
	for _, bad := range [][]float64{{1, 2, 3}, {-1, 0, 10, 10}, {95, 0, 10, 10}, {0, 0, 0, 5}} {
		if _, err := cpRect(bad, "rect"); err == nil {
			t.Fatalf("rect %v accepted", bad)
		}
	}
	if r, err := cpRect([]float64{4.8123456, 21, 65, 17}, "rect"); err != nil || r[0] != 4.812 {
		t.Fatal(r, err)
	}
	if _, err := cpTimezone("America/Los_Angeles"); err != nil {
		t.Fatal(err)
	}
	if _, err := cpTimezone("Mars/Base"); err == nil {
		t.Fatal("unknown zone accepted")
	}
	audio := map[string][]byte{
		"audio/wav":  append([]byte("RIFF\x00\x00\x00\x00WAVEfmt "), make([]byte, 16)...),
		"audio/ogg":  []byte("OggS\x00\x02rest"),
		"audio/mpeg": []byte("ID3\x04\x00rest"),
		"audio/mp4":  []byte("\x00\x00\x00\x20ftypM4A rest"),
		"audio/aac":  {0xFF, 0xF1, 0x50, 0x80},
		"audio/webm": {0x1A, 0x45, 0xDF, 0xA3, 0x01},
	}
	for want, body := range audio {
		m, err := cpValidateMedia(body, "", nil, nil)
		if err != nil || m.ContentType != want || m.Kind != "audio" {
			t.Fatalf("%s: %+v %v", want, m.ContentType, err)
		}
	}
	img, err := cpValidateMedia(syntheticPNG(t, 12, 20, 90), "image/png", nil, nil)
	if err != nil || img.Kind != "image" || *img.Width != 12 || *img.Height != 20 || len(img.SHA256) != 64 {
		t.Fatal(img, err)
	}
	if _, err := cpValidateMedia(syntheticPNG(t, 4, 4, 9), "image/jpeg", nil, nil); err == nil {
		t.Fatal("type mismatch accepted")
	}
	if _, err := cpValidateMedia([]byte("%PDF-1.4"), "", nil, nil); err == nil {
		t.Fatal("pdf accepted")
	}
	files := map[string]*cpUpload{
		"export/dreams.json_files/sample-window-1a2b3.png": {FileName: "a"},
		"export/other/sample-window-1a2b3.png":             {FileName: "b"},
		"export/dreams.json_files/solo.png":                {FileName: "c"},
	}
	if f := cpMatchMedia(files, "dreams.json_Files/sample-window-1A2B3.png", "sample-window.png"); f == nil || f.FileName != "a" {
		t.Fatal("relative path match", f)
	}
	if f := cpMatchMedia(files, "https://cdn.discordapp.com/attachments/1/2/sample-window-1A2B3.png", ""); f != nil {
		t.Fatal("ambiguous base name must not match", f)
	}
	if f := cpMatchMedia(files, "https://cdn.discordapp.com/x/solo.png", "solo.png"); f == nil || f.FileName != "c" {
		t.Fatal("unique base name", f)
	}
	if cpCleanFileName("..%2F..%2Fetc/pass wd<>.png") != "pass wd_.png" {
		t.Fatal(cpCleanFileName("..%2F..%2Fetc/pass wd<>.png"))
	}
}

func TestCommonplaceBundleValidation(t *testing.T) {
	raw, err := os.ReadFile("testdata/commonplace/bundle-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var b cpBundle
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		t.Fatal(err)
	}
	if err := cpValidateBundle(&b); err != nil {
		t.Fatal(err)
	}
	b.Lines[0].Speaker = "nobody"
	b.Lines[1].Artifact = "page"
	b.Annotations[0].Anchor.Line = "l99"
	b.Moment.Timezone = "Nowhere/Here"
	b.Artifacts = append(b.Artifacts, b.Artifacts[0])
	err = cpValidateBundle(&b)
	if err == nil {
		t.Fatal("broken manifest accepted")
	}
	for _, want := range []string{"lines[0].speaker", "lines[1].artifact", "annotations[0].anchor.line", "moment.timezone", `artifacts[3].key "shot-1" is repeated`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}
