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
    mode = "swipe",
    // Head to head: the optional skip reasons are showing for the current pair.
    skipOpen = false;
  const doneCards = new Set(),
    donePairs = new Set(),
    undoStack = [];
  // The card (or pair) an undo brought back stays in front until it gets a
  // new reaction, whatever order later server responses list the queue in
  // (or whether they list it at all), while the board still has the horn.
  // pinGen orders undos: the latest one issued wins the front.
  let frontCard = null,
    frontPair = null,
    frontPairShown = null, // the pinned pair as it was on screen (its sides stay put)
    pinGen = 0,
    pinnedGen = 0;
  // The watermark of the taste state on screen (see TasteModel.newerOrSame):
  // a reply computed from older history never replaces newer state, whatever
  // order requests were sent or answered in.
  let applied = null;
  // The card and pair on screen (TasteModel.keepShown): a reply that arrives
  // after one appeared never swaps it out from under the owner's thumb.
  // answers lists this device's recent reactions, numbered by answerSeq.
  let held = { swipe: null, pair: null },
    answerSeq = 0;
  const answers = [];
  const tagState = new Map(); // listingId -> Set of chips (optimistic)

  function el(tag, className, text) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    if (text != null) n.textContent = text;
    return n;
  }
  // After every committed reaction: a one-tap undo for 4 seconds.
  // The toast belongs to one reaction (entry): a later failure of an earlier
  // reaction must not hide the toast of the newest one.
  let toastTimer = null,
    toastFor = null;
  function hideUndoToast(entry) {
    if (entry && entry !== toastFor) return;
    clearTimeout(toastTimer);
    toastFor = null;
    document.querySelector("#taste-toast")?.remove();
  }
  function showUndoToast(text, entry) {
    hideUndoToast();
    toastFor = entry || null;
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
    toastTimer = setTimeout(() => hideUndoToast(), 4000);
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
  // Applies a /taste state reply unless the state on screen is newer.
  // Returns whether it was applied.
  function accept(next) {
    if (!T.newerOrSame(next.watermark, applied)) return false;
    applied = next.watermark || applied;
    data = next;
    for (const [id, chips] of Object.entries(next.tags || {})) tagState.set(id, new Set(chips));
    render();
    return true;
  }
  // The queue and pairs as shown: the server's, with an undo's pinned card or
  // pair in front, then the card or pair already on screen (until it is
  // answered or no longer valid). data keeps the server's order, so a pin or
  // hold that lets go leaves no trace. Validated on every render.
  function shown() {
    const boardReady = boardState === "ready";
    const kept = T.keepFront(data, { frontCard, frontPair, pairShown: frontPairShown }, { listing, boardReady });
    ({ frontCard, frontPair } = kept);
    const s = T.keepShown(kept, held, { mode, listing, doneCards, donePairs, answers, recent: data?.recent, skips: data?.skips, boardReady });
    held = s.held;
    return s;
  }
  // Called with what a render put on screen.
  function hold(kind, item) {
    held = T.holdShown(held, kind, item, { mode, active, seq: answerSeq, events: applied?.events ?? null });
  }
  function pin(entry, gen) {
    if (gen < pinnedGen) return; // a newer undo already holds the front
    pinnedGen = gen;
    if (entry.kind === "swipe") frontCard = entry.listingId;
    else {
      frontPair = entry.key;
      frontPairShown = entry.pair;
    }
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
  // The listing photo; without one (or when it fails to load), the card shows
  // the placeholder drawing and onMissing() can put the "Open listing" link
  // on it.
  function visual(l, className, onMissing) {
    const v = el("div", className);
    const missing = () => {
      v.classList.add("no-photo");
      onMissing?.(v);
    };
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
          missing();
        },
        { once: true },
      );
      v.append(img);
    } else missing();
    return v;
  }
  // "Open listing ↗": the source page in a new tab, on every card that has a
  // URL. Never a reaction. The press, release and click stop here, so the
  // swipe card's drag never starts (a press that wanders off the link and is
  // released on the card finds no drag to end), and in Head to head the link
  // sits beside the choice button, never inside it. Enter and Space open the
  // link and never reach the keyboard shortcuts. It sits under the card's
  // facts, off the photo; only a card without a photo shows it on the
  // placeholder drawing (onPlaceholder), where it is the obvious next step.
  function viewListing(l) {
    const url = M.safeURL(l.url);
    if (!url || url === "#") return null;
    const a = el("a", "taste-view-listing", "Open listing ↗");
    a.href = url;
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    a.draggable = false;
    a.title = "Open the listing in a new tab (not a reaction)";
    a.setAttribute("aria-label", `Open listing for ${l.maker} ${l.model} (opens in a new tab, not a reaction)`);
    for (const type of ["pointerdown", "pointerup", "click"]) a.addEventListener(type, (e) => e.stopPropagation());
    a.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") e.stopPropagation();
    });
    a.addEventListener("dragstart", (e) => e.preventDefault());
    return a;
  }
  // Puts the link on a photo-less card's placeholder (once).
  function onPlaceholder(view, parent) {
    if (!view || view.parentNode) return; // placed already (the card may not be on the page yet)
    view.classList.add("on-placeholder");
    view.title = "No photo here: open the listing in a new tab";
    parent.append(view);
  }
  // Whether the reaction was made looking at a photo (every card involved).
  const photoShown = (box) => {
    const visuals = [...box.querySelectorAll(".swipe-visual, .pair-visual")];
    return visuals.length > 0 && visuals.every((v) => !v.classList.contains("no-photo"));
  };
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
    return T.nextCards(shown().queue, doneCards).find((q) => listing(q.listingId)) || null;
  }
  function currentPair() {
    return T.nextPairs(shown().pairs, donePairs).find((p) => listing(p.a) && listing(p.b)) || null;
  }
  function renderSwipe() {
    const deck = $("#swipe-deck");
    deck.replaceChildren();
    const q = currentCard();
    hold("swipe", q);
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
    const view = viewListing(l);
    const photo = visual(l, "swipe-visual", (v) => onPlaceholder(view, v));
    const body = facts(l, { ...(data.scores?.[l.id] || { taste: 50, sd: 1 }), reason: q.reason === "new" ? "new to you" : "model unsure" });
    if (view && !view.parentNode) body.append(view);
    card.append(photo, body);
    for (const [cls, text] of [
      ["stamp right", "COOL"],
      ["stamp left", "NOT FOR ME"],
      ["stamp up", "MUST SEE"],
    ])
      card.append(el("span", cls, text));
    attachDrag(card);
    deck.append(card);
  }
  // How far the choices have linked the comparable horns, from the newest
  // applied state (render runs after accept, which honours the watermark).
  // Nothing without a graph, or while Head to head explains a shortage.
  function renderProgress() {
    const line = $("#pair-progress");
    const text = data && !T.pairShortage(data) ? T.linkText(data.pairGraph) : "";
    line.textContent = text;
    line.hidden = !text;
  }
  function renderPair() {
    renderProgress();
    const box = $("#pair-cards");
    box.replaceChildren();
    const p = currentPair();
    hold("pair", p);
    for (const id of ["#pair-a", "#pair-b", "#pair-both", "#pair-neither", "#pair-skip"]) $(id).disabled = !p;
    if (!p) skipOpen = false;
    renderSkipPanel();
    if (!p) {
      const empty = el("div", "taste-empty");
      const shortage = T.pairShortage(data);
      if (shortage) empty.classList.add("pair-shortage");
      empty.append(
        el("span", "empty-mark", "◎"),
        el("h3", "", !data ? "Loading…" : boardState !== "ready" && data.pairs?.length ? waitingText() : shortage ? shortage.title : "No close calls left."),
        el("p", "", !data ? "" : boardState !== "ready" && data.pairs?.length ? waitingNote() : shortage ? shortage.note : "The model is sure about every pairing it can make today."),
      );
      box.append(empty);
      return;
    }
    for (const [side, id] of [
      ["a", p.a],
      ["b", p.b],
    ]) {
      const l = listing(id);
      // The link sits beside the choice button (never inside it): under the
      // card, or over a missing photo's placeholder, so opening the listing
      // is not a choice.
      const slot = el("div", "pair-slot");
      const b = el("button", "pair-card");
      b.type = "button";
      b.dataset.side = side;
      b.dataset.id = id;
      b.setAttribute("aria-label", `Choose ${l.maker} ${l.model}`);
      const view = viewListing(l);
      b.append(visual(l, "pair-visual", () => onPlaceholder(view, slot)), facts(l));
      b.addEventListener("click", () => react({ kind: "pair", action: side }));
      slot.prepend(b);
      if (view && !view.parentNode) slot.append(view);
      box.append(slot);
    }
  }
  // The skip reasons under the choices: one tap skips with that reason;
  // "Just skip" skips without one. Built once; shown while skipOpen.
  function renderSkipPanel() {
    const panel = $("#pair-skip-panel"),
      box = $("#pair-skip-reasons");
    if (!box.children.length) {
      T.SKIP_REASONS.forEach((r, i) => {
        // The number is the key that picks it while the reasons are open.
        const b = el("button", "chip");
        b.append(el("b", "chip-key", String(i + 1)), document.createTextNode(" " + r.label));
        b.type = "button";
        b.dataset.reason = r.code;
        b.setAttribute("aria-label", `Skip: ${r.label} (${i + 1})`);
        b.addEventListener("click", () => react({ kind: "pair", action: "skip", reasons: [r.code] }));
        box.append(b);
      });
    }
    panel.hidden = !skipOpen;
    $("#pair-skip").setAttribute("aria-expanded", String(skipOpen));
    $("#pair-skip").classList.toggle("open", skipOpen);
  }
  function openSkip(on = !skipOpen) {
    if (on && !currentPair()) return;
    skipOpen = on;
    renderSkipPanel();
    // Focus lands on Cancel, so a stray Enter or Space never skips.
    if (on) $("#pair-skip-cancel").focus({ preventScroll: true });
  }
  // A short name for a listing in the skipped-pairs list.
  function shortName(id) {
    const l = listing(id);
    return l ? `${l.maker} ${l.model}`.trim() : "a listing no longer on the board";
  }
  async function setGuard(kind, id, active, button) {
    button.disabled = true;
    try {
      accept(await api(`/taste/${kind}/${id}`, { active }, "PUT"));
      status(kind === "exclusions" ? (active ? "Excluded again." : "Put back into Head to head.") : active ? "Lesson in use again." : "Lesson switched off.");
    } catch (e) {
      button.disabled = false;
      status(e.message, true);
    }
  }
  // "Skipped pairs": each live skip with what the daily check found and
  // changed, exclusions the owner can undo, and lessons in use.
  function renderSkips() {
    const v = data?.skips,
      box = $("#taste-skips-box");
    const any = v && (v.total || v.exclusions?.length || v.lessons?.length);
    box.hidden = !any;
    if (!any) return;
    $("#taste-skips-count").textContent = `· ${v.total}${v.pending ? ` · ${v.pending} waiting for the daily check` : ""}`;
    const exclusions = new Map((v.exclusions || []).map((x) => [x.id, x]));
    const rules = $("#taste-skip-rules");
    rules.replaceChildren();
    for (const r of v.rules || []) li(rules, T.lessonText(r), "In use for picking pairs", "rule");
    for (const l of (v.lessons || []).filter((l) => l.live)) {
      const item = el("li", "lesson");
      const what = "Lesson: " + (l.dimension === "price" ? `prices ${l.ratio}× apart` : `${l.dimension} ${String(l.classes).replace("|", " vs ")}`);
      item.append(el("span", "", what), el("small", "", !l.active ? "Switched off" : l.inEffect ? "In use (it recurred)" : "Recorded; used once another skip agrees"));
      const b = el("button", "chip quiet", l.active ? "Stop using" : "Use again");
      b.type = "button";
      b.addEventListener("click", () => setGuard("lessons", l.id, !l.active, b));
      item.append(b);
      rules.append(item);
    }
    rules.hidden = !rules.children.length;
    // Every exclusion that is on (or that you switched off), each with its
    // own toggle, whether or not its skip is among those listed below.
    const excluded = $("#taste-exclusions");
    excluded.replaceChildren();
    for (const x of (v.exclusions || []).filter((x) => x.active || x.changedBy === "owner")) {
      const item = el("li", "exclusion");
      item.dataset.exclusion = x.id;
      const where = x.scope === "all" ? "Head to head and Swipe" : "Head to head";
      const state = !x.active ? "you put it back" : x.inForce ? `out of ${where}` : `no longer applies: ${x.lifted}`;
      item.append(el("span", "", `Excluded: ${shortName(x.listingId)}`), el("small", "", `${String(x.reason).replace("_", " ")} · ${state}`));
      if (x.active ? x.inForce : true) {
        const b = el("button", "chip quiet", x.active ? "Undo exclusion" : "Exclude again");
        b.type = "button";
        b.setAttribute("aria-label", `${x.active ? "Undo exclusion of" : "Exclude again"} ${shortName(x.listingId)}`);
        b.addEventListener("click", () => setGuard("exclusions", x.id, !x.active, b));
        item.append(b);
      }
      excluded.append(item);
    }
    excluded.hidden = !excluded.children.length;
    const list = $("#taste-skips");
    list.replaceChildren();
    for (const s of v.items || []) {
      const item = el("li", "skip-item");
      item.dataset.event = s.eventId;
      item.append(el("span", "skip-pair", `${shortName(s.a)} vs ${shortName(s.b)}`));
      const said = [...(s.reasons || []).map(T.skipReasonLabel), s.note ? `“${s.note}”` : ""].filter(Boolean).join(" · ");
      if (said) item.append(el("small", "", `You: ${said}`));
      const f = s.finding;
      item.append(el("p", "skip-finding", T.skipCauseText(f) + (f?.explanation ? ` — ${f.explanation}` : "")));
      for (const a of f?.actions || []) {
        const row = el("div", "skip-action");
        const x = a.exclusionId ? exclusions.get(a.exclusionId) : null;
        row.append(el("small", "", x && !x.active ? `${a.text} · you put it back` : x && !x.inForce ? `${a.text} · no longer applies: ${x.lifted}` : a.text));
        if (x && (x.inForce || !x.active)) {
          const b = el("button", "chip quiet", x.active ? "Undo exclusion" : "Exclude again");
          b.type = "button";
          b.setAttribute("aria-label", `${x.active ? "Undo exclusion of" : "Exclude again"} ${shortName(x.listingId)}`);
          b.addEventListener("click", () => setGuard("exclusions", x.id, !x.active, b));
          row.append(b);
        }
        item.append(row);
      }
      list.append(item);
    }
    if (!list.children.length) li(list, "No skips standing.", "", "none");
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
    const graph = T.graphText(data?.pairGraph);
    $("#taste-graph").textContent = graph;
    $("#taste-graph").hidden = !graph;
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
    renderSkips();
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
    $("#taste-keys").textContent = swipe ? "← not for me · → cool · ↑ must see · Z undo" : "← / → pick a side · ↑ both · ↓ neither · K skip · Z undo";
    renderSwipe();
    renderPair();
    renderChips();
    renderLearned();
    $("#taste-undo").disabled = $("#pair-undo").disabled = !undoStack.length;
    $("#taste-count").textContent = T.nextCards(shown().queue, doneCards).length;
  }

  // ---- Reactions ------------------------------------------------------------
  function post(body) {
    return api("/taste/events", { clientId: T.clientId(), ...body });
  }
  // The ids of the card or pair actually rendered.
  function renderedIds(kind) {
    if (kind === "swipe") return [...document.querySelectorAll("#swipe-deck .swipe-card")].map((c) => c.dataset.id);
    return [...document.querySelectorAll("#pair-cards .pair-card")].map((c) => c.dataset.id);
  }
  const onScreen = (kind, item) => T.matchesRendered(kind, item, renderedIds(kind));
  // What a reaction would be about is not what is on screen (state changed
  // without a render): send nothing, show what is current instead.
  function refuse() {
    skipOpen = false;
    render();
    status("The cards changed. Check them and try again.", true);
  }
  async function react(r) {
    if (!data) return;
    let body, entry;
    if (r.kind === "swipe") {
      const q = currentCard();
      if (!q) return;
      if (!onScreen("swipe", q)) return refuse();
      body = { kind: "swipe", mode: "swipe", listingId: q.listingId, action: r.action, meta: { hadPhoto: photoShown($("#swipe-deck")) } };
      doneCards.add(q.listingId);
      entry = { kind: "swipe", listingId: q.listingId, front: q.listingId === frontCard, gen: pinnedGen };
      if (entry.front) frontCard = null;
    } else {
      const p = currentPair();
      if (!p) return;
      if (!onScreen("pair", p)) return refuse();
      body = { kind: "pair", mode: "pair", listingId: p.a, otherListingId: p.b, action: r.action, meta: { hadPhoto: photoShown($("#pair-cards")) } };
      if (r.action === "skip") {
        // Not a choice: optional reasons, and the note being written (if
        // any) goes with the skip for the daily check to read.
        if (r.reasons?.length) body.meta.skipReasons = r.reasons;
        const note = $("#taste-note").value.trim();
        if (note) body.note = note;
      }
      skipOpen = false;
      donePairs.add(T.pairKey(p));
      entry = { kind: "pair", key: T.pairKey(p), pair: p, front: T.pairKey(p) === frontPair, gen: pinnedGen, note: body.note };
      if (entry.front) frontPair = null;
    }
    $("#taste-note").value = "";
    // A card or pair shown before this answer and sharing one of its horns
    // (see keepShown) is not held any longer.
    answers.push({ seq: ++answerSeq, ids: [body.listingId, body.otherListingId].filter(Boolean) });
    if (answers.length > 50) answers.shift();
    entry.pending = post(body);
    undoStack.push(entry);
    const said =
      r.kind === "swipe"
        ? { right: "Cool.", left: "Not for me.", up: "Must see!" }[r.action]
        : r.action === "skip"
          ? `Skipped${r.reasons?.length ? " · " + r.reasons.map(T.skipReasonLabel).join(", ") : ""}. It won't count.`
          : { a: "Left one.", b: "Right one.", both: "Both.", neither: "Neither." }[r.action];
    status(said);
    showUndoToast(said, entry);
    render();
    try {
      const out = await entry.pending;
      entry.eventId = out.event.id;
      accept(out);
    } catch (e) {
      // Already gone if it was undone while its POST was in flight.
      const i = undoStack.indexOf(entry);
      if (i >= 0) undoStack.splice(i, 1);
      hideUndoToast(entry);
      if (entry.kind === "swipe") doneCards.delete(entry.listingId);
      else donePairs.delete(entry.key);
      // A skip's note is not lost with a failed request.
      if (entry.note && !$("#taste-note").value) $("#taste-note").value = entry.note;
      // A failed reaction to a card an undo brought back leaves it in front,
      // unless a newer undo has pinned another card since.
      if (entry.front && pinnedGen === entry.gen) pin(entry, entry.gen);
      status(e.message, true);
      render();
    }
  }
  async function undo() {
    const entry = undoStack.pop();
    if (!entry) return;
    hideUndoToast();
    render();
    const gen = ++pinGen;
    try {
      if (!entry.eventId) entry.eventId = (await entry.pending).event.id;
      const out = await post({ kind: "undo", undoes: entry.eventId });
      if (entry.kind === "swipe") doneCards.delete(entry.listingId);
      else donePairs.delete(entry.key);
      // Put the undone card or pair back in front, even when the server's
      // queue lists it further down (or not at all), or this reply is stale.
      accept(out);
      pin(entry, gen);
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
      return q && onScreen("swipe", q) ? { listingId: q.listingId } : null;
    }
    const p = currentPair();
    return p && onScreen("pair", p) ? { listingId: p.a, otherListingId: p.b } : null;
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
    // While a drag is active, a release or cancel anywhere ends it, so a press
    // that leaves the card and is released outside can never linger.
    const release = () => {
      drag = null;
      window.removeEventListener("pointerup", end, true);
      window.removeEventListener("pointercancel", cancel, true);
    };
    const springBack = () => {
      release();
      card.classList.remove("dragging");
      card.classList.add("springing");
      card.style.transform = "";
      card.dataset.leaning = "";
      for (const stamp of Object.values(stamps)) if (stamp) stamp.style.opacity = "";
      setTimeout(() => card.classList.remove("springing"), 320);
    };
    const move = (e) => {
      if (!drag || e.pointerId !== drag.id) return;
      // A mouse or pen whose buttons are all up is no longer dragging.
      if (drag.pointerType !== "touch" && e.buttons === 0) return springBack();
      drag.dx = e.clientX - drag.x;
      drag.dy = e.clientY - drag.y;
      drag.axis = T.lockAxis(drag.axis, drag.dx, drag.dy);
      drag.samples.push({ t: e.timeStamp, x: e.clientX, y: e.clientY });
      if (drag.samples.length > 12) drag.samples.shift();
      if (!drag.axis) return;
      if (!drag.captured) {
        drag.captured = true;
        card.classList.add("dragging");
      }
      // Follow the finger along the locked axis (a little give on the other).
      const x = drag.axis === "x" ? drag.dx : drag.dx * 0.15;
      const y = drag.axis === "y" ? Math.min(drag.dy, 40) : Math.max(-200, Math.min(drag.dy, 40)) * 0.35;
      card.style.transform = `translate(${x}px, ${y}px) rotate(${x / 18}deg)`;
      show(state());
    };
    function end(e) {
      if (!drag || e.pointerId !== drag.id) return;
      const s = state();
      if (!s.commit) return springBack();
      release();
      card.classList.remove("dragging");
      card.classList.add("gone-" + s.direction);
      react({ kind: "swipe", action: s.direction });
    }
    // Scrolling (or anything else that takes the pointer) never commits.
    function cancel(e) {
      if (drag && e.pointerId === drag.id) springBack();
    }
    card.addEventListener("pointerdown", (e) => {
      if (e.button !== 0) return;
      if (drag) springBack(); // a stale drag never carries over to a new press
      drag = { id: e.pointerId, x: e.clientX, y: e.clientY, dx: 0, dy: 0, axis: null, captured: false, pointerType: e.pointerType || "mouse", samples: [{ t: e.timeStamp, x: e.clientX, y: e.clientY }] };
      // Mouse and pen: capture at once, so moves and the release outside the
      // card still reach it. Touch is implicitly captured by the card already.
      if (drag.pointerType !== "touch") card.setPointerCapture?.(e.pointerId);
      window.addEventListener("pointerup", end, true);
      window.addEventListener("pointercancel", cancel, true);
    });
    card.addEventListener("pointermove", move);
    card.addEventListener("pointerup", end);
    card.addEventListener("pointercancel", cancel);
  }
  document.addEventListener("keydown", (e) => {
    if (!active || e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
    // A focused "Open listing" link keeps its keys (Enter opens it; nothing
    // else is a reaction while it has focus).
    if (e.target.closest?.("input, textarea, select, [contenteditable], .taste-view-listing") || document.querySelector("dialog[open]")) return;
    const a = T.keyAction(e.key, mode, { skipOpen: mode === "pair" && skipOpen });
    if (!a) return;
    e.preventDefault();
    if (a.kind === "undo") undo();
    else if (a.kind === "skip-open") openSkip();
    else if (a.kind === "skip-cancel") openSkip(false);
    else if (a.kind === mode) react(a);
  });

  $("#taste-mode-swipe").addEventListener("click", () => {
    mode = "swipe";
    skipOpen = false;
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
  $("#pair-skip").addEventListener("click", () => openSkip());
  $("#pair-skip-none").addEventListener("click", () => react({ kind: "pair", action: "skip", reasons: [] }));
  $("#pair-skip-cancel").addEventListener("click", () => openSkip(false));
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
      // What was on screen stays held while the view is hidden (hold()
      // records nothing then, and hidden renders keep showing it), so coming
      // back shows, and votes on, the same card or pair until it is answered.
      active = on;
      if (on) load();
    },
    boardFailed() {
      boardState = "failed";
      if (data) render();
    },
    boardLoaded() {
      boardState = "ready";
      if (data) render(); // also validates pins against the board just loaded
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
