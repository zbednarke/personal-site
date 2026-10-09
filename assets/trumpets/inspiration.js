/* Horn inspiration board: instant capture of links and images.
 * Private data lives in the API only: nothing is written to localStorage,
 * sessionStorage or analytics, and shared links travel in the URL fragment
 * (#inspiration/add?url=…), which never reaches the server or its logs.
 * Fetched titles, notes and descriptions render with textContent only. */
(() => {
  "use strict";
  const IM = window.InspirationModel,
    TM = window.TrumpetModel,
    $ = (s) => document.querySelector(s),
    BASE = "/trumpets/api/v1/trumpets",
    LOGIN = "Your private session needs a login. Reload the page to sign in.";
  const state = {
    items: [],
    archived: [],
    archivedLoaded: false,
    vocab: { makers: [], tags: [] },
    loaded: false,
    loading: null,
    seeded: false,
    active: false,
    tags: new Set(),
    priorities: new Set(),
    detailId: null,
    quickId: null,
    highlightId: null,
  };
  const previews = new Map(); // entry id → local object URL (this page only)
  const uploads = new Map(); // entry id → {state, error, retry}
  const saves = new Map(); // entry id → pending autosave
  const cardCache = new Map();
  const filters = $("#inspiration-filters"),
    grid = $("#inspiration-grid"),
    captureInput = $("#inspiration-capture"),
    dialog = $("#inspiration-detail"),
    detailForm = $("#inspiration-form");

  function el(tag, className, text) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    if (text != null) n.textContent = text;
    return n;
  }
  function button(className, text, onClick, label) {
    const b = el("button", className, text);
    b.type = "button";
    if (label) b.setAttribute("aria-label", label);
    if (onClick) b.addEventListener("click", onClick);
    return b;
  }
  function uuid() {
    if (crypto.randomUUID) return crypto.randomUUID();
    const b = crypto.getRandomValues(new Uint8Array(16));
    b[6] = (b[6] & 0x0f) | 0x40;
    b[8] = (b[8] & 0x3f) | 0x80;
    const h = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
    return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
  }
  function shortDate(v) {
    return v ? new Date(v).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) : "";
  }

  async function api(path, body, method) {
    const m = method || (body !== undefined ? "POST" : "GET");
    const response = await fetch(BASE + path, {
      method: m,
      credentials: "same-origin",
      cache: "no-store",
      ...(m !== "GET" ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body ?? {}) } : {}),
    });
    if (response.status === 204) return null;
    let data = null;
    try {
      data = await response.json();
    } catch {}
    if (!response.ok) {
      const error = new Error(response.status === 401 ? LOGIN : data?.error || `Data service returned ${response.status}`);
      error.status = response.status;
      error.body = data;
      throw error;
    }
    return data;
  }
  function imageURL(image) {
    return `${BASE}/inspiration/images/${encodeURIComponent(image.id)}`;
  }

  // ---- State helpers ---------------------------------------------------------
  function find(id) {
    return state.items.find((x) => x.id === id) || state.archived.find((x) => x.id === id) || null;
  }
  function upsert(row) {
    const target = row.archivedAt ? state.archived : state.items;
    const other = row.archivedAt ? state.items : state.archived;
    const otherIndex = other.findIndex((x) => x.id === row.id);
    let existing = null;
    if (otherIndex >= 0) existing = other.splice(otherIndex, 1)[0];
    const index = target.findIndex((x) => x.id === row.id);
    if (index >= 0) existing = target[index];
    if (existing) {
      for (const k of Object.keys(existing)) if (!(k in row) && !["temp", "pendingQuick"].includes(k)) delete existing[k];
      Object.assign(existing, row);
      if (index < 0) target.unshift(existing);
      return existing;
    }
    target.unshift(row);
    return row;
  }
  function removeLocal(id) {
    state.items = state.items.filter((x) => x.id !== id);
    state.archived = state.archived.filter((x) => x.id !== id);
    if (previews.has(id)) URL.revokeObjectURL(previews.get(id));
    previews.delete(id);
    uploads.delete(id);
    saves.delete(id);
    cardCache.delete(id);
    if (state.quickId === id) hideQuick();
    if (state.detailId === id && dialog.open) dialog.close();
  }
  function rekey(map, from, to) {
    if (map.has(from)) {
      map.set(to, map.get(from));
      map.delete(from);
    }
  }
  // An optimistic entry keeps its object identity when the server row arrives.
  function promote(entry, row) {
    const from = entry.id;
    delete entry.temp;
    Object.assign(entry, row);
    rekey(previews, from, row.id);
    rekey(uploads, from, row.id);
    cardCache.delete(from);
    if (state.quickId === from) state.quickId = row.id;
    if (state.detailId === from) state.detailId = row.id;
    if (entry.pendingQuick) {
      const fields = entry.pendingQuick;
      delete entry.pendingQuick;
      queuePatch(row.id, fields);
    }
  }
  function makeTemp(fields) {
    return {
      id: "temp-" + uuid(),
      temp: true,
      clientCaptureId: uuid(),
      tags: [],
      why: "",
      priority: "someday",
      priceSeen: null,
      priceCurrency: "USD",
      pinned: false,
      images: [],
      revision: 0,
      createdAt: new Date().toISOString(),
      ...fields,
    };
  }

  // ---- Loading ---------------------------------------------------------------
  async function fetchBoard() {
    const data = await api("/inspiration");
    const temps = state.items.filter((x) => x.temp);
    state.items = [];
    for (const row of data.inspirations || []) upsert(row);
    state.items = [...temps, ...state.items.sort((a, b) => b.pinned - a.pinned || new Date(b.createdAt) - new Date(a.createdAt))];
    state.vocab = data.vocab || state.vocab;
    fillVocab();
    return data;
  }
  function ensureLoaded(seed = false) {
    if (!state.loading)
      state.loading = fetchBoard()
        .then(() => (state.loaded = true))
        .catch((e) => {
          state.loading = null;
          hint(e.message, true);
          throw e;
        });
    return state.loading.then(async () => {
      if (seed && !state.seeded && !state.items.length) {
        state.seeded = true;
        try {
          const result = await api("/inspiration/seed", {});
          if (result?.added) {
            await fetchBoard();
            for (const x of state.items) if (x.metadataStatus === "pending") enrich(x.id);
          }
        } catch (e) {
          hint(e.message, true);
        }
      }
      render();
      if (seed) enrichStale();
    });
  }
  async function loadArchived() {
    const data = await api("/inspiration?archived=1");
    state.archived = [];
    for (const row of data.inspirations || []) upsert(row);
    state.archivedLoaded = true;
  }
  // Pending previews older than a minute are retried on load, 3 at a time.
  let enrichRunning = 0;
  const enrichQueue = [];
  function enrichStale() {
    const cutoff = Date.now() - 60000;
    for (const x of state.items)
      if (!x.temp && x.metadataStatus === "pending" && new Date(x.createdAt).getTime() < cutoff && !enrichQueue.includes(x.id)) enrichQueue.push(x.id);
    pumpEnrich();
  }
  function pumpEnrich() {
    while (enrichRunning < 3 && enrichQueue.length) {
      const id = enrichQueue.shift();
      enrichRunning++;
      enrich(id).finally(() => {
        enrichRunning--;
        pumpEnrich();
      });
    }
  }
  async function enrich(id, force = false) {
    try {
      const row = await api(`/inspiration/${id}/enrich${force ? "?force=1" : ""}`, {});
      if (!find(id)) return;
      upsert(row);
      reapplyPending(id);
      render();
      if (state.detailId === id) renderDetail({ media: true });
    } catch (e) {
      if (force) toast(e.message);
    }
  }

  // ---- Rendering -------------------------------------------------------------
  function fillVocab() {
    const makers = $("#inspiration-makers"),
      tags = $("#inspiration-tags");
    makers.replaceChildren(...(state.vocab.makers || []).map((m) => new Option(m)));
    tags.replaceChildren(...[...new Set([...(state.vocab.tags || []), ...IM.tagSuggestions])].map((t) => new Option(t)));
  }
  function filterState() {
    return {
      query: filters.elements.search.value,
      maker: filters.elements.maker.value,
      source: filters.elements.source.value,
      hasPrice: filters.elements.hasPrice.checked,
      tags: [...state.tags],
      priorities: [...state.priorities],
    };
  }
  function renderFilters() {
    const makerSelect = filters.elements.maker,
      old = makerSelect.value;
    const makers = [...new Set([...state.items, ...state.archived].map((x) => x.maker).filter(Boolean))].sort((a, b) => a.localeCompare(b));
    makerSelect.replaceChildren(new Option("All makers", ""), ...makers.map((m) => new Option(m, m)));
    makerSelect.value = makers.includes(old) ? old : "";
    const used = [...new Set([...state.items, ...(filters.elements.archived.checked ? state.archived : [])].flatMap((x) => x.tags || []))].sort();
    for (const t of [...state.tags]) if (!used.includes(t)) state.tags.delete(t);
    $("#tag-filter").replaceChildren(
      ...used.map((t) => {
        const b = button("chip" + (state.tags.has(t) ? " selected" : ""), t, () => {
          state.tags.has(t) ? state.tags.delete(t) : state.tags.add(t);
          render();
        });
        b.setAttribute("aria-pressed", String(state.tags.has(t)));
        return b;
      }),
    );
    for (const b of $("#priority-filter").children) {
      const on = state.priorities.has(b.dataset.priority);
      b.classList.toggle("selected", on);
      b.setAttribute("aria-pressed", String(on));
    }
    const active =
      ["maker", "source"].filter((n) => filters.elements[n].value).length +
      state.tags.size +
      state.priorities.size +
      Number(filters.elements.hasPrice.checked) +
      Number(filters.elements.archived.checked) +
      Number(filters.elements.sort.value !== "newest");
    $("#inspiration-filter-toggle").textContent = "Filters & sort" + (active ? ` · ${active} active` : "");
  }
  function render() {
    renderFilters();
    const showArchived = filters.elements.archived.checked;
    const base = showArchived ? [...state.items, ...state.archived] : state.items;
    const rows = IM.sortInspirations(IM.filterInspirations(base, filterState()), filters.elements.sort.value);
    const cards = rows.map(card);
    grid.replaceChildren(...cards);
    const live = state.items.length;
    $("#inspiration-count").textContent = live;
    $("#inspiration-result-count").textContent = state.loaded
      ? `${rows.length} ${rows.length === 1 ? "entry" : "entries"}${showArchived ? " / including archived" : ""} / ${live} on the board`
      : "Loading inspiration…";
    $("#inspiration-empty").hidden = !state.loaded || rows.length > 0;
    $("#inspiration-empty-copy").textContent = base.length
      ? "No entries match these filters. Clear a filter to broaden the board."
      : "Paste a link, paste or drop a screenshot, or tap 📷. On your phone, share to the “Horn inspiration” Shortcut (see the README).";
  }
  function mediaFor(x) {
    const media = el("div", "insp-media");
    const image = (x.images || [])[0];
    const local = previews.get(x.id);
    if (local || image) {
      const img = el("img");
      img.alt = IM.cardTitle(x);
      img.loading = "lazy";
      img.decoding = "async";
      if (!local && image.width && image.height) {
        img.width = image.width;
        img.height = image.height;
      }
      img.src = local || imageURL(image);
      img.addEventListener(
        "error",
        () => {
          img.remove();
          media.classList.add("no-photo", "placeholder");
        },
        { once: true },
      );
      media.append(img);
    } else {
      media.classList.add("no-photo", "placeholder");
      if (x.temp || x.metadataStatus === "pending") media.classList.add("skeleton");
    }
    return media;
  }
  function statusNote(x) {
    const upload = uploads.get(x.id);
    if (upload?.state === "uploading") return el("span", "insp-status working", "Uploading…");
    if (upload?.state === "failed") {
      const wrap = el("span", "insp-status failed");
      wrap.append(document.createTextNode("Upload failed · "));
      wrap.append(
        button("retry", "Retry", (e) => {
          e.stopPropagation();
          upload.retry?.();
        }),
      );
      return wrap;
    }
    if (x.temp || x.metadataStatus === "pending") return el("span", "insp-status working", x.kind === "link" ? "Fetching preview…" : "Saving…");
    if (x.metadataStatus === "failed" && !(x.images || []).length) return el("span", "insp-status", "Preview unavailable");
    if (x.metadataStatus === "partial" && ["instagram", "facebook", "tiktok"].includes(x.provider) && !(x.images || []).length)
      return el("span", "insp-status", "Add a screenshot for a preview");
    return null;
  }
  function hornLine(x) {
    if (!x.hornId) return null;
    const h = x.horn || {};
    return el(
      "p",
      "insp-tracked",
      h.acquired ? "Acquired ✓" : `Tracked in Observatory · ${h.activeOffers || 0} live offer${h.activeOffers === 1 ? "" : "s"}`,
    );
  }
  function cardSignature(x) {
    const u = uploads.get(x.id);
    return JSON.stringify([x.revision, x.updatedAt, x.metadataStatus, x.title, (x.images || []).map((i) => i.id), x.temp, u?.state, previews.has(x.id), x.archivedAt, x.horn, x.pinned, x.priority, x.maker, x.model, x.tags, x.priceSeen, x.why, state.highlightId === x.id]);
  }
  function card(x) {
    const signature = cardSignature(x);
    const cached = cardCache.get(x.id);
    if (cached && cached.signature === signature) return cached.node;
    const article = el("article", "inspiration-card");
    article.dataset.id = x.id;
    article.classList.toggle("pinned", !!x.pinned);
    article.classList.toggle("archived", !!x.archivedAt);
    article.classList.toggle("highlight", state.highlightId === x.id);
    article.classList.toggle("pending", !!x.temp);
    const media = mediaFor(x);
    media.append(el("span", "badge provider-badge", IM.badgeLabel(x)));
    const note = statusNote(x);
    if (note) media.append(note);
    const pin = button("pin" + (x.pinned ? " on" : ""), "📌", (e) => {
      e.stopPropagation();
      if (!x.temp) queuePatch(x.id, { pinned: !x.pinned }, { immediate: true });
    }, `${x.pinned ? "Unpin" : "Pin"} ${IM.cardTitle(x)}`);
    pin.setAttribute("aria-pressed", String(!!x.pinned));
    media.append(pin);
    article.append(media);
    const body = el("div", "insp-body");
    const title = el("h2", "insp-title");
    const open = button("insp-open", IM.cardTitle(x), null, `Open ${IM.cardTitle(x)}`);
    title.append(open);
    body.append(title);
    const makerLine = [x.maker, x.model].filter(Boolean).join(" · ");
    if (makerLine && makerLine !== IM.cardTitle(x)) body.append(el("p", "insp-maker", makerLine));
    const meta = el("div", "insp-meta");
    meta.append(el("span", "priority-pill p-" + x.priority, IM.priorityLabel(x.priority)));
    const price = IM.formatPriceSeen(x);
    if (price) {
      const p = el("span", "insp-price", price);
      if (x.priceAuto) p.append(el("i", "", "auto"));
      meta.append(p);
    }
    body.append(meta);
    if ((x.tags || []).length) {
      const tags = el("div", "tags");
      for (const t of x.tags.slice(0, 3)) tags.append(el("span", "", t));
      body.append(tags);
    }
    const tracked = hornLine(x);
    if (tracked) body.append(tracked);
    body.append(el("p", "insp-date", x.archivedAt ? `Archived ${shortDate(x.archivedAt)}` : `Added ${shortDate(x.createdAt)}`));
    article.append(body);
    article.addEventListener("click", (e) => {
      if (e.target.closest("button:not(.insp-open)")) return;
      if (!x.temp) openDetail(x.id);
    });
    cardCache.set(x.id, { signature, node: article });
    return article;
  }
  function highlight(id) {
    state.highlightId = id;
    render();
    const node = grid.querySelector(`[data-id="${CSS.escape(id)}"]`);
    node?.scrollIntoView({ block: "center", behavior: "smooth" });
    setTimeout(() => {
      if (state.highlightId === id) {
        state.highlightId = null;
        render();
      }
    }, 2500);
  }

  // ---- Toast, hint and quick strip ------------------------------------------
  let toastTimer = null;
  function toast(text, actionLabel, action, ms = 5000) {
    const box = $("#inspiration-toast"),
      act = $("#inspiration-toast-action");
    $("#inspiration-toast-text").textContent = text;
    act.hidden = !actionLabel;
    act.textContent = actionLabel || "";
    act.onclick = actionLabel
      ? () => {
          box.hidden = true;
          action();
        }
      : null;
    box.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (box.hidden = true), ms);
  }
  function hint(text, error = false) {
    const n = $("#capture-hint");
    n.textContent = text;
    n.classList.toggle("error", error);
  }
  let quickTimer = null;
  function armQuickTimer() {
    clearTimeout(quickTimer);
    quickTimer = setTimeout(() => {
      if ($("#quick-strip").contains(document.activeElement)) armQuickTimer();
      else hideQuick();
    }, 15000);
  }
  function showQuick(id) {
    const x = find(id);
    if (!x) return;
    state.quickId = id;
    $("#quick-strip").hidden = false;
    $("#quick-maker").value = x.maker || "";
    $("#quick-why").value = x.why || "";
    renderPriorityChips($("#quick-priority"), x.priority, (p) => queuePatch(state.quickId, { priority: p }, { immediate: true }));
    armQuickTimer();
  }
  function hideQuick() {
    state.quickId = null;
    $("#quick-strip").hidden = true;
    clearTimeout(quickTimer);
  }
  function renderPriorityChips(container, current, onPick) {
    container.replaceChildren(
      ...IM.priorities.map((p) => {
        const b = button("chip p-" + p + (p === current ? " selected" : ""), IM.priorityLabel(p), () => {
          for (const c of container.children) {
            c.classList.toggle("selected", c === b);
            c.setAttribute("aria-pressed", String(c === b));
          }
          onPick(p);
        });
        b.setAttribute("aria-pressed", String(p === current));
        return b;
      }),
    );
  }

  // ---- Autosave (600 ms debounce, expectedRevision, conflict merge) ---------
  const fieldOf = {
    url: (x) => x.sourceUrl || "",
    archived: (x) => !!x.archivedAt,
    hornId: (x) => x.hornId ?? null,
    priceSeen: (x) => x.priceSeen ?? null,
    priceSeenOn: (x) => x.priceSeenOn || null,
    imageOrder: (x) => (x.images || []).map((i) => i.id),
    tags: (x) => x.tags || [],
    pinned: (x) => !!x.pinned,
  };
  const valueOf = (x, k) => (fieldOf[k] ? fieldOf[k](x) : x[k] ?? "");
  const same = (a, b) => JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
  function applyLocal(x, fields) {
    for (const [k, v] of Object.entries(fields)) {
      if (k === "url") x.sourceUrl = v || "";
      else if (k === "archived") x.archivedAt = v ? x.archivedAt || new Date().toISOString() : null;
      else if (k === "imageOrder") x.images = v.map((id) => x.images.find((i) => i.id === id)).filter(Boolean).concat(x.images.filter((i) => !v.includes(i.id)));
      else x[k] = v;
    }
  }
  function reapplyPending(id) {
    const s = saves.get(id),
      x = find(id);
    if (s && x) applyLocal(x, s.fields);
  }
  function setSaveState(id, text, retry) {
    if (state.detailId !== id) return;
    const n = $("#inspiration-save");
    n.replaceChildren(document.createTextNode(text));
    n.classList.toggle("error", !!retry);
    if (retry) n.append(button("retry", "Retry", retry));
  }
  function queuePatch(id, fields, { immediate = false } = {}) {
    const x = find(id);
    if (!x) return;
    if (x.temp) {
      x.pendingQuick = { ...(x.pendingQuick || {}), ...fields };
      applyLocal(x, fields);
      render();
      return;
    }
    const s = saves.get(id) || { fields: {}, base: {}, timer: null, busy: false, failed: false };
    for (const [k, v] of Object.entries(fields)) {
      if (!(k in s.base)) s.base[k] = valueOf(x, k);
      s.fields[k] = v;
    }
    s.failed = false;
    saves.set(id, s);
    applyLocal(x, fields);
    setSaveState(id, "Saving…");
    clearTimeout(s.timer);
    s.timer = setTimeout(() => flush(id), immediate ? 0 : 600);
    if (immediate || "archived" in fields) render();
  }
  async function flush(id) {
    const s = saves.get(id),
      x = find(id);
    if (!s || !x || s.busy || !Object.keys(s.fields).length) return;
    const fields = s.fields,
      base = s.base;
    s.fields = {};
    s.base = {};
    s.busy = true;
    try {
      const row = await api(`/inspiration/${id}`, { expectedRevision: x.revision, ...fields }, "PATCH");
      upsert(row);
      reapplyPending(id);
      setSaveState(id, Object.keys(s.fields).length ? "Saving…" : "Saved");
      if ("url" in fields) enrich(id);
    } catch (e) {
      if (e.status === 409 && e.body?.conflict && e.body.inspiration) {
        // Re-apply fields the other device did not touch; the server wins
        // where both edited the same field.
        const server = e.body.inspiration;
        let conflicted = false;
        upsert(server);
        for (const [k, v] of Object.entries(fields)) {
          if (!same(valueOf(server, k), base[k])) conflicted = true;
          else if (!(k in s.fields)) {
            s.fields[k] = v;
            s.base[k] = valueOf(server, k);
          }
        }
        reapplyPending(id);
        if (conflicted) toast("Updated on another device.");
      } else if (e.status === 409 && e.body?.duplicate) {
        toast("Already on your board");
        await refresh();
      } else if (e.status === 400 || e.status === 404) {
        setSaveState(id, "Couldn't save · " + e.message);
        await refresh();
      } else {
        for (const [k, v] of Object.entries(fields)) if (!(k in s.fields)) {
          s.fields[k] = v;
          s.base[k] = base[k];
        }
        s.failed = true;
        setSaveState(id, "Couldn't save · ", () => {
          s.failed = false;
          flush(id);
        });
      }
    } finally {
      s.busy = false;
      render();
      if (state.detailId === id) renderDetail({ media: "imageOrder" in fields || "url" in fields });
      if (Object.keys(s.fields).length && !s.failed) s.timer = setTimeout(() => flush(id), 0);
    }
  }
  async function refresh() {
    try {
      await fetchBoard();
      if (state.archivedLoaded) await loadArchived();
    } catch {}
    render();
    if (state.detailId && dialog.open) renderDetail({ media: false });
  }

  // ---- Capture --------------------------------------------------------------
  async function create(body) {
    try {
      return await api("/inspiration", body);
    } catch (e) {
      // A dropped connection retries once with the same capture id (idempotent).
      if (e instanceof TypeError) return api("/inspiration", body);
      throw e;
    }
  }
  function handleDuplicate(body) {
    const row = upsert(body.inspiration);
    render();
    if (body.archived)
      toast(`You archived this on ${shortDate(row.archivedAt)}. Restore?`, "Restore", () => queuePatch(row.id, { archived: false }, { immediate: true }), 8000);
    else {
      toast("Already on your board");
      highlight(row.id);
    }
  }
  async function captureLink(url, why = "") {
    hint("");
    const c = IM.classifyURL(url);
    if (!c) return hint("That link can't be added", true);
    const live = state.items.find((x) => !x.temp && (x.canonicalUrl === c.canonical || (c.mediaId && x.provider === c.provider && x.providerMediaId === c.mediaId)));
    if (live) {
      toast("Already on your board");
      return highlight(live.id);
    }
    const entry = makeTemp({ kind: "link", sourceUrl: url, canonicalUrl: c.canonical, provider: c.provider, providerMediaId: c.mediaId, mediaFormat: c.format, metadataStatus: "pending", why });
    state.items.unshift(entry);
    render();
    showQuick(entry.id);
    let row;
    try {
      row = await create({ clientCaptureId: entry.clientCaptureId, url, why });
    } catch (e) {
      removeLocal(entry.id);
      render();
      if (e.status === 409 && e.body?.duplicate) return handleDuplicate(e.body);
      if (!captureInput.value) captureInput.value = url; // keep the unsent link
      return hint(e.message, true);
    }
    promote(entry, row);
    render();
    toast(`Added · ${IM.badgeLabel(row)}`, "Undo", () => removeEntry(row.id));
    enrich(row.id);
  }
  function isImageFile(f) {
    return f && (/^image\//.test(f.type || "") || /\.(heic|heif|jpe?g|png|gif|webp)$/i.test(f.name || ""));
  }
  function unsupported(message) {
    const e = new Error(message);
    e.unsupported = true;
    return e;
  }
  async function prepareImage(file) {
    const type = (file.type || "").toLowerCase();
    if (type === "image/gif") {
      const plan = IM.resizePlan(0, 0, type, file.size);
      if (plan.mode === "reject") throw unsupported(plan.reason);
      return { blob: file };
    }
    let bitmap = null;
    try {
      bitmap = await createImageBitmap(file);
    } catch {}
    if (!bitmap) {
      const plan = IM.fallbackPlan(type, file.size);
      if (plan.mode === "reject") throw unsupported(plan.reason);
      return { blob: file };
    }
    const plan = IM.resizePlan(bitmap.width, bitmap.height, type, file.size);
    const canvas = typeof OffscreenCanvas === "function" ? new OffscreenCanvas(plan.width, plan.height) : Object.assign(document.createElement("canvas"), { width: plan.width, height: plan.height });
    const ctx = canvas.getContext("2d");
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, plan.width, plan.height);
    ctx.drawImage(bitmap, 0, 0, plan.width, plan.height);
    bitmap.close?.();
    const blob = canvas.convertToBlob
      ? await canvas.convertToBlob({ type: plan.type, quality: plan.quality })
      : await new Promise((resolve) => canvas.toBlob(resolve, plan.type, plan.quality));
    if (!blob || blob.size > IM.maxUploadBytes) throw unsupported("This image is too large. Try a screenshot.");
    return { blob, width: plan.width, height: plan.height };
  }
  async function uploadBlob(id, prepared, position) {
    const headers = { "Content-Type": prepared.blob.type, "X-Image-Position": String(position) };
    if (prepared.width) headers["X-Image-Width"] = String(prepared.width);
    if (prepared.height) headers["X-Image-Height"] = String(prepared.height);
    const response = await fetch(`${BASE}/inspiration/${id}/images`, { method: "POST", credentials: "same-origin", cache: "no-store", headers, body: prepared.blob });
    let data = null;
    try {
      data = await response.json();
    } catch {}
    if (!response.ok) throw new Error(response.status === 401 ? LOGIN : data?.error || `Upload failed (${response.status})`);
    return data.image;
  }
  // Two uploads at a time; nothing is persisted locally if the page closes.
  const uploadQueue = [];
  let uploadsRunning = 0;
  function enqueueUpload(job) {
    uploadQueue.push(job);
    pumpUploads();
  }
  function pumpUploads() {
    while (uploadsRunning < 2 && uploadQueue.length) {
      const job = uploadQueue.shift();
      uploadsRunning++;
      job().finally(() => {
        uploadsRunning--;
        pumpUploads();
      });
    }
  }
  function uploadJob(entry, file, prepared, position) {
    const job = async () => {
      uploads.set(entry.id, { state: "uploading" });
      render();
      try {
        if (entry.temp) {
          promote(entry, await create({ clientCaptureId: entry.clientCaptureId, kind: "image", why: entry.why || "" }));
          uploads.set(entry.id, { state: "uploading" });
        }
        const image = await uploadBlob(entry.id, await prepared, position);
        entry.images = [...(entry.images || []).filter((i) => i.id !== image.id), image].sort((a, b) => (a.role === b.role ? a.position - b.position : a.role === "upload" ? -1 : 1));
        uploads.delete(entry.id);
      } catch (e) {
        if (e.unsupported) {
          if (entry.temp || (entry.kind === "image" && !(entry.images || []).length)) {
            if (!entry.temp) api(`/inspiration/${entry.id}`, undefined, "DELETE").catch(() => {});
            removeLocal(entry.id);
          } else uploads.delete(entry.id);
          hint(e.message, true);
          toast(e.message);
        } else uploads.set(entry.id, { state: "failed", error: e.message, retry: () => enqueueUpload(uploadJob(entry, file, prepareImage(file), position)) });
      }
      render();
      if (state.detailId === entry.id) renderDetail({ media: true });
    };
    return job;
  }
  function captureImage(file, why = "") {
    if (!isImageFile(file)) return hint("Paste a link or an image", true);
    hint("");
    const entry = makeTemp({ kind: "image", provider: "upload", mediaFormat: "image", metadataStatus: "skipped", why });
    previews.set(entry.id, URL.createObjectURL(file));
    uploads.set(entry.id, { state: "uploading" });
    state.items.unshift(entry);
    render();
    showQuick(entry.id);
    const prepared = prepareImage(file);
    prepared.catch(() => {});
    enqueueUpload(uploadJob(entry, file, prepared, 0));
  }
  function addImagesToEntry(id, files) {
    const entry = find(id);
    if (!entry) return;
    let position = (entry.images || []).filter((i) => i.role === "upload").length;
    for (const file of files) {
      if (!isImageFile(file)) continue;
      if ((entry.images || []).length + uploadQueue.length >= 20) return toast("At most 20 images per entry");
      const prepared = prepareImage(file);
      prepared.catch(() => {});
      enqueueUpload(uploadJob(entry, file, prepared, Math.min(19, position++)));
    }
  }
  function submitCapture() {
    const parsed = IM.parseCaptureInput(captureInput.value);
    if (parsed.empty) return hint("Paste a link or an image", true);
    if (parsed.error) return hint(parsed.error, true);
    const why = [parsed.why, $("#capture-why").value.trim()].filter(Boolean).join(" ").slice(0, 4000);
    captureInput.value = "";
    $("#capture-why").value = "";
    $("#capture-why-toggle").open = false;
    captureLink(parsed.url, why);
  }
  async function removeEntry(id) {
    const x = find(id);
    if (!x) return;
    try {
      if (!x.temp) await api(`/inspiration/${id}`, undefined, "DELETE");
      removeLocal(id);
      render();
    } catch (e) {
      toast(e.message);
    }
  }

  // ---- Detail ---------------------------------------------------------------
  async function openDetail(id) {
    let x = find(id);
    if (!x && !state.archivedLoaded) {
      try {
        await loadArchived();
      } catch {}
      x = find(id);
    }
    if (!x) return toast("That entry isn't on your board");
    state.detailId = id;
    history.replaceState(null, "", location.pathname + location.search + "#inspiration/" + encodeURIComponent(id));
    $("#inspiration-save").replaceChildren();
    renderDetail({ media: true, fields: true });
    if (!dialog.open) dialog.showModal();
  }
  function renderDetail({ media = false, fields = true } = {}) {
    const x = find(state.detailId);
    if (!x) return;
    $("#inspiration-detail-title").textContent = IM.cardTitle(x);
    if (media) renderMedia(x);
    const original = $("#inspiration-original"),
      href = TM.safeURL(x.sourceUrl || x.canonicalUrl || "");
    original.hidden = !href;
    if (href) original.href = href;
    original.textContent = x.provider === "instagram" ? "Open on Instagram ↗" : "Open original ↗";
    $("#inspiration-meta").textContent = [x.authorName, x.siteName, shortDate(x.createdAt) && "Added " + shortDate(x.createdAt), x.archivedAt && "Archived"].filter(Boolean).join(" · ");
    const help = $("#inspiration-hint");
    const needsShot = x.metadataStatus === "partial" && ["instagram", "facebook", "tiktok"].includes(x.provider);
    help.hidden = !(needsShot || x.metadataStatus === "failed");
    help.textContent = needsShot ? "Add a screenshot for a preview." : x.metadataStatus === "failed" ? `Preview unavailable${x.metadataError ? " (" + x.metadataError + ")" : ""}. The link still works.` : "";
    if (fields) {
      const f = detailForm.elements;
      const put = (name, value) => {
        if (document.activeElement !== f[name]) f[name].value = value ?? "";
      };
      put("title", x.title);
      put("url", x.sourceUrl);
      put("maker", x.maker);
      put("model", x.model);
      put("why", x.why);
      put("priceSeen", x.priceSeen ?? "");
      put("priceCurrency", x.priceCurrency || "USD");
      put("priceSeenOn", x.priceSeenOn || "");
      f.priceSeenOn.max = new Date(Date.now() + 86400000).toISOString().slice(0, 10);
      f.pinned.checked = !!x.pinned;
      $("#inspiration-meta").append(x.priceAuto && x.priceSeen != null ? el("span", "auto-note", " · price prefilled from the page (auto)") : "");
      renderPriorityChips($("#detail-priority"), x.priority, (p) => queuePatch(x.id, { priority: p }, { immediate: true }));
      renderTagChips(x);
    }
    renderHorn(x);
    renderImageList(x);
    $("#inspiration-archive").textContent = x.archivedAt ? "Restore" : "Archive";
    $("#inspiration-refresh").hidden = !x.canonicalUrl;
  }
  function placeholder(label) {
    const p = el("div", "insp-placeholder no-photo");
    p.append(el("span", "unverified", label));
    return p;
  }
  function renderMedia(x) {
    const box = $("#inspiration-media"),
      thumbs = $("#inspiration-thumbs");
    box.replaceChildren();
    thumbs.replaceChildren();
    box.className = "inspiration-media";
    const images = x.images || [];
    const local = previews.get(x.id);
    const embed = IM.embedFor(x);
    const show = (src, alt) => {
      const img = el("img");
      img.src = src;
      img.alt = alt;
      img.decoding = "async";
      box.replaceChildren(img);
    };
    if (embed) {
      // Click-to-load: no third-party request until the viewer asks for it.
      box.classList.add("aspect-" + embed.aspect.replace(":", "x"));
      const poster = button("embed-poster", "", () => {
        const frame = el("iframe");
        frame.src = embed.src;
        frame.title = IM.cardTitle(x);
        frame.allow = "autoplay; encrypted-media; picture-in-picture; fullscreen";
        frame.allowFullscreen = true;
        // The page sends no-referrer; YouTube's player needs an origin referrer (Error 153).
        frame.referrerPolicy = "strict-origin-when-cross-origin";
        frame.setAttribute("referrerpolicy", "strict-origin-when-cross-origin");
        frame.loading = "eager";
        box.replaceChildren(frame);
      }, embed.kind === "youtube" ? "Play video" : "Load Instagram embed");
      const first = images[0];
      if (local || first) {
        const img = el("img");
        img.src = local || imageURL(first);
        img.alt = "";
        poster.append(img);
      } else poster.classList.add("no-photo");
      poster.append(el("span", "play", embed.kind === "youtube" ? "▶" : "▶ Load embed"));
      box.append(poster);
    } else if (local || images.length) show(local || imageURL(images[0]), IM.cardTitle(x));
    else box.append(placeholder(x.kind === "image" ? "UPLOADING" : "NO PREVIEW YET"));
    if (images.length > 1 || (embed && images.length))
      for (const image of images) {
        const t = button("thumb", "", () => {
          box.className = "inspiration-media";
          show(imageURL(image), IM.cardTitle(x));
        }, image.role === "thumbnail" ? "Show preview image" : "Show image");
        const img = el("img");
        img.src = imageURL(image);
        img.alt = "";
        img.loading = "lazy";
        t.append(img);
        thumbs.append(t);
      }
  }
  function renderTagChips(x) {
    $("#tag-chips").replaceChildren(
      ...(x.tags || []).map((t) => {
        const chip = el("span", "chip selected", t);
        chip.append(button("chip-x", "✕", () => queuePatch(x.id, { tags: x.tags.filter((v) => v !== t) }), `Remove tag ${t}`));
        return chip;
      }),
    );
  }
  function addTag(raw) {
    const x = find(state.detailId);
    if (!x) return;
    const next = IM.normalizeTags([...(x.tags || []), ...String(raw).split(",")]);
    $("#tag-entry").value = "";
    if (!same(next, x.tags)) {
      queuePatch(x.id, { tags: next });
      renderTagChips(x);
    }
  }
  function renderHorn(x) {
    const box = $("#inspiration-horn");
    box.replaceChildren();
    const horns = window.TrumpetApp?.horns() || [];
    if (x.hornId) {
      const h = x.horn || horns.find((v) => v.id === x.hornId) || {};
      box.append(
        el("p", "insp-tracked", `${[h.maker, h.model].filter(Boolean).join(" ")} · ${h.acquired ? "Acquired ✓" : `Tracked in Observatory · ${h.activeOffers || 0} live offer${h.activeOffers === 1 ? "" : "s"}`}`),
        button("quiet", "Unlink", () => queuePatch(x.id, { hornId: null }, { immediate: true })),
      );
      return;
    }
    const suggestions = IM.hornSuggestions(x, horns);
    box.append(el("p", "field-help", suggestions.length ? "Suggested from the Observatory (never linked automatically):" : "Not tracked."));
    for (const h of suggestions)
      box.append(button("chip suggestion", `Link ${h.maker} ${h.model}${h.acquired ? " (acquired)" : ""}`, () => queuePatch(x.id, { hornId: h.id }, { immediate: true })));
    if (horns.length) {
      const select = el("select", "horn-select");
      select.setAttribute("aria-label", "Link another tracked horn");
      select.append(new Option("Link another tracked horn…", ""));
      for (const h of [...horns].sort((a, b) => `${a.maker} ${a.model}`.localeCompare(`${b.maker} ${b.model}`))) select.append(new Option(`${h.maker} ${h.model}`, h.id));
      select.addEventListener("change", () => select.value && queuePatch(x.id, { hornId: select.value }, { immediate: true }));
      box.append(select);
    }
  }
  function renderImageList(x) {
    const list = $("#inspiration-image-list");
    list.replaceChildren();
    const images = x.images || [];
    const uploadsOnly = images.filter((i) => i.role === "upload");
    for (const image of images) {
      const row = el("div", "image-row");
      const img = el("img");
      img.src = imageURL(image);
      img.alt = "";
      img.loading = "lazy";
      row.append(img, el("span", "field-help", image.role === "thumbnail" ? "Preview copy" : `Image ${uploadsOnly.indexOf(image) + 1}`));
      if (image.role === "upload") {
        const i = uploadsOnly.indexOf(image);
        const move = (delta) => {
          const order = uploadsOnly.map((v) => v.id);
          [order[i], order[i + delta]] = [order[i + delta], order[i]];
          queuePatch(x.id, { imageOrder: [...order, ...images.filter((v) => v.role !== "upload").map((v) => v.id)] }, { immediate: true });
          renderImageList(x);
          renderMedia(x);
        };
        const left = button("quiet", "←", () => move(-1), "Move image earlier");
        left.disabled = i === 0;
        const right = button("quiet", "→", () => move(1), "Move image later");
        right.disabled = i === uploadsOnly.length - 1;
        row.append(left, right);
      }
      row.append(
        button("quiet danger-text", "✕", async () => {
          try {
            await api(`/inspiration/${x.id}/images/${image.id}`, undefined, "DELETE");
            x.images = x.images.filter((v) => v.id !== image.id);
            renderImageList(x);
            renderMedia(x);
            render();
          } catch (e) {
            toast(e.message);
          }
        }, "Remove image"),
      );
      list.append(row);
    }
    if (!images.length) list.append(el("p", "field-help", "No images yet. Paste a screenshot while this is open, or add images."));
  }

  // ---- Events -----------------------------------------------------------------
  $("#capture-form").addEventListener("submit", (e) => {
    e.preventDefault();
    submitCapture();
  });
  captureInput.addEventListener("input", () => hint(""));
  $("#capture-camera").addEventListener("click", () => $("#capture-file").click());
  $("#capture-file").addEventListener("change", (e) => {
    const why = $("#capture-why").value.trim();
    for (const f of e.target.files) captureImage(f, why);
    e.target.value = "";
  });
  $("#quick-maker").addEventListener("input", (e) => state.quickId && queuePatch(state.quickId, { maker: e.target.value.trim() }));
  $("#quick-why").addEventListener("input", (e) => state.quickId && queuePatch(state.quickId, { why: e.target.value }));
  $("#quick-strip").addEventListener("input", armQuickTimer);
  $("#quick-close").addEventListener("click", hideQuick);
  $("#priority-filter").replaceChildren(
    ...IM.priorities.map((p) => {
      const b = button("chip p-" + p, IM.priorityLabel(p), () => {
        state.priorities.has(p) ? state.priorities.delete(p) : state.priorities.add(p);
        render();
      });
      b.dataset.priority = p;
      b.setAttribute("aria-pressed", "false");
      return b;
    }),
  );
  for (const [value, label] of IM.sources) filters.elements.source.add(new Option(label, value));
  filters.addEventListener("submit", (e) => e.preventDefault());
  filters.addEventListener("input", async (e) => {
    if (e.target.name === "archived" && e.target.checked && !state.archivedLoaded) {
      try {
        await loadArchived();
      } catch (error) {
        toast(error.message);
      }
    }
    render();
  });
  $("#inspiration-filter-toggle").addEventListener("click", () => {
    const expanded = filters.classList.toggle("is-expanded");
    $("#inspiration-filter-toggle").setAttribute("aria-expanded", String(expanded));
  });
  // Detail form autosave.
  detailForm.addEventListener("submit", (e) => e.preventDefault());
  detailForm.addEventListener("input", (e) => {
    const x = find(state.detailId);
    if (!x) return;
    const { name, value } = e.target;
    if (["title", "maker", "model"].includes(name)) queuePatch(x.id, { [name]: value.trim() });
    else if (name === "why") queuePatch(x.id, { why: value });
    else if (name === "url") {
      const v = value.trim();
      if (!v && x.kind === "image") queuePatch(x.id, { url: null });
      else if (IM.classifyURL(v)) queuePatch(x.id, { url: v });
      else setSaveState(x.id, "Enter a valid link to save it");
    } else if (name === "priceSeen") {
      const n = value === "" ? null : Number(value);
      if (n === null || (Number.isFinite(n) && n >= 0 && n <= 1000000)) queuePatch(x.id, { priceSeen: n });
    } else if (name === "priceCurrency") {
      const v = value.trim().toUpperCase();
      if (/^[A-Z]{3}$/.test(v)) queuePatch(x.id, { priceCurrency: v });
    } else if (name === "priceSeenOn") queuePatch(x.id, { priceSeenOn: value || null });
    else if (name === "pinned") queuePatch(x.id, { pinned: e.target.checked }, { immediate: true });
  });
  $("#tag-entry").addEventListener("keydown", (e) => {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      addTag(e.target.value);
    }
  });
  $("#tag-entry").addEventListener("change", (e) => e.target.value && addTag(e.target.value));
  $("#close-inspiration").addEventListener("click", () => dialog.close());
  dialog.addEventListener("close", () => {
    const id = state.detailId;
    state.detailId = null;
    $("#inspiration-media").replaceChildren(); // stops any playing embed
    if (id && saves.get(id)) flush(id);
    if (location.hash.startsWith("#inspiration/")) history.replaceState(null, "", location.pathname + location.search + "#inspiration");
  });
  $("#inspiration-refresh").addEventListener("click", async (e) => {
    const id = state.detailId;
    e.target.disabled = true;
    setSaveState(id, "Refreshing preview…");
    await enrich(id, true);
    setSaveState(id, "Preview refreshed");
    e.target.disabled = false;
  });
  $("#inspiration-archive").addEventListener("click", () => {
    const x = find(state.detailId);
    if (!x) return;
    const archiving = !x.archivedAt;
    queuePatch(x.id, { archived: archiving }, { immediate: true });
    if (archiving) {
      dialog.close();
      toast("Archived", "Undo", () => queuePatch(x.id, { archived: false }, { immediate: true }));
    } else renderDetail({});
  });
  $("#inspiration-delete").addEventListener("click", async () => {
    const x = find(state.detailId);
    if (!x || !confirm("Delete this entry permanently, including its images? This can't be undone.")) return;
    await removeEntry(x.id);
    toast("Deleted permanently");
  });
  $("#inspiration-add-images").addEventListener("click", () => $("#inspiration-detail-file").click());
  $("#inspiration-detail-file").addEventListener("change", (e) => {
    addImagesToEntry(state.detailId, [...e.target.files]);
    e.target.value = "";
  });

  // Global paste and drop while the board is shown.
  function textField(t) {
    return t?.closest?.("input, textarea, select, [contenteditable='true']");
  }
  document.addEventListener("paste", (e) => {
    if (!state.active || !e.clipboardData) return;
    const files = [...e.clipboardData.items].filter((i) => i.kind === "file" && /^image\//.test(i.type)).map((i) => i.getAsFile()).filter(Boolean);
    if (files.length) {
      e.preventDefault();
      if (dialog.open && state.detailId) addImagesToEntry(state.detailId, files);
      else files.forEach((f) => captureImage(f, $("#capture-why").value.trim()));
      return;
    }
    const field = textField(e.target);
    if (dialog.open || (field && field !== captureInput)) return;
    const text = e.clipboardData.getData("text/plain") || e.clipboardData.getData("text");
    const parsed = IM.parseCaptureInput(text);
    if (field === captureInput) {
      // Auto-submit: a single link pasted into an empty capture bar.
      if (captureInput.value.trim() || !parsed.url) return;
      e.preventDefault();
      captureLink(parsed.url, [parsed.why, $("#capture-why").value.trim()].filter(Boolean).join(" ").slice(0, 4000));
      $("#capture-why").value = "";
      return;
    }
    if (parsed.url) {
      e.preventDefault();
      captureLink(parsed.url, parsed.why);
    } else if (parsed.error) hint(parsed.error, true);
  });
  const overlay = $("#drop-overlay");
  let dragDepth = 0;
  const draggable = (e) => state.active && !dialog.open && [...(e.dataTransfer?.types || [])].some((t) => t === "Files" || t === "text/uri-list");
  document.addEventListener("dragenter", (e) => {
    if (!draggable(e)) return;
    dragDepth++;
    overlay.hidden = false;
  });
  document.addEventListener("dragover", (e) => {
    if (!draggable(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  });
  document.addEventListener("dragleave", (e) => {
    if (!draggable(e)) return;
    dragDepth = Math.max(0, dragDepth - 1);
    if (!dragDepth) overlay.hidden = true;
  });
  document.addEventListener("drop", (e) => {
    if (!draggable(e)) return;
    e.preventDefault();
    dragDepth = 0;
    overlay.hidden = true;
    const files = [...e.dataTransfer.files].filter(isImageFile);
    if (files.length) return files.forEach((f) => captureImage(f, $("#capture-why").value.trim()));
    const uri = (e.dataTransfer.getData("text/uri-list") || "").split(/\r?\n/).find((l) => l && !l.startsWith("#"));
    const parsed = IM.parseCaptureInput(uri || e.dataTransfer.getData("text/plain"));
    if (parsed.url) captureLink(parsed.url, parsed.why);
    else hint("Paste a link or an image", true);
  });
  window.addEventListener("beforeunload", (e) => {
    // Uploads in flight or failed would be lost: nothing is kept locally.
    if (uploadsRunning || uploadQueue.length || uploads.size) {
      e.preventDefault();
      e.returnValue = "";
    }
  });

  window.InspirationBoard = {
    setActive(on) {
      state.active = on;
      if (!on) {
        overlay.hidden = true;
        return;
      }
      ensureLoaded(true).catch(() => {});
      // Desktop only: autofocus would pop the keyboard over the board on phones.
      if (matchMedia("(hover: hover) and (pointer: fine)").matches && !dialog.open && !location.hash.startsWith("#inspiration/")) captureInput.focus({ preventScroll: true });
    },
    route(sub) {
      if (sub.startsWith("/add")) {
        const params = new URLSearchParams(sub.split("?")[1] || "");
        // Drop the shared link from the address bar and history immediately.
        history.replaceState(null, "", location.pathname + location.search + "#inspiration");
        const note = (params.get("note") || "").trim();
        const parsed = IM.parseCaptureInput(params.get("url") || note);
        ensureLoaded(true)
          .then(() => {
            if (!parsed.url) return hint(parsed.error || "Paste a link or an image", true);
            const why = params.get("url") ? note : parsed.why;
            captureLink(parsed.url, why.slice(0, 4000));
          })
          .catch(() => {});
        return;
      }
      const id = decodeURIComponent(sub.replace(/^\//, ""));
      ensureLoaded(true)
        .then(() => (id ? openDetail(id) : dialog.open && dialog.close()))
        .catch(() => {});
    },
    forHorn(hornId) {
      return ensureLoaded(false)
        .then(() => state.items.filter((x) => x.hornId === hornId))
        .catch(() => []);
    },
    thumbLink(x) {
      const a = el("a", "insp-thumb-link");
      a.href = "#inspiration/" + encodeURIComponent(x.id);
      const image = (x.images || [])[0];
      if (image) {
        const img = el("img");
        img.src = imageURL(image);
        img.alt = "";
        img.loading = "lazy";
        a.append(img);
      } else a.append(el("span", "insp-thumb-placeholder no-photo"));
      a.append(el("span", "", IM.cardTitle(x)));
      return a;
    },
  };
  // Fill the nav count without seeding; the board seeds when first opened.
  ensureLoaded(false).catch(() => {});
})();
