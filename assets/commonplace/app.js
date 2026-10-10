/* Commonplace: index, capture, the Moment view, search and import.
 * Framework-free. Talks only to /commonplace/api (behind Google sign-in). */
(() => {
  "use strict";
  const M = globalThis.CommonplaceModel;
  const API = "/commonplace/api/v1/commonplace";
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => [...r.querySelectorAll(s)];
  const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const reduce = matchMedia("(prefers-reduced-motion: reduce)");
  const narrow = matchMedia("(max-width: 980px)");
  const store = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch {} },
  };
  const BROWSER_TZ = (() => { try { return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"; } catch { return "UTC"; } })();

  // ---- API ---------------------------------------------------------------------
  async function api(path, body, method) {
    const m = method || (body !== undefined ? "POST" : "GET");
    const init = { method: m, credentials: "same-origin", cache: "no-store", headers: {} };
    if (body instanceof FormData) {
      init.body = body;
      init.headers["X-Commonplace-Upload"] = "1";
    } else if (body instanceof Blob) {
      init.body = body;
    } else if (m !== "GET") {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body ?? {});
    }
    if (init.extraHeaders) Object.assign(init.headers, init.extraHeaders);
    const response = await fetch(API + path, init);
    let data = null;
    try { data = await response.json(); } catch {}
    if (!response.ok) {
      const err = new Error(response.status === 401 ? "You are signed out. Sign in again to continue." : (data && (data.error || "")) || `The archive answered ${response.status}`);
      err.status = response.status;
      err.body = data;
      throw err;
    }
    return data;
  }
  async function uploadFile(momentId, file) {
    const headers = { "Content-Type": file.type || "application/octet-stream", "X-File-Name": encodeURIComponent(file.name || "file") };
    if (file.type === "image/webp") {
      try {
        const bmp = await createImageBitmap(file);
        headers["X-Image-Width"] = String(bmp.width);
        headers["X-Image-Height"] = String(bmp.height);
      } catch {}
    }
    const response = await fetch(`${API}/moments/${momentId}/artifacts`, { method: "POST", credentials: "same-origin", headers, body: file });
    const data = await response.json().catch(() => null);
    if (!response.ok) throw new Error((data && data.error) || `Upload failed (${response.status})`);
    return data.artifact;
  }
  const mediaURL = (id) => `${API}/media/${encodeURIComponent(id)}`;

  // ---- Icons ---------------------------------------------------------------------
  const svg = (d, extra = "") => `<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" ${extra}>${d}</svg>`;
  const ICON = {
    back: svg('<path d="M10 3 5 8l5 5"/>'),
    search: svg('<circle cx="7" cy="7" r="4.5"/><path d="m10.5 10.5 3 3"/>'),
    space: svg('<circle cx="5.5" cy="6" r="3"/><circle cx="11.5" cy="5" r="1.8"/><circle cx="10" cy="11.3" r="2.4"/>'),
    theme: svg('<circle cx="8" cy="8" r="5.5"/><path d="M8 2.5v11a5.5 5.5 0 0 0 0-11Z" fill="currentColor"/>'),
    import: svg('<path d="M8 2v8M4.5 6.5 8 10l3.5-3.5M3 13.5h10"/>'),
    pen: svg('<path d="M10.5 2.5l3 3L6 13H3v-3z"/><path d="M9 4l3 3"/>'),
    thread: svg('<path d="M1.5 9c2-4 4-4 6.5 0s4.5 4 6.5 0"/><circle cx="5" cy="7" r="1.3" fill="currentColor"/><circle cx="11" cy="10.4" r="1.3" fill="currentColor"/>'),
    check: svg('<path d="m3.5 8.5 3 3 6-7"/>'),
    erase: svg('<path d="M6 13.5h7.5M2.8 9.6l6-6.3a1.4 1.4 0 0 1 2 0l2.3 2.3a1.4 1.4 0 0 1 0 2L8.6 12.3a3 3 0 0 1-4.2 0L2.8 10.7a.8.8 0 0 1 0-1.1Z"/>'),
    plus: svg('<path d="M8 3v10M3 8h10"/>'),
    out: svg('<path d="M6 3h7v7M13 3 4 12"/>', 'width="12" height="12"'),
    arrow: svg('<path d="M3 8h10M9 4l4 4-4 4"/>', 'width="13" height="13"'),
    seal: '<svg viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.2" aria-hidden="true"><circle cx="6" cy="6" r="4.6"/><path d="m4 6.1 1.4 1.4L8.2 4.6" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    box: svg('<rect x="2.5" y="3.5" width="11" height="9" rx="1.5" stroke-dasharray="2 2"/>'),
  };

  // ---- Shared chrome -------------------------------------------------------------
  const stage = $("#stage"), capsule = $("#capsule"), dock = $("#dock");
  let toastTimer = 0, undoFn = null;
  function toast(msg, undo) {
    $("#toastMsg").textContent = msg;
    undoFn = undo || null;
    $("#toastUndo").hidden = !undo;
    $("#toast").classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => $("#toast").classList.remove("show"), undo ? 6000 : 3200);
  }
  $("#toastUndo").addEventListener("click", () => {
    const fn = undoFn;
    undoFn = null;
    $("#toast").classList.remove("show");
    if (fn) fn();
  });

  const THEMES = ["", "dark", "light"];
  function applyTheme(t) {
    if (t) document.documentElement.dataset.theme = t;
    else delete document.documentElement.dataset.theme;
  }
  applyTheme(store.get("commonplace.theme") || "");
  function cycleTheme() {
    const cur = document.documentElement.dataset.theme || "";
    const next = THEMES[(THEMES.indexOf(cur) + 1) % THEMES.length];
    applyTheme(next);
    store.set("commonplace.theme", next);
    toast(next ? `${next[0].toUpperCase() + next.slice(1)} theme` : "Theme follows your device");
  }
  function setLight(phase, special) {
    document.body.dataset.light = phase || "day";
    document.body.dataset.special = special ? "1" : "";
  }

  function avatar(p, extra = "") {
    if (p === "me") return `<span class="av me ${extra}" aria-hidden="true">You</span>`;
    return `<span class="av ${p.special ? "special" : ""} ${extra}" style="--h:${M.hue(p.id)}" aria-hidden="true">${esc(M.initials(p.name))}</span>`;
  }

  function capsuleFor(kind, moment) {
    const right = `<span class="csep"></span>
      <button class="cbtn icon" id="cSearch" type="button" title="Search (/)" aria-label="Search">${ICON.search}</button>
      <a class="cbtn icon" href="#space${moment ? "?focus=" + moment.id : ""}" id="cSpace" title="Idea space" aria-label="Idea space">${ICON.space}</a>
      <button class="cbtn icon" id="cTheme" type="button" title="Switch light and dark" aria-label="Switch light and dark">${ICON.theme}</button>`;
    if (kind === "moment") {
      const lead = moment.people.filter((p) => p.role !== "mentioned")[0];
      const lab = M.wallLabel(moment);
      capsule.innerHTML = `<a class="cbtn" href="#" id="cBack" title="Back to the Commonplace">${ICON.back}<span class="hide-phone">Commonplace</span></a><span class="csep"></span>
        <span class="ctitle">${lead ? avatar(lead) : `<span class="kdot ${moment.kind}"></span>`}<span>${esc(lead ? lead.name : M.KIND_LABEL[moment.kind])}</span><small>${esc(M.formatDate(lab.parts, { short: true, noYear: true }))} · ${esc(lab.hour.hm + " " + lab.hour.ap)}</small></span>${right}`;
    } else {
      capsule.innerHTML = `<a class="brandmark" href="#">Commonplace</a><span class="csep hide-phone"></span>
        <a class="cbtn hide-phone" href="/jazz/">Jazz</a>
        <a class="cbtn" href="#import" title="Import">${ICON.import}<span class="hide-phone">Import</span></a>${right}`;
    }
    $("#cSearch").addEventListener("click", () => openPalette(""));
    $("#cTheme").addEventListener("click", cycleTheme);
  }

  function busy(on) {
    stage.setAttribute("aria-busy", on ? "true" : "false");
  }
  function failure(err) {
    busy(false);
    stage.innerHTML = `<div class="wrap"><div class="empty glass">${esc(err.message || "Something went wrong.")}<div style="margin-top:14px"><a class="pill" href="#">Back to the Commonplace</a></div></div></div>`;
  }

  // ---- Router ----------------------------------------------------------------------
  let current = { name: "" };
  let lastIndexScroll = 0;
  async function router() {
    const r = M.route(location.hash);
    const share = M.parseShareHash(location.hash);
    if (current.name === "index") lastIndexScroll = scrollY;
    if (current.name === "space" && r.name !== "space" && globalThis.CommonplaceSpace) globalThis.CommonplaceSpace.close();
    const prev = current;
    current = r;
    closeSheet();
    $("#space-root").hidden = r.name !== "space";
    for (const el of [$("#sky"), $("#page"), capsule]) el.hidden = r.name === "space";
    dock.hidden = r.name !== "moment";
    if (r.name === "space") {
      dock.hidden = true;
      if (globalThis.CommonplaceSpace) globalThis.CommonplaceSpace.open({ api, focus: r.q.focus || "", root: $("#space-root") });
      return;
    }
    if (r.name === "moment") return showMoment(r.id, r.q, prev);
    setLight("day", false);
    if (r.name === "import") return showImport();
    if (r.name === "add" && share) return showIndex({ share });
    return showIndex({ q: r.q });
  }
  addEventListener("hashchange", router);

  // ---- Index -------------------------------------------------------------------------
  const filters = { kind: "", person: "", source: "", year: "" };
  let listOffset = 0;
  let peopleCache = [];

  async function showIndex(opts = {}) {
    capsuleFor("index");
    busy(true);
    const q = (opts.q && opts.q.q) || "";
    stage.innerHTML = `<div class="wrap">
      <div class="index-head"><div><div class="eyebrow">Private · ${esc(new Date().getFullYear())}</div><h1 class="h-display">Commonplace</h1>
        <p class="lede">Conversations, ideas and dreams, kept exactly as they were said. The margin is yours.</p></div></div>
      <form class="capture glass" id="capture" autocomplete="off">
        <label class="sr" for="capText">Capture</label>
        <textarea id="capText" placeholder="Paste a screenshot, words or a link. Drop files here."></textarea>
        <div class="capture-row"><span class="hint" id="capHint">Nothing else is needed. Who, when and why can come later.</span>
          <div class="capture-actions">
            <label class="pill" style="position:relative;overflow:hidden">${ICON.plus}<span>Files</span><input id="capFiles" type="file" multiple accept="image/*,audio/*" style="position:absolute;inset:0;opacity:0;cursor:pointer" /></label>
            <button class="pill primary" id="capSave" type="submit">Keep it</button>
          </div></div>
        <div class="status" id="capStatus" role="status"></div>
      </form>
      <div class="filters" role="group" aria-label="Filter and search">
        <label class="search-inline glass">${ICON.search}<span class="sr">Search</span><input id="q" type="search" placeholder="Search words, notes, people" value="${esc(q)}" /></label>
        <span id="kindChips"></span>
        <select class="chip" id="fPerson" aria-label="Person"><option value="">Everyone</option></select>
        <select class="chip" id="fSource" aria-label="Where"><option value="">Anywhere</option>${M.SOURCES.map((s) => `<option value="${s}">${M.SOURCE_LABEL[s]}</option>`).join("")}</select>
        <select class="chip" id="fYear" aria-label="Year"><option value="">Any year</option></select>
      </div>
      <section class="otd" id="otd" hidden aria-label="On this day"></section>
      <section id="list" aria-label="Moments"></section>
    </div>`;
    $("#kindChips").innerHTML = ["", ...M.KINDS].map((k) => `<button type="button" class="chip" data-kind="${k}" aria-pressed="${filters.kind === k}">${k ? `<span class="kdot ${k}"></span>${M.KIND_LABEL[k]}` : "All"}</button>`).join(" ");
    $$("#kindChips .chip").forEach((b) => b.addEventListener("click", () => { filters.kind = b.dataset.kind; $$("#kindChips .chip").forEach((c) => c.setAttribute("aria-pressed", String(c === b))); loadList(true); }));
    for (const [id, key] of [["fPerson", "person"], ["fSource", "source"], ["fYear", "year"]]) {
      const el = $("#" + id);
      el.addEventListener("change", () => { filters[key] = el.value; loadList(true); });
    }
    $("#fSource").value = filters.source;
    bindCapture(opts.share);
    const qEl = $("#q");
    let qt = 0;
    qEl.addEventListener("input", () => { clearTimeout(qt); qt = setTimeout(() => (qEl.value.trim() ? runInlineSearch(qEl.value) : loadList(true)), 220); });
    qEl.addEventListener("keydown", (e) => { if (e.key === "Escape") { qEl.value = ""; loadList(true); } });
    api("/people").then((d) => {
      peopleCache = d.people;
      const sel = $("#fPerson");
      if (!sel) return;
      sel.insertAdjacentHTML("beforeend", d.people.map((p) => `<option value="${p.id}">${esc(p.name)} (${p.moments})</option>`).join(""));
      sel.value = filters.person;
    }).catch(() => {});
    loadOnThisDay();
    if (q) await runInlineSearch(q);
    else await loadList(true);
    if (!opts.share && lastIndexScroll && !q) scrollTo(0, lastIndexScroll);
  }

  async function loadOnThisDay() {
    const now = new Date();
    const today = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
    try {
      const d = await api(`/on-this-day?today=${today}`);
      const el = $("#otd");
      if (!el || !d.moments.length) return;
      el.hidden = false;
      el.innerHTML = `<div class="eyebrow">On this day</div><div class="otd-row">${d.moments.map((m) => {
        const lab = M.wallLabel(m);
        return `<a class="otd-card glass" href="#m/${m.id}"><span class="eyebrow">${lab.parts ? lab.parts.year : ""} · ${esc(lab.relative)}</span><b>${esc(m.title || M.excerpt(m.excerpt, 90) || M.KIND_LABEL[m.kind])}</b></a>`;
      }).join("")}</div>`;
    } catch {}
  }

  /** "iMessage · #channel", without repeating the source in its own detail. */
  function whereText(m) {
    const label = M.SOURCE_LABEL[m.source] || "";
    const detail = (m.sourceDetail || "").trim();
    if (!detail) return label;
    return detail.toLowerCase().startsWith(label.toLowerCase()) ? detail : `${label} · ${detail}`;
  }
  function cardHTML(m) {
    const lab = M.wallLabel(m);
    const names = m.people.filter((p) => p.role !== "mentioned").map((p) => p.name);
    const c = m.counts || {};
    const bits = [`<span><span class="kdot ${m.kind}"></span> ${M.KIND_LABEL[m.kind]}</span>`];
    if (names.length) bits.push(`<span>${esc(names.slice(0, 3).join(", "))}</span>`);
    bits.push(`<span>${esc(whereText(m))}</span>`);
    if (c.images && !/screenshot|image/i.test(m.sourceDetail)) bits.push(`<span>${c.images} ${c.images === 1 ? "image" : "images"}</span>`);
    if (c.pencil) bits.push(`<span class="pencil">${c.pencil} in pencil</span>`);
    const special = M.isSpecial(m);
    return `<a class="card ${m.kind === "dream" ? "dream" : "glass"} ${special ? "special" : ""}" href="#m/${m.id}" data-id="${m.id}">
      ${m.coverArtifactId ? `<img class="cover" src="${mediaURL(m.coverArtifactId)}" alt="" loading="lazy" decoding="async" />` : ""}
      <div class="when"><span class="hour">${esc(lab.hour.hm)}<small>${esc(lab.hour.ap)}</small></span><span class="date">${esc(M.formatDate(lab.parts, { short: true }))}<br />${esc(lab.relative)}</span></div>
      <div>${m.title ? `<p class="ttl">${esc(m.title)}</p>` : ""}${m.excerpt ? `<p class="ex">${esc(M.excerpt(m.excerpt, 220))}</p>` : ""}</div>
      <div class="foot">${bits.join("")}</div></a>`;
  }

  async function loadList(reset) {
    const list = $("#list");
    if (!list) return;
    if (reset) listOffset = 0;
    const qs = new URLSearchParams({ limit: "60", offset: String(listOffset) });
    for (const k of Object.keys(filters)) if (filters[k]) qs.set(k, filters[k]);
    try {
      const d = await api(`/moments?${qs}`);
      busy(false);
      const yearSel = $("#fYear");
      if (yearSel && yearSel.options.length === 1) {
        yearSel.insertAdjacentHTML("beforeend", d.facets.years.map((y) => `<option>${y}</option>`).join(""));
        yearSel.value = filters.year;
      }
      if (reset) list.innerHTML = "";
      if (!d.moments.length && reset) {
        list.innerHTML = `<div class="empty glass">${d.facets.total ? "Nothing matches these filters." : "Nothing kept yet. Paste a screenshot or a few words above, or import from Discord or a bundle."}</div>`;
        return;
      }
      let grid = $(".moments", list);
      if (!grid) {
        list.insertAdjacentHTML("beforeend", `<div class="moments"></div>`);
        grid = $(".moments", list);
      }
      grid.insertAdjacentHTML("beforeend", d.moments.map(cardHTML).join(""));
      $(".more", list)?.remove();
      listOffset += d.moments.length;
      if (d.more) {
        list.insertAdjacentHTML("beforeend", `<div class="more"><button class="pill" type="button">More Moments</button></div>`);
        $(".more button", list).addEventListener("click", () => loadList(false));
      }
    } catch (err) {
      busy(false);
      list.innerHTML = `<div class="empty glass">${esc(err.message)}</div>`;
    }
  }

  function snippetHTML(s) {
    return M.splitSnippet(s).map((p) => (p.mark ? `<mark>${esc(p.text)}</mark>` : esc(p.text))).join("");
  }
  const HIT_LABEL = { line: "said", note: "margin", artifact: "original", moment: "label", person: "person" };
  function resultsHTML(data) {
    if (!data.results.length) return `<div class="empty glass">Nothing found for “${esc(data.query)}”.</div>`;
    return `<div class="results">${data.results.map((r) => {
      const m = r.moment, lab = M.wallLabel(m);
      return `<div class="result glass"><a class="rh" href="#m/${m.id}"><span class="kdot ${m.kind}"></span><b>${esc(m.title || M.excerpt(m.excerpt, 80) || M.KIND_LABEL[m.kind])}</b><span>${esc(M.formatDate(lab.parts, { short: true }))} · ${esc(lab.hour.hm + " " + lab.hour.ap)}</span></a>
        ${r.hits.map((h) => `<a class="hit ${h.kind === "note" && h.state === "pencil" ? "note" : ""}" href="#m/${m.id}${h.anchorId ? `?at=${h.kind}:${h.anchorId}` : ""}"><span class="k">${HIT_LABEL[h.kind] || h.kind}</span>${snippetHTML(h.snippet)}</a>`).join("")}</div>`;
    }).join("")}</div>`;
  }
  async function runInlineSearch(q) {
    const list = $("#list");
    try {
      const d = await api(`/search?q=${encodeURIComponent(q)}`);
      busy(false);
      if ($("#q") && $("#q").value.trim() === q.trim()) list.innerHTML = resultsHTML(d);
    } catch (err) {
      list.innerHTML = `<div class="empty glass">${esc(err.message)}</div>`;
    }
  }

  // ---- Capture -------------------------------------------------------------------------
  function bindCapture(share) {
    const form = $("#capture"), text = $("#capText"), status = $("#capStatus");
    let files = [];
    const setFiles = (list) => {
      files = [...files, ...[...list].filter((f) => /^(image|audio)\//.test(f.type) || /\.(png|jpe?g|webp|gif|mp3|m4a|aac|wav|ogg|webm)$/i.test(f.name))];
      $("#capHint").textContent = files.length ? `${files.length} ${files.length === 1 ? "file" : "files"} ready: ${files.map((f) => f.name).join(", ")}` : "Nothing else is needed. Who, when and why can come later.";
    };
    $("#capFiles").addEventListener("change", (e) => { setFiles(e.target.files); e.target.value = ""; if (files.length && !text.value.trim()) capture(); });
    text.addEventListener("paste", (e) => {
      const pasted = [...(e.clipboardData?.files || [])];
      if (pasted.length) {
        e.preventDefault();
        setFiles(pasted);
        if (!text.value.trim()) capture(); // a pasted screenshot is kept instantly
      }
    });
    for (const ev of ["dragenter", "dragover"]) form.addEventListener(ev, (e) => { e.preventDefault(); form.classList.add("drag"); });
    for (const ev of ["dragleave", "drop"]) form.addEventListener(ev, () => form.classList.remove("drag"));
    form.addEventListener("drop", (e) => {
      e.preventDefault();
      if (e.dataTransfer.files.length) { setFiles(e.dataTransfer.files); capture(); return; }
      const dropped = e.dataTransfer.getData("text/uri-list") || e.dataTransfer.getData("text/plain");
      if (dropped) text.value = (text.value + " " + dropped).trim();
    });
    form.addEventListener("submit", (e) => { e.preventDefault(); capture(); });
    text.addEventListener("keydown", (e) => { if ((e.metaKey || e.ctrlKey) && e.key === "Enter") capture(); });
    let saving = false;
    async function capture(extra = {}) {
      if (saving) return;
      const parsed = M.parseCapture(text.value);
      if (!parsed.url && !parsed.text && !files.length) { status.textContent = "Paste or drop something first."; return; }
      saving = true;
      $("#capSave").disabled = true;
      status.className = "status";
      status.textContent = "Keeping it…";
      try {
        const body = { clientCaptureId: extra.captureId || M.newId(), text: parsed.text, url: parsed.url, timezone: BROWSER_TZ, ...extra.fields };
        if (!parsed.url && !parsed.text && files.some((f) => f.type.startsWith("audio/"))) body.source = "voice";
        const created = await api("/moments", body);
        const id = created.moment.id;
        for (let i = 0; i < files.length; i++) {
          status.textContent = `Keeping file ${i + 1} of ${files.length}…`;
          await uploadFile(id, files[i]);
        }
        files = [];
        text.value = "";
        location.hash = `#m/${id}?details=1`;
      } catch (err) {
        status.className = "status err";
        status.textContent = err.message;
      } finally {
        saving = false;
        const b = $("#capSave");
        if (b) b.disabled = false;
      }
    }
    if (share && (share.url || share.text)) {
      text.value = [share.text, share.url].filter(Boolean).join(" ");
      capture({ fields: share.title ? { linkTitle: share.title } : {} });
    } else if (!narrow.matches) text.focus({ preventScroll: true });
  }

  // ---- Moment view ------------------------------------------------------------------------
  const S = { full: null, view: "", margin: false, marking: false, threadsByKnot: null };
  const VIEW_LABEL = { shots: "Screenshots", text: "Text", link: "Link", audio: "Audio" };
  function viewsOf(full) {
    const v = [];
    const kinds = new Set(full.artifacts.map((a) => a.kind));
    if (kinds.has("image")) v.push("shots");
    if (full.lines.length || kinds.has("text")) v.push("text");
    if (kinds.has("link")) v.push("link");
    if (kinds.has("audio")) v.push("audio");
    return v.length ? v : ["text"];
  }

  async function showMoment(id, q, prev) {
    busy(true);
    let full;
    try {
      full = await api(`/moments/${encodeURIComponent(id)}`);
    } catch (err) {
      capsuleFor("index");
      return failure(err);
    }
    if (current.name !== "moment" || current.id !== id) return;
    S.full = full;
    const views = viewsOf(full);
    S.view = views.includes(q.view) ? q.view : prev && prev.name === "moment" && prev.id === id && views.includes(S.view) ? S.view : views[0];
    S.margin = q.margin === "1" || (prev && prev.name === "moment" && prev.id === id && S.margin);
    S.marking = false;
    renderMoment();
    busy(false);
    if (prev && prev.name !== "moment" || !prev || prev.id !== id) scrollTo(0, 0);
    if (q.at) setTimeout(() => reveal(q.at), 60);
    if (q.details === "1") {
      history.replaceState(null, "", `#m/${id}`);
      current.q = {};
      openDetails();
    }
  }

  function renderMoment() {
    const full = S.full, m = full.moment;
    const lab = M.wallLabel(m, full.lines);
    setLight(lab.phase, M.isSpecial(m));
    capsuleFor("moment", m);
    const views = viewsOf(full);
    const sender = m.people.filter((p) => p.role !== "mentioned");
    const mentioned = m.people.filter((p) => p.role === "mentioned");
    const shots = full.artifacts.filter((a) => a.kind === "image");
    const detail = (m.sourceDetail || "").toLowerCase().startsWith(M.SOURCE_LABEL[m.source].toLowerCase()) ? m.sourceDetail.slice(M.SOURCE_LABEL[m.source].length).replace(/^\s*·\s*/, "") : m.sourceDetail;
    const whereSmall = [detail, shots.length && !/screenshot/i.test(detail) ? `${shots.length} ${shots.length === 1 ? "screenshot" : "screenshots"}` : ""].filter(Boolean).join(" · ");
    stage.innerHTML = `<article class="moment" aria-label="${esc(M.KIND_LABEL[m.kind])}, ${esc(lab.date)}, ${esc(lab.hour.hm + " " + lab.hour.ap)}">
      <div class="grid ${S.margin ? "margin-open" : ""}" id="grid">
        <aside class="label" aria-label="Provenance">
          <div class="kind"><span class="kdot ${m.kind}"></span>${esc(M.KIND_LABEL[m.kind])}</div>
          <div class="hourrow"><div class="hour">${esc(lab.hour.hm)}<span class="ap">${esc(lab.hour.ap)}</span></div>${M.dialSVG(lab.parts && lab.parts.hourFloat)}</div>
          <div class="hournote">${esc(lab.note)}${lab.captured ? " This is when it was kept; set when it happened in Details." : ""}</div>
          ${m.title ? `<div style="font:400 20px/1.3 var(--serif)">${esc(m.title)}</div>` : ""}
          <dl class="prov">
            <div><dt>With</dt><dd>${sender.length ? sender.map((p) => `<span class="person">${avatar(p)}${esc(p.name)}</span>`).join(" ") + ' <span style="color:var(--ink-3)">and you</span>' : "Just you"}${mentioned.length ? `<small>mentions ${esc(mentioned.map((p) => p.name).join(", "))}</small>` : ""}</dd></div>
            <div><dt>Where</dt><dd>${esc(M.SOURCE_LABEL[m.source])}${whereSmall ? `<small>${esc(whereSmall)}</small>` : ""}</dd></div>
            <div><dt>When</dt><dd>${esc(lab.date)}<small>${esc(lab.relative)}${lab.span ? ", " + esc(lab.span) : ""}${lab.tz !== BROWSER_TZ ? ` · ${esc(lab.tz.replace(/_/g, " "))}` : ""}</small></dd></div>
            ${lab.after ? `<div><dt>After</dt><dd>${esc(lab.after)}</dd></div>` : ""}
            ${m.why ? `<div><dt>Why</dt><dd style="font-family:var(--serif)">${esc(m.why)}</dd></div>` : ""}
          </dl>
          <div class="seal">${ICON.seal}Originals · never altered</div>
          <button class="nbtn edit" type="button" id="details">${ICON.pen}Details</button>
        </aside>
        <div class="reading">
          <div class="orig-tag"><span id="tagL"></span><span id="tagR"></span></div>
          <div class="artifact" id="artifact"></div>
        </div>
        <div class="margin" id="margin" aria-label="Margin notes"><div class="mhead"><span>Margin</span><button id="mtoggle" type="button">${S.margin ? "Fold" : "Unfold"}</button></div>
          <div class="margin-tools" id="mtools"></div><div class="notes" id="notes"></div></div>
      </div>
      ${full.threads.map(threadHTML).join("")}
      ${doorsHTML(full.doorways)}
    </article>`;
    $("#details").addEventListener("click", openDetails);
    $("#mtoggle").addEventListener("click", () => setMargin(!S.margin));
    renderDock(views);
    renderView();
    bindThreads();
  }

  function renderDock(views) {
    const full = S.full;
    dock.innerHTML = `${views.length > 1 ? `<div class="seg" role="group" aria-label="View">${views.map((v, i) => `<button type="button" data-view="${v}" aria-pressed="${S.view === v}" title="${VIEW_LABEL[v]} (${i + 1})">${VIEW_LABEL[v]}</button>`).join("")}</div>` : ""}
      <button class="dbtn" id="marginBtn" type="button" aria-pressed="${S.margin}" title="Margin (M)">${ICON.pen}<span class="lbl">Margin</span><span class="count" id="mcount">${M.pencilCount(full.annotations) || M.visibleNotes(full.annotations).length}</span></button>
      ${full.threads.length ? `<button class="dbtn" id="threadBtn" type="button" title="Thread">${ICON.thread}<span class="lbl">Thread</span></button>` : ""}
      <a class="dbtn" href="#space?focus=${full.moment.id}" title="Idea space">${ICON.space}<span class="lbl">Idea space</span></a>`;
    $$("[data-view]", dock).forEach((b) => b.addEventListener("click", () => setView(b.dataset.view)));
    $("#marginBtn").addEventListener("click", () => setMargin(!S.margin));
    $("#threadBtn")?.addEventListener("click", () => $(".thread")?.scrollIntoView({ behavior: reduce.matches ? "auto" : "smooth", block: "center" }));
  }
  function updateCount() {
    const c = $("#mcount");
    if (c) c.textContent = M.pencilCount(S.full.annotations) || M.visibleNotes(S.full.annotations).length;
  }

  // Views of the original.
  function lineName(l) {
    return l.speaker === "me" ? "You" : l.speakerName || "—";
  }
  function shotsHTML() {
    const full = S.full;
    const shots = full.artifacts.filter((a) => a.kind === "image");
    return `<div class="strip">${shots.map((a, i) => {
      const spots = full.lines.filter((l) => l.artifactId === a.id && l.rect).map((l) => `<span class="spot anc" data-a="line:${l.id}" style="${rectStyle(l.rect)}"></span>`).join("") +
        full.annotations.filter((n) => n.artifactId === a.id && n.rect && n.state !== "erased").map((n) => `<span class="spot" data-a="note:${n.id}" style="${rectStyle(n.rect)}"></span>`).join("");
      const first = full.lines.find((l) => l.artifactId === a.id && l.at);
      const when = first ? M.hourText(M.localParts(first.at, full.moment.timezone)) : "";
      const ratio = a.width && a.height ? `aspect-ratio:${a.width}/${a.height}` : "";
      return `<figure class="frame" data-a="art:${a.id}"><figcaption class="frame-cap"><span>${i + 1} / ${shots.length}${when ? " · " + esc(when) : ""}</span><span>${esc(a.caption || a.originalName || "")}</span></figcaption>
        <div class="shot" data-art="${a.id}" style="${ratio}">${a.stored ? `<img src="${mediaURL(a.id)}" alt="${esc(a.alt || `Screenshot ${i + 1}`)}" ${a.width ? `width="${a.width}" height="${a.height}"` : ""} loading="${i < 2 ? "eager" : "lazy"}" decoding="async" draggable="false" />` : `<div class="missing">Image not uploaded yet<br />re-import with the file to fill it in</div>`}${spots}</div></figure>`;
    }).join("")}</div>`;
  }
  const rectStyle = (r) => `left:${r[0]}%;top:${r[1]}%;width:${r[2]}%;height:${r[3]}%`;

  function textHTML() {
    const full = S.full, tz = full.moment.timezone;
    const byKey = new Map(full.lines.map((l) => [l.key, l]));
    let html = "", prev = null, lastDay = "";
    for (const l of full.lines) {
      const p = l.at ? M.localParts(l.at, tz) : null;
      const dayKey = l.dayLabel || (p ? M.formatDate(p, { short: true }) : "");
      if (prev && prev.at && l.at) {
        const gap = new Date(l.at) - new Date(prev.at);
        if (gap > 3 * 3600000) html += `<div class="silence"><i></i><span>${esc(M.duration(gap))} of quiet</span></div>`;
      }
      if (dayKey && dayKey !== lastDay) {
        html += `<div class="day">${esc(dayKey)}${l.timeLabel ? " · " + esc(l.timeLabel) : p ? " · " + esc(M.hourText(p)) : ""}</div>`;
        lastDay = dayKey;
      } else if (l.timeLabel) html += `<div class="day">${esc(l.timeLabel)}</div>`;
      const meta = M.metaBits(JSON.parse(JSON.stringify(l.meta || {})));
      const reply = meta.replyTo ? byKey.get(meta.replyTo) : null;
      const newSpeaker = !prev || prev.speaker !== l.speaker || prev.speakerName !== l.speakerName;
      html += `<div class="tx-line ${l.speaker === "me" ? "me" : ""} ${newSpeaker ? "newspeaker" : ""}" data-line="${l.id}">
        <div class="tx-who">${newSpeaker ? esc(lineName(l)) : ""}</div>
        <div class="tx-body">
          ${reply || meta.replyTo ? `<div class="replyto">${reply ? `${esc(lineName(reply))}: “${esc(M.excerpt(reply.text, 60))}”` : `reply to ${esc(meta.replyToName || "a message")}`}</div>` : ""}
          <p class="anc" data-a="line:${l.id}">${esc(l.text) || "<em style=\"color:var(--ink-3)\">(no words)</em>"}</p>
          ${meta.bits.length || meta.reactions.length || (p && !l.timeLabel) ? `<div class="tx-meta">${p ? `<span>${esc(M.hourText(p))}</span>` : ""}${meta.bits.map((b) => `<span>${esc(b)}</span>`).join("")}${meta.reactions.map((r) => `<span class="re">${esc(r)}</span>`).join("")}</div>` : ""}
        </div>
        <button class="addnote" type="button" data-addline="${l.id}" title="Write in the margin here" aria-label="Write a note on this line">${ICON.plus}</button>
      </div>`;
      prev = l;
    }
    const texts = full.artifacts.filter((a) => a.kind === "text");
    html += texts.map((a) => `<pre class="verbatim anc" data-a="art:${a.id}">${esc(a.text)}</pre>`).join("");
    return `<div class="tx">${html || '<p class="view-empty">No transcript yet. The original is in the other views.</p>'}</div>`;
  }

  function linkHTML() {
    const full = S.full, tz = full.moment.timezone;
    return `<div class="lk">${full.artifacts.filter((a) => a.kind === "link").map((a) => {
      let site = a.siteName;
      try { site = site || new URL(a.url).hostname; } catch {}
      const cap = a.capturedAt ? M.localParts(a.capturedAt, tz) : null;
      return `<article class="lk-page" data-a="art:${a.id}"><div class="site">${esc(site)}</div>${a.title ? `<h3>${esc(a.title)}</h3>` : ""}
        <a href="${esc(a.url)}" target="_blank" rel="noopener noreferrer">${esc(a.url)}</a>
        ${a.capturedText ? `<p class="cap">${esc(a.capturedText)}</p>` : ""}
        <div class="seal">${ICON.seal}${cap ? `Captured ${esc(M.formatDate(cap, { short: true }))} · ` : ""}its claims are its own</div></article>`;
    }).join("")}</div>`;
  }
  function audioHTML() {
    const full = S.full;
    return `<div class="au">${full.artifacts.filter((a) => a.kind === "audio").map((a) => `<div class="au-card glass" data-a="art:${a.id}">
      <div class="eyebrow">${esc(a.caption || a.originalName || "Recording")}${a.durationMs ? " · " + esc(M.duration(a.durationMs)) : ""}</div>
      ${a.stored ? `<audio controls preload="none" src="${mediaURL(a.id)}"></audio>` : `<div class="view-empty">Recording not uploaded yet.</div>`}</div>`).join("")}</div>`;
  }
  const VIEW_HTML = { shots: shotsHTML, text: textHTML, link: linkHTML, audio: audioHTML };
  const VIEW_TAG = {
    shots: ["Original pixels · never altered", (f) => `${f.artifacts.filter((a) => a.kind === "image").length} screens`],
    text: ["Reading copy · original kept", (f) => `${f.lines.length} lines`],
    link: ["Shared page · as captured", () => "its claims are its own"],
    audio: ["The recording · as received", () => ""],
  };

  function renderView() {
    $("#artifact").innerHTML = VIEW_HTML[S.view]();
    $("#tagL").textContent = VIEW_TAG[S.view][0];
    $("#tagR").textContent = VIEW_TAG[S.view][1](S.full);
    $$("[data-addline]").forEach((b) => b.addEventListener("click", () => compose({ lineId: b.dataset.addline })));
    renderTools();
    renderNotes();
    $$(".shot img").forEach((img) => img.addEventListener("load", layoutNotes, { once: true }));
    bindMarking();
  }

  function setView(v) {
    if (v === S.view) return;
    const keep = placeMarker();
    S.view = v;
    $$("[data-view]", dock).forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.view === v)));
    renderView();
    history.replaceState(null, "", `#m/${S.full.moment.id}${v !== viewsOf(S.full)[0] ? "?view=" + v : ""}`);
    if (keep) {
      const target = anchorEl(keep);
      if (target) target.scrollIntoView({ block: "center", behavior: "auto" });
    }
  }
  // The passage at the top of the screen, so a view switch keeps your place.
  function placeMarker() {
    const els = $$('#artifact [data-a^="line:"], #artifact [data-a^="art:"]');
    const top = narrow.matches ? 90 : 110;
    const el = els.find((e) => e.getBoundingClientRect().bottom > top);
    return el ? el.dataset.a : "";
  }
  function anchorEl(a) {
    let el = $(`#artifact [data-a="${a}"]`);
    if (!el && a.startsWith("line:")) {
      const l = S.full.lines.find((x) => x.id === a.slice(5));
      if (l && l.artifactId) el = $(`#artifact [data-a="art:${l.artifactId}"]`);
    }
    return el;
  }

  function reveal(at) {
    const [kind, id] = at.split(":");
    if (kind === "note") {
      const n = S.full.annotations.find((x) => x.id === id);
      if (!n) return;
      setMargin(true);
      const el = $(`.note[data-n="${id}"]`);
      const anchor = noteAnchor(n);
      (anchor || el)?.scrollIntoView({ block: "center", behavior: reduce.matches ? "auto" : "smooth" });
      flash(anchor);
      if (el) { el.classList.add("open"); flash(el); }
      return;
    }
    if (kind === "line") {
      if (!anchorEl("line:" + id) || S.view === "shots") {
        if (viewsOf(S.full).includes("text")) setView("text");
      }
    }
    if (kind === "artifact") {
      const a = S.full.artifacts.find((x) => x.id === id);
      const v = a && { image: "shots", text: "text", link: "link", audio: "audio" }[a.kind];
      if (v && v !== S.view) setView(v);
    }
    const el = anchorEl((kind === "artifact" ? "art" : kind) + ":" + id);
    if (el) {
      el.scrollIntoView({ block: "center", behavior: reduce.matches ? "auto" : "smooth" });
      flash(el);
    }
  }
  function flash(el) {
    if (!el) return;
    el.classList.add("hot");
    setTimeout(() => el.classList.remove("hot"), 1800);
  }

  // ---- The margin --------------------------------------------------------------------------
  function noteAnchor(n) {
    if (n.rect && n.artifactId) return $(`#artifact [data-a="note:${n.id}"]`) || (S.view !== "shots" ? null : null);
    if (n.lineId) return $(`#artifact [data-a="line:${n.lineId}"]`);
    if (n.artifactId) return $(`#artifact [data-a="art:${n.artifactId}"]`);
    return null;
  }
  const TYPE_LABEL = (t) => (t === "note" ? "Note" : t.replace(/-/g, " ").replace(/^./, (c) => c.toUpperCase()));
  function noteHTML(n) {
    const pencil = n.state === "pencil";
    let go = "";
    if (n.linkMomentId) go = `<a class="note-go" href="#m/${n.linkMomentId}">${esc(n.linkLabel || "Open that Moment")} ${ICON.arrow}</a>`;
    else if (n.linkUrl) go = `<a class="note-go" href="${esc(n.linkUrl)}" target="_blank" rel="noopener noreferrer">${esc(n.linkLabel || n.linkUrl)} ${ICON.out}</a>`;
    const who = n.author === "owner" ? "yours" : pencil ? "suggested" : "kept";
    return `<div class="note ${pencil ? "pencil" : "ink"} ${S.margin ? "open" : ""}" data-n="${n.id}">
      <button class="note-tick" type="button" aria-expanded="${S.margin}"><i></i><span>${esc(TYPE_LABEL(n.type))} · ${who}</span></button>
      <div class="note-body">
        ${n.title ? `<p class="note-t">${esc(n.title)}</p>` : ""}
        ${n.body ? `<p class="note-s">${esc(n.body)}</p>` : ""}
        ${go}
        <div class="note-act">
          ${pencil ? `<button class="nbtn keep" type="button" data-act="keep">${ICON.check}Keep</button>` : ""}
          <button class="nbtn" type="button" data-act="edit">${ICON.pen}Edit</button>
          <button class="nbtn" type="button" data-act="erase">${ICON.erase}Erase</button>
        </div>
      </div></div>`;
  }
  function renderNotes() {
    const box = $("#notes");
    if (!box) return;
    $$(".note").forEach((e) => e.remove());
    $$("#artifact .noted").forEach((e) => e.classList.remove("noted", "inked"));
    const notes = M.visibleNotes(S.full.annotations);
    for (const n of notes) {
      box.insertAdjacentHTML("beforeend", noteHTML(n));
      const a = noteAnchor(n);
      if (a) {
        a.classList.add("noted");
        if (n.state === "ink") a.classList.add("inked");
      }
    }
    $$(".note").forEach(bindNote);
    updateCount();
    layoutNotes();
  }
  function bindNote(el) {
    const n = () => S.full.annotations.find((x) => x.id === el.dataset.n);
    el.querySelector(".note-tick").addEventListener("click", () => {
      el.classList.toggle("open");
      el.querySelector(".note-tick").setAttribute("aria-expanded", String(el.classList.contains("open")));
      layoutNotes();
    });
    el.addEventListener("mouseenter", () => noteAnchor(n())?.classList.add("hot"));
    el.addEventListener("mouseleave", () => noteAnchor(n())?.classList.remove("hot"));
    el.addEventListener("focusin", () => noteAnchor(n())?.classList.add("hot"));
    el.addEventListener("focusout", () => noteAnchor(n())?.classList.remove("hot"));
    el.querySelector('[data-act="keep"]')?.addEventListener("click", () => keepNote(el, n()));
    el.querySelector('[data-act="erase"]').addEventListener("click", () => eraseNote(el, n()));
    el.querySelector('[data-act="edit"]').addEventListener("click", () => editNote(el, n()));
  }
  async function patchNote(n, patch) {
    try {
      const d = await api(`/annotations/${n.id}`, { expectedRevision: n.revision, ...patch }, "PATCH");
      Object.assign(n, d.annotation);
      return true;
    } catch (err) {
      if (err.status === 409 && err.body && err.body.annotation) Object.assign(n, err.body.annotation);
      toast(err.status === 409 ? "That note changed elsewhere; showing the latest." : err.message);
      renderNotes();
      return false;
    }
  }
  async function keepNote(el, n) {
    if (!(await patchNote(n, { state: "ink" }))) return;
    el.classList.remove("pencil");
    el.classList.add("ink", "inking");
    el.querySelector(".keep")?.remove();
    el.querySelector(".note-tick span").textContent = `${TYPE_LABEL(n.type)} · kept`;
    noteAnchor(n)?.classList.add("inked");
    updateCount();
    toast("Kept in ink", async () => { if (await patchNote(n, { state: "pencil" })) renderNotes(); });
  }
  async function eraseNote(el, n) {
    const before = n.state;
    el.classList.add("erasing");
    const ok = await patchNote(n, { state: "erased" });
    if (!ok) return;
    setTimeout(renderNotes, reduce.matches ? 0 : 500);
    toast("Erased from the margin", async () => { if (await patchNote(n, { state: before })) renderNotes(); });
  }
  function editNote(el, n) {
    el.classList.add("open", "editing");
    let t = el.querySelector(".note-t"), s = el.querySelector(".note-s");
    if (!t) { el.querySelector(".note-body").insertAdjacentHTML("afterbegin", '<p class="note-t"></p>'); t = el.querySelector(".note-t"); }
    if (!s) { t.insertAdjacentHTML("afterend", '<p class="note-s"></p>'); s = el.querySelector(".note-s"); }
    t.contentEditable = "true";
    s.contentEditable = "true";
    t.setAttribute("aria-label", "Note title");
    s.setAttribute("aria-label", "Note text");
    t.focus();
    const acts = el.querySelector(".note-act");
    const old = acts.innerHTML;
    acts.innerHTML = `<button class="nbtn keep" type="button" data-act="save">${ICON.check}Save in ink</button><button class="nbtn" type="button" data-act="cancel">Cancel</button>`;
    const prior = { title: n.title, body: n.body, state: n.state };
    acts.querySelector('[data-act="cancel"]').addEventListener("click", () => renderNotes());
    acts.querySelector('[data-act="save"]').addEventListener("click", async () => {
      const title = t.innerText.trim(), body = s.innerText.trim();
      if (!title && !body) { toast("A note needs words. Use Erase to remove it."); return; }
      if (await patchNote(n, { title, body })) {
        renderNotes();
        toast("Saved in your words", async () => { if (await patchNote(n, prior)) renderNotes(); });
      }
    });
    void old;
    layoutNotes();
  }
  function layoutNotes() {
    const grid = $("#grid");
    if (!grid) return;
    const notes = $$(".note");
    if (narrow.matches) {
      // Phone: notes unfold inline under what they annotate (unanchored ones lead).
      const lastAfter = new Map();
      const head = $("#artifact");
      for (const el of notes) {
        const n = S.full.annotations.find((x) => x.id === el.dataset.n);
        const a = noteAnchor(n);
        const host = a ? a.closest(".frame, .tx-line, .lk-page, .au-card, .verbatim") || a : null;
        if (!host) { head.insertAdjacentElement("beforebegin", el); continue; }
        const after = lastAfter.get(host) || host;
        after.insertAdjacentElement("afterend", el);
        lastAfter.set(host, el);
        el.style.top = "";
      }
      return;
    }
    const box = $("#notes");
    notes.forEach((el) => box.appendChild(el));
    const base = box.getBoundingClientRect().top;
    const items = notes.map((el, i) => {
      const n = S.full.annotations.find((x) => x.id === el.dataset.n);
      const a = noteAnchor(n);
      const r = a ? a.getBoundingClientRect() : null;
      return { anchorTop: r ? r.top - base + Math.min(6, r.height / 3) : i * 2, height: el.offsetHeight + (el.classList.contains("open") ? 24 : 0) };
    });
    const tops = M.layoutNotes(items, 10);
    notes.forEach((el, i) => (el.style.top = Math.max(0, tops[i]) + "px"));
    const bottom = notes.reduce((mx, el, i) => Math.max(mx, tops[i] + el.offsetHeight), 0);
    box.style.minHeight = bottom + 40 + "px";
  }
  function setMargin(on) {
    S.margin = on;
    $("#grid")?.classList.toggle("margin-open", on);
    $$(".note").forEach((n) => { n.classList.toggle("open", on); n.querySelector(".note-tick").setAttribute("aria-expanded", String(on)); });
    $("#marginBtn")?.setAttribute("aria-pressed", String(on));
    const t = $("#mtoggle");
    if (t) t.textContent = on ? "Fold" : "Unfold";
    if (narrow.matches) renderTools();
    layoutNotes();
  }
  function renderTools() {
    const tools = $("#mtools");
    if (!tools) return;
    tools.innerHTML = `<button class="nbtn" type="button" id="addNote">${ICON.plus}Add a note</button>${S.view === "shots" ? `<button class="nbtn" type="button" id="markBox" aria-pressed="${S.marking}">${ICON.box}${S.marking ? "Drag on a screenshot…" : "Mark a box"}</button>` : ""}`;
    $("#addNote").addEventListener("click", () => compose({}));
    $("#markBox")?.addEventListener("click", () => { S.marking = !S.marking; renderTools(); bindMarking(); if (S.marking) toast("Drag a box over the screenshot to anchor a note"); });
    // On phones the margin column is hidden: put the tools above the original.
    if (narrow.matches) {
      if (S.margin) $("#artifact").insertAdjacentElement("beforebegin", tools);
      else $("#margin").insertAdjacentElement("beforeend", tools);
    }
  }
  function bindMarking() {
    $$(".shot").forEach((shot) => {
      shot.classList.toggle("marking", S.marking);
      if (shot._bound) return;
      shot._bound = true;
      let start = null, box = null;
      const pct = (e) => {
        const r = shot.getBoundingClientRect();
        return [Math.min(100, Math.max(0, ((e.clientX - r.left) / r.width) * 100)), Math.min(100, Math.max(0, ((e.clientY - r.top) / r.height) * 100))];
      };
      shot.addEventListener("pointerdown", (e) => {
        if (!S.marking) return;
        e.preventDefault();
        shot.setPointerCapture(e.pointerId);
        start = pct(e);
        box = document.createElement("div");
        box.className = "drawbox";
        shot.appendChild(box);
      });
      shot.addEventListener("pointermove", (e) => {
        if (!start) return;
        const p = pct(e);
        const r = [Math.min(start[0], p[0]), Math.min(start[1], p[1]), Math.abs(p[0] - start[0]), Math.abs(p[1] - start[1])];
        box.style.cssText = rectStyle(r);
        box._r = r;
      });
      shot.addEventListener("pointerup", () => {
        if (!start) return;
        const r = box && box._r;
        start = null;
        box?.remove();
        if (!r || r[2] < 1.5 || r[3] < 0.8) return;
        S.marking = false;
        renderTools();
        bindMarking();
        compose({ artifactId: shot.dataset.art, rect: r.map((v) => Math.round(v * 100) / 100) });
      });
    });
  }

  // ---- Sheets: compose, details, palette ------------------------------------------------------
  let sheetEl = null, scrimEl = null, sheetReturn = null;
  function openSheet(html, label) {
    closeSheet();
    sheetReturn = document.activeElement;
    scrimEl = document.createElement("div");
    scrimEl.className = "scrim";
    scrimEl.addEventListener("click", closeSheet);
    sheetEl = document.createElement("div");
    sheetEl.className = "sheet glass";
    sheetEl.setAttribute("role", "dialog");
    sheetEl.setAttribute("aria-modal", "true");
    sheetEl.setAttribute("aria-label", label);
    sheetEl.innerHTML = html;
    document.body.append(scrimEl, sheetEl);
    return sheetEl;
  }
  function closeSheet() {
    scrimEl?.remove();
    sheetEl?.remove();
    const back = sheetReturn;
    scrimEl = sheetEl = sheetReturn = null;
    if (back && back.isConnected) back.focus?.({ preventScroll: true });
  }

  function compose(anchor) {
    const full = S.full;
    let where = "Anywhere in this Moment";
    if (anchor.lineId) {
      const l = full.lines.find((x) => x.id === anchor.lineId);
      where = `On ${lineName(l)}: “${M.excerpt(l.text, 70)}”`;
    } else if (anchor.rect) where = "On the box you drew";
    const el = openSheet(`<h2>Write in the margin</h2><div class="eyebrow" style="margin-bottom:12px">${esc(where)} · in ink</div>
      <form id="composeForm" style="display:grid;gap:12px">
        <label class="field"><span>Note</span><textarea class="input" id="cTitle" required placeholder="Your words"></textarea></label>
        <label class="field"><span>More (optional)</span><textarea class="input" id="cBody" style="min-height:60px"></textarea></label>
        <div class="grid2">
          <label class="field"><span>Kind of note</span><select class="input" id="cType">${["note", "idea", "person", "quote", "link", "care"].map((t) => `<option value="${t}">${TYPE_LABEL(t)}</option>`).join("")}</select></label>
          <label class="field"><span>Link (optional)</span><input class="input" id="cLink" type="url" placeholder="https://" /></label>
        </div>
        <div class="status" id="cStatus"></div>
        <div class="actions"><button class="pill" type="button" id="cCancel">Cancel</button><button class="pill primary" type="submit">Write it in ink</button></div>
      </form>`, "Write in the margin");
    $("#cTitle", el).focus();
    $("#cCancel", el).addEventListener("click", closeSheet);
    $("#composeForm", el).addEventListener("submit", async (e) => {
      e.preventDefault();
      const body = { title: $("#cTitle", el).value.trim(), body: $("#cBody", el).value.trim(), type: $("#cType", el).value, ...anchor };
      const link = $("#cLink", el).value.trim();
      if (link) body.linkUrl = link;
      try {
        const d = await api(`/moments/${full.moment.id}/annotations`, body);
        full.annotations.push(d.annotation);
        closeSheet();
        if (anchor.rect) renderView();
        else renderNotes();
        setMargin(true);
        toast("Written in ink");
      } catch (err) {
        $("#cStatus", el).className = "status err";
        $("#cStatus", el).textContent = err.message;
      }
    });
  }

  const COMMON_ZONES = ["America/Los_Angeles", "America/Denver", "America/Chicago", "America/New_York", "Europe/London", "Europe/Paris", "Europe/Berlin", "Asia/Tokyo", "Australia/Sydney", "UTC"];
  async function openDetails() {
    const m = S.full.moment;
    const people = await api("/people").then((d) => d.people).catch(() => []);
    const tz = m.timezone || BROWSER_TZ;
    const el = openSheet(`<h2>Details</h2>
      <form id="detForm" style="display:grid;gap:12px">
        <label class="field"><span>Title (optional)</span><input class="input" id="dTitle" value="${esc(m.title)}" /></label>
        <div class="grid2">
          <label class="field"><span>Kind</span><select class="input" id="dKind">${M.KINDS.map((k) => `<option value="${k}" ${k === m.kind ? "selected" : ""}>${M.KIND_LABEL[k]}</option>`).join("")}</select></label>
          <label class="field"><span>Where</span><select class="input" id="dSource">${M.SOURCES.map((s) => `<option value="${s}" ${s === m.source ? "selected" : ""}>${M.SOURCE_LABEL[s]}</option>`).join("")}</select></label>
        </div>
        <label class="field"><span>Where, in detail</span><input class="input" id="dDetail" value="${esc(m.sourceDetail)}" placeholder="A channel, a place, a thread" /></label>
        <div class="grid2">
          <label class="field"><span>When it happened</span><input class="input" type="datetime-local" id="dAt" value="${M.isoToLocalInput(m.occurredAt, tz)}" /></label>
          <label class="field"><span>Until</span><input class="input" type="datetime-local" id="dEnd" value="${M.isoToLocalInput(m.endedAt, tz)}" /></label>
        </div>
        <label class="field"><span>Time zone you were in</span><input class="input" id="dTz" list="tzList" value="${esc(tz)}" /><datalist id="tzList">${[...new Set([BROWSER_TZ, ...COMMON_ZONES])].map((z) => `<option value="${z}">`).join("")}</datalist></label>
        <div class="field"><span>Who</span><div class="peoplepick" id="dPeople"></div><button class="nbtn" type="button" id="dAddPerson" style="justify-self:start">${ICON.plus}Add a person</button>
          <datalist id="peopleList">${people.map((p) => `<option value="${esc(p.name)}">`).join("")}</datalist></div>
        <label class="field"><span>Why you kept it</span><textarea class="input" id="dWhy">${esc(m.why)}</textarea></label>
        <div class="status" id="dStatus"></div>
        <div class="actions"><button class="pill" type="button" id="dCancel">Close</button><button class="pill primary" type="submit">Save</button></div>
      </form>`, "Details");
    const rows = $("#dPeople", el);
    const addRow = (p = { name: "", role: "sender" }) => {
      rows.insertAdjacentHTML("beforeend", `<div class="row"><input class="input" list="peopleList" placeholder="Name" value="${esc(p.name)}" aria-label="Name" />
        <select class="input" aria-label="Role">${["sender", "recipient", "mentioned"].map((r) => `<option value="${r}" ${r === p.role ? "selected" : ""}>${r === "sender" ? "With" : r === "recipient" ? "To" : "Mentioned"}</option>`).join("")}</select>
        <button class="nbtn" type="button" aria-label="Remove">×</button></div>`);
      const row = rows.lastElementChild;
      row.querySelector("button").addEventListener("click", () => row.remove());
    };
    m.people.forEach(addRow);
    if (!m.people.length) addRow();
    $("#dAddPerson", el).addEventListener("click", () => addRow());
    $("#dCancel", el).addEventListener("click", closeSheet);
    $("#dTitle", el).focus();
    $("#detForm", el).addEventListener("submit", async (e) => {
      e.preventDefault();
      const zone = $("#dTz", el).value.trim() || "UTC";
      const at = $("#dAt", el).value, end = $("#dEnd", el).value;
      const peopleRows = $$(".row", rows).map((r) => ({ name: r.querySelector("input").value.trim(), role: r.querySelector("select").value })).filter((p) => p.name);
      const known = new Map(people.map((p) => [p.name.toLowerCase(), p.id]));
      const body = {
        expectedRevision: m.revision, title: $("#dTitle", el).value, kind: $("#dKind", el).value, source: $("#dSource", el).value,
        sourceDetail: $("#dDetail", el).value, timezone: zone, why: $("#dWhy", el).value,
        occurredAt: at ? M.zonedToISO(at, zone) : "", endedAt: end ? M.zonedToISO(end, zone) : "",
        people: peopleRows.map((p) => (known.has(p.name.toLowerCase()) ? { id: known.get(p.name.toLowerCase()), role: p.role } : p)),
      };
      if (!at && !m.occurredAt) delete body.occurredAt;
      try {
        S.full = await api(`/moments/${m.id}`, body, "PATCH");
        closeSheet();
        renderMoment();
        toast("Details saved");
      } catch (err) {
        $("#dStatus", el).className = "status err";
        $("#dStatus", el).textContent = err.status === 409 ? "This Moment changed elsewhere. Close and reopen to see the latest." : err.message;
      }
    });
  }

  function openPalette(q) {
    const el = openSheet(`<label class="pinput">${ICON.search}<span class="sr">Search</span><input id="pq" type="search" autocomplete="off" placeholder="Search every Moment: words, notes, people" value="${esc(q)}" /></label><div id="pres"></div>`, "Search");
    const input = $("#pq", el);
    input.focus();
    let t = 0;
    const run = async () => {
      const v = input.value.trim();
      if (!v) { $("#pres", el).innerHTML = ""; return; }
      try {
        const d = await api(`/search?q=${encodeURIComponent(v)}`);
        if (input.value.trim() === v) $("#pres", el).innerHTML = resultsHTML(d);
        $$("#pres a", el).forEach((a) => a.addEventListener("click", closeSheet));
      } catch (err) {
        $("#pres", el).innerHTML = `<p class="status err">${esc(err.message)}</p>`;
      }
    };
    input.addEventListener("input", () => { clearTimeout(t); t = setTimeout(run, 200); });
    if (q) run();
  }

  // ---- Thread and doorways ------------------------------------------------------------------
  function threadHTML(t) {
    const g = M.threadGeometry(t.knots);
    const pts = [];
    for (let x = 0; x <= 100; x += 1) pts.push(`${x} ${M.waveY(x).toFixed(2)}`);
    const segs = [];
    let from = 0;
    for (const gap of g.gaps) {
      segs.push(`<path d="${pathBetween(from, gap.from)}"/><path class="gap" d="${pathBetween(gap.from, gap.to)}"/>`);
      from = gap.to;
    }
    segs.push(`<path d="${pathBetween(from, 100)}"/>`);
    const here = S.full.moment.id;
    const people = new Set(t.knots.map((k) => k.speaker).filter(Boolean));
    const knots = t.knots.map((k, i) => `<a class="knot ${k.style} ${k.momentId === here ? "here" : ""} ${i % 2 ? "up" : ""}" style="left:${g.x[i]}%;top:${(M.waveY(g.x[i]) * 1.6).toFixed(1)}px"
        href="#m/${k.momentId}${k.lineId ? "?at=line:" + k.lineId : ""}" data-k="${i}" aria-label="${esc(k.label || M.excerpt(k.excerpt, 40))}, ${esc(k.at ? M.formatDate(M.localParts(k.at, k.timezone), { short: true }) : "")}"><i></i><span class="klabel">${esc(k.label || M.excerpt(k.excerpt, 24))}</span></a>`).join("");
    const first = t.knots[0], last = t.knots[t.knots.length - 1];
    const span = first && last && first.at && last.at ? `${M.formatDate(M.localParts(first.at, first.timezone), { short: true })} to ${M.hourText(M.localParts(last.at, last.timezone))}${first.momentId !== last.momentId ? " " + M.formatDate(M.localParts(last.at, last.timezone), { short: true }) : ""}` : "";
    return `<section class="thread" aria-label="Thread" data-thread="${t.id}">
      <div class="thread-head"><div><div class="eyebrow">Thread · ${t.state === "pencil" ? "suggested, in pencil" : "yours"}</div>
        ${t.title ? `<h2 class="thread-title">${esc(t.title)}</h2>` : `<h2 class="thread-title unnamed"><button type="button" class="namebtn" data-thread="${t.id}">Name this thread…</button></h2>`}</div>
        <div class="thread-meta">${t.knots.length} ${t.knots.length === 1 ? "knot" : "knots"}${people.size ? " · " + esc([...people].map((p) => (p === "me" ? "you" : p)).join(", ")) : ""}${span ? " · " + esc(span) : ""}</div></div>
      <div class="strand"><svg viewBox="0 0 100 160" preserveAspectRatio="none" aria-hidden="true"><g transform="scale(1 1.6)">${segs.join("")}</g></svg>
        ${g.gaps.map((gap) => `<span class="gaplabel" style="left:${(gap.from + gap.to) / 2}%;top:${(M.waveY((gap.from + gap.to) / 2) * 1.6 + 14).toFixed(0)}px">${esc(gap.label)}</span>`).join("")}${knots}</div>
      <div class="legend"><span class="f"><i></i>fire</span><span class="p"><i></i>along the way</span></div></section>`;
  }
  function pathBetween(a, b) {
    let p = "";
    const step = Math.max(0.5, (b - a) / 40);
    for (let x = a; x <= b + 0.001; x += step) p += (p ? "L" : "M") + x.toFixed(2) + " " + M.waveY(x).toFixed(2) + " ";
    return p || `M${a} ${M.waveY(a)}`;
  }
  function bindThreads() {
    $$(".thread").forEach((sec) => {
      const t = S.full.threads.find((x) => x.id === sec.dataset.thread);
      $$(".knot", sec).forEach((k) => {
        const knot = t.knots[+k.dataset.k];
        k.addEventListener("click", (e) => {
          if (knot.momentId === S.full.moment.id) {
            e.preventDefault();
            if (knot.lineId) reveal("line:" + knot.lineId);
            else scrollTo({ top: 0, behavior: reduce.matches ? "auto" : "smooth" });
          }
        });
        const show = () => {
          const p = $("#peek");
          const lp = knot.at ? M.localParts(knot.at, knot.timezone) : null;
          p.innerHTML = `<div class="eyebrow">${esc(knot.speaker === "me" ? "You" : knot.speaker || M.KIND_LABEL[knot.momentKind])}${lp ? " · " + esc(M.formatDate(lp, { short: true }) + ", " + M.hourText(lp)) : ""}</div><p>${esc(M.excerpt(knot.excerpt, 140))}</p><small>${knot.momentId === S.full.moment.id ? "Show it in this Moment" : "Open that Moment"}</small>`;
          const r = k.getBoundingClientRect();
          p.style.left = Math.max(16, Math.min(innerWidth - 296, r.left + r.width / 2 - 140)) + "px";
          p.style.top = Math.max(16, r.top - 140) + "px";
          p.classList.add("show");
        };
        const hide = () => $("#peek").classList.remove("show");
        k.addEventListener("mouseenter", show);
        k.addEventListener("focus", show);
        k.addEventListener("mouseleave", hide);
        k.addEventListener("blur", hide);
      });
      $(".namebtn", sec)?.addEventListener("click", (e) => nameThread(e.currentTarget, t));
    });
  }
  function nameThread(btn, t) {
    const h = btn.parentElement;
    h.innerHTML = `<form class="namer"><input aria-label="Thread name" placeholder="Name this thread" /><button class="pill primary" type="submit">Name it</button></form>`;
    const input = $("input", h);
    input.focus();
    $("form", h).addEventListener("submit", async (e) => {
      e.preventDefault();
      const title = input.value.trim();
      if (!title) return;
      try {
        const d = await api(`/threads/${t.id}`, { expectedRevision: t.revision, title }, "PATCH");
        Object.assign(t, d.thread);
        renderMoment();
        toast("Thread named");
      } catch (err) { toast(err.message); }
    });
  }
  function doorsHTML(doors) {
    const items = doors.map((d) => {
      if (d.kind === "link") {
        let site = d.url;
        try { site = new URL(d.url).hostname; } catch {}
        return `<a class="door glass" href="${esc(d.url)}" target="_blank" rel="noopener noreferrer"><span class="dk">Link · ${esc(site)} ${ICON.out}</span><blockquote>${esc(d.title)}</blockquote>${d.why ? `<span class="why ${d.state === "ink" ? "ink" : ""}">${esc(d.why)}</span>` : ""}</a>`;
      }
      const lp = d.at ? M.localParts(d.at, d.timezone) : null;
      return `<a class="door ${d.momentKind === "dream" ? "night-door" : "glass"}" href="#m/${d.momentId}"><span class="dk"><span class="kdot ${d.momentKind}"></span>${esc(M.KIND_LABEL[d.momentKind] || "Moment")}${lp ? " · " + esc(M.formatDate(lp, { short: true }) + ", " + M.hourText(lp)) : ""}</span><blockquote>${esc(M.excerpt(d.title || d.excerpt, 140))}</blockquote>${d.why ? `<span class="why ${d.state === "ink" ? "ink" : ""}">${esc(d.why)}</span>` : ""}</a>`;
    });
    return `<section class="doors" aria-label="Where this leads"><div class="eyebrow">Doorways · where this Moment leads</div>
      ${items.length ? `<div class="doors-grid">${items.join("")}</div>` : ""}<p class="doors-empty">More Moments will appear here as you add them.</p></section>`;
  }

  // ---- Import ------------------------------------------------------------------------------
  function showImport() {
    capsuleFor("index");
    busy(false);
    stage.innerHTML = `<div class="wrap">
      <div class="eyebrow">Private · import</div><h1 class="h-display">Bring things <em>in</em></h1>
      <p class="lede">Every import is previewed first and can be repeated safely: nothing is duplicated.</p>
      <div class="imports">
        <section class="panel glass" aria-labelledby="dH">
          <h2 id="dH">Discord, once</h2>
          <p>Export one channel with DiscordChatExporter as <code>JSON</code>; add <code>--media</code> to keep its images. Choose the export folder, or a zip of it (up to 30 MB).</p>
          <div class="picks">
            <label class="pill">Choose folder<input type="file" id="dFolder" webkitdirectory directory multiple /></label>
            <label class="pill">Choose zip<input type="file" id="dZip" accept=".zip,application/zip" /></label>
          </div>
          <div class="grid2" style="display:grid;grid-template-columns:1fr 1fr;gap:10px">
            <label class="field"><span>Kind</span><select class="input" id="dKind">${M.KINDS.map((k) => `<option value="${k}">${M.KIND_LABEL[k]}</option>`).join("")}</select></label>
            <label class="field"><span>Follow-up gap (hours)</span><input class="input" id="dGap" type="number" min="0.1" max="720" step="0.5" value="3" /></label>
          </div>
          <label class="field"><span>Your time zone then</span><input class="input" id="dTzI" value="${esc(BROWSER_TZ)}" /></label>
          <div class="status" id="dStatusI" role="status"></div>
          <div class="progress" hidden id="dProg"><i></i></div>
          <div id="dPreview"></div>
          <div class="actions" style="display:flex;gap:8px;flex-wrap:wrap"><button class="pill" type="button" id="dPreviewBtn" disabled>Preview</button><button class="pill primary" type="button" id="dGo" disabled>Import</button></div>
        </section>
        <section class="panel glass" aria-labelledby="bH">
          <h2 id="bH">A bundle</h2>
          <p>A <code>manifest.json</code> describing one full Moment (see the README), plus its screenshots and recordings. Files are matched by the names the manifest uses. Re-importing updates in place; your kept, erased and edited notes stay as you left them.</p>
          <div class="picks">
            <label class="pill">Manifest<input type="file" id="bManifest" accept=".json,application/json" /></label>
            <label class="pill">Files<input type="file" id="bFiles" multiple accept="image/*,audio/*" /></label>
          </div>
          <div class="status" id="bStatus" role="status"></div>
          <div class="progress" hidden id="bProg"><i></i></div>
          <div id="bPreview"></div>
          <div class="actions" style="display:flex;gap:8px;flex-wrap:wrap"><button class="pill" type="button" id="bPreviewBtn" disabled>Preview</button><button class="pill primary" type="button" id="bGo" disabled>Import</button></div>
        </section>
      </div></div>`;
    bindDiscordImport();
    bindBundleImport();
  }

  function setStatus(el, msg, err) {
    el.className = "status" + (err ? " err" : "");
    el.textContent = msg;
  }
  function setProgress(el, frac) {
    el.hidden = frac == null;
    if (frac != null) el.firstElementChild.style.width = Math.round(frac * 100) + "%";
  }

  function bindDiscordImport() {
    const st = $("#dStatusI");
    const D = { exportFile: null, media: [], zip: null, preview: null, me: "" };
    const rel = (f) => f.webkitRelativePath || f.name;
    $("#dFolder").addEventListener("change", async (e) => {
      const files = [...e.target.files];
      const path = M.pickExportFile(files.map(rel));
      D.zip = null;
      D.exportFile = files.find((f) => rel(f) === path) || null;
      D.media = files.filter((f) => f !== D.exportFile && !/\.json$/i.test(f.name) && !/(^|\/)\./.test(rel(f)));
      if (!D.exportFile) return setStatus(st, "No DiscordChatExporter JSON found in that folder.", true);
      try {
        const head = JSON.parse(await D.exportFile.text());
        $("#dKind").value = M.suggestKind(head.channel && head.channel.name);
        setStatus(st, `#${head.channel?.name || "channel"} · ${(head.messages || []).length} messages · ${D.media.length} media files`);
      } catch { return setStatus(st, "That JSON file could not be read.", true); }
      $("#dPreviewBtn").disabled = false;
      preview();
    });
    $("#dZip").addEventListener("change", (e) => {
      D.zip = e.target.files[0] || null;
      D.exportFile = null;
      D.media = [];
      if (!D.zip) return;
      if (D.zip.size > 30 * 1024 * 1024) return setStatus(st, "That zip is over 30 MB. Choose the export folder instead; it uploads in batches.", true);
      setStatus(st, `${D.zip.name} · ${(D.zip.size / 1048576).toFixed(1)} MB`);
      $("#dPreviewBtn").disabled = false;
      preview();
    });
    $("#dPreviewBtn").addEventListener("click", preview);
    $("#dGo").addEventListener("click", run);
    function baseForm(dry) {
      const fd = new FormData();
      fd.append("kind", $("#dKind").value);
      fd.append("gapHours", $("#dGap").value || "3");
      fd.append("timezone", $("#dTzI").value.trim() || BROWSER_TZ);
      if (D.me) fd.append("me", D.me);
      if (dry) fd.append("dryRun", "1");
      if (D.zip) fd.append("archive", D.zip, D.zip.name);
      else fd.append("export", D.exportFile, rel(D.exportFile));
      return fd;
    }
    async function preview() {
      if (!D.zip && !D.exportFile) return;
      setStatus(st, "Previewing…");
      const fd = baseForm(true);
      if (!D.zip) fd.append("mediaManifest", JSON.stringify(D.media.map(rel)));
      try {
        D.preview = await api("/import/discord", fd);
        renderPreview();
        setStatus(st, "Preview ready. Nothing has been saved yet.");
        $("#dGo").disabled = false;
      } catch (err) { setStatus(st, err.message, true); }
    }
    function renderPreview() {
      const p = D.preview, t = p.totals;
      const fresh = t.momentsCreated || 0, existing = t.momentsUpdated || 0;
      $("#dGo").textContent = fresh ? `Import ${fresh} ${fresh === 1 ? "Moment" : "Moments"}` : "Import (updates only)";
      $("#dPreview").innerHTML = `<div class="preview">
        <div class="pv-sum"><span><b>${p.groups.length}</b> Moments</span><span><b>${fresh}</b> new</span><span><b>${existing}</b> already here</span><span><b>${t.messages || 0}</b> messages</span><span><b>${(p.groups || []).reduce((s, g) => s + g.mediaMatched, 0)}</b> media matched</span>${t.skippedSystem ? `<span>${t.skippedSystem} system skipped</span>` : ""}</div>
        <div class="field"><span>Which author is you?</span><div class="authors">${[{ id: "", name: "None of these" }, ...p.authors].map((a) => `<label><input type="radio" name="me" value="${esc(a.id)}" ${(D.me || "") === a.id ? "checked" : ""} /> ${esc(a.nickname || a.name)}${a.messages ? ` <span class="count">${a.messages}</span>` : ""}</label>`).join("")}</div></div>
        ${p.groups.map((g) => {
          const lp = M.localParts(g.startedAt, p.timezone);
          return `<div class="pv-group"><span class="t">${esc(M.formatDate(lp, { short: true }))} · ${esc(M.hourText(lp))} · ${g.messages} ${g.messages === 1 ? "message" : "messages"}${g.attachments ? ` · ${g.mediaMatched}/${g.attachments} media` : ""} ${g.existing ? "· already here" : '<span class="new">· new</span>'}</span>
            <span class="x">${esc(g.starter)}: ${esc(g.excerpt || "(attachment)")}</span></div>`;
        }).join("")}</div>`;
      $$('#dPreview input[name="me"]').forEach((r) => r.addEventListener("change", () => { D.me = r.value; }));
    }
    async function run() {
      $("#dGo").disabled = true;
      const prog = $("#dProg");
      try {
        let totals = {};
        if (D.zip) {
          setProgress(prog, 0.3);
          totals = (await api("/import/discord", baseForm(false))).totals;
        } else {
          const batches = D.media.length ? M.batchFiles(D.media, 16 * 1024 * 1024, D.exportFile.size) : [[]];
          for (let i = 0; i < batches.length; i++) {
            setStatus(st, `Importing… batch ${i + 1} of ${batches.length}`);
            setProgress(prog, i / batches.length);
            const fd = baseForm(false);
            for (const f of batches[i]) fd.append("media", f, rel(f));
            const r = await api("/import/discord", fd);
            for (const [k, v] of Object.entries(r.totals)) if (/created|New|Stored/.test(k)) totals[k] = (totals[k] || 0) + v;
            totals.mediaMissing = r.totals.mediaMissing || 0;
          }
        }
        setProgress(prog, 1);
        setStatus(st, `Done: ${totals.momentsCreated || 0} new Moments, ${totals.messagesNew || 0} messages, ${totals.mediaStored || 0} media stored${totals.mediaMissing ? `, ${totals.mediaMissing} media not found` : ""}.`);
        $("#dPreview").insertAdjacentHTML("afterbegin", `<p><a class="pill" href="#" id="dSee">See them</a></p>`);
        $("#dSee").addEventListener("click", () => { filters.source = "discord"; });
      } catch (err) {
        setStatus(st, err.message, true);
        $("#dGo").disabled = false;
      }
    }
  }

  function bindBundleImport() {
    const st = $("#bStatus");
    const B = { manifestFile: null, manifest: null, files: [] };
    const refs = () => (B.manifest?.artifacts || []).filter((a) => a.kind === "image" || a.kind === "audio").map((a) => a.file || a.key);
    const matches = (ref) => B.files.find((f) => f.name === ref || f.name.replace(/\.[^.]+$/, "") === ref);
    const update = () => {
      const ok = !!B.manifest;
      $("#bPreviewBtn").disabled = !ok;
      if (ok) {
        const r = refs();
        const missing = r.filter((x) => !matches(x));
        setStatus(st, `${B.manifest.externalKey || "(no externalKey)"} · ${r.length - missing.length}/${r.length} files chosen${missing.length ? ` · missing: ${missing.join(", ")}` : ""}`);
      }
    };
    $("#bManifest").addEventListener("change", async (e) => {
      B.manifestFile = e.target.files[0] || null;
      try { B.manifest = B.manifestFile ? JSON.parse(await B.manifestFile.text()) : null; }
      catch { B.manifest = null; return setStatus(st, "That manifest is not valid JSON.", true); }
      update();
    });
    $("#bFiles").addEventListener("change", (e) => { B.files = [...e.target.files]; update(); });
    const form = (dry, files) => {
      const fd = new FormData();
      fd.append("manifest", JSON.stringify(B.manifest));
      if (dry) fd.append("dryRun", "1");
      for (const ref of refs()) {
        const f = files.find((x) => x === matches(ref));
        if (f) fd.append(ref, f, f.name);
      }
      return fd;
    };
    const summary = (r) => `<div class="preview"><div class="pv-sum"><span><b>${r.created ? "New" : "Update"}</b></span>${Object.entries(r.counts).map(([k, v]) => `<span><b>${v}</b> ${esc(k)}</span>`).join("")}<span><b>${r.filesStored}</b> files to store</span><span><b>${r.filesKept}</b> kept</span></div>
      ${r.missingFiles.length && !r.dryRun ? `<div class="pv-group"><span class="t">Waiting for files</span><span class="x">${esc(r.missingFiles.join(", "))}</span></div>` : ""}
      ${r.warnings.map((w) => `<div class="pv-group"><span class="x">${esc(w)}</span></div>`).join("")}
      ${r.unusedFiles.length ? `<div class="pv-group"><span class="t">Not referenced</span><span class="x">${esc(r.unusedFiles.join(", "))}</span></div>` : ""}</div>`;
    const problems = (err) => (err.body && err.body.problems ? err.body.problems.join("\n") : err.message);
    $("#bPreviewBtn").addEventListener("click", async () => {
      setStatus(st, "Previewing…");
      try {
        const r = await api("/import/bundle", form(true, []));
        $("#bPreview").innerHTML = summary(r);
        setStatus(st, "Preview ready (files are checked on import). Nothing has been saved yet.");
        $("#bGo").disabled = false;
      } catch (err) { setStatus(st, problems(err), true); }
    });
    $("#bGo").addEventListener("click", async () => {
      $("#bGo").disabled = true;
      const chosen = refs().map(matches).filter(Boolean);
      const batches = chosen.length ? M.batchFiles(chosen, 16 * 1024 * 1024, 64 * 1024) : [[]];
      try {
        let last = null;
        for (let i = 0; i < batches.length; i++) {
          setStatus(st, `Importing… ${i + 1} of ${batches.length}`);
          setProgress($("#bProg"), i / batches.length);
          last = await api("/import/bundle", form(false, batches[i]));
        }
        setProgress($("#bProg"), 1);
        $("#bPreview").innerHTML = summary(last) + `<p><a class="pill primary" href="#m/${last.momentId}">Open the Moment</a></p>`;
        setStatus(st, "Imported.");
      } catch (err) {
        setStatus(st, problems(err), true);
        $("#bGo").disabled = false;
      }
    });
  }

  // ---- Keyboard ------------------------------------------------------------------------------
  document.addEventListener("keydown", (e) => {
    const typing = e.target.isContentEditable || /INPUT|TEXTAREA|SELECT/.test(e.target.tagName);
    if (e.key === "Escape" && sheetEl) { closeSheet(); return; }
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k" && current.name !== "space") { e.preventDefault(); openPalette(""); return; }
    if (typing || e.metaKey || e.ctrlKey || e.altKey || current.name === "space") return;
    if (e.key === "/") { e.preventDefault(); if (current.name === "index" && $("#q")) $("#q").focus(); else openPalette(""); return; }
    if (current.name !== "moment" || !S.full) return;
    const views = viewsOf(S.full);
    const k = e.key.toLowerCase();
    if (k === "m") setMargin(!S.margin);
    else if (k === "t" && views.length > 1) setView(views[(views.indexOf(S.view) + 1) % views.length]);
    else if (/^[1-4]$/.test(k) && views[+k - 1]) setView(views[+k - 1]);
    else if (k === "i") location.hash = `#space?focus=${S.full.moment.id}`;
  });

  // ---- Ambient wisps -------------------------------------------------------------------------
  const cv = $("#wisps"), cx = cv.getContext("2d");
  let W = 0, H = 0, DPR = 1, wraf = 0, lastFrame = 0;
  let seed = 5;
  const rnd = () => (seed = (seed * 16807) % 2147483647) / 2147483647;
  const wisps = Array.from({ length: 6 }, () => ({ x: rnd(), y: rnd(), r: 0.25 + rnd() * 0.35, a: rnd() * 6.28, sp: 0.00004 + rnd() * 0.00006 }));
  const motes = Array.from({ length: 40 }, () => ({ x: rnd(), y: rnd(), z: 0.3 + rnd() * 0.7, a: rnd() * 6.28 }));
  function size() { DPR = Math.min(2, devicePixelRatio || 1); W = innerWidth; H = innerHeight; cv.width = W * DPR; cv.height = H * DPR; cx.setTransform(DPR, 0, 0, DPR, 0, 0); }
  function draw(t) {
    cx.clearRect(0, 0, W, H);
    const rgb = getComputedStyle(document.body).getPropertyValue("--wisp").trim() || "150,140,230";
    for (const w of wisps) {
      const x = (w.x + Math.sin(t * w.sp + w.a) * 0.08) * W, y = (w.y + Math.cos(t * w.sp * 1.3 + w.a) * 0.06) * H, rr = w.r * Math.max(W, H);
      const g = cx.createRadialGradient(x, y, 0, x, y, rr);
      g.addColorStop(0, `rgba(${rgb},.07)`);
      g.addColorStop(1, `rgba(${rgb},0)`);
      cx.fillStyle = g;
      cx.beginPath(); cx.arc(x, y, rr, 0, 6.29); cx.fill();
    }
    for (const m of motes) {
      const x = ((m.x + Math.sin(t * 0.00007 * m.z + m.a) * 0.02) % 1) * W, y = ((((m.y - t * 0.000006 * m.z) % 1) + 1) % 1) * H;
      cx.fillStyle = `rgba(${rgb},${0.4 * m.z * (0.6 + 0.4 * Math.sin(t * 0.001 + m.a))})`;
      cx.beginPath(); cx.arc(x, y, 0.6 + m.z * 1.1, 0, 6.29); cx.fill();
    }
  }
  function loop(t) { if (t - lastFrame > 33) { draw(t); lastFrame = t; } wraf = requestAnimationFrame(loop); }
  function startWisps() { cancelAnimationFrame(wraf); size(); if (reduce.matches || current.name === "space") draw(0); else wraf = requestAnimationFrame(loop); }
  reduce.addEventListener?.("change", startWisps);
  document.addEventListener("visibilitychange", () => { if (document.hidden) cancelAnimationFrame(wraf); else startWisps(); });
  addEventListener("resize", () => { size(); draw(performance.now()); layoutNotes(); });
  narrow.addEventListener?.("change", () => { if (current.name === "moment" && S.full) renderView(); });
  if (document.fonts && document.fonts.ready) document.fonts.ready.then(layoutNotes);

  startWisps();
  router();
  globalThis.__commonplace = { S, reveal, setView, setMargin, api };
})();
