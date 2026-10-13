/* Pure helpers for taste training (Swipe and Head to head). No DOM, no storage. */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.TasteModel = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  // Card drags. A commit is deliberate: a horizontal drag past 40% of the card
  // width (never more than 140 px needed), or a fast fling (> 0.6 px/ms over
  // the last ~80 ms, same direction, past 60 px). Up ("must see") needs a
  // long, mostly vertical drag (> 160 px, |dy| > 2|dx|) and, for touch, a card
  // already grabbed sideways: vertical touch drags scroll the page
  // (touch-action: pan-y), and touch has the Must-see button. Anything else
  // springs back.
  const AXIS_LOCK_PX = 10;
  const FLING_WINDOW_MS = 80;
  const FLING_SPEED = 0.6;
  const FLING_MIN_PX = 60;
  const UP_PX = 160;
  const commitDistance = (width = 360) => Math.min(140, width * 0.4);
  // The drag's axis, decided once it has moved ~10 px; then it stays.
  function lockAxis(axis, dx, dy) {
    if (axis) return axis;
    if (Math.max(Math.abs(dx), Math.abs(dy)) < AXIS_LOCK_PX) return null;
    return Math.abs(dx) >= Math.abs(dy) ? "x" : "y";
  }
  // Velocity (px/ms) from pointer samples [{t, x, y}] over the last ~80 ms.
  function velocity(samples) {
    const list = samples || [];
    if (list.length < 2) return { vx: 0, vy: 0 };
    const last = list[list.length - 1];
    let first = last;
    for (let i = list.length - 2; i >= 0 && last.t - list[i].t <= FLING_WINDOW_MS; i--) first = list[i];
    const dt = last.t - first.t;
    return dt > 0 ? { vx: (last.x - first.x) / dt, vy: (last.y - first.y) / dt } : { vx: 0, vy: 0 };
  }
  // What releasing now would do and how far along it is (progress 0..1;
  // commit = releasing now commits). d: { dx, dy, width, axis, pointerType, vx }.
  function dragState(d) {
    const { dx = 0, dy = 0, width = 360, axis = null, pointerType = "mouse", vx = 0 } = d || {};
    const upAllowed = axis !== null && (pointerType !== "touch" || axis === "x");
    if (upAllowed && dy < 0 && Math.abs(dy) > 2 * Math.abs(dx)) {
      return { direction: "up", progress: Math.min(1, -dy / UP_PX), commit: -dy > UP_PX };
    }
    if (axis !== "x" || dx === 0) return { direction: null, progress: 0, commit: false };
    const distance = Math.abs(dx);
    const fling = distance > FLING_MIN_PX && Math.sign(vx) === Math.sign(dx) && Math.abs(vx) > FLING_SPEED;
    const commit = distance >= commitDistance(width) || fling;
    return { direction: dx > 0 ? "right" : "left", progress: commit ? 1 : Math.min(1, distance / commitDistance(width)), commit };
  }
  // The swipe a released drag commits, or null (spring back). Without an
  // explicit axis, the drag's own dominant axis is used.
  function gesture(dx, dy, width = 360, opts = {}) {
    const axis = opts.axis !== undefined ? opts.axis : lockAxis(null, dx, dy);
    const s = dragState({ dx, dy, width, axis, pointerType: opts.pointerType || "mouse", vx: opts.vx || 0 });
    return s.commit ? s.direction : null;
  }
  // Head to head skip reasons (optional, one tap each); the codes are the
  // server's tasteSkipReasons.
  const SKIP_REASONS = [
    { code: "not_comparable", label: "Not comparable" },
    { code: "bad_info", label: "Bad or missing info" },
    { code: "stale", label: "Sold / stale" },
    { code: "duplicate", label: "Duplicate" },
    { code: "not_bb", label: "Not a Bb trumpet" },
    { code: "other", label: "Other" },
  ];
  const skipReasonLabel = (code) => SKIP_REASONS.find((r) => r.code === code)?.label || code;
  // Keyboard shortcuts; null when the key means nothing in that mode.
  // In Head to head, K opens the skip reasons; while they are open, 1–6 skip
  // with that reason, K (again) skips without one and Escape closes them.
  function keyAction(key, mode, opts = {}) {
    const k = String(key || "").toLowerCase();
    if (mode === "pair" && opts.skipOpen) {
      if (k === "escape") return { kind: "skip-cancel" };
      if (k === "k") return { kind: "pair", action: "skip", reasons: [] };
      const i = Number(k) - 1;
      if (/^[1-9]$/.test(k) && SKIP_REASONS[i]) return { kind: "pair", action: "skip", reasons: [SKIP_REASONS[i].code] };
    }
    if (k === "z" || k === "u" || k === "backspace") return { kind: "undo" };
    if (mode === "pair") {
      if (k === "k") return { kind: "skip-open" };
      if (k === "arrowleft" || k === "1" || k === "a") return { kind: "pair", action: "a" };
      if (k === "arrowright" || k === "2") return { kind: "pair", action: "b" };
      if (k === "arrowup" || k === "b") return { kind: "pair", action: "both" };
      if (k === "arrowdown" || k === "n") return { kind: "pair", action: "neither" };
      return null;
    }
    if (k === "arrowleft" || k === "x") return { kind: "swipe", action: "left" };
    if (k === "arrowright" || k === "l") return { kind: "swipe", action: "right" };
    if (k === "arrowup" || k === "s") return { kind: "swipe", action: "up" };
    return null;
  }
  const pairKey = (p) => [p.a, p.b].sort().join("|");
  // The server queue minus cards already answered on this device.
  function nextCards(queue, done) {
    return (queue || []).filter((q) => !done.has(q.listingId));
  }
  function nextPairs(pairs, done) {
    return (pairs || []).filter((p) => !done.has(pairKey(p)));
  }
  // After an undo: the undone card (or pair) goes first, wherever the server
  // listed it. The server's order among equally ranked horns (for example all
  // horns found in one run share a first-seen time) is not a promise, so
  // "back in front" cannot rely on it. Keeps the server's item when present.
  // hornOf(listingId), when given, also drops other listings of the same horn
  // (the server lists one listing per horn, and may pick a different one).
  function cardToFront(queue, listingId, hornOf) {
    const list = queue || [];
    const item = list.find((q) => q.listingId === listingId) || { listingId, reason: "new", taste: 50, sd: 1 };
    const horn = hornOf?.(listingId);
    const sameHorn = (id) => horn != null && hornOf(id) === horn;
    return [item, ...list.filter((q) => q.listingId !== listingId && !sameHorn(q.listingId))];
  }
  // Whether a board listing can still be pinned in front after an undo: not
  // gone from the board, acquired or passed (the server queue skips those).
  function pinnable(l) {
    return !!l && !l.acquired && l.feedback?.interestState !== "pass";
  }
  // The server's tasteComparable, mirrored for pins: Head to head compares
  // only active, verified listings with a photo and a price, checked in the
  // last 10 days, not acquired or passed.
  const COMPARABLE_CHECKED_MS = 10 * 24 * 3600 * 1000;
  function comparable(l, now = Date.now()) {
    return (
      pinnable(l) &&
      l.status === "active" &&
      l.verificationState === "verified" &&
      (l.images?.length || 0) > 0 &&
      l.price > 0 &&
      !!l.currency &&
      !!l.lastChecked &&
      now - Date.parse(l.lastChecked) <= COMPARABLE_CHECKED_MS
    );
  }
  // As cardToFront, for a pair key "a|b"; hornOf also drops pairs of the
  // same two horns under other listings.
  function pairToFront(pairs, key, hornOf) {
    const list = pairs || [];
    const [a, b] = key.split("|");
    const item = list.find((p) => pairKey(p) === key) || { a, b, p: 0.5, sd: 1 };
    const horns = hornOf ? [hornOf(a), hornOf(b)].sort().join("|") : null;
    const sameHorns = (p) => horns != null && hornOf(p.a) != null && [hornOf(p.a), hornOf(p.b)].sort().join("|") === horns;
    return [item, ...list.filter((p) => pairKey(p) !== key && !sameHorns(p))];
  }
  // Keeps an undo's pinned card and pair in front of a taste state, or lets go
  // of them once their horns have left the board (or, for a pair, are no
  // longer comparable). While the board has not loaded (boardReady false) a
  // pin is kept unchecked: it is validated when the board loads.
  // Returns { queue, pairs, frontCard, frontPair }.
  function keepFront(state, pins, { listing, boardReady = true, now = Date.now() }) {
    let { frontCard, frontPair } = pins;
    let queue = state?.queue,
      pairs = state?.pairs;
    const hornOf = (id) => listing(id)?.hornId;
    if (frontCard && boardReady && !pinnable(listing(frontCard))) frontCard = null;
    if (frontCard) queue = cardToFront(queue, frontCard, hornOf);
    if (frontPair && boardReady && !frontPair.split("|").every((id) => comparable(listing(id), now))) frontPair = null;
    if (frontPair) pairs = pairToFront(pairs, frontPair, hornOf);
    return { queue, pairs, frontCard, frontPair };
  }
  // Whether a taste state reply is at least as new as the one on screen, by
  // the watermark the server computes with it (newest event, preferences
  // revision). Ties apply; replies without a watermark always apply.
  function newerOrSame(next, applied) {
    if (!next || !applied) return true;
    if (next.events !== applied.events) return next.events > applied.events;
    return next.preferences >= applied.preferences;
  }
  // Head to head only compares listings worth comparing (the server's
  // tasteComparable: active, verified, a photo, a price, checked in the last
  // 10 days, not acquired or passed); with too few it says so.
  function pairShortage(state) {
    const eligible = state?.pairEligible,
      minimum = state?.pairMinimum;
    if (eligible == null || minimum == null || eligible >= minimum) return null;
    return {
      title: `Head to head needs more verified listings with photos — ${eligible} eligible`,
      note: `Pairs use only active, verified listings with a photo and a price, checked in the last 10 days, and need at least ${minimum}. Swipe works on every listing.`,
    };
  }
  // How well the comparisons connect the eligible horns (one island: every
  // horn can be compared with every other through the choices made).
  function graphText(g) {
    if (!g?.eligible) return "";
    const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;
    if (g.components <= 1) return `Comparisons connect all ${plural(g.eligible, "eligible horn")}`;
    return `Comparisons connect ${g.largest} of ${plural(g.eligible, "eligible horn")} (${plural(g.components - 1, "island")})`;
  }
  // The quiet progress line under the Head to head question. The graph's
  // nodes are horns (one comparable listing each), and "largest" counts the
  // horns in the biggest connected group, so until there is one group the
  // line says "in the main group", not "connected"; before any comparison
  // links two horns it says so. Empty without a graph or with fewer than
  // two eligible horns.
  function linkText(g) {
    if (!g || !(g.eligible >= 2)) return "";
    if (g.components <= 1) return `All ${g.eligible} eligible horns connected`;
    if (!(g.largest >= 2)) return `No horns linked yet · ${g.eligible} eligible`;
    return `Linking horns · ${g.largest} of ${g.eligible} in the main group`;
  }
  // What a skip investigation concluded, in a few words.
  function skipCauseText(f) {
    if (!f) return "Waiting for the daily check";
    const dims = { price: "price", condition: "condition", era: "era", other: "something else" };
    return (
      {
        data_problem: "Data problem" + (f.side === "a" ? " (left listing)" : f.side === "b" ? " (right listing)" : ""),
        wrong_category: "Not a Bb trumpet",
        stale: "Stale or gone",
        duplicate: "Duplicate",
        incomparable: `Genuinely different${dims[f.dimension] ? " (" + dims[f.dimension] + ")" : ""}`,
        unclear: "Your call / unclear",
      }[f.cause] || "Checked"
    );
  }
  // A lesson rule in effect, as a sentence.
  function lessonText(r) {
    if (!r) return "";
    const n = `${r.lessons} skip${r.lessons === 1 ? "" : "s"}`;
    if (r.dimension === "price") return `Avoiding pairs whose prices are ${r.ratio}× or more apart (${n})`;
    return `Avoiding ${r.dimension} ${String(r.classes || "").replace("|", " vs ")} pairs (${n})`;
  }
  const kindLabels = {
    maker: "maker",
    family: "model",
    finish: "finish",
    bell: "bell",
    leadpipe: "leadpipe",
    trait: "trait",
    era: "era",
    price: "price",
    source: "source",
  };
  // Liked/disliked traits and makers, strongest first; weak signals hidden.
  function learned(weights, limit = 6) {
    const list = (weights || []).filter((w) => kindLabels[w.kind]);
    const fmt = (w) => ({ ...w, kindLabel: kindLabels[w.kind] });
    return {
      liked: list.filter((w) => w.weight > 0.05).sort((a, b) => b.weight - a.weight).slice(0, limit).map(fmt),
      disliked: list.filter((w) => w.weight < -0.05).sort((a, b) => a.weight - b.weight).slice(0, limit).map(fmt),
    };
  }
  function confidenceLabel(model) {
    const c = model?.confidence || 0;
    const n = (model?.swipes || 0) + (model?.pairs || 0);
    const level = c < 0.15 ? "Just starting" : c < 0.5 ? "Building" : c < 0.8 ? "Fairly confident" : "Confident";
    return { level, percent: Math.round(c * 100), text: `${level} · ${n} reaction${n === 1 ? "" : "s"}` };
  }
  function signed(v) {
    const n = Math.round(v * 100) / 100;
    return (n > 0 ? "+" : n < 0 ? "−" : "±") + Math.abs(n).toFixed(2);
  }
  function weightText(w) {
    return `${signed(w.weight)} ± ${(Math.round(w.sd * 100) / 100).toFixed(2)}`;
  }
  function changeText(c) {
    return `${c.after > c.before ? "↑" : "↓"} ${signed(c.before)} → ${signed(c.after)}`;
  }
  // Statements derived from notes ("likes raw brass ×2").
  function noteStatements(signals) {
    const out = [];
    for (const [term, n] of Object.entries(signals?.favoredAttributes || {})) out.push(`Likes ${term}${n > 1 ? " ×" + n : ""}`);
    for (const [term, n] of Object.entries(signals?.dislikedAttributes || {})) out.push(`Not keen on ${term}${n > 1 ? " ×" + n : ""}`);
    for (const p of signals?.pricePreferences || []) out.push(`Price-sensitive on ${p.maker}`);
    return out;
  }
  function splitList(text) {
    return String(text || "")
      .split(",")
      .map((v) => v.trim())
      .filter(Boolean);
  }
  function preferencesFromForm(f) {
    return {
      statements: String(f.statements || "")
        .split("\n")
        .map((v) => v.trim())
        .filter(Boolean),
      likedMakers: splitList(f.likedMakers),
      dislikedMakers: splitList(f.dislikedMakers),
      likedTraits: splitList(f.likedTraits),
      dislikedTraits: splitList(f.dislikedTraits),
      priceGuidance: String(f.priceGuidance || "").trim(),
    };
  }
  function clientId(random = Math.random) {
    let s = "t";
    const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789";
    for (let i = 0; i < 20; i++) s += alphabet[Math.floor(random() * alphabet.length)];
    return s;
  }
  return { SKIP_REASONS, skipReasonLabel, skipCauseText, lessonText, gesture, dragState, lockAxis, velocity, commitDistance, keyAction, pairKey, nextCards, nextPairs, cardToFront, pairToFront, pinnable, comparable, keepFront, newerOrSame, pairShortage, graphText, linkText, learned, confidenceLabel, weightText, changeText, noteStatements, preferencesFromForm, clientId };
});
