/* Taste training: Swipe and Head to head. Reactions are append-only events in
 * the private API; nothing is written to localStorage, URLs or analytics. */
(() => {
  "use strict";
  const T = window.TasteModel,
    M = window.TrumpetModel,
    $ = (s) => document.querySelector(s);
  let data = null,
    boardState = "loading", // loading | ready | failed: listings come from the main board
    active = false,
    loading = null,
    mode = "swipe";
  const doneCards = new Set(),
    donePairs = new Set(),
    undoStack = [];
  const tagState = new Map(); // listingId -> Set of chips (optimistic)

  function el(tag, className, text) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    if (text != null) n.textContent = text;
    return n;
  }
  // After every committed reaction: a one-tap undo for 4 seconds.
  let toastTimer = null;
  function hideUndoToast() {
    clearTimeout(toastTimer);
    document.querySelector("#taste-toast")?.remove();
  }
  function showUndoToast(text) {
    hideUndoToast();
    const toast = el("div", "toast taste-toast");
    toast.id = "taste-toast";
    toast.setAttribute("role", "status");
    const undoButton = el("button", "", "Undo");
    undoButton.type = "button";
    undoButton.addEventListener("click", () => {
      hideUndoToast();
      undo();
    });
    toast.append(el("span", "", text), undoButton);
    document.body.append(toast);
    toastTimer = setTimeout(hideUndoToast, 4000);
  }
  function status(text, error = false) {
    const n = $("#taste-status");
    n.textContent = text;
    n.classList.toggle("error", error);
  }
  async function api(path, body, method = "POST", headers = { "Content-Type": "application/json" }) {
    const response = await fetch("/trumpets/api/v1/trumpets" + path, {
      credentials: "same-origin",
      cache: "no-store",
      ...(body !== undefined ? { method, headers, body: headers["Content-Type"] === "application/json" ? JSON.stringify(body) : body } : {}),
    });
    let payload = {};
    try {
      payload = await response.json();
    } catch {}
    if (!response.ok) {
      const error = new Error(response.status === 401 ? "Signed out. Sign in again to continue." : payload.error || `Data service returned ${response.status}`);
      error.status = response.status;
      throw error;
    }
    return payload;
  }
  function accept(next) {
    data = next;
    for (const [id, chips] of Object.entries(next.tags || {})) tagState.set(id, new Set(chips));
    render();
  }
  async function load() {
    if (loading) return loading;
    loading = api("/taste")
      .then(accept)
      .catch((e) => status(e.message, true))
      .finally(() => (loading = null));
    return loading;
  }

  // ---- Rendering ------------------------------------------------------------
  const listing = (id) => window.TrumpetApp?.listing(id);
  function visual(l, className) {
    const v = el("div", className);
    if (l.images?.length) {
      const img = el("img");
      img.src = l.images[0];
      img.alt = l.title;
      img.referrerPolicy = "no-referrer";
      img.draggable = false;
      img.addEventListener(
        "error",
        () => {
          img.remove();
          v.classList.add("no-photo");
        },
        { once: true },
      );
      v.append(img);
    } else v.classList.add("no-photo");
    return v;
  }
  function facts(l, score) {
    const body = el("div", "taste-card-body");
    const kicker = el("div", "card-kicker");
    kicker.append(el("span", "maker", l.maker), el("span", "status " + l.status, l.verificationState === "candidate" ? "Unverified" : l.status));
    body.append(kicker, el("h3", "model", l.model));
    const price = el("div", "price");
    price.append(el("strong", "", M.money(l.price, l.currency)));
    if (l.shipping != null) price.append(el("span", "shipping", `+ ${M.money(l.shipping, l.currency)} shipping`));
    body.append(price, el("p", "source", `${l.seller || l.source}${l.seller ? " / " + l.source : ""}`));
    const tags = el("div", "tags");
    for (const t of (l.tags || []).slice(0, 4)) tags.append(el("span", "", t));
    body.append(tags);
    if (score) body.append(el("p", "taste-score", `Taste ${Math.round(score.taste)} · ±${score.sd.toFixed(2)} · ${score.reason || ""}`.replace(/ · $/, "")));
    return body;
  }
  // The queue names listings by id; until the board has loaded there is
  // nothing to show, which is not the same as having reacted to everything.
  function waitingText() {
    return boardState === "failed" ? "Listings didn't load." : "Loading listings…";
  }
  function waitingNote() {
    return boardState === "failed" ? "Tap Refresh board above to try again." : "";
  }
  function currentCard() {
    return T.nextCards(data?.queue, doneCards).find((q) => listing(q.listingId)) || null;
  }
  function currentPair() {
    return T.nextPairs(data?.pairs, donePairs).find((p) => listing(p.a) && listing(p.b)) || null;
  }
  function renderSwipe() {
    const deck = $("#swipe-deck");
    deck.replaceChildren();
    const q = currentCard();
    for (const id of ["#swipe-left", "#swipe-up", "#swipe-right"]) $(id).disabled = !q;
    if (!q) {
      const empty = el("div", "taste-empty");
      empty.append(
        el("span", "empty-mark", "◎"),
        el("h3", "", !data ? "Loading…" : boardState !== "ready" && data.queue?.length ? waitingText() : "All caught up."),
        el("p", "", !data ? "" : boardState !== "ready" && data.queue?.length ? waitingNote() : "Every horn has a reaction. Try Head to head, or come back when new listings arrive."),
      );
      deck.append(empty);
      return;
    }
    const l = listing(q.listingId);
    const card = el("article", "swipe-card");
    card.dataset.id = l.id;
    card.tabIndex = 0;
    card.setAttribute("aria-label", `${l.maker} ${l.model}, ${M.money(l.price, l.currency)}. Swipe right for cool, left for not for me, up for must see.`);
    card.append(visual(l, "swipe-visual"), facts(l, { ...(data.scores?.[l.id] || { taste: 50, sd: 1 }), reason: q.reason === "new" ? "new to you" : "model unsure" }));
    for (const [cls, text] of [
      ["stamp right", "COOL"],
      ["stamp left", "NOT FOR ME"],
      ["stamp up", "MUST SEE"],
    ])
      card.append(el("span", cls, text));
    attachDrag(card);
    deck.append(card);
  }
  function renderPair() {
    const box = $("#pair-cards");
    box.replaceChildren();
    const p = currentPair();
    for (const id of ["#pair-a", "#pair-b", "#pair-both", "#pair-neither"]) $(id).disabled = !p;
    if (!p) {
      const empty = el("div", "taste-empty");
      empty.append(el("span", "empty-mark", "◎"), el("h3", "", !data ? "Loading…" : boardState !== "ready" && data.pairs?.length ? waitingText() : "No close calls left."), el("p", "", !data ? "" : boardState !== "ready" && data.pairs?.length ? waitingNote() : "The model is sure about every pairing it can make today."));
      box.append(empty);
      return;
    }
    for (const [side, id] of [
      ["a", p.a],
      ["b", p.b],
    ]) {
      const l = listing(id);
      const b = el("button", "pair-card");
      b.type = "button";
      b.dataset.side = side;
      b.setAttribute("aria-label", `Choose ${l.maker} ${l.model}`);
      b.append(visual(l, "pair-visual"), facts(l));
      b.addEventListener("click", () => react({ kind: "pair", action: side }));
      box.append(b);
    }
  }
  function renderChips() {
    const box = $("#taste-chips");
    box.replaceChildren();
    const q = mode === "swipe" ? currentCard() : null;
    box.hidden = !q;
    if (!q) return;
    const chips = tagState.get(q.listingId) || new Set();
    for (const chip of data?.chips || []) {
      const b = el("button", "chip", chip);
      b.type = "button";
      b.setAttribute("aria-pressed", String(chips.has(chip)));
      b.addEventListener("click", () => toggleTag(q.listingId, chip, b));
      box.append(b);
    }
  }
  function li(list, text, meta, cls = "") {
    const item = el("li", cls);
    item.append(el("span", "", text));
    if (meta) item.append(el("small", "", meta));
    list.append(item);
  }
  function renderLearned() {
    const model = data?.model || {};
    const conf = T.confidenceLabel(model);
    $("#taste-confidence-text").textContent = conf.text;
    $("#taste-confidence-bar").style.width = conf.percent + "%";
    const { liked, disliked } = T.learned(data?.weights);
    for (const [id, rows, none] of [
      ["#taste-liked", liked, "Nothing yet — swipe right on horns you like."],
      ["#taste-disliked", disliked, "Nothing yet."],
    ]) {
      const list = $(id);
      list.replaceChildren();
      for (const w of rows) li(list, w.label, `${w.kindLabel} · ${T.weightText(w)}`);
      if (!rows.length) li(list, none, "", "none");
    }
    const changes = $("#taste-changes");
    changes.replaceChildren();
    for (const c of data?.changes || []) li(changes, c.label, T.changeText(c));
    if (!data?.changes?.length) li(changes, "No big moves this week.", "", "none");
    const notes = $("#taste-notes");
    notes.replaceChildren();
    for (const s of T.noteStatements(data?.noteSignals)) li(notes, s, "");
    for (const e of (data?.recent || []).filter((e) => e.kind === "note").slice(0, 4)) {
      const item = el("li", "note-item");
      const l = listing(e.listingId);
      item.append(el("span", "", e.note || "Voice note"), el("small", "", l ? `${l.maker} ${l.model}` : ""));
      if (e.hasAudio) {
        const audio = el("audio");
        audio.controls = true;
        audio.preload = "none";
        audio.src = `/trumpets/api/v1/trumpets/taste/voice/${e.id}`;
        item.append(audio);
      }
      notes.append(item);
    }
    if (!notes.children.length) li(notes, "Notes are optional; patterns in them show up here.", "", "none");
    renderPrefs();
  }
  let prefsDirty = false;
  function renderPrefs() {
    const p = data?.preferences;
    if (!p) return;
    $("#taste-prefs-count").textContent = p.revision ? `· ${p.statements.length} line${p.statements.length === 1 ? "" : "s"}` : "";
    if (prefsDirty) return;
    const f = $("#taste-prefs");
    f.elements.statements.value = p.statements.join("\n");
    for (const k of ["likedMakers", "dislikedMakers", "likedTraits", "dislikedTraits"]) f.elements[k].value = p[k].join(", ");
    f.elements.priceGuidance.value = p.priceGuidance;
  }
  function render() {
    const swipe = mode === "swipe";
    $("#swipe-stage").hidden = !swipe;
    $("#pair-stage").hidden = swipe;
    $("#taste-mode-swipe").setAttribute("aria-pressed", String(swipe));
    $("#taste-mode-pair").setAttribute("aria-pressed", String(!swipe));
    $("#taste-keys").textContent = swipe ? "← not for me · → cool · ↑ must see · Z undo" : "← / → pick a side · ↑ both · ↓ neither · Z undo";
    renderSwipe();
    renderPair();
    renderChips();
    renderLearned();
    $("#taste-undo").disabled = $("#pair-undo").disabled = !undoStack.length;
    $("#taste-count").textContent = T.nextCards(data?.queue, doneCards).length;
  }

  // ---- Reactions ------------------------------------------------------------
  function post(body) {
    return api("/taste/events", { clientId: T.clientId(), ...body });
  }
  async function react(r) {
    if (!data) return;
    let body, entry;
    if (r.kind === "swipe") {
      const q = currentCard();
      if (!q) return;
      body = { kind: "swipe", mode: "swipe", listingId: q.listingId, action: r.action };
      doneCards.add(q.listingId);
      entry = { kind: "swipe", listingId: q.listingId };
    } else {
      const p = currentPair();
      if (!p) return;
      body = { kind: "pair", mode: "pair", listingId: p.a, otherListingId: p.b, action: r.action };
      donePairs.add(T.pairKey(p));
      entry = { kind: "pair", key: T.pairKey(p) };
    }
    $("#taste-note").value = "";
    entry.pending = post(body);
    undoStack.push(entry);
    const said = r.kind === "swipe" ? { right: "Cool.", left: "Not for me.", up: "Must see!" }[r.action] : { a: "Left one.", b: "Right one.", both: "Both.", neither: "Neither." }[r.action];
    status(said);
    showUndoToast(said);
    render();
    try {
      const out = await entry.pending;
      entry.eventId = out.event.id;
      accept(out);
    } catch (e) {
      undoStack.splice(undoStack.indexOf(entry), 1);
      hideUndoToast();
      if (entry.kind === "swipe") doneCards.delete(entry.listingId);
      else donePairs.delete(entry.key);
      status(e.message, true);
      render();
    }
  }
  async function undo() {
    const entry = undoStack.pop();
    if (!entry) return;
    hideUndoToast();
    render();
    try {
      if (!entry.eventId) entry.eventId = (await entry.pending).event.id;
      const out = await post({ kind: "undo", undoes: entry.eventId });
      if (entry.kind === "swipe") doneCards.delete(entry.listingId);
      else donePairs.delete(entry.key);
      accept(out);
      // Put the undone card or pair back in front.
      if (entry.kind === "swipe" && !data.queue.some((q) => q.listingId === entry.listingId)) data.queue.unshift({ listingId: entry.listingId, reason: "new", taste: 50, sd: 1 });
      if (entry.kind === "pair") {
        const [a, b] = entry.key.split("|");
        if (!data.pairs.some((p) => T.pairKey(p) === entry.key)) data.pairs.unshift({ a, b, p: 0.5, sd: 1 });
      }
      status("Undone.");
      render();
    } catch (e) {
      status(e.message, true);
    }
  }
  async function toggleTag(listingId, chip, button) {
    const chips = tagState.get(listingId) || new Set();
    const on = !chips.has(chip);
    on ? chips.add(chip) : chips.delete(chip);
    tagState.set(listingId, chips);
    button.setAttribute("aria-pressed", String(on));
    try {
      accept(await post({ kind: "tag", listingId, tag: chip, action: on ? "add" : "remove" }));
    } catch (e) {
      on ? chips.delete(chip) : chips.add(chip);
      button.setAttribute("aria-pressed", String(!on));
      status(e.message, true);
    }
  }
  function noteTarget() {
    if (mode === "swipe") {
      const q = currentCard();
      return q ? { listingId: q.listingId } : null;
    }
    const p = currentPair();
    return p ? { listingId: p.a, otherListingId: p.b } : null;
  }
  async function saveNote() {
    const text = $("#taste-note").value.trim(),
      target = noteTarget();
    if (!text || !target) return status(text ? "Nothing to attach the note to." : "Write a note first.", true);
    const b = $("#taste-note-save");
    b.disabled = true;
    try {
      accept(await post({ kind: "note", mode, note: text, ...target }));
      $("#taste-note").value = "";
      status("Note saved privately.");
    } catch (e) {
      status(e.message, true);
    } finally {
      b.disabled = false;
    }
  }

  // ---- Hold-to-talk voice notes (stored privately; never transcribed) -------
  let recorder = null,
    started = 0,
    chunks = [];
  async function startVoice() {
    if (recorder || !noteTarget()) return;
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      chunks = [];
      recorder = new MediaRecorder(stream);
      recorder.ondataavailable = (e) => e.data.size && chunks.push(e.data);
      recorder.start();
      started = performance.now();
      $("#taste-voice").classList.add("recording");
      $("#taste-voice").textContent = "● Recording… release to save";
    } catch {
      recorder = null;
      status("Microphone unavailable.", true);
    }
  }
  function stopVoice() {
    if (!recorder) return;
    const r = recorder,
      target = noteTarget(),
      duration = Math.round(performance.now() - started);
    recorder = null;
    $("#taste-voice").classList.remove("recording");
    $("#taste-voice").textContent = "🎙 Hold to talk";
    r.onstop = async () => {
      r.stream.getTracks().forEach((t) => t.stop());
      if (duration < 400 || !target) return status("Hold the button while you talk.", true);
      const type = (r.mimeType || "audio/webm").split(";")[0];
      const blob = new Blob(chunks, { type });
      const q = new URLSearchParams({ clientId: T.clientId(), mode, durationMs: String(duration), ...target });
      status("Saving voice note…");
      try {
        accept(await api("/taste/voice?" + q, blob, "POST", { "Content-Type": type }));
        status("Voice note saved privately.");
      } catch (e) {
        status(e.message, true);
      }
    };
    r.stop();
  }

  // ---- Gestures and keyboard -----------------------------------------------
  // Thresholds live in TasteModel.dragState: a commit is deliberate (40% of
  // the card or a fast fling); anything else springs back. Vertical touch
  // drags belong to the page (touch-action: pan-y); the browser then cancels
  // the pointer, which never commits.
  function attachDrag(card) {
    let drag = null;
    const stamps = {
      right: card.querySelector(".stamp.right"),
      left: card.querySelector(".stamp.left"),
      up: card.querySelector(".stamp.up"),
    };
    const state = () =>
      T.dragState({ dx: drag.dx, dy: drag.dy, width: card.offsetWidth, axis: drag.axis, pointerType: drag.pointerType, vx: T.velocity(drag.samples).vx });
    const show = (s) => {
      for (const [dir, stamp] of Object.entries(stamps)) if (stamp) stamp.style.opacity = dir === s.direction ? String(s.commit ? 1 : 0.15 + 0.6 * s.progress) : "";
      card.dataset.leaning = s.commit ? s.direction : "";
    };
    const springBack = () => {
      drag = null;
      card.classList.remove("dragging");
      card.classList.add("springing");
      card.style.transform = "";
      card.dataset.leaning = "";
      for (const stamp of Object.values(stamps)) if (stamp) stamp.style.opacity = "";
      setTimeout(() => card.classList.remove("springing"), 320);
    };
    const move = (e) => {
      if (!drag || e.pointerId !== drag.id) return;
      drag.dx = e.clientX - drag.x;
      drag.dy = e.clientY - drag.y;
      drag.axis = T.lockAxis(drag.axis, drag.dx, drag.dy);
      drag.samples.push({ t: e.timeStamp, x: e.clientX, y: e.clientY });
      if (drag.samples.length > 12) drag.samples.shift();
      if (!drag.axis) return;
      if (!drag.captured) {
        drag.captured = true;
        card.classList.add("dragging");
        // Touch is implicitly captured by the card already.
        if (drag.pointerType !== "touch") card.setPointerCapture?.(e.pointerId);
      }
      // Follow the finger along the locked axis (a little give on the other).
      const x = drag.axis === "x" ? drag.dx : drag.dx * 0.15;
      const y = drag.axis === "y" ? Math.min(drag.dy, 40) : Math.max(-200, Math.min(drag.dy, 40)) * 0.35;
      card.style.transform = `translate(${x}px, ${y}px) rotate(${x / 18}deg)`;
      show(state());
    };
    const end = (e) => {
      if (!drag || e.pointerId !== drag.id) return;
      const s = state();
      if (!s.commit) return springBack();
      drag = null;
      card.classList.remove("dragging");
      card.classList.add("gone-" + s.direction);
      react({ kind: "swipe", action: s.direction });
    };
    card.addEventListener("pointerdown", (e) => {
      if (e.button !== 0 || drag) return;
      drag = { id: e.pointerId, x: e.clientX, y: e.clientY, dx: 0, dy: 0, axis: null, captured: false, pointerType: e.pointerType || "mouse", samples: [{ t: e.timeStamp, x: e.clientX, y: e.clientY }] };
    });
    card.addEventListener("pointermove", move);
    card.addEventListener("pointerup", end);
    // Scrolling (or anything else that takes the pointer) never commits.
    card.addEventListener("pointercancel", (e) => drag && e.pointerId === drag.id && springBack());
  }
  document.addEventListener("keydown", (e) => {
    if (!active || e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.target.closest?.("input, textarea, select, [contenteditable]") || document.querySelector("dialog[open]")) return;
    const a = T.keyAction(e.key, mode);
    if (!a) return;
    e.preventDefault();
    if (a.kind === "undo") undo();
    else if (a.kind === mode) react(a);
  });

  $("#taste-mode-swipe").addEventListener("click", () => {
    mode = "swipe";
    render();
  });
  $("#taste-mode-pair").addEventListener("click", () => {
    mode = "pair";
    render();
  });
  $("#swipe-left").addEventListener("click", () => react({ kind: "swipe", action: "left" }));
  $("#swipe-right").addEventListener("click", () => react({ kind: "swipe", action: "right" }));
  $("#swipe-up").addEventListener("click", () => react({ kind: "swipe", action: "up" }));
  for (const [id, action] of [
    ["#pair-a", "a"],
    ["#pair-b", "b"],
    ["#pair-both", "both"],
    ["#pair-neither", "neither"],
  ])
    $(id).addEventListener("click", () => react({ kind: "pair", action }));
  $("#taste-undo").addEventListener("click", undo);
  $("#pair-undo").addEventListener("click", undo);
  $("#taste-note-save").addEventListener("click", saveNote);
  const voice = $("#taste-voice");
  if (window.MediaRecorder && navigator.mediaDevices?.getUserMedia) {
    voice.hidden = false;
    voice.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      startVoice();
    });
    for (const ev of ["pointerup", "pointerleave", "pointercancel"]) voice.addEventListener(ev, stopVoice);
    voice.addEventListener("keydown", (e) => {
      if ((e.key === " " || e.key === "Enter") && !e.repeat) {
        e.preventDefault();
        startVoice();
      }
    });
    voice.addEventListener("keyup", (e) => {
      if (e.key === " " || e.key === "Enter") stopVoice();
    });
    voice.addEventListener("contextmenu", (e) => e.preventDefault());
  }
  const prefs = $("#taste-prefs");
  prefs.addEventListener("input", () => (prefsDirty = true));
  prefs.addEventListener("submit", async (e) => {
    e.preventDefault();
    const out = $("#taste-prefs-status"),
      b = $("#taste-prefs-save");
    const values = Object.fromEntries(["statements", "likedMakers", "dislikedMakers", "likedTraits", "dislikedTraits", "priceGuidance"].map((k) => [k, prefs.elements[k].value]));
    b.disabled = true;
    out.textContent = "Saving…";
    try {
      const saved = await api("/taste/preferences", { ...T.preferencesFromForm(values), expectedRevision: data?.preferences?.revision ?? 0 }, "PUT");
      prefsDirty = false;
      out.textContent = "Saved";
      await load();
      data.preferences = saved;
      renderPrefs();
    } catch (err) {
      out.textContent = err.status === 409 ? "Changed elsewhere — reload to see the latest." : err.message;
    } finally {
      b.disabled = false;
    }
  });

  window.TasteBoard = {
    setActive(on) {
      active = on;
      if (on) load();
    },
    boardFailed() {
      boardState = "failed";
      if (data) render();
    },
    boardLoaded() {
      boardState = "ready";
      if (data) render();
      else if (active) load();
      else
        api("/taste")
          .then((d) => {
            if (!data) accept(d);
          })
          .catch(() => {});
    },
  };
})();
