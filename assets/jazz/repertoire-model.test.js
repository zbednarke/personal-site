const { test } = require("node:test");
const assert = require("node:assert/strict");
const path = require("node:path");
const fs = require("node:fs");
const model = require("./repertoire-model.js");

const goals = { ballad: { target: 10, measure: "deeplyLearned" }, upbeat: { target: 8, measure: "gigReady" }, pop: { target: 5, measure: "gigReady" } };
const rules = { minBallads: 2, minUpbeat: 6, minPop: 2, minTotal: 12 };

function tune(tuneId, category, overrides = {}) {
  return model.deriveTuneState({ tuneId, title: tuneId, category, chosen: true, position: 0, keysKnown: [], milestones: {}, ...overrides });
}
const deep = { melodyByEar: "solid", lyrics: "solid", transcription: "solid" };
const gig = { gigReady: "solid" };

test("slugify matches the shared Go fixture", () => {
  const fixture = JSON.parse(fs.readFileSync(path.join(__dirname, "../../jazz-api/testdata/repertoire_slugs.json"), "utf8"));
  for (const [input, expected] of fixture) assert.equal(model.slugify(input), expected, input);
});

test("derived state mirrors the server rules", () => {
  assert.equal(tune("a", "ballad").practiceStatus, "not_started");
  assert.equal(tune("a", "ballad", { milestones: deep, keysKnown: ["C", "F"] }).deeplyLearned, true);
  assert.equal(tune("a", "ballad", { milestones: deep, keysKnown: ["C"] }).deeplyLearned, false);
  assert.equal(tune("a", "ballad", { totalPracticeMs: 120000 }).practiceStatus, "learning");
  assert.equal(tune("a", "ballad", { milestones: gig }).practiceStatus, "gig_ready");
  assert.equal(tune("a", "ballad", { keysKnown: ["C"] }).milestones.keysKnown, "learning");
  assert.equal(model.nextMilestoneStatus("not_started"), "learning");
  assert.equal(model.nextMilestoneStatus("learning"), "solid");
  assert.equal(model.nextMilestoneStatus("solid"), "not_started");
});

test("category progress counts chosen tunes only and caps at the goal", () => {
  const ballads = Array.from({ length: 11 }, (_, index) => tune(`b${index}`, "ballad", { milestones: deep, keysKnown: ["C", "D"] }));
  ballads.push(tune("unchosen", "ballad", { chosen: false, milestones: deep, keysKnown: ["C", "D"] }));
  ballads.push(tune("archived", "ballad", { archivedAt: "2026-10-01T00:00:00Z", milestones: deep, keysKnown: ["C", "D"] }));
  const [ballad, upbeat] = model.categoryProgress([...ballads, tune("u", "upbeat", { milestones: gig })], goals);
  assert.deepEqual([ballad.chosen, ballad.achieved, ballad.counted, ballad.onDeck, ballad.overChosen, ballad.percent], [11, 11, 10, 1, true, 100]);
  assert.deepEqual([upbeat.chosen, upbeat.counted, upbeat.gigReady], [1, 1, 1]);
});

test("set readiness verdicts", () => {
  const many = (category, count) => Array.from({ length: count }, (_, index) => tune(`${category}${index}`, category, { milestones: gig }));
  assert.equal(model.setReadiness([...many("ballad", 2), ...many("upbeat", 8), ...many("pop", 2)], rules).label, "Ready to hold a set");
  assert.equal(model.setReadiness([...many("ballad", 1), ...many("upbeat", 4), ...many("pop", 2), ...many("standard", 4)], rules).label, "Almost: need 2 more upbeat, 1 ballad");
  assert.equal(model.setReadiness([...many("upbeat", 6), ...many("ballad", 2), ...many("pop", 2)], rules).label, "Almost: need 2 more of anything");
  const none = model.setReadiness([tune("x", "ballad")], rules);
  assert.equal(none.verdict, "not_yet");
  assert.equal(none.label, "Not yet: need 6 more upbeat, 2 ballads, 2 pop, 2 more of anything");
  const callable = model.setReadiness([...many("ballad", 1), tune("archived", "pop", { milestones: gig, archivedAt: "x" })], rules).callable;
  assert.equal(callable.ballad.length, 1);
  assert.equal(callable.pop.length, 0);
});

test("pace projection around week boundaries and past the target", () => {
  const done = [tune("u1", "upbeat", { milestones: gig }), tune("u2", "upbeat", { milestones: gig })];
  const start = model.paceProjection(done, "2026-10-05", "2027-03-20", 1, goals, "2026-10-05");
  assert.deepEqual([start.week, start.expected, start.onPace, start.done, start.goalTotal], [1, 0, true, 2, 23]);
  assert.equal(model.paceProjection(done, "2026-10-11", "2027-03-20", 1, goals, "2026-10-05").week, 1);
  const weekThree = model.paceProjection(done, "2026-10-19", "2027-03-20", 1, goals, "2026-10-05");
  assert.deepEqual([weekThree.week, weekThree.expected, weekThree.behind, weekThree.onPace], [3, 2, 0, true]);
  const behind = model.paceProjection(done, "2026-11-02", "2027-03-20", 1, goals, "2026-10-05");
  assert.deepEqual([behind.expected, behind.behind, behind.onPace], [4, 2, false]);
  assert.equal(behind.projectedDate, "2027-03-29");
  assert.equal(behind.projectedLate, true);
  const late = model.paceProjection(done, "2027-04-01", "2027-03-20", 1, goals, "2026-10-05");
  assert.deepEqual([late.pastTarget, late.weeksLeft, late.expected], [true, 0, 23]);
});

test("transposes concert keys to written B-flat trumpet keys", () => {
  for (const [concert, written] of [["C", "D"], ["Bb", "C"], ["F#m", "G#m"], ["Eb", "F"], ["E", "F#"], ["B", "C#"], ["Ab", "Bb"], ["Dbm", "Ebm"], ["Gm", "Am"]]) {
    assert.equal(model.transposeKey(concert, "bb-trumpet"), written, concert);
  }
  assert.equal(model.transposeKey("bb", "concert"), "Bb");
  assert.equal(model.transposeKey("nope"), "");
});

test("keys normalize and dedupe enharmonically", () => {
  assert.equal(model.normalizeKey("bb"), "Bb");
  assert.equal(model.normalizeKey("E♭ minor"), "Ebm");
  assert.deepEqual(model.addKey(["Bb"], "a#").keys, ["Bb"]);
  assert.match(model.addKey(["Bb"], "a#").error, /already/);
  assert.deepEqual(model.addKey(["Bb"], "a#m").keys, ["Bb", "A#m"]);
  assert.match(model.addKey([], "H").error, /Use a key/);
});

test("merging after a conflict keeps non-conflicting local edits and lets the server win the rest", () => {
  const base = tune("s", "ballad", { title: "Skylark", notes: "", keysKnown: ["C"], milestones: { lyrics: "learning" } });
  const local = { ...base, notes: "Breathe before the bridge", title: "Skylark (mine)", milestones: { ...base.milestones, melodyByEar: "solid" } };
  const server = { ...base, title: "Skylark (theirs)", revision: 5, milestones: { ...base.milestones, lyrics: "solid" } };
  const merged = model.mergeServerTune(base, local, server);
  assert.deepEqual(merged.conflicts, ["title"]);
  assert.deepEqual(merged.patch, { notes: "Breathe before the bridge", milestones: { melodyByEar: "solid" } });
  assert.equal(merged.tune.title, "Skylark (theirs)");
  assert.equal(merged.tune.milestones.lyrics, "solid");
  assert.equal(merged.tune.milestones.melodyByEar, "solid");
  assert.equal(merged.tune.revision, 5);
});

test("an archive or restore that hits a conflict is re-sent, not dropped", () => {
  const base = tune("s", "ballad", { revision: 3 });
  const local = { ...base, archivedAt: "2026-10-09T00:00:00Z" };
  const server = { ...base, revision: 4, notes: "edited elsewhere" };
  const merged = model.mergeServerTune(base, local, server);
  assert.deepEqual(merged.patch, { archived: true });
  assert.ok(merged.tune.archivedAt);
  assert.equal(merged.tune.notes, "edited elsewhere");
  const restored = model.mergeServerTune({ ...base, archivedAt: "x" }, base, { ...base, archivedAt: "x", revision: 4 });
  assert.deepEqual(restored.patch, { archived: false });
  assert.equal(restored.tune.archivedAt, undefined);
});

test("practice suggestions follow the by-ear-first path", () => {
  assert.equal(model.suggestFocus(tune("a", "ballad")), "melody");
  assert.equal(model.suggestFocus(tune("a", "ballad", { milestones: { melodyByEar: "solid" }, keysKnown: ["C"] })), "key");
  assert.equal(model.suggestFocus(tune("a", "upbeat", { milestones: { melodyByEar: "solid" }, keysKnown: ["C", "F"] })), "transcribe");
  const pick = model.suggestTuneOfWeek([
    tune("u1", "upbeat", { milestones: gig }),
    tune("b1", "ballad", { position: 1 }),
    tune("b0", "ballad", { position: 0 }),
    tune("b2", "ballad", { position: 2, totalPracticeMs: 1000 }),
  ], goals);
  assert.equal(pick.tuneId, "b2");
  assert.equal(model.isRusty(tune("g", "pop", { milestones: gig, lastPracticedDate: "2026-09-20" }), "2026-10-09"), true);
  assert.equal(model.isRusty(tune("g", "pop", { milestones: gig, lastPracticedDate: "2026-10-01" }), "2026-10-09"), false);
  const order = model.suggestedSetOrder({ upbeat: ["u1", "u2", "u3", "u4"], ballad: ["b1", "b2"], pop: ["p1"], standard: [] });
  assert.deepEqual(order, ["u1", "u2", "b1", "u3", "p1", "u4", "b2"]);
});

test("Practice now builds a valid linked block definition", () => {
  const definition = model.practiceBlockDefinition({ tuneId: "skylark", title: "Skylark" }, { label: "Learn melody by ear", instructions: "By ear.", minutes: 15 }, { position: 4, suffix: "abc123" });
  assert.deepEqual(definition, { blockKey: "tune-skylark-abc123", position: 4, title: "Skylark: learn melody by ear", instructions: "By ear.", category: "repertoire", track: "musician", targetMinutes: 15, tuneId: "skylark", dayOnly: true });
  assert.match(definition.blockKey, /^[a-z0-9-]{1,80}$/);
  assert.equal(model.practiceBlockDefinition({ tuneId: "x", title: "X" }, { label: "Set", instructions: "" }, { keepDaily: true }).dayOnly, false);
  assert.equal(model.lastPracticedLabel("2026-10-06", "2026-10-09"), "Last practiced 3 days ago");
  assert.equal(model.formatPracticeTime(8100000), "2h 15m");
});
