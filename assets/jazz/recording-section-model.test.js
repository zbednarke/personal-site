const test = require("node:test");
const assert = require("node:assert/strict");
const Model = require("./recording-section-model.js");

test("a conflict refreshes the picker recording with the latest assignment revision", () => {
  const stale = { id: "take-1", practiceBlockId: "warm-up", sectionAssignmentRevision: 0, notes: "keep", mediaKind: "video", contentType: "audio/wav", skillIds: ["sound"], durationMs: 32000 };
  assert.equal(Model.applyFreshRecording(stale, [{ id: "take-1", practiceBlockId: "long-tones", sectionAssignmentRevision: 2, notes: "keep" }]), true);
  assert.equal(stale.practiceBlockId, "long-tones");
  assert.equal(stale.sectionAssignmentRevision, 2);
  assert.equal(stale.mediaKind, "video");
  assert.equal(stale.contentType, "audio/wav");
  assert.deepEqual(stale.skillIds, ["sound"]);
  assert.equal(stale.durationMs, 32000);
});

test("archive and Studio label refreshes do not pause or seek active playback", () => {
  let renders = 0;
  const media = { paused: false, ended: false, currentTime: 42.5, pause() { throw new Error("must not pause"); } };
  assert.equal(Model.playbackIsActive(media), true);
  const before = Model.refreshLabelsWithoutInterrupting(media, () => { renders += 1; });
  assert.equal(renders, 1);
  assert.deepEqual(before, { currentTime: 42.5, wasPaused: false });
  assert.equal(media.currentTime, 42.5);
  assert.equal(media.paused, false);
});

test("Today reparents a player with its time and playing state intact", () => {
  const mediaNode = { id: "player" };
  const wrapper = { children: [], append(value) { this.children.push(value); } };
  const host = { children: [], append(value) { this.children.push(value); } };
  const media = { paused: false, ended: false, currentTime: 17, playCalls: 0, play() { this.playCalls += 1; return Promise.resolve(); } };
  const snapshot = Model.reparentMedia(media, mediaNode, host, wrapper);
  assert.deepEqual(snapshot, { currentTime: 17, wasPlaying: true });
  assert.equal(host.children[0], wrapper);
  assert.equal(wrapper.children[0], mediaNode);
  assert.equal(media.currentTime, 17);
  assert.equal(media.playCalls, 0);
});
