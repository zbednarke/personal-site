/* Workbench: the glass sheet on every private page. Framework-free.
 * The server owns the thread; this page keeps a cache (the event fold in
 * WorkbenchModel), an offline outbox in IndexedDB and a live event stream.
 * Pages may define window.WorkbenchContext = () => ({ view, selection, app })
 * to describe what is on screen; otherwise the hash and text selection are used. */
(() => {
  "use strict";
  if (window.__workbench || !window.WorkbenchModel) return;
  const M = window.WorkbenchModel;
  const API = M.API;
  const APP = (location.pathname.match(/^\/(jazz|trumpets|commonplace)\//) || [])[1] || "jazz";

  // ---- Small utilities ------------------------------------------------------------

  // Preferences (device id, last thread, nudges) live in IndexedDB beside the
  // outbox, never in localStorage, which the private pages keep empty.
  const prefs = new Map();
  let deviceId = M.newId("d");
  const coarse = () => matchMedia("(pointer: coarse)").matches;
  const el = (tag, attrs, ...kids) => {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v == null || v === false) continue;
      if (k === "class") node.className = v;
      else if (k === "html") node.innerHTML = v;
      else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
      else node.setAttribute(k, v === true ? "" : v);
    }
    for (const kid of kids.flat()) if (kid != null && kid !== false) node.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
    return node;
  };
  const ICON = {
    close: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>',
    more: '<svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.8"/><circle cx="12" cy="12" r="1.8"/><circle cx="19" cy="12" r="1.8"/></svg>',
    send: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V5M5 12l7-7 7 7"/></svg>',
    mic: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5 11a7 7 0 0014 0M12 18v3"/></svg>',
    image: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="3"/><circle cx="9" cy="10" r="2"/><path d="M21 16l-5-5-8 8"/></svg>',
  };

  async function api(path, opts = {}) {
    const headers = Object.assign({}, opts.body && !opts.raw ? { "Content-Type": "application/json" } : {}, opts.headers || {});
    const res = await fetch(API + path, { method: opts.method || "GET", body: opts.body, headers, credentials: "same-origin", cache: "no-store" });
    let data = null;
    try { data = await res.json(); } catch { /* empty */ }
    if (!res.ok) {
      const err = new Error((data && data.error) || `HTTP ${res.status}`);
      err.status = res.status;
      err.data = data;
      throw err;
    }
    return data;
  }

  // ---- Outbox in IndexedDB (memory fallback) ---------------------------------------------

  let dbp = null;
  function openDB() {
    return dbp || (dbp = new Promise((resolve, reject) => {
      const req = indexedDB.open("workbench", 2);
      req.onupgradeneeded = () => {
        const db = req.result;
        if (!db.objectStoreNames.contains("outbox")) db.createObjectStore("outbox", { keyPath: "clientId" });
        if (!db.objectStoreNames.contains("prefs")) db.createObjectStore("prefs");
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    }));
  }
  async function idbRun(store, mode, fn) {
    const db = await openDB();
    return new Promise((resolve, reject) => {
      const tx = db.transaction(store, mode);
      const r = fn(tx.objectStore(store));
      tx.oncomplete = () => resolve(r && r.result);
      tx.onerror = () => reject(tx.error);
    });
  }
  function idbStorage() {
    return { all: () => idbRun("outbox", "readonly", (s) => s.getAll()), put: (item) => idbRun("outbox", "readwrite", (s) => s.put(item)), delete: (id) => idbRun("outbox", "readwrite", (s) => s.delete(id)) };
  }
  const local = {
    get: (k) => prefs.get(k) || null,
    set(k, v) {
      prefs.set(k, v);
      if (window.indexedDB) idbRun("prefs", "readwrite", (s) => s.put(v, k)).catch(() => {});
    },
  };
  async function loadPrefs() {
    try {
      if (!window.indexedDB) return;
      const keys = await idbRun("prefs", "readonly", (s) => s.getAllKeys());
      const values = await idbRun("prefs", "readonly", (s) => s.getAll());
      (keys || []).forEach((k, i) => prefs.set(k, values[i]));
    } catch { /* private mode: session-only preferences */ }
    if (local.get("wb.device")) deviceId = local.get("wb.device");
    else local.set("wb.device", deviceId);
  }
  let storage;
  try { storage = window.indexedDB ? idbStorage() : M.memoryStorage(); } catch { storage = M.memoryStorage(); }

  const outbox = M.createOutbox(storage, {
    upload: (item, att) => api(`/threads/${item.threadId}/attachments`, {
      method: "POST", raw: true, body: att.blob,
      headers: Object.assign({ "Content-Type": att.contentType || "application/octet-stream", "X-Workbench-Upload": "1", "X-Workbench-Client-Id": att.clientId }, att.durationMs ? { "X-Workbench-Duration-Ms": String(Math.round(att.durationMs)) } : {}),
    }),
    post: (item, ids) => api(`/threads/${item.threadId}/messages`, {
      method: "POST", body: JSON.stringify({ clientId: item.clientId, text: item.text || "", context: item.context, attachmentIds: ids, effort: item.effort || "low", deviceId }),
    }),
  });
  let outboxItems = [];
  outbox.subscribe((items) => { outboxItems = items; scheduleRender(); });
  let retryTimer = null;
  async function flushOutbox() {
    clearTimeout(retryTimer);
    await outbox.flush().catch(() => {});
    const items = await outbox.items().catch(() => []);
    const next = items.filter((i) => i.status === "queued" && i.notBefore).map((i) => i.notBefore).sort()[0];
    if (next) retryTimer = setTimeout(flushOutbox, Math.max(500, next - Date.now()));
  }
  addEventListener("online", () => outbox.items().then((items) => Promise.all(items.map((i) => (i.status === "queued" ? outbox.retry(i.clientId) : null)))).then(flushOutbox));

  // ---- State ------------------------------------------------------------------------

  const state = M.createState();
  let threads = [];
  let open = false, mode = M.layoutFor(innerWidth), es = null, connected = false, deep = false, statusCard = null;
  const draftLocal = { text: "", synced: "", focused: false };
  let pendingAttachments = [];

  // ---- DOM --------------------------------------------------------------------------

  const root = el("div", { class: `wb wb-mode-${mode}`, "data-app": APP });
  const dot = el("span", { class: "wb-dot", hidden: true, "aria-hidden": "true" });
  const pill = el("button", { class: "wb-pill wb-glass", type: "button", "aria-label": "Open Workbench (`)", "aria-expanded": "false", onclick: () => toggle(true) },
    el("span", { class: "wb-glyph", "aria-hidden": "true" }), el("span", { class: "wb-pill-label" }, "Workbench"), dot);
  const titleEl = el("div", { class: "wb-title", id: "wb-title" }, "Workbench");
  const threadSelect = el("select", { class: "wb-threads", "aria-label": "Thread", onchange: () => selectThread(threadSelect.value) });
  const menuBtn = el("button", { class: "wb-icon", type: "button", "aria-label": "More", "aria-expanded": "false", html: ICON.more, onclick: () => toggleMenu() });
  const closeBtn = el("button", { class: "wb-icon", type: "button", "aria-label": "Close Workbench", html: ICON.close, onclick: () => toggle(false) });
  const grab = el("button", { class: "wb-grab", type: "button", "aria-label": "Close Workbench", onclick: () => toggle(false) });
  const deepChip = el("button", { class: "wb-chip", type: "button", "aria-pressed": "false", title: "Think harder on the next message (higher effort)", onclick: () => { deep = !deep; deepChip.setAttribute("aria-pressed", String(deep)); } }, "Deep work");
  const notifyChip = el("button", { class: "wb-chip", type: "button", onclick: () => enablePush() }, "Notifications");
  const nudgeBox = el("input", { type: "checkbox", onchange: () => enablePush(true) });
  const nudgeChip = el("label", { class: "wb-chip" }, nudgeBox, "Ping me when replies are ready");
  const installChip = el("button", { class: "wb-chip", type: "button", hidden: true, onclick: () => install() }, "Install app");
  const menu = el("div", { class: "wb-menu", hidden: true },
    el("button", { class: "wb-chip", type: "button", onclick: () => newThread() }, "New thread"),
    deepChip,
    el("button", { class: "wb-chip", type: "button", onclick: () => loadStatus() }, "Site status"),
    el("button", { class: "wb-chip", type: "button", onclick: () => handoff() }, "Open on my other devices"),
    notifyChip, nudgeChip, installChip);
  const log = el("div", { class: "wb-log", role: "log", "aria-live": "polite", "aria-relevant": "additions text", tabindex: "0" });
  const statusLine = el("div", { class: "wb-status", "aria-live": "polite" });
  const input = el("textarea", { class: "wb-input", rows: "1", placeholder: "Ask, or drop a half-formed idea…", "aria-label": "Message", enterkeyhint: "send" });
  const sendBtn = el("button", { class: "wb-send", type: "button", "aria-label": "Send", html: ICON.send, onclick: () => send() });
  const micBtn = el("button", { class: "wb-mic", type: "button", "aria-label": "Hold to talk", "aria-pressed": "false", html: ICON.mic });
  const fileInput = el("input", { type: "file", accept: "image/*", multiple: true, hidden: true, onchange: () => { addImages([...fileInput.files]); fileInput.value = ""; } });
  const attachBtn = el("button", { class: "wb-attach", type: "button", "aria-label": "Attach a screenshot", html: ICON.image, onclick: () => fileInput.click() });
  const attachRow = el("div", { class: "wb-hint", hidden: true });
  const sheet = el("div", { class: "wb-sheet wb-glass", role: "dialog", "aria-modal": "false", "aria-labelledby": "wb-title", hidden: true },
    grab,
    el("div", { class: "wb-head" }, titleEl, threadSelect, menuBtn, closeBtn),
    menu, log,
    el("div", { class: "wb-foot" }, statusLine, attachRow, el("div", { class: "wb-compose" }, attachBtn, fileInput, input, micBtn, sendBtn)));
  root.append(pill, sheet);

  function mount() {
    document.body.append(root);
    applyMode();
    liftPill();
  }

  // ---- Layout -------------------------------------------------------------------------

  function pageIsDark() {
    if (matchMedia("(prefers-color-scheme: dark)").matches) return true;
    for (const node of [document.body, document.documentElement]) {
      const m = getComputedStyle(node).backgroundColor.match(/rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?/);
      if (m && (m[4] === undefined || Number(m[4]) > 0.5)) return (0.2126 * m[1] + 0.7152 * m[2] + 0.0722 * m[3]) / 255 < 0.4;
    }
    return false;
  }

  function applyMode() {
    mode = M.layoutFor(innerWidth);
    root.className = `wb wb-mode-${mode}${open ? " wb-open" : ""}${pageIsDark() ? " wb-dark" : ""}`;
    document.documentElement.classList.toggle("wb-split-open", open && mode === "split");
    sheet.setAttribute("aria-modal", mode === "sheet" ? "true" : "false");
  }
  addEventListener("resize", () => { applyMode(); liftPill(); });
  if (window.visualViewport) {
    const kb = () => {
      const vv = window.visualViewport;
      const inset = Math.max(0, innerHeight - vv.height - vv.offsetTop);
      root.style.setProperty("--wb-kb", `${Math.round(inset)}px`);
    };
    visualViewport.addEventListener("resize", kb);
    visualViewport.addEventListener("scroll", kb);
  }

  // Keep the pill clear of fixed bars the page puts in the same corner
  // (Commonplace's dock, the signed-out banner).
  function liftPill() {
    if (open) return;
    root.style.setProperty("--wb-lift", "0px");
    const r = pill.getBoundingClientRect();
    let lift = 0;
    for (const x of [r.left + 2, r.left + r.width / 2, r.right - 2]) {
      for (const y of [r.top + 2, r.bottom - 2]) {
        for (const node of document.elementsFromPoint(x, y)) {
          if (root.contains(node) || node === document.body || node === document.documentElement) continue;
          for (let n = node; n && n !== document.body; n = n.parentElement) {
            const cs = getComputedStyle(n);
            if (cs.position === "fixed" || cs.position === "sticky") {
              const top = n.getBoundingClientRect().top;
              if (cs.visibility !== "hidden" && top > innerHeight / 2) lift = Math.max(lift, r.bottom - top + 10);
              break;
            }
          }
        }
      }
    }
    root.style.setProperty("--wb-lift", `${Math.max(0, Math.round(lift))}px`);
  }
  let liftTimer = null;
  const queueLift = () => { if (!liftTimer) liftTimer = setTimeout(() => { liftTimer = null; liftPill(); }, 120); };
  new MutationObserver((records) => { if (!open && records.some((r) => !root.contains(r.target))) queueLift(); })
    .observe(document.documentElement, { childList: true, subtree: true, attributes: true, attributeFilter: ["hidden", "class", "style", "open"] });
  setInterval(() => { if (!document.hidden && !open) liftPill(); }, 2000);
  addEventListener("hashchange", () => setTimeout(liftPill, 50));

  function toggle(next) {
    open = next == null ? !open : next;
    sheet.hidden = !open;
    pill.setAttribute("aria-expanded", String(open));
    applyMode();
    if (open) {
      ensureThread().then(() => { scheduleRender(); scrollToEnd(true); });
      if (!coarse()) setTimeout(() => input.focus(), 30);
    } else {
      pill.focus({ preventScroll: true });
    }
  }
  function toggleMenu(next) {
    const show = next == null ? menu.hidden : next;
    menu.hidden = !show;
    menuBtn.setAttribute("aria-expanded", String(show));
  }

  document.addEventListener("keydown", (e) => {
    if (M.isToggleKey(e)) { e.preventDefault(); toggle(); return; }
    if (e.key === "Escape" && open && root.contains(document.activeElement)) toggle(false);
  });

  // ---- Threads, snapshot and stream ---------------------------------------------------------

  let ensuring = null;
  function ensureThread() {
    if (state.thread) return Promise.resolve();
    if (!ensuring) ensuring = (async () => {
      const want = new URLSearchParams(location.search).get("workbench") || local.get("wb.thread");
      const res = await api("/threads");
      threads = res.threads || [];
      let id = threads.find((t) => t.id === want) ? want : threads[0] && threads[0].id;
      if (!id) {
        const t = await api("/threads", { method: "POST", body: "{}" });
        threads = [t];
        id = t.id;
      }
      await selectThread(id);
    })().catch((err) => { state.notice = `Workbench is unavailable: ${err.message}`; scheduleRender(); }).finally(() => { ensuring = null; });
    return ensuring;
  }

  async function selectThread(id) {
    const snap = await api(`/threads/${id}`);
    M.applySnapshot(state, snap);
    local.set("wb.thread", id);
    draftLocal.text = draftLocal.synced = snap.draft ? snap.draft.text : "";
    input.value = draftLocal.text;
    autosize();
    // Messages still in the outbox show as pending bubbles.
    for (const item of await outbox.items().catch(() => [])) if (item.threadId === id) M.addPending(state, item);
    renderThreads();
    connect();
    scheduleRender();
    scrollToEnd(true);
  }

  async function newThread() {
    const t = await api("/threads", { method: "POST", body: "{}" });
    threads.unshift(t);
    toggleMenu(false);
    await selectThread(t.id);
  }

  async function refreshThreads() {
    try {
      const res = await api("/threads");
      threads = res.threads || [];
      const pending = threads.reduce((n, t) => n + (t.pendingApprovals || 0), 0);
      setDot(pending);
      renderThreads();
    } catch { /* signed out or offline: the guard banner explains */ }
  }

  const EVENTS = ["message.created", "message.started", "message.delta", "message.reset", "message.completed", "tool.started", "tool.finished",
    "approval.requested", "approval.resolved", "approval.result", "draft.updated", "run.started", "run.finished", "spend.updated", "thread.updated"];

  function connect() {
    if (es) es.close();
    if (!state.thread) return;
    es = new EventSource(M.streamURL(state.thread.id, state.lastEventId));
    es.onopen = () => { connected = true; scheduleRender(); };
    es.onerror = () => {
      connected = false;
      scheduleRender();
      if (es && es.readyState === EventSource.CLOSED) {
        // Not a transient drop (e.g. signed out): re-sync from a snapshot later.
        setTimeout(() => { if (state.thread) selectThread(state.thread.id).catch(() => {}); }, 5000);
      }
    };
    for (const type of EVENTS) es.addEventListener(type, onEvent);
  }

  function onEvent(e) {
    let payload;
    try { payload = JSON.parse(e.data); } catch { return; }
    const ev = { id: Number(e.lastEventId), type: e.type, payload };
    if (!M.applyEvent(state, ev)) return;
    if (ev.type === "draft.updated") {
      const merged = M.mergeDraft({ text: input.value, synced: draftLocal.synced, focused: document.activeElement === input }, state.draft, deviceId);
      if (merged.adopt) {
        input.value = merged.text;
        draftLocal.synced = merged.text;
        autosize();
      } else if (state.draft.deviceId === deviceId) draftLocal.synced = state.draft.text;
    }
    if (ev.type === "thread.updated") renderThreads();
    if (ev.type.startsWith("approval.")) setDot(M.pendingApprovals(state));
    scheduleRender();
    if (state.draft.deviceId && state.draft.deviceId !== deviceId) setTimeout(scheduleRender, 6500);
  }

  // ---- Sending ----------------------------------------------------------------------------

  function context() {
    let selection = "";
    try { selection = String(getSelection() || ""); } catch { /* ignore */ }
    if (root.contains(getSelection && getSelection().anchorNode)) selection = "";
    return M.pageContext({ pathname: location.pathname, hash: location.hash, title: document.title, selection, width: innerWidth, height: innerHeight, coarse: coarse(), now: new Date(), timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, hook: window.WorkbenchContext });
  }

  async function queue(text, attachments) {
    await ensureThread();
    if (!state.thread) return;
    const item = { clientId: M.newId("c"), threadId: state.thread.id, text, context: context(), effort: deep ? "high" : "low", attachments, createdAt: Date.now() };
    M.addPending(state, item);
    scheduleRender();
    scrollToEnd(true);
    await outbox.enqueue(item);
    if (deep) { deep = false; deepChip.setAttribute("aria-pressed", "false"); }
    flushOutbox();
  }

  function send() {
    const text = input.value.trim();
    if (!text && !pendingAttachments.length) return;
    const atts = pendingAttachments;
    pendingAttachments = [];
    renderAttachRow();
    input.value = "";
    draftLocal.synced = "";
    clearTimeout(draftTimer);
    autosize();
    queue(text, atts);
  }

  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing && (!coarse() || e.metaKey || e.ctrlKey)) { e.preventDefault(); send(); }
  });
  let draftTimer = null;
  input.addEventListener("input", () => {
    autosize();
    clearTimeout(draftTimer);
    draftTimer = setTimeout(saveDraft, 700);
  });
  input.addEventListener("focus", () => { draftLocal.focused = true; });
  input.addEventListener("blur", () => { draftLocal.focused = false; });
  input.addEventListener("paste", (e) => {
    const files = [...(e.clipboardData ? e.clipboardData.files : [])].filter((f) => f.type.startsWith("image/"));
    if (files.length) { e.preventDefault(); addImages(files); }
  });
  sheet.addEventListener("dragover", (e) => { if ([...e.dataTransfer.types].includes("Files")) e.preventDefault(); });
  sheet.addEventListener("drop", (e) => {
    const files = [...e.dataTransfer.files].filter((f) => f.type.startsWith("image/"));
    if (files.length) { e.preventDefault(); addImages(files); }
  });

  async function saveDraft() {
    if (!state.thread) return;
    const text = input.value;
    if (text === draftLocal.synced) return;
    try {
      await api(`/threads/${state.thread.id}/draft`, { method: "PUT", body: JSON.stringify({ text, deviceId }) });
      draftLocal.synced = text;
    } catch { /* offline: the next keystroke retries */ }
  }

  function autosize() {
    input.style.height = "auto";
    input.style.height = `${Math.min(input.scrollHeight + 2, innerHeight * 0.4)}px`;
    sendBtn.disabled = !input.value.trim() && !pendingAttachments.length;
  }

  function addImages(files) {
    for (const f of files.slice(0, 4)) {
      if (f.size > 20 * 1024 * 1024) continue;
      pendingAttachments.push({ kind: "image", contentType: f.type, blob: f, name: f.name });
    }
    renderAttachRow();
    autosize();
  }
  function renderAttachRow() {
    attachRow.hidden = !pendingAttachments.length;
    attachRow.textContent = pendingAttachments.length ? `${pendingAttachments.length} screenshot${pendingAttachments.length === 1 ? "" : "s"} attached · ` : "";
    if (pendingAttachments.length) attachRow.append(el("button", { class: "wb-chip", type: "button", onclick: () => { pendingAttachments = []; renderAttachRow(); autosize(); } }, "Remove"));
  }

  // ---- Hold to talk ------------------------------------------------------------------------

  let rec = null;
  async function startRecording() {
    if (rec) return;
    if (!navigator.mediaDevices || !window.MediaRecorder) { flash("Voice notes need a browser with microphone recording."); return; }
    rec = { chunks: [], started: Date.now(), keep: true, stopped: false };
    micBtn.setAttribute("aria-pressed", "true");
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: false, noiseSuppression: false } });
      if (!rec || rec.stopped) { stream.getTracks().forEach((t) => t.stop()); return; }
      const type = ["audio/webm;codecs=opus", "audio/mp4", "audio/ogg;codecs=opus"].find((t) => MediaRecorder.isTypeSupported && MediaRecorder.isTypeSupported(t));
      const r = new MediaRecorder(stream, type ? { mimeType: type } : undefined);
      rec.recorder = r;
      rec.stream = stream;
      r.ondataavailable = (e) => { if (e.data && e.data.size) rec && rec.chunks.push(e.data); };
      r.onstop = () => finishRecording();
      r.start(250);
      tickRecording();
    } catch (err) {
      rec = null;
      micBtn.setAttribute("aria-pressed", "false");
      flash(err && err.name === "NotAllowedError" ? "The microphone is blocked for this page." : "Could not start recording.");
    }
  }
  function stopRecording(keep) {
    if (!rec) return;
    rec.keep = keep;
    rec.stopped = true;
    rec.ended = Date.now();
    micBtn.setAttribute("aria-pressed", "false");
    if (rec.recorder && rec.recorder.state !== "inactive") rec.recorder.stop();
    else if (!rec.recorder) rec = null;
  }
  function finishRecording() {
    const r = rec;
    rec = null;
    if (!r) return;
    if (r.stream) r.stream.getTracks().forEach((t) => t.stop());
    const durationMs = (r.ended || Date.now()) - r.started;
    scheduleRender();
    if (!r.keep || durationMs < 400 || !r.chunks.length) return;
    const blob = new Blob(r.chunks, { type: (r.recorder && r.recorder.mimeType) || "audio/webm" });
    queue(input.value.trim(), [{ kind: "audio", contentType: blob.type.split(";")[0], blob, durationMs }]);
    input.value = "";
    autosize();
  }
  function tickRecording() {
    if (!rec) return;
    scheduleRender();
    setTimeout(tickRecording, 250);
  }
  micBtn.addEventListener("pointerdown", (e) => { e.preventDefault(); try { micBtn.setPointerCapture(e.pointerId); } catch { /* ignore */ } startRecording(); });
  micBtn.addEventListener("pointerup", () => stopRecording(true));
  micBtn.addEventListener("pointercancel", () => stopRecording(false));
  micBtn.addEventListener("contextmenu", (e) => e.preventDefault());
  micBtn.addEventListener("keydown", (e) => {
    if (e.key !== " " && e.key !== "Enter") return;
    e.preventDefault();
    if (rec) stopRecording(true); else startRecording();
  });

  // ---- Approvals, status, push, install, hand-off ---------------------------------------

  async function decide(id, approve, btns) {
    btns.forEach((b) => (b.disabled = true));
    try {
      await api(`/approvals/${id}`, { method: "POST", body: JSON.stringify({ decision: approve ? "approve" : "reject", deviceId }) });
    } catch (err) {
      if (err.status === 409) flash("Already answered on another device.");
      else { flash(`Could not send: ${err.message}`); btns.forEach((b) => (b.disabled = false)); }
    }
  }

  async function loadStatus() {
    toggleMenu(false);
    statusCard = { kind: "status", loading: true };
    scheduleRender();
    try { statusCard = await api("/status"); } catch (err) { statusCard = { kind: "status", error: err.message }; }
    scheduleRender();
    scrollToEnd(true);
  }

  async function handoff() {
    toggleMenu(false);
    if (!state.thread) return;
    try {
      const res = await api(`/threads/${state.thread.id}/handoff`, { method: "POST", body: JSON.stringify({ deviceId, path: location.pathname + location.hash }) });
      flash(res.sent ? `Sent to ${res.sent} other device${res.sent === 1 ? "" : "s"}.` : "No other devices have notifications on.");
    } catch (err) { flash(`Could not send: ${err.message}`); }
  }

  function b64ToBytes(s) {
    const pad = "=".repeat((4 - (s.length % 4)) % 4);
    const raw = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
    return Uint8Array.from(raw, (c) => c.charCodeAt(0));
  }
  function deviceLabel() {
    const kind = M.deviceKind(innerWidth, coarse());
    const ua = navigator.userAgent;
    const os = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac/.test(ua) ? "Mac" : /Windows/.test(ua) ? "Windows" : "Linux";
    return `${kind} · ${os}`;
  }
  async function enablePush(quiet) {
    try {
      if (!("serviceWorker" in navigator) || !("PushManager" in window)) { if (!quiet) flash("This browser cannot receive notifications here. On iPhone, install the app first."); return; }
      const key = await api("/push/key");
      if (!key.enabled) { if (!quiet) flash("Notifications are not configured on the server yet."); return; }
      if (Notification.permission !== "granted" && (await Notification.requestPermission()) !== "granted") { if (!quiet) flash("Notifications are blocked."); return; }
      const reg = await navigator.serviceWorker.ready;
      const sub = (await reg.pushManager.getSubscription()) || (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64ToBytes(key.publicKey) }));
      await api(`/push/subscriptions/${deviceId}`, { method: "PUT", body: JSON.stringify({ label: deviceLabel(), nudges: nudgeBox.checked, subscription: sub.toJSON() }) });
      local.set("wb.nudges", nudgeBox.checked ? "1" : "0");
      notifyChip.setAttribute("aria-pressed", "true");
      if (!quiet) flash("Notifications are on for this device.");
    } catch (err) {
      if (!quiet) flash(`Could not turn on notifications: ${err.message}`);
    }
  }

  let installPrompt = null;
  addEventListener("beforeinstallprompt", (e) => { e.preventDefault(); installPrompt = e; installChip.hidden = false; });
  async function install() {
    if (!installPrompt) return;
    installPrompt.prompt();
    await installPrompt.userChoice.catch(() => {});
    installPrompt = null;
    installChip.hidden = true;
  }

  function registerWorker() {
    if (!("serviceWorker" in navigator) || !window.isSecureContext) return;
    navigator.serviceWorker.register(`/${APP}/workbench-sw.js`, { scope: `/${APP}/` }).then(() => {
      if (window.Notification && Notification.permission === "granted") enablePush(true);
    }).catch(() => { /* optional */ });
    navigator.serviceWorker.addEventListener("message", (e) => {
      if (e.data && e.data.type === "workbench-open" && e.data.threadId) { toggle(true); if (!state.thread || state.thread.id !== e.data.threadId) selectThread(e.data.threadId); }
    });
  }

  // ---- Rendering ---------------------------------------------------------------------------

  let renderQueued = false;
  function scheduleRender() {
    if (renderQueued) return;
    renderQueued = true;
    requestAnimationFrame(() => { renderQueued = false; render(); });
  }
  let flashText = "", flashTimer = null;
  function flash(text) {
    flashText = text;
    clearTimeout(flashTimer);
    flashTimer = setTimeout(() => { flashText = ""; scheduleRender(); }, 5000);
    scheduleRender();
  }
  function setDot(n) {
    dot.hidden = !n;
    dot.textContent = n ? String(n) : "";
    pill.setAttribute("aria-label", n ? `Open Workbench: ${n} waiting for approval (\`)` : "Open Workbench (`)");
  }

  function renderThreads() {
    const current = state.thread && state.thread.id;
    titleEl.replaceChildren((state.thread && state.thread.title) || "Workbench", el("small", {}, "One thread, every device"));
    const list = threads.slice();
    if (state.thread && !list.some((t) => t.id === current)) list.unshift(state.thread);
    threadSelect.replaceChildren(...list.map((t) => el("option", { value: t.id, selected: t.id === current }, M.clip((t.id === current && state.thread.title) || t.title || "Untitled thread", 32))));
    threadSelect.hidden = list.length < 2;
  }

  const rendered = new Map(); // item id → { node, sig }

  function attachmentNodes(atts) {
    return (atts || []).map((a) => {
      if (a.local) return el("span", { class: "wb-att" }, a.kind === "audio" ? `Voice note${a.durationMs ? ` · ${Math.round(a.durationMs / 1000)} s` : ""} (waiting to upload)` : "Screenshot (waiting to upload)");
      const src = `${API}/attachments/${a.id}`;
      if (a.kind === "audio") return el("span", { class: "wb-att" }, "Voice note attached", el("audio", { controls: true, preload: "none", src }));
      return el("span", { class: "wb-att" }, el("a", { href: src, target: "_blank", rel: "noopener" }, "Screenshot"));
    });
  }

  function messageNode(item) {
    const cls = `wb-msg wb-msg-${item.role}${item.pending ? " wb-pending" : ""}${item.status === "streaming" ? " wb-streaming" : ""}`;
    const node = el("div", { class: cls, "data-id": item.id });
    if (item.role === "assistant") node.innerHTML = M.renderMarkdown(item.text);
    else node.textContent = item.text;
    node.append(...attachmentNodes(item.attachments));
    const meta = item.pending ? (item.status === "failed" ? "Not sent" : navigator.onLine ? "Sending…" : "Queued, sends when you're back online")
      : item.status === "error" ? "Interrupted" : item.status === "blocked" ? "Not answered: monthly budget used" : "";
    if (meta) node.append(el("span", { class: "wb-meta" }, meta));
    return node;
  }

  function approvalNode(item) {
    const a = state.approvals.get(item.approvalId);
    if (!a) return el("div");
    const d = a.detail || {};
    const action = d.action || null;
    const tag = a.kind === "triage" ? (M.TRIAGE[d.category] || "Triage") : "Needs your OK";
    const preview = (action ? action.detail : d) || {};
    const body = preview.body || preview.text || preview.instructions || "";
    const node = el("div", { class: "wb-card", "data-approval": a.id, "data-status": a.status },
      el("span", { class: "wb-tag" }, tag),
      el("h4", {}, a.kind === "triage" && action ? action.title : a.title),
      a.kind === "triage" && d.reason ? el("p", {}, d.reason) : null,
      body ? el("div", { class: "wb-preview" }, M.clip(body, 600)) : null);
    if (a.status === "pending") {
      const yes = el("button", { class: "wb-btn wb-btn-primary", type: "button" }, a.kind === "triage" ? "Confirm" : "Approve");
      const no = el("button", { class: "wb-btn", type: "button" }, "Not now");
      yes.addEventListener("click", () => decide(a.id, true, [yes, no]));
      no.addEventListener("click", () => decide(a.id, false, [yes, no]));
      node.append(el("div", { class: "wb-actions" }, yes, no));
    } else {
      const res = a.result || {};
      const card = res.card || {};
      const text = a.status === "approved" ? (res.text ? `Done. ${res.text}` : "Approved, running…") : a.status === "failed" ? `Failed: ${res.error || "unknown error"}` : "Declined.";
      node.append(el("div", { class: `wb-state wb-state-${a.status}` }, text, card.url ? [" ", el("a", { href: card.url, target: card.url.startsWith("/") ? "_self" : "_blank", rel: "noopener" }, "Open")] : null));
    }
    return node;
  }

  function lightRow(label, run) {
    const light = M.lightFor(run);
    const text = run ? `${label}: ${run.status === "completed" ? run.conclusion : run.status}${run.sha ? ` · ${run.sha}` : ""}` : `${label}: no runs`;
    return el("div", { class: "wb-light" }, el("i", { "data-light": light, "aria-hidden": "true" }), run && run.url ? el("a", { href: run.url, target: "_blank", rel: "noopener" }, text) : text);
  }

  function cardNode(card) {
    if (card.kind === "status") {
      if (card.loading) return el("div", { class: "wb-card" }, el("span", { class: "wb-tag" }, "Site status"), el("p", {}, "Checking…"));
      if (card.error) return el("div", { class: "wb-card" }, el("span", { class: "wb-tag" }, "Site status"), el("p", {}, card.error));
      return el("div", { class: "wb-card", "data-card": "status" },
        el("span", { class: "wb-tag" }, "Site status"),
        el("div", { class: "wb-lights" },
          lightRow("CI on main", card.ci), lightRow("Deploy", card.deploy),
          ...(card.prs || []).map((p) => el("div", { class: "wb-light" }, el("i", { "data-light": p.checks.failed ? "red" : p.checks.pending ? "running" : "green", "aria-hidden": "true" }),
            el("a", { href: p.url, target: "_blank", rel: "noopener" }, `#${p.number} ${M.clip(p.title, 60)}`), ` · ${p.checks.passed}✓ ${p.checks.failed}✗ ${p.checks.pending}…`))));
    }
    if (card.kind === "triage") return el("div", { class: "wb-card" }, el("span", { class: "wb-tag" }, M.TRIAGE[card.category] || "Triage"), el("h4", {}, card.question || card.idea), card.reason ? el("p", {}, card.reason) : null);
    const titles = { moment: "Moment saved", issue: "Issue created", comment: "Comment posted", practice: "Practice section added", tune: "Tune updated", note: "Note added" };
    return el("div", { class: "wb-card" }, el("span", { class: "wb-tag" }, titles[card.kind] || "Done"),
      el("h4", {}, card.title || card.note || card.changes || (card.number ? `#${card.number}` : "")),
      card.url ? el("a", { href: card.url, target: card.url.startsWith("/") ? "_self" : "_blank", rel: "noopener" }, "Open") : null);
  }

  function signature(item) {
    if (item.kind === "message") return `${item.text.length}|${item.text.slice(-24)}|${item.status}|${item.pending}|${(item.attachments || []).length}|${navigator.onLine}`;
    if (item.kind === "approval") { const a = state.approvals.get(item.approvalId) || {}; return `${a.status}|${JSON.stringify(a.result || {}).length}`; }
    return "card";
  }

  function nearBottom() { return log.scrollHeight - log.scrollTop - log.clientHeight < 80; }
  function scrollToEnd(force) { if (force || nearBottom()) requestAnimationFrame(() => { log.scrollTop = log.scrollHeight; }); }

  function render() {
    if (!open) return;
    const stick = nearBottom();
    const items = M.timeline(state);
    const keep = new Set();
    let prev = null;
    for (const item of items) {
      keep.add(item.id);
      const sig = signature(item);
      let entry = rendered.get(item.id);
      if (!entry || entry.sig !== sig) {
        const node = item.kind === "message" ? messageNode(item) : item.kind === "approval" ? approvalNode(item) : cardNode(item.card);
        if (entry) entry.node.replaceWith(node);
        entry = { node, sig };
        rendered.set(item.id, entry);
      }
      const want = prev ? prev.nextSibling : log.firstChild;
      if (entry.node !== want) log.insertBefore(entry.node, want);
      prev = entry.node;
    }
    for (const [id, entry] of rendered) if (!keep.has(id)) { entry.node.remove(); rendered.delete(id); }
    // Transient rows after the timeline: steps, notices, the empty state, a status card.
    log.querySelectorAll(".wb-transient").forEach((n) => n.remove());
    const extra = [];
    if (!items.length) extra.push(el("div", { class: "wb-empty" }, el("strong", {}, "What's on your mind?"), "Ask about this page, drop an idea to triage, or hold the mic and talk."));
    if (state.running && state.steps.length) extra.push(el("div", { class: "wb-steps" }, ...state.steps.slice(-4).map((s) => el("span", {}, M.stepLabel(s)))));
    if (state.running && !state.steps.length && !items.some((i) => i.status === "streaming")) extra.push(el("div", { class: "wb-notice" }, "Thinking…"));
    if (state.notice) extra.push(el("div", { class: `wb-notice${state.noticeStatus && state.noticeStatus !== "done" ? " wb-bad" : ""}` }, state.notice));
    if (statusCard) extra.push(cardNode(statusCard));
    for (const n of extra) { n.classList.add("wb-transient"); log.append(n); }

    const left = [];
    if (rec) left.push(el("span", { class: "wb-rec" }, `● Recording ${((Date.now() - rec.started) / 1000).toFixed(1)} s, release to send`));
    else if (flashText) left.push(el("span", {}, flashText));
    else if (M.remoteTyping(state, deviceId, Date.now())) left.push(el("span", {}, "Typing on another device…"));
    else if (!connected && state.thread) left.push(el("span", {}, navigator.onLine ? "Reconnecting…" : "Offline: messages will wait here"));
    const ob = M.outboxLabel(outboxItems.filter((i) => !state.thread || i.threadId === state.thread.id));
    if (ob) left.push(el("span", { class: "wb-outbox" }, ob));
    statusLine.replaceChildren(el("span", {}, ...left), el("span", {}, M.spendLine(state.spend)));
    sendBtn.disabled = !input.value.trim() && !pendingAttachments.length;
    if (stick) scrollToEnd(true);
  }

  // ---- Boot --------------------------------------------------------------------------------

  async function boot() {
    await loadPrefs();
    window.__workbench.deviceId = deviceId;
    mount();
    nudgeBox.checked = local.get("wb.nudges") === "1";
    renderThreads();
    registerWorker();
    refreshThreads();
    flushOutbox();
    if (new URLSearchParams(location.search).get("workbench")) toggle(true);
    document.addEventListener("visibilitychange", () => { if (!document.hidden) { refreshThreads(); flushOutbox(); } });
  }

  window.__workbench = {
    state, outbox, deviceId,
    open: () => toggle(true), close: () => toggle(false), isOpen: () => open, mode: () => mode,
    connected: () => connected, send: (text) => { input.value = text; send(); }, selectThread, flush: flushOutbox,
  };
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot); else boot();
})();
