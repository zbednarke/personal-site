/* Pure helpers for taste training (Swipe and Head to head). No DOM, no storage. */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.TasteModel = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  // A drag becomes a swipe past ~28% of the card width (at least 70px), or
  // an upward drag past 90px that is mostly vertical.
  function gesture(dx, dy, width = 360) {
    const threshold = Math.max(70, width * 0.28);
    if (dy < -90 && Math.abs(dy) > Math.abs(dx) * 1.2) return "up";
    if (Math.abs(dx) >= threshold && Math.abs(dx) > Math.abs(dy)) return dx > 0 ? "right" : "left";
    return null;
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
  return { gesture, keyAction, pairKey, nextCards, nextPairs, learned, confidenceLabel, weightText, changeText, noteStatements, preferencesFromForm, clientId };
});
