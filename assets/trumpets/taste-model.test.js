const test = require("node:test"),
  assert = require("node:assert/strict"),
  T = require("./taste-model");

test("drags become swipes only past a threshold", () => {
  assert.equal(T.gesture(140, 10, 360), "right");
  assert.equal(T.gesture(-140, 20, 360), "left");
  assert.equal(T.gesture(20, -130, 360), "up");
  assert.equal(T.gesture(60, 5, 360), null);
  assert.equal(T.gesture(-40, -60, 360), null);
  // A diagonal drag that is mostly horizontal is not "up".
  assert.equal(T.gesture(160, -100, 360), "right");
  // Wide desktop cards need a proportionally longer drag.
  assert.equal(T.gesture(110, 0, 600), null);
});

test("keyboard shortcuts per mode", () => {
  assert.deepEqual(T.keyAction("ArrowRight", "swipe"), { kind: "swipe", action: "right" });
  assert.deepEqual(T.keyAction("ArrowUp", "swipe"), { kind: "swipe", action: "up" });
  assert.deepEqual(T.keyAction("ArrowLeft", "pair"), { kind: "pair", action: "a" });
  assert.deepEqual(T.keyAction("n", "pair"), { kind: "pair", action: "neither" });
  assert.deepEqual(T.keyAction("z", "pair"), { kind: "undo" });
  assert.equal(T.keyAction("q", "swipe"), null);
});

test("local answers leave the server queue and pairs", () => {
  const done = new Set(["one"]);
  assert.deepEqual(
    T.nextCards([{ listingId: "one" }, { listingId: "two" }], done).map((q) => q.listingId),
    ["two"],
  );
  const pairs = [
    { a: "b", b: "a" },
    { a: "a", b: "c" },
  ];
  assert.deepEqual(T.nextPairs(pairs, new Set([T.pairKey({ a: "a", b: "b" })])), [{ a: "a", b: "c" }]);
});

test("learned panel lists strongest liked and disliked traits", () => {
  const weights = [
    { feature: "finish:raw brass", kind: "finish", label: "Raw brass", weight: 0.9, sd: 0.4 },
    { feature: "maker:fixture", kind: "maker", label: "Fixturecraft", weight: 0.3, sd: 0.6 },
    { feature: "finish:gold plate", kind: "finish", label: "Gold plate", weight: -0.7, sd: 0.5 },
    { feature: "x:noise", kind: "maker", label: "Noise", weight: 0.01, sd: 1 },
  ];
  const l = T.learned(weights);
  assert.deepEqual(l.liked.map((w) => w.label), ["Raw brass", "Fixturecraft"]);
  assert.deepEqual(l.disliked.map((w) => w.label), ["Gold plate"]);
  assert.equal(T.weightText(l.liked[0]), "+0.90 ± 0.40");
  assert.equal(T.changeText({ before: 0.2, after: -0.3 }), "↓ +0.20 → −0.30");
  assert.equal(T.confidenceLabel({ confidence: 0, swipes: 0, pairs: 0 }).text, "Just starting · 0 reactions");
  assert.equal(T.confidenceLabel({ confidence: 0.6, swipes: 9, pairs: 3 }).level, "Fairly confident");
});

test("notes and owner preferences", () => {
  assert.deepEqual(
    T.noteStatements({ favoredAttributes: { "raw brass": 2 }, dislikedAttributes: { "gold plate": 1 }, pricePreferences: [{ maker: "Fixturecraft" }] }),
    ["Likes raw brass ×2", "Not keen on gold plate", "Price-sensitive on Fixturecraft"],
  );
  assert.deepEqual(
    T.preferencesFromForm({ statements: "cool horn, not dark horn\n\n raw over shiny ", likedMakers: "Fixturecraft, , Brassworks", dislikedMakers: "", likedTraits: "raw brass", dislikedTraits: "", priceGuidance: "  value " }),
    { statements: ["cool horn, not dark horn", "raw over shiny"], likedMakers: ["Fixturecraft", "Brassworks"], dislikedMakers: [], likedTraits: ["raw brass"], dislikedTraits: [], priceGuidance: "value" },
  );
  const id = T.clientId();
  assert.match(id, /^[a-z0-9]{21}$/);
  assert.notEqual(id, T.clientId());
});
