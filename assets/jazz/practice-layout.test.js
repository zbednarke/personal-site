const { test } = require("node:test");
const assert = require("node:assert/strict");
const layout = require("./practice-layout.js");
test("reordering is a draft until saved and supports undoing removals", () => {
  const base = layout.create(["a", "b", "c"]);
  let draft = layout.move(base, "c", 0);
  draft = layout.remove(draft, "b");
  assert.deepEqual(layout.kept(draft), ["c", "a"]);
  assert.deepEqual(base.order, ["a", "b", "c"]);
  draft = layout.remove(draft, "b");
  assert.deepEqual(layout.kept(draft), ["c", "a", "b"]);
  assert.equal(layout.dirty(draft), true);
});
test("reconciliation preserves edits and incorporates another device's additions and deletions", () => {
  let draft = layout.move(layout.create(["a", "b", "c"]), "c", 0);
  draft = layout.remove(draft, "a");
  draft = layout.reconcile(draft, ["a", "c", "new"]);
  assert.deepEqual(layout.kept(draft), ["c", "new"]);
  assert.deepEqual(draft.removed, ["a"]);
  draft = layout.reconcile(draft, ["c", "new"]);
  assert.equal(layout.dirty(draft), false);
});
test("all sections can be marked, undone, and a new section can arrive", () => {
  let draft = layout.remove(layout.create(["a"]), "a");
  assert.deepEqual(layout.kept(draft), []);
  draft = layout.reconcile(draft, ["a", "new"]);
  assert.deepEqual(layout.kept(draft), ["new"]);
  assert.deepEqual(layout.kept(layout.remove(draft, "a")), ["a", "new"]);
});
test("drop positions use variable row midpoints and handle either end", () => {
  const rows = [{ top: 100, height: 40 }, { top: 150, height: 100 }];
  assert.equal(layout.insertionIndex(90, rows), 0);
  assert.equal(layout.insertionIndex(140, rows), 1);
  assert.equal(layout.insertionIndex(220, rows), 2);
  assert.equal(layout.insertionIndex(100, []), 0);
});
