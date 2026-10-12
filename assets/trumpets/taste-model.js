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
  // Keyboard shortcuts; null when the key means nothing in that mode.
  function keyAction(key, mode) {
    const k = String(key || "").toLowerCase();
    if (k === "z" || k === "u" || k === "backspace") return { kind: "undo" };
    if (mode === "pair") {
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
  return { gesture, dragState, lockAxis, velocity, commitDistance, keyAction, pairKey, nextCards, nextPairs, learned, confidenceLabel, weightText, changeText, noteStatements, preferencesFromForm, clientId };
});
