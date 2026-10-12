const test = require("node:test"),
  assert = require("node:assert/strict"),
  T = require("./taste-model");

test("drags commit only past 40% of the card (140 px at most)", () => {
  assert.equal(T.commitDistance(390), 140);
  assert.equal(T.commitDistance(300), 120);
  assert.equal(T.gesture(140, 10, 390), "right");
  assert.equal(T.gesture(-150, 20, 390), "left");
  // The reported iPhone case: a ~100-120 px partial drag used to commit.
  assert.equal(T.gesture(100, 0, 390), null);
  assert.equal(T.gesture(120, 4, 390), null);
  assert.equal(T.gesture(-130, 0, 390), null);
  assert.equal(T.gesture(60, 5, 360), null);
  // A diagonal drag that is mostly horizontal is not "up".
  assert.equal(T.gesture(160, -100, 390), "right");
});

test("a fast fling commits a shorter drag in the same direction", () => {
  const fling = { axis: "x", vx: 0.9 };
  assert.equal(T.gesture(80, 0, 390, fling), "right");
  assert.equal(T.gesture(-80, 0, 390, { axis: "x", vx: -0.9 }), "left");
  assert.equal(T.gesture(50, 0, 390, fling), null, "a fling still needs 60 px");
  assert.equal(T.gesture(80, 0, 390, { axis: "x", vx: 0.4 }), null, "a slow release springs back");
  assert.equal(T.gesture(80, 0, 390, { axis: "x", vx: -0.9 }), null, "flicking back does not commit");
  // Velocity uses only the last ~80 ms of samples.
  const v = T.velocity([
    { t: 0, x: 0, y: 0 },
    { t: 500, x: 10, y: 0 },
    { t: 540, x: 40, y: 0 },
    { t: 580, x: 90, y: 0 },
  ]);
  assert.ok(Math.abs(v.vx - 1) < 1e-9, String(v.vx));
  assert.deepEqual(T.velocity([{ t: 0, x: 0, y: 0 }]), { vx: 0, vy: 0 });
});

test("up needs a long vertical drag, and for touch a card grabbed sideways", () => {
  assert.equal(T.gesture(20, -170, 390, { axis: "y", pointerType: "mouse" }), "up");
  assert.equal(T.gesture(20, -170, 390, { axis: "y", pointerType: "pen" }), "up");
  assert.equal(T.gesture(20, -130, 390, { axis: "y", pointerType: "mouse" }), null, "the old 90 px rule is gone");
  assert.equal(T.gesture(100, -170, 390, { axis: "y", pointerType: "mouse" }), null, "|dy| must exceed 2|dx|");
  // A vertical touch drag is page scrolling, never "must see".
  assert.equal(T.gesture(0, -300, 390, { axis: "y", pointerType: "touch" }), null);
  assert.equal(T.gesture(10, -300, 390, { axis: "x", pointerType: "touch" }), "up");
  assert.equal(T.gesture(0, -300, 390, { axis: null, pointerType: "mouse" }), null, "no axis yet");
});

test("the axis locks after ~10 px so diagonal jitter cannot flip intent", () => {
  assert.equal(T.lockAxis(null, 6, -5), null);
  assert.equal(T.lockAxis(null, 12, -5), "x");
  assert.equal(T.lockAxis(null, 3, -11), "y");
  assert.equal(T.lockAxis("x", 0, -200), "x", "a decided axis stays");
  // Locked sideways: a long horizontal drag commits even as it wanders up.
  assert.equal(T.gesture(150, -120, 390, { axis: "x", pointerType: "touch" }), "right");
  // Locked vertical (mouse): wandering sideways never becomes left/right.
  assert.equal(T.gesture(170, -60, 390, { axis: "y", pointerType: "mouse" }), null);
});

test("drag progress ramps toward the commit point", () => {
  const half = T.dragState({ dx: 70, dy: 0, width: 390, axis: "x" });
  assert.deepEqual(half, { direction: "right", progress: 0.5, commit: false });
  assert.deepEqual(T.dragState({ dx: 140, dy: 0, width: 390, axis: "x" }), { direction: "right", progress: 1, commit: true });
  assert.equal(T.dragState({ dx: 0, dy: -80, width: 390, axis: "y", pointerType: "mouse" }).progress, 0.5);
  assert.deepEqual(T.dragState({ dx: 5, dy: 3, width: 390, axis: null }), { direction: null, progress: 0, commit: false });
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

test("an undone card or pair goes back in front wherever the server lists it", () => {
  const queue = [
    { listingId: "x", reason: "new" },
    { listingId: "y", reason: "new" },
    { listingId: "undone", reason: "uncertain", taste: 61 },
  ];
  // Listed further down: moved to the front, keeping the server's item.
  assert.deepEqual(T.cardToFront(queue, "undone"), [queue[2], queue[0], queue[1]]);
  assert.equal(queue[0].listingId, "x", "the server's queue is not mutated");
  // Already first: unchanged. Missing: added in front.
  assert.deepEqual(T.cardToFront(queue, "x"), queue);
  assert.deepEqual(T.cardToFront([{ listingId: "x" }], "gone").map((q) => q.listingId), ["gone", "x"]);
  assert.deepEqual(T.cardToFront(undefined, "gone"), [{ listingId: "gone", reason: "new", taste: 50, sd: 1 }]);
  const pairs = [
    { a: "a", b: "c", p: 0.4 },
    { a: "b", b: "a", p: 0.7 },
  ];
  assert.deepEqual(T.pairToFront(pairs, T.pairKey({ a: "a", b: "b" })), [pairs[1], pairs[0]]);
  assert.deepEqual(T.pairToFront([], "a|b"), [{ a: "a", b: "b", p: 0.5, sd: 1 }]);
});

test("an undone card is pinned while its horn is still on the board, once per horn", () => {
  const horns = { x: "h1", y: "h2", relist: "h3", undone: "h3" };
  const queue = [{ listingId: "x" }, { listingId: "relist" }, { listingId: "y" }];
  // The server now lists the horn under another listing: shown once, as the undone one.
  assert.deepEqual(
    T.cardToFront(queue, "undone", (id) => horns[id]).map((q) => q.listingId),
    ["undone", "x", "y"],
  );
  // Without horn information nothing else is dropped.
  assert.deepEqual(T.cardToFront(queue, "undone").map((q) => q.listingId), ["undone", "x", "relist", "y"]);
  assert.equal(T.pinnable({ feedback: { interestState: "watch" } }), true);
  assert.equal(T.pinnable(null), false, "gone from the board");
  assert.equal(T.pinnable({ acquired: true, feedback: {} }), false);
  assert.equal(T.pinnable({ feedback: { interestState: "pass" } }), false);
});

test("head to head explains a shortage of comparable listings", () => {
  assert.equal(T.pairShortage({ pairEligible: 6, pairMinimum: 6 }), null);
  assert.equal(T.pairShortage({}), null, "older servers: no claim");
  const s = T.pairShortage({ pairEligible: 2, pairMinimum: 6 });
  assert.equal(s.title, "Head to head needs more verified listings with photos — 2 eligible");
  assert.match(s.note, /at least 6/);
});

test("the comparison graph line", () => {
  assert.equal(T.graphText(null), "");
  assert.equal(T.graphText({ eligible: 0, components: 0, largest: 0 }), "");
  assert.equal(T.graphText({ eligible: 92, components: 4, largest: 84 }), "Comparisons connect 84 of 92 eligible horns (3 islands)");
  assert.equal(T.graphText({ eligible: 7, components: 2, largest: 6 }), "Comparisons connect 6 of 7 eligible horns (1 island)");
  assert.equal(T.graphText({ eligible: 12, components: 1, largest: 12 }), "Comparisons connect all 12 eligible horns");
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
