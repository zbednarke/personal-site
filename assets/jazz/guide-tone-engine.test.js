const test = require("node:test");
const assert = require("node:assert/strict");
const E = require("./guide-tone-engine.js");

test("minor seventh guide tones are spelled correctly", () => {
  const chord = { root: "C", quality: "m7" };
  assert.equal(E.chordSymbol(chord, "concert"), "Cm7");
  assert.equal(E.guideToneName(chord, 3), "E♭");
  assert.equal(E.guideToneName(chord, 7), "B♭");
  assert.deepEqual(E.targetForChord(chord, 3, "concert"), {
    degree: 3,
    concertPitchClass: 3,
    writtenPitchClass: 3,
    concertName: "E♭",
    displayName: "E♭",
  });
});

test("Bb trumpet display transposes the chart but grades concert pitch", () => {
  const chord = { root: "C", quality: "m7" };
  assert.equal(E.chordSymbol(chord, "bb-trumpet"), "Dm7");
  assert.deepEqual(E.targetForChord(chord, 3, "bb-trumpet"), {
    degree: 3,
    concertPitchClass: 3,
    writtenPitchClass: 5,
    concertName: "E♭",
    displayName: "F",
  });
  assert.equal(E.targetForChord(chord, 7, "bb-trumpet").displayName, "C");
});

test("dominant guide tones preserve theoretical spelling", () => {
  const chord = { root: "G", quality: "7b9" };
  assert.equal(E.guideToneName(chord, 3), "B");
  assert.equal(E.guideToneName(chord, 7), "F");
  assert.equal(E.chordSymbol(chord, "bb-trumpet"), "A7(♭9)");
  assert.equal(E.targetForChord(chord, 3, "bb-trumpet").displayName, "C♯");
});

test("pitch grading ignores octave", () => {
  assert.deepEqual(E.gradePitch(63, 3), { playedPitchClass: 3, correct: true });
  assert.deepEqual(E.gradePitch(75, 3), { playedPitchClass: 3, correct: true });
  assert.equal(E.gradePitch(62, 3).correct, false);
});

test("Blue Bossa flattens to seventeen chord events and sixty-four beats", () => {
  const bars = Array.from({ length: 15 }, () => [{ root: "C", quality: "m7", beats: 4 }]);
  bars.push([{ root: "D", quality: "m7b5", beats: 2 }, { root: "G", quality: "7b9", beats: 2 }]);
  const events = E.flattenTune({ bars });
  assert.equal(events.length, 17);
  assert.equal(events.reduce((sum, event) => sum + event.beats, 0), 64);
  assert.equal(E.eventDurationMS(events[0], 60), 4000);
  assert.equal(E.eventDurationMS(events[16], 60), 2000);
});
