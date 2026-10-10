package main

// Seed tunes are inserted once per user on the first repertoire read. Reference
// URLs stay empty (the UI offers a search link) rather than guessing one.
type repertoireSeed struct {
	TuneID          string
	Title           string
	Category        string
	Chosen          bool
	ReferenceArtist string
	ReferenceTitle  string
}

var repertoireSeeds = []repertoireSeed{
	{"my-funny-valentine", "My Funny Valentine", "ballad", true, "Chet Baker", "Chet Baker Sings (1954)"},
	{"i-fall-in-love-too-easily", "I Fall in Love Too Easily", "ballad", true, "Chet Baker / Miles Davis", "Chet Baker Sings (1954); Miles, Seven Steps to Heaven (1963)"},
	{"skylark", "Skylark", "ballad", true, "", ""},
	{"old-folks", "Old Folks", "ballad", true, "Miles Davis", "Someday My Prince Will Come (1961)"},
	{"in-a-sentimental-mood", "In a Sentimental Mood", "ballad", true, "Duke Ellington & John Coltrane", "Duke Ellington & John Coltrane (1963)"},
	{"everything-happens-to-me", "Everything Happens to Me", "ballad", true, "Chet Baker", ""},
	{"when-i-fall-in-love", "When I Fall in Love", "ballad", true, "Miles Davis", "Steamin' (rec. 1956)"},
	{"i-cant-get-started", "I Can't Get Started", "ballad", true, "Bunny Berigan", "1937 recording"},
	{"darn-that-dream", "Darn That Dream", "ballad", true, "", ""},
	{"round-midnight", "'Round Midnight", "ballad", true, "Miles Davis", "'Round About Midnight (1957)"},

	{"blue-bossa", "Blue Bossa", "upbeat", true, "Kenny Dorham", "Joe Henderson, Page One (1963)"},
	{"autumn-leaves", "Autumn Leaves", "upbeat", true, "Cannonball Adderley w/ Miles Davis", "Somethin' Else (1958)"},
	{"song-for-my-father", "Song for My Father", "upbeat", true, "Horace Silver", "Song for My Father (1965)"},
	{"cantaloupe-island", "Cantaloupe Island", "upbeat", true, "Herbie Hancock w/ Freddie Hubbard", "Empyrean Isles (1964)"},
	{"watermelon-man", "Watermelon Man", "upbeat", true, "Herbie Hancock", "Takin' Off (1962)"},
	{"the-sidewinder", "The Sidewinder", "upbeat", true, "Lee Morgan", "The Sidewinder (1964)"},
	{"bye-bye-blackbird", "Bye Bye Blackbird", "upbeat", true, "Miles Davis", "'Round About Midnight (1957)"},
	{"bb-blues", "Bb Blues", "upbeat", true, "", ""},

	{"leave-the-door-open", "Leave the Door Open", "pop", true, "Silk Sonic", "An Evening with Silk Sonic (2021)"},
	{"die-with-a-smile", "Die With a Smile", "pop", true, "Lady Gaga & Bruno Mars", "single (2024)"},
	{"golden-hour", "Golden Hour", "pop", true, "JVKE", "single (2022)"},
	{"valerie", "Valerie", "pop", true, "Amy Winehouse", "Mark Ronson ft. Amy Winehouse version (2007)"},
	{"cant-help-falling-in-love", "Can't Help Falling in Love", "pop", true, "Elvis Presley", "Blue Hawaii (1961)"},
}

// Tunes that only exist in the legacy campaign roadmap (DATA.repertoire).
// They are imported as unchosen standards when started or practiced.
var legacyOnlyTunes = []repertoireSeed{
	{"all-of-me", "All of Me", "standard", false, "", ""},
	{"there-will-never", "There Will Never Be Another You", "standard", false, "", ""},
	{"satin-doll", "Satin Doll", "standard", false, "", ""},
	{"summertime", "Summertime", "standard", false, "", ""},
	{"solar", "Solar", "standard", false, "", ""},
}

var preludeToAKiss = repertoireSeed{"prelude-to-a-kiss", "Prelude to a Kiss", "ballad", false, "Duke Ellington", ""}

// legacyStageMilestones maps a legacy 0..6 roadmap stage (Not started, Melody,
// Form, Changes, Can solo, From memory, Gig ready) onto milestone statuses.
// Rules apply cumulatively; nothing beyond what the stage implies is set.
func legacyStageMilestones(stage int) map[string]string {
	result := map[string]string{}
	if stage >= 1 {
		result["melodyByEar"] = "learning"
	}
	if stage >= 3 {
		result["changes"] = "learning"
	}
	if stage >= 4 {
		result["improvise"] = "learning"
	}
	if stage >= 5 {
		result["melodyByEar"] = "solid"
		result["changes"] = "solid"
	}
	if stage >= 6 {
		result["gigReady"] = "solid"
	}
	return result
}
