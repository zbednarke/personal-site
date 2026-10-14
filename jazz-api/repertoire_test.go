package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSlugifyTuneTitleMatchesSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/repertoire_slugs.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][2]string
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		got := slugifyTuneTitle(c[0])
		if got != c[1] {
			t.Errorf("slugify(%q) = %q, want %q", c[0], got, c[1])
		}
		if got != "" && !tuneIDPattern.MatchString(got) {
			t.Errorf("slugify(%q) = %q is not a valid tune id", c[0], got)
		}
	}
}

func TestPracticeNowBlockKeyIsValid(t *testing.T) {
	slug := strings.Repeat("a", 47) + "b"
	if !tuneIDPattern.MatchString(slug) || !validBlockKey("tune-"+slug+"-x9y8z7") {
		t.Fatal("a maximum-length tune slug must fit a Practice now block key")
	}
}

func TestTuneKeyNormalization(t *testing.T) {
	for raw, want := range map[string]string{"bb": "Bb", "F#M": "F#m", "E♭ minor": "Ebm", " c ": "C", "g min": "Gm", "Ab major": "Ab"} {
		if got, ok := normalizeTuneKey(raw); !ok || got != want {
			t.Errorf("normalizeTuneKey(%q) = %q,%v want %q", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "H", "Bbb", "C7", "Do"} {
		if _, ok := normalizeTuneKey(raw); ok {
			t.Errorf("normalizeTuneKey(%q) should be invalid", raw)
		}
	}
	keys, err := normalizeTuneKeys([]string{"Bb", "A#", "bb", "F", "Gm", "A#m", "Bbm"})
	if err != nil || !reflect.DeepEqual(keys, []string{"Bb", "F", "Gm", "A#m"}) {
		t.Fatalf("enharmonic dedup: %v %v", keys, err)
	}
	if _, err := normalizeTuneKeys([]string{"C", "X"}); err == nil {
		t.Fatal("invalid key accepted")
	}
	all := []string{"C", "Db", "D", "Eb", "E", "F", "Gb", "G", "Ab", "A", "Bb", "B", "Cm"}
	if _, err := normalizeTuneKeys(all); err == nil {
		t.Fatal("more than 12 keys accepted")
	}
}

func milestoneTune(status map[string]string, keys ...string) repertoireTune {
	milestones := map[string]string{}
	for _, m := range tuneMilestoneColumns {
		milestones[m.Key] = "not_started"
	}
	for key, value := range status {
		milestones[key] = value
	}
	return repertoireTune{Milestones: milestones, KeysKnown: keys}
}

func TestDeriveTuneStateTruthTable(t *testing.T) {
	deep := map[string]string{"melodyByEar": "solid", "lyrics": "solid", "transcription": "solid"}
	cases := []struct {
		name   string
		tune   repertoireTune
		deep   bool
		status string
		keys   string
	}{
		{"untouched", milestoneTune(nil), false, "not_started", "not_started"},
		{"all four", milestoneTune(deep, "C", "Eb"), true, "learning", "solid"},
		{"one key", milestoneTune(deep, "C"), false, "learning", "learning"},
		{"melody learning", milestoneTune(map[string]string{"melodyByEar": "learning", "lyrics": "solid", "transcription": "solid"}, "C", "D"), false, "learning", "solid"},
		{"no lyrics", milestoneTune(map[string]string{"melodyByEar": "solid", "transcription": "solid"}, "C", "D"), false, "learning", "solid"},
		{"no transcription", milestoneTune(map[string]string{"melodyByEar": "solid", "lyrics": "solid"}, "C", "D"), false, "learning", "solid"},
		{"gig ready", milestoneTune(map[string]string{"gigReady": "solid"}), false, "gig_ready", "not_started"},
		{"gig learning", milestoneTune(map[string]string{"gigReady": "learning"}), false, "learning", "not_started"},
	}
	for _, c := range cases {
		deriveTuneState(&c.tune)
		if c.tune.DeeplyLearned != c.deep || c.tune.PracticeStatus != c.status || c.tune.Milestones["keysKnown"] != c.keys {
			t.Errorf("%s: deep=%v status=%s keys=%s", c.name, c.tune.DeeplyLearned, c.tune.PracticeStatus, c.tune.Milestones["keysKnown"])
		}
	}
	practiced := milestoneTune(nil)
	practiced.applyPractice(tunePractice{TotalPracticeMS: 120000, TakeCount: 1, SessionCount: 1, LastPracticedDate: "2026-10-09"})
	deriveTuneState(&practiced)
	if practiced.PracticeStatus != "learning" || practiced.Milestones["melodyByEar"] != "not_started" {
		t.Fatal("practice must promote to learning without touching milestones")
	}
}

func TestLegacyStageMilestones(t *testing.T) {
	want := []map[string]string{
		{},
		{"melodyByEar": "learning"},
		{"melodyByEar": "learning"},
		{"melodyByEar": "learning", "changes": "learning"},
		{"melodyByEar": "learning", "changes": "learning", "improvise": "learning"},
		{"melodyByEar": "solid", "changes": "solid", "improvise": "learning"},
		{"melodyByEar": "solid", "changes": "solid", "improvise": "learning", "gigReady": "solid"},
	}
	for stage, expected := range want {
		if got := legacyStageMilestones(stage); !reflect.DeepEqual(got, expected) {
			t.Errorf("stage %d: %v want %v", stage, got, expected)
		}
	}
}

func TestApplyTunePatchValidatesAndDiffs(t *testing.T) {
	current := milestoneTune(nil, "C")
	current.Title, current.Category, current.Chosen = "Skylark", "ballad", true
	deriveTuneState(&current)
	solid, bad := "solid", "done"
	title := "  Skylark (Carmichael) "
	keys := []string{"C", "a#", "Bb", "F"}
	archived := true
	next, err := applyTunePatch(current, patchTuneRequest{Title: &title, KeysKnown: &keys, Milestones: &tuneMilestonesPatch{MelodyByEar: &solid}, Archived: &archived}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if next.Title != "Skylark (Carmichael)" || !reflect.DeepEqual(next.KeysKnown, []string{"C", "A#", "F"}) || next.ArchivedAt == nil {
		t.Fatalf("patch applied incorrectly: %+v", next)
	}
	if current.Milestones["melodyByEar"] != "not_started" || len(current.KeysKnown) != 1 {
		t.Fatal("patch mutated the current tune")
	}
	events := diffTune(current, next)
	types := []string{}
	for _, event := range events {
		types = append(types, event.Type)
	}
	if !reflect.DeepEqual(types, []string{"tune.updated", "milestone.changed", "key.added", "key.added", "tune.archived"}) {
		t.Fatalf("events: %v", types)
	}
	if events[1].Payload["field"] != "melodyByEar" || events[1].Payload["from"] != "not_started" || events[1].Payload["to"] != "solid" {
		t.Fatalf("milestone event payload: %v", events[1].Payload)
	}
	if _, err := applyTunePatch(current, patchTuneRequest{Milestones: &tuneMilestonesPatch{Lyrics: &bad}}, time.Now()); err == nil {
		t.Fatal("invalid milestone accepted")
	}
	empty, long := "", strings.Repeat("x", 161)
	for _, patch := range []patchTuneRequest{{Title: &empty}, {Title: &long}, {Category: &bad}} {
		if _, err := applyTunePatch(current, patch, time.Now()); err == nil {
			t.Fatalf("invalid patch accepted: %+v", patch)
		}
	}
	insecure := "http://example.com"
	if _, err := applyTunePatch(current, patchTuneRequest{ReferenceURL: &insecure}, time.Now()); err == nil {
		t.Fatal("non-https reference accepted")
	}
	if len(diffTune(current, current)) != 0 {
		t.Fatal("an unchanged tune produced events")
	}
}

func TestMondayOf(t *testing.T) {
	for date, want := range map[string]string{"2026-10-09": "2026-10-05", "2026-10-05": "2026-10-05", "2026-10-11": "2026-10-05", "2026-10-12": "2026-10-12"} {
		if got := mondayOf(date); got != want {
			t.Errorf("mondayOf(%s) = %s want %s", date, got, want)
		}
	}
}

func TestSeedListMatchesSpec(t *testing.T) {
	counts := map[string]int{}
	for _, seed := range repertoireSeeds {
		counts[seed.Category]++
		if !tuneIDPattern.MatchString(seed.TuneID) || slugifyTuneTitle(seed.Title) != seed.TuneID && seed.TuneID != "bb-blues" {
			t.Errorf("seed %s does not match its title slug", seed.TuneID)
		}
	}
	if len(repertoireSeeds) != 23 || counts["ballad"] != 10 || counts["upbeat"] != 8 || counts["pop"] != 5 {
		t.Fatalf("seed counts: %d %v", len(repertoireSeeds), counts)
	}
}
