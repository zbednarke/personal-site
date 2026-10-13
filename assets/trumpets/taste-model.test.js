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

test("pins: validated against the board once it has loaded; pairs must stay comparable and appear once per pair of horns", () => {
  const now = Date.parse("2026-10-12T12:00:00Z");
  const good = (id, horn) => ({ id, hornId: horn, status: "active", verificationState: "verified", images: ["x"], price: 2000, currency: "USD", lastChecked: "2026-10-11T12:00:00Z", feedback: { interestState: "watch" } });
  const board = { a: good("a", "ha"), b: good("b", "hb"), a2: good("a2", "ha"), c: { ...good("c", "hc"), lastChecked: "2026-09-20T00:00:00Z" } };
  const listing = (id) => board[id] || null;
  assert.equal(T.comparable(board.a, now), true);
  assert.equal(T.comparable(board.c, now), false, "not checked in 10 days");
  assert.equal(T.comparable({ ...board.a, price: null }, now), false);
  assert.equal(T.comparable({ ...board.a, images: [] }, now), false);
  assert.equal(T.comparable({ ...board.a, verificationState: "candidate" }, now), false);
  // The pinned pair goes first; the same two horns under other listings go.
  const state = { queue: [{ listingId: "b" }], pairs: [{ a: "a2", b: "b" }, { a: "b", b: "c" }] };
  let kept = T.keepFront(state, { frontCard: null, frontPair: "a|b" }, { listing, now });
  assert.deepEqual(kept.pairs, [{ a: "a", b: "b", p: 0.5, sd: 1 }, { a: "b", b: "c" }]);
  assert.equal(kept.frontPair, "a|b");
  // A pair with a side no longer comparable lets go.
  kept = T.keepFront(state, { frontCard: null, frontPair: "b|c" }, { listing, now });
  assert.equal(kept.frontPair, null);
  assert.deepEqual(kept.pairs, state.pairs);
  // A card whose horn left the board lets go, but only once the board has
  // loaded: before that the pin is kept (and checked when it loads).
  kept = T.keepFront(state, { frontCard: "gone", frontPair: null }, { listing, now, boardReady: false });
  assert.equal(kept.frontCard, "gone");
  assert.equal(kept.queue[0].listingId, "gone");
  kept = T.keepFront(state, { frontCard: "gone", frontPair: null }, { listing, now, boardReady: true });
  assert.equal(kept.frontCard, null);
  assert.deepEqual(kept.queue, state.queue);
});

test("a reply applies only if its watermark is not older than the one on screen", () => {
  const on = { events: 10, preferences: 2 };
  assert.equal(T.newerOrSame({ events: 11, preferences: 2 }, on), true);
  assert.equal(T.newerOrSame({ events: 10, preferences: 2 }, on), true, "ties apply");
  assert.equal(T.newerOrSame({ events: 10, preferences: 3 }, on), true);
  assert.equal(T.newerOrSame({ events: 9, preferences: 5 }, on), false, "computed before an event on screen");
  assert.equal(T.newerOrSame({ events: 10, preferences: 1 }, on), false);
  assert.equal(T.newerOrSame(undefined, on), true, "older servers send none");
  assert.equal(T.newerOrSame(on, null), true, "nothing on screen yet");
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

test("head to head progress line: the main group until the graph is connected", () => {
  // "largest" counts horns in the biggest group, so it is "in the main group".
  assert.equal(T.linkText({ eligible: 177, components: 163, largest: 15, share: 0.085 }), "Linking horns · 15 of 177 in the main group");
  // Before any comparison links two horns: say so, not "1 of 177".
  assert.equal(T.linkText({ eligible: 177, components: 177, largest: 1, share: 0.006 }), "No horns linked yet · 177 eligible");
  assert.equal(T.linkText({ eligible: 177, components: 1, largest: 177, share: 1 }), "All 177 eligible horns connected");
  // No graph (an older API), or nothing to link: no line at all.
  assert.equal(T.linkText(undefined), "");
  assert.equal(T.linkText(null), "");
  assert.equal(T.linkText({}), "");
  assert.equal(T.linkText({ eligible: 0, components: 0, largest: 0, share: 0 }), "");
  assert.equal(T.linkText({ eligible: 1, components: 1, largest: 1, share: 1 }), "");
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

test("head to head skip: K opens the reasons, then 1-6, K or Escape", () => {
  assert.deepEqual(T.keyAction("k", "pair"), { kind: "skip-open" });
  assert.deepEqual(T.keyAction("K", "pair"), { kind: "skip-open" });
  assert.equal(T.keyAction("k", "swipe"), null, "no skip in Swipe");
  // While the reasons are open, digits pick a reason instead of a side.
  assert.deepEqual(T.keyAction("1", "pair", { skipOpen: true }), { kind: "pair", action: "skip", reasons: ["not_comparable"] });
  assert.deepEqual(T.keyAction("5", "pair", { skipOpen: true }), { kind: "pair", action: "skip", reasons: ["not_bb"] });
  assert.equal(T.keyAction("9", "pair", { skipOpen: true }), null);
  assert.deepEqual(T.keyAction("k", "pair", { skipOpen: true }), { kind: "pair", action: "skip", reasons: [] });
  assert.deepEqual(T.keyAction("Escape", "pair", { skipOpen: true }), { kind: "skip-cancel" });
  assert.deepEqual(T.keyAction("z", "pair", { skipOpen: true }), { kind: "undo" });
  assert.deepEqual(T.keyAction("1", "pair"), { kind: "pair", action: "a" }, "closed, 1 still picks the left horn");
  assert.deepEqual(
    T.SKIP_REASONS.map((r) => r.label),
    ["Not comparable", "Bad or missing info", "Sold / stale", "Duplicate", "Not a Bb trumpet", "Other"],
  );
  assert.equal(T.skipReasonLabel("stale"), "Sold / stale");
});

test("skipped pairs: what the investigation found, in words", () => {
  assert.equal(T.skipCauseText(null), "Waiting for the daily check");
  assert.equal(T.skipCauseText({ cause: "data_problem", side: "b" }), "Data problem (right listing)");
  assert.equal(T.skipCauseText({ cause: "incomparable", dimension: "price" }), "Genuinely different (price)");
  assert.equal(T.skipCauseText({ cause: "wrong_category" }), "Not a Bb trumpet");
  assert.equal(T.lessonText({ dimension: "price", ratio: 2.5, lessons: 2 }), "Avoiding pairs whose prices are 2.5× or more apart (2 skips)");
  assert.equal(T.lessonText({ dimension: "condition", classes: "new|project", lessons: 3 }), "Avoiding condition new vs project pairs (3 skips)");
});

// The card or pair on screen stays until it is answered (the phone report:
// a reply arriving after the next pair appeared swapped one side of it).
// Fictional listings only.
{
  const now = Date.parse("2026-10-12T12:00:00Z");
  const good = (id, horn) => ({ id, hornId: horn, status: "active", verificationState: "verified", images: ["x"], price: 2000, currency: "USD", lastChecked: "2026-10-11T12:00:00Z", feedback: { interestState: "watch" } });
  const board = { a: good("a", "ha"), b: good("b", "hb"), c: good("c", "hc"), d: good("d", "hd"), e: good("e", "he"), a2: good("a2", "ha") };
  const listing = (id) => board[id] || null;
  const P = (a, b) => ({ a, b, p: 0.5, sd: 1 });
  const ctx = (over = {}) => ({ mode: "pair", listing, now, doneCards: new Set(), donePairs: new Set(), answers: [], recent: [], ...over });
  // keepFront with no pins, then keepShown: what the page does on each render.
  const show = (state, held, over) => T.keepShown(T.keepFront(state, { frontCard: null, frontPair: null }, { listing, now }), held, ctx(over));
  const ids = (pairs) => pairs.map(T.pairKey);

  test("the pair on screen stays when a reply lists a different first pair", () => {
    // Old list: a|b answered, b|c appears next and is held.
    const old = { pairs: [P("a", "b"), P("b", "c"), P("c", "d")] };
    const donePairs = new Set(["a|b"]);
    let s = show(old, null, { donePairs });
    const held = T.holdShown(s.held, "pair", T.nextPairs(s.pairs, donePairs)[0], { mode: "pair", seq: 1, events: 10 });
    assert.equal(T.pairKey(held.pair.item), "b|c");
    // The reply (avoiding recent horns) leads with d|e and drops b|c.
    const reply = { pairs: [P("d", "e"), P("c", "e")] };
    s = show(reply, held, { donePairs, answers: [{ seq: 1, ids: ["a", "b"] }] });
    assert.deepEqual(ids(T.nextPairs(s.pairs, donePairs)), ["b|c", "d|e", "c|e"], "held in front of the newest list");
    assert.equal(s.held.pair, held.pair);
    // Re-recording the same pair keeps when it first appeared.
    assert.equal(T.holdShown(s.held, "pair", P("c", "b"), { mode: "pair", seq: 5, events: 20 }).pair.seq, 1);
    // Once answered, the next pair is the newest list's first.
    donePairs.add("b|c");
    s = show(reply, s.held, { donePairs });
    assert.equal(s.held.pair, null);
    assert.deepEqual(ids(T.nextPairs(s.pairs, donePairs)), ["d|e", "c|e"]);
  });

  test("with nothing held (the reply came before the next render) the newest list is used directly", () => {
    const s = show({ pairs: [P("d", "e"), P("b", "c")], queue: [{ listingId: "e" }, { listingId: "c" }] }, { swipe: null, pair: null });
    assert.deepEqual(ids(s.pairs), ["d|e", "b|c"]);
    assert.deepEqual(s.queue.map((q) => q.listingId), ["e", "c"]);
    assert.equal(T.keepShown({ pairs: undefined }, null, ctx()).pairs, undefined, "no state yet");
  });

  test("a held pair lets go when it is no longer valid", () => {
    const held = { swipe: null, pair: { item: P("b", "c"), seq: 1, events: 10 } };
    const reply = { pairs: [P("d", "e")] };
    const front = (over) => T.nextPairs(show(reply, held, over).pairs, over?.donePairs || new Set()).map(T.pairKey)[0];
    assert.equal(front(), "b|c");
    assert.equal(front({ mode: "swipe" }), "d|e", "the mode switched");
    assert.equal(front({ donePairs: new Set(["b|c"]) }), "d|e", "answered or skipped here");
    assert.equal(front({ listing: (id) => (id === "c" ? null : listing(id)) }), "d|e", "a listing left the board");
    assert.equal(front({ listing: (id) => (id === "c" ? { ...board.c, feedback: { interestState: "pass" } } : listing(id)) }), "d|e", "no longer comparable");
    assert.equal(front({ listing: (id) => (id === "c" ? null : listing(id)), boardReady: false }), "b|c", "kept while the board loads");
    assert.equal(front({ answers: [{ seq: 2, ids: ["c", "e"] }] }), "d|e", "an answer since it appeared shares a horn");
    assert.equal(front({ answers: [{ seq: 1, ids: ["c", "e"] }] }), "b|c", "an answer from before it appeared does not count");
    assert.equal(front({ recent: [{ id: 11, kind: "pair", listingId: "c", otherListingId: "b", action: "skip" }] }), "d|e", "skipped in a newer state");
    assert.equal(front({ recent: [{ id: 11, kind: "pair", listingId: "c", otherListingId: "d", action: "a" }] }), "b|c", "another pair of horns");
    assert.equal(front({ recent: [{ id: 9, kind: "pair", listingId: "b", otherListingId: "c" }] }), "b|c", "an older event");
  });

  test("an undo's pin keeps priority over the held pair or card", () => {
    const held = { swipe: { item: { listingId: "c", reason: "new" }, seq: 1, events: 10 }, pair: { item: P("b", "c"), seq: 1, events: 10 } };
    const state = { pairs: [P("d", "e")], queue: [{ listingId: "d" }] };
    let kept = T.keepFront(state, { frontCard: null, frontPair: "a|b" }, { listing, now });
    let s = T.keepShown(kept, held, ctx());
    assert.deepEqual(ids(s.pairs), ["a|b", "d|e"]);
    assert.equal(s.held.pair, null, "the pinned pair is what is shown now");
    kept = T.keepFront(state, { frontCard: "a", frontPair: null }, { listing, now });
    s = T.keepShown(kept, held, ctx({ mode: "swipe" }));
    assert.deepEqual(s.queue.map((q) => q.listingId), ["a", "d"]);
    assert.equal(s.held.swipe, null);
    // The undone pair comes back with its sides where the owner saw them,
    // however the server lists it (or not at all).
    kept = T.keepFront({ pairs: [P("b", "a"), P("d", "e")] }, { frontCard: null, frontPair: "a|b", pairShown: P("a", "b") }, { listing, now });
    assert.deepEqual(kept.pairs, [P("a", "b"), P("d", "e")]);
    kept = T.keepFront({ pairs: [P("d", "e")] }, { frontCard: null, frontPair: "a|b", pairShown: P("b", "a") }, { listing, now });
    assert.deepEqual(kept.pairs[0], P("b", "a"));
    // A pin of the held pair itself is no conflict.
    kept = T.keepFront(state, { frontCard: null, frontPair: "b|c" }, { listing, now });
    assert.deepEqual(ids(T.keepShown(kept, held, ctx()).pairs), ["b|c", "d|e"]);
  });

  test("the swipe card on screen stays, once per horn, until answered", () => {
    const held = { swipe: { item: { listingId: "c", reason: "new", taste: 50, sd: 1 }, seq: 3, events: 10 }, pair: null };
    const reply = { queue: [{ listingId: "e" }, { listingId: "c", reason: "unsure" }, { listingId: "a" }] };
    let s = show(reply, held, { mode: "swipe" });
    assert.deepEqual(s.queue.map((q) => q.listingId), ["c", "e", "a"]);
    assert.equal(s.queue[0].reason, "new", "the card as shown");
    // Another listing of a held horn is not shown again after it.
    s = show({ queue: [{ listingId: "a2" }, { listingId: "e" }] }, { swipe: { item: { listingId: "a" }, seq: 0, events: 1 } }, { mode: "swipe" });
    assert.deepEqual(s.queue.map((q) => q.listingId), ["a", "e"]);
    assert.equal(show(reply, held, { mode: "swipe", doneCards: new Set(["c"]) }).queue[0].listingId, "e", "answered");
    assert.equal(show(reply, held, { mode: "pair" }).queue[0].listingId, "e", "the mode switched");
    assert.equal(show(reply, held, { mode: "swipe", listing: (id) => (id === "c" ? { ...board.c, acquired: true } : listing(id)) }).queue[0].listingId, "e", "acquired");
    assert.equal(show(reply, held, { mode: "swipe", recent: [{ id: 12, kind: "swipe", listingId: "c" }] }).queue[0].listingId, "e", "swiped in a newer state");
    assert.equal(show(reply, held, { mode: "swipe", answers: [{ seq: 4, ids: ["c"] }] }).queue[0].listingId, "e", "an answer since it appeared");
  });

  test("an exclusion in force or a price lesson in effect releases the hold", () => {
    const held = { swipe: { item: { listingId: "c" }, seq: 1, events: 10 }, pair: { item: P("b", "c"), seq: 1, events: 10 } };
    const reply = { pairs: [P("d", "e")], queue: [{ listingId: "e" }] };
    const x = (over) => ({ exclusions: [{ id: 1, listingId: "c", scope: "pair", active: true, inForce: true, ...over }] });
    const pairFront = (skips, over) => T.keepShown(T.keepFront(reply, {}, { listing, now }), held, ctx({ skips, ...over })).pairs[0];
    const cardFront = (skips) => T.keepShown(T.keepFront(reply, {}, { listing, now }), held, ctx({ mode: "swipe", skips })).queue[0].listingId;
    assert.equal(T.pairKey(pairFront(x())), "d|e", "Head to head exclusion of one side");
    assert.equal(T.pairKey(pairFront(x({ scope: "all" }))), "d|e");
    assert.equal(T.pairKey(pairFront(x({ active: false }))), "b|c", "switched off by the owner");
    assert.equal(T.pairKey(pairFront(x({ inForce: false }))), "b|c", "no longer applies");
    assert.equal(T.pairKey(pairFront(x({ listingId: "e" }))), "b|c", "another listing");
    assert.equal(cardFront(x()), "c", "a Head to head exclusion leaves Swipe alone");
    assert.equal(cardFront(x({ scope: "all" })), "e", "scope all covers Swipe");
    // Price lessons: b and c are both 2000 USD; make c 5000.
    const pricey = (id) => (id === "c" ? { ...board.c, price: 5000 } : listing(id));
    assert.equal(T.pairKey(pairFront({ rules: [{ dimension: "price", ratio: 2.5, lessons: 2 }] }, { listing: pricey })), "d|e", "2.5× apart");
    assert.equal(T.pairKey(pairFront({ rules: [{ dimension: "price", ratio: 3, lessons: 2 }] }, { listing: pricey })), "b|c");
    assert.equal(T.pairKey(pairFront({ rules: [{ dimension: "condition", classes: "new|project", lessons: 2 }] }, { listing: pricey })), "b|c", "not checked here");
    assert.equal(T.priceRuleAvoids({ dimension: "price", ratio: 1.5 }, { price: 1000, currency: "USD" }, { price: 1500, currency: "EUR" }), false, "only within one currency");
    assert.equal(T.priceRuleAvoids({ dimension: "price", ratio: 1.5 }, { price: 1000, currency: "USD" }, { price: 1490, currency: "USD" }), false);
    assert.equal(T.priceRuleAvoids({ dimension: "price", ratio: 1.5 }, { price: 1000, currency: "USD" }, { price: 1500, currency: "USD" }), true);
  });

  test("a full recent window that does not reach back to the hold releases it", () => {
    const held = { swipe: null, pair: { item: P("b", "c"), seq: 1, events: 10 } };
    const window = (from) => Array.from({ length: T.RECENT_CAP }, (_, i) => ({ id: from + T.RECENT_CAP - 1 - i, kind: "tag", listingId: "z" }));
    const front = (recent) => T.pairKey(T.keepShown(T.keepFront({ pairs: [P("d", "e")] }, {}, { listing, now }), held, ctx({ recent })).pairs[0]);
    assert.equal(front(window(11)), "b|c", "the window starts right after the watermark");
    assert.equal(front(window(5)), "b|c");
    assert.equal(front(window(12)), "d|e", "event 11 may have been dropped");
    assert.equal(front(window(12).slice(1)), "b|c", "a window under the cap is the whole history");
  });

  test("a reaction is only for what is rendered", () => {
    assert.equal(T.matchesRendered("pair", P("a", "b"), ["a", "b"]), true);
    assert.equal(T.matchesRendered("pair", P("a", "b"), ["b", "a"]), false, "sides swapped");
    assert.equal(T.matchesRendered("pair", P("a", "b"), ["a", "c"]), false);
    assert.equal(T.matchesRendered("pair", P("a", "b"), []), false);
    assert.equal(T.matchesRendered("swipe", { listingId: "a" }, ["a"]), true);
    assert.equal(T.matchesRendered("swipe", { listingId: "a" }, ["b"]), false);
    assert.equal(T.matchesRendered("swipe", null, ["a"]), false);
  });

  test("only what the owner can see is held", () => {
    const empty = { swipe: null, pair: null };
    assert.deepEqual(T.holdShown(empty, "pair", P("a", "b"), { mode: "swipe" }), empty, "the other mode's stage is hidden");
    assert.deepEqual(T.holdShown(empty, "swipe", { listingId: "a" }, { mode: "swipe", active: false }), empty, "the Taste view is hidden");
    const h = T.holdShown(empty, "swipe", { listingId: "a" }, { mode: "swipe", seq: 2, events: 7 });
    assert.deepEqual(h.swipe, { item: { listingId: "a" }, seq: 2, events: 7 });
    assert.equal(T.holdShown(h, "swipe", { listingId: "b" }, { mode: "swipe", seq: 3 }).swipe.item.listingId, "b");
    assert.equal(T.holdShown(h, "swipe", null, { mode: "swipe" }).swipe, null, "nothing on screen");
  });
}
