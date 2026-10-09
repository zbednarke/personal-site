(() => {
  "use strict";

  const API_BASE = "./api/v1";
  const DATA = globalThis.JAZZ_DATA;
  const M = globalThis.JazzRepertoireModel;
  const KEY_DISPLAY_KEY = "zach-jazz-key-display-v1";
  const REFRESH_INTERVAL_MS = 5000;
  if (!DATA || !M) return;

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

  const state = {
    tunes: [],
    archived: [],
    unlinked: [],
    settings: { setTargetDate: DATA.repertoirePace.targetDate },
    week: { start: "", today: "", practiceMs: 0, jazzPracticeMs: 0 },
    loaded: false,
    loading: null,
    error: "",
    lastFetch: 0,
    refreshTimer: null,
    confirmed: new Map(),
    chains: new Map(),
    display: readDisplay(),
    detailTuneId: "",
    detailOpener: null,
    detailTab: "details",
    history: null,
    historyRequest: 0,
    noteTimer: null,
    drag: null,
  };

  function readDisplay() {
    try {
      return localStorage.getItem(KEY_DISPLAY_KEY) === "bb-trumpet" ? "bb-trumpet" : "concert";
    } catch {
      return "concert";
    }
  }

  function writeDisplay(value) {
    state.display = value;
    try { localStorage.setItem(KEY_DISPLAY_KEY, value); } catch { /* per-viewer convenience only */ }
  }

  async function api(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
      ...options,
      headers: {
        ...(options.body ? { "Content-Type": "application/json" } : {}),
        ...(options.headers || {}),
      },
    });
    const body = response.status === 204 ? null : await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(body?.error || `Request failed (${response.status})`);
      error.status = response.status;
      error.body = body;
      throw error;
    }
    return body;
  }

  function localDateKey(date = new Date()) {
    return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
  }

  function escapeHTML(value) {
    const span = document.createElement("span");
    span.textContent = String(value ?? "");
    return span.innerHTML;
  }

  let toastTimer = null;
  function toast(message) {
    const element = $("#toast");
    if (!element) return;
    element.textContent = message;
    element.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => element.classList.remove("show"), 2600);
  }

  const online = () => navigator.onLine !== false;
  const categoryLabel = (key) => M.CATEGORIES.find((item) => item.key === key)?.label || "Tunes";
  const today = () => localDateKey();

  function byId(tuneId) {
    if (!tuneId) return null;
    return state.tunes.find((tune) => tune.tuneId === tuneId) || state.archived.find((tune) => tune.tuneId === tuneId) || null;
  }

  function title(tuneId) {
    return byId(tuneId)?.title || "";
  }

  function displayKey(key) {
    return M.transposeKey(key, state.display) || key;
  }

  function keysLabel(tune) {
    return (tune.keysKnown || []).map(displayKey).join(", ");
  }

  function findRecordingURL(tune) {
    if (/^https:\/\//.test(tune.referenceUrl || "")) return tune.referenceUrl;
    const query = [tune.referenceArtist, tune.title].filter(Boolean).join(" ");
    return `https://www.youtube.com/results?search_query=${encodeURIComponent(query)}`;
  }

  function referenceLink(tune) {
    const label = tune.referenceUrl ? "Reference recording" : "Find recording";
    return `<a class="tune-reference-link" href="${escapeHTML(findRecordingURL(tune))}" target="_blank" rel="noopener noreferrer">${label}</a>`;
  }

  function setStatus(message, tone = "") {
    const status = $("#repertoire-status");
    if (!status) return;
    status.textContent = message;
    status.dataset.tone = tone;
  }

  function applyResponse(result) {
    // A refresh can land after a save it raced; keep tunes with edits in flight
    // or a newer confirmed revision instead of reverting them.
    const server = [...(result.tunes || []), ...(Array.isArray(result.archived) ? result.archived : [])];
    const confirmed = new Map();
    const incoming = server.map((tune) => {
      const known = state.confirmed.get(tune.tuneId);
      if (state.chains.has(tune.tuneId) || (known && known.revision > tune.revision)) {
        confirmed.set(tune.tuneId, known || tune);
        return byId(tune.tuneId) || M.deriveTuneState(tune);
      }
      confirmed.set(tune.tuneId, tune);
      return M.deriveTuneState(tune);
    });
    state.tunes = incoming.filter((tune) => !tune.archivedAt);
    state.archived = incoming.filter((tune) => tune.archivedAt);
    state.unlinked = result.unlinked || [];
    state.settings = { setTargetDate: result.settings?.setTargetDate || DATA.repertoirePace.targetDate };
    state.week = { ...state.week, ...(result.week || {}) };
    state.confirmed = confirmed;
    state.loaded = true;
    state.error = "";
  }

  function load({ force = false } = {}) {
    if (state.loading) return state.loading;
    if (state.loaded && !force) return Promise.resolve(state.tunes);
    state.loading = (async () => {
      try {
        applyResponse(await api(`/repertoire?includeArchived=1&today=${today()}`));
        state.lastFetch = Date.now();
        render();
        notify();
      } catch (error) {
        state.error = error.message;
        if (!state.loaded) render();
        setStatus(online() ? `Repertoire unavailable: ${error.message}` : "Offline: repertoire changes are not saved", "error");
      } finally {
        state.loading = null;
      }
      return state.tunes;
    })();
    return state.loading;
  }

  // Practice changes tune history; coalesce refreshes to at most one per 5 s.
  function invalidate() {
    if (!state.loaded && !state.loading) return;
    if (state.refreshTimer) return;
    const wait = Math.max(300, state.lastFetch + REFRESH_INTERVAL_MS - Date.now());
    state.refreshTimer = setTimeout(() => {
      state.refreshTimer = null;
      load({ force: true }).then(() => { if (state.detailTuneId && state.detailTab === "history") loadHistory(state.detailTuneId); });
    }, wait);
  }

  function notify() {
    dispatchEvent(new CustomEvent("jazz:repertoire-changed"));
    refreshTuneCards();
  }

  function applyChanges(tune, changes) {
    const next = { ...tune, milestones: { ...(tune.milestones || {}) }, keysKnown: [...(tune.keysKnown || [])] };
    Object.entries(changes).forEach(([field, value]) => {
      if (field === "milestones") Object.assign(next.milestones, value);
      else if (field === "keysKnown") next.keysKnown = [...value];
      else if (field === "archived") next.archivedAt = value ? new Date().toISOString() : undefined;
      else next[field] = value;
    });
    return M.deriveTuneState(next);
  }

  function replaceTune(tune) {
    const derived = M.deriveTuneState(tune);
    state.tunes = state.tunes.filter((item) => item.tuneId !== tune.tuneId);
    state.archived = state.archived.filter((item) => item.tuneId !== tune.tuneId);
    if (derived.archivedAt) state.archived.push(derived);
    else state.tunes.push(derived);
    const categoryOrder = Object.fromEntries(M.CATEGORIES.map((item, index) => [item.key, index]));
    state.tunes.sort((a, b) => categoryOrder[a.category] - categoryOrder[b.category] || a.position - b.position || a.title.localeCompare(b.title));
  }

  function setDetailSync(message, tone = "") {
    $$("[data-tune-sync]").forEach((element) => {
      element.textContent = message;
      element.dataset.tone = tone;
    });
  }

  // Edits are optimistic, serialized per tune and protected by the tune's revision.
  function patchTune(tuneId, changes, { practiceBlockId = "" } = {}) {
    const current = byId(tuneId);
    if (!current) return Promise.reject(new Error("Unknown tune"));
    if (!online()) {
      toast("Offline: changes not saved");
      setDetailSync("Offline: not saved", "pending");
      return Promise.reject(new Error("offline"));
    }
    replaceTune(applyChanges(current, changes));
    render();
    notify();
    setDetailSync("Saving…", "saving");
    const previous = state.chains.get(tuneId) || Promise.resolve();
    const run = previous.catch(() => {}).then(async () => {
      const base = state.confirmed.get(tuneId) || current;
      const send = (body, revision) => api(`/repertoire/tunes/${encodeURIComponent(tuneId)}?today=${today()}`, {
        method: "PATCH",
        body: JSON.stringify({ ...body, expectedRevision: revision, clientMutationId: crypto.randomUUID(), ...(practiceBlockId ? { practiceBlockId } : {}) }),
      });
      try {
        const saved = await send(changes, base.revision);
        state.confirmed.set(tuneId, saved);
        replaceTune(saved);
      } catch (error) {
        if (error.status !== 409 || !error.body?.tune) {
          replaceTune(base);
          throw error;
        }
        const merged = M.mergeServerTune(base, applyChanges(base, changes), error.body.tune);
        if (merged.conflicts.length) toast("Updated on another device.");
        state.confirmed.set(tuneId, error.body.tune);
        replaceTune(merged.tune);
        if (Object.keys(merged.patch).length) {
          const saved = await send(merged.patch, error.body.tune.revision);
          state.confirmed.set(tuneId, saved);
          replaceTune(saved);
        }
      }
    });
    state.chains.set(tuneId, run);
    return run.then(() => {
      setDetailSync("Saved", "saved");
    }).catch((error) => {
      setDetailSync("Sync pending", "pending");
      toast(`Tune not saved: ${error.message}`);
      throw error;
    }).finally(() => {
      if (state.chains.get(tuneId) === run) state.chains.delete(tuneId);
      render();
      notify();
    });
  }

  async function createTune(fields) {
    const tune = await api(`/repertoire/tunes?today=${today()}`, {
      method: "POST",
      body: JSON.stringify({ ...fields, clientMutationId: crypto.randomUUID() }),
    });
    state.confirmed.set(tune.tuneId, tune);
    state.unlinked = state.unlinked.filter((item) => item.tuneId !== tune.tuneId);
    replaceTune(tune);
    render();
    notify();
    return M.deriveTuneState(tune);
  }

  /* ---------- rendering ---------- */

  function chipMarkup(tune, milestone, { interactive = false } = {}) {
    const isKeys = milestone.key === "keysKnown";
    const status = tune.milestones?.[milestone.key] || "not_started";
    const statusLabel = { not_started: "Not started", learning: "Learning", solid: "Solid" }[status];
    const detail = isKeys ? (keysLabel(tune) || "none yet") : statusLabel;
    const label = `${milestone.label}: ${detail}`;
    const text = isKeys && tune.keysKnown?.length ? keysLabel(tune) : milestone.short;
    if (interactive && !isKeys) {
      return `<button type="button" class="milestone-chip" data-status="${status}" data-milestone="${milestone.key}" aria-pressed="${status === "solid"}" aria-label="${escapeHTML(label)}. Activate to change." title="${escapeHTML(label)}">${escapeHTML(text)}</button>`;
    }
    return `<span class="milestone-chip" data-status="${status}" role="img" aria-label="${escapeHTML(label)}" title="${escapeHTML(label)}">${escapeHTML(text)}</span>`;
  }

  function badgesMarkup(tune) {
    return [
      tune.deeplyLearned ? '<span class="tune-badge deep">Deeply learned</span>' : "",
      tune.milestones?.gigReady === "solid" ? '<span class="tune-badge gig">Gig-ready</span>' : "",
      !tune.chosen ? '<span class="tune-badge">Not chosen</span>' : "",
      tune.archivedAt ? '<span class="tune-badge">Archived</span>' : "",
    ].join("");
  }

  function practiceLine(tune) {
    const parts = [M.lastPracticedLabel(tune.lastPracticedDate, today())];
    if (tune.totalPracticeMs > 0) parts.push(`${M.formatPracticeTime(tune.totalPracticeMs)} total`);
    if (tune.takeCount) parts.push(`${tune.takeCount} take${tune.takeCount === 1 ? "" : "s"}`);
    return parts.join(" · ");
  }

  function tuneCardMarkup(tune) {
    return `
      <article class="repertoire-tune-card${tune.practiceStatus === "gig_ready" ? " gig-ready" : ""}${tune.deeplyLearned ? " deep" : ""}" data-tune-card="${escapeHTML(tune.tuneId)}" data-category="${escapeHTML(tune.category)}">
        <header>
          <button type="button" class="tune-reorder-handle" data-tune-reorder aria-label="Reorder ${escapeHTML(tune.title)}. Use arrow keys to move." title="Drag or use arrow keys to reorder"><span aria-hidden="true">⠿</span></button>
          <div>
            <h3>${escapeHTML(tune.title)}</h3>
            <p>${escapeHTML(tune.referenceArtist || "Reference to choose")}${tune.concertKey ? ` · ${escapeHTML(displayKey(tune.concertKey))}` : ""}</p>
          </div>
        </header>
        <div class="milestone-chips">${M.MILESTONES.map((milestone) => chipMarkup(tune, milestone)).join("")}</div>
        <div class="tune-badges">${badgesMarkup(tune)}</div>
        <p class="tune-practice-line">${escapeHTML(practiceLine(tune))}</p>
        <div class="tune-card-actions">
          <button type="button" class="button button-primary" data-tune-practice>Practice now</button>
          <a class="button button-quiet" href="#repertoire/${encodeURIComponent(tune.tuneId)}" data-tune-details>Details</a>
        </div>
      </article>`;
  }

  function renderGoals() {
    const container = $("#repertoire-goals");
    if (!container) return;
    const progress = M.categoryProgress(state.tunes, DATA.repertoireGoals);
    container.innerHTML = progress.map((item) => {
      const measure = item.measure === "deeplyLearned" ? "deeply learned" : "gig-ready";
      const secondary = item.measure === "deeplyLearned" ? `${item.gigReady} gig-ready` : `${item.deeplyLearned} deeply learned`;
      const chosen = item.overChosen ? `${item.chosen} chosen / ${item.target} goal` : `${item.chosen} chosen`;
      return `
        <article class="repertoire-goal" data-category="${item.category}">
          <span>${escapeHTML(categoryLabel(item.category))}</span>
          <strong>${item.counted} / ${item.target}<small> ${measure}</small></strong>
          <div class="repertoire-meter" role="progressbar" aria-label="${escapeHTML(categoryLabel(item.category))} progress" aria-valuemin="0" aria-valuemax="${item.target}" aria-valuenow="${item.counted}"><span style="width:${item.percent}%"></span></div>
          <small>${secondary} · ${chosen}${item.onDeck ? ` · ${item.onDeck} on deck` : ""}</small>
        </article>`;
    }).join("");
  }

  function renderPace() {
    const container = $("#repertoire-pace");
    if (!container) return;
    const target = state.settings.setTargetDate || DATA.repertoirePace.targetDate;
    const pace = M.paceProjection(state.tunes, today(), target, DATA.repertoirePace.tunesPerWeek, DATA.repertoireGoals, DATA.repertoirePace.startDate);
    const targetLabel = new Date(`${target}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" });
    const verdict = pace.pastTarget ? (pace.needed ? `${pace.needed} to go; target passed` : "Goal reached")
      : pace.onPace ? "on pace" : `${pace.behind} behind`;
    const weekMinutes = state.tunes.reduce((sum, tune) => sum + Number(tune.weekPracticeMs || 0), 0);
    const jazzShare = state.week.practiceMs ? Math.round((state.week.jazzPracticeMs / state.week.practiceMs) * 100) : 0;
    const repertoireShare = state.week.jazzPracticeMs ? Math.min(100, Math.round((Number(state.week.repertoirePracticeMs || 0) / state.week.jazzPracticeMs) * 100)) : 0;
    const suggestion = M.suggestTuneOfWeek(state.tunes, DATA.repertoireGoals);
    container.innerHTML = `
      <p class="eyebrow">Pace · ${DATA.repertoirePace.tunesPerWeek} tune/week</p>
      <h2 id="repertoire-pace-title">Week ${pace.week} · ${pace.done} of ${pace.goalTotal} done · <em data-tone="${pace.onPace ? "good" : "behind"}">${escapeHTML(verdict)}</em></h2>
      <p>${pace.needed ? `${pace.needed} tunes to go for ${escapeHTML(targetLabel)}. At this pace you finish around ${escapeHTML(new Date(`${pace.projectedDate}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }))}${pace.projectedLate ? ", after the target" : ""}.` : "Every goal is met."}</p>
      <dl class="repertoire-week">
        <div><dt>Repertoire this week</dt><dd>${M.formatPracticeTime(weekMinutes)}</dd></div>
        <div><dt>Share of jazz practice</dt><dd>${state.week.jazzPracticeMs ? `${repertoireShare}%` : "—"}</dd></div>
        <div><dt>Jazz / trumpet split</dt><dd>${state.week.practiceMs ? `${jazzShare} / ${100 - jazzShare}` : "—"}</dd></div>
      </dl>
      ${suggestion ? `<div class="tune-of-week"><span>Suggested tune this week</span><strong>${escapeHTML(suggestion.title)}</strong><small>${escapeHTML(categoryLabel(suggestion.category))} · next: ${escapeHTML(focusPreset(M.suggestFocus(suggestion)).label.toLowerCase())}</small><button type="button" class="button button-primary" data-practice-tune="${escapeHTML(suggestion.tuneId)}">Practice now</button></div>` : ""}`;
  }

  function renderSetReadiness() {
    const container = $("#repertoire-set");
    if (!container) return;
    const readiness = M.setReadiness(state.tunes, DATA.setRules);
    const groups = [["ballad", "Ballads"], ["upbeat", "Upbeat"], ["pop", "Pop"], ["standard", "Standards"]].filter(([key]) => readiness.callable[key].length);
    const row = (tune) => {
      const rusty = M.isRusty(tune, today());
      const days = M.daysSince(tune.lastPracticedDate, today());
      return `<li${rusty ? ' data-rusty="true"' : ""}><a href="#repertoire/${encodeURIComponent(tune.tuneId)}">${escapeHTML(tune.title)}</a><span>${escapeHTML(keysLabel(tune) || (tune.concertKey ? displayKey(tune.concertKey) : "key?"))}</span>${rusty ? `<button type="button" data-practice-tune="${escapeHTML(tune.tuneId)}" data-focus="set" title="Not played in ${days === null ? "a while" : `${days} days`}">Rusty · run it</button>` : referenceLink(tune)}</li>`;
    };
    const order = M.suggestedSetOrder(readiness.callable);
    container.innerHTML = `
      <p class="eyebrow">Set readiness</p>
      <h2 id="repertoire-set-title" data-verdict="${readiness.verdict}">${escapeHTML(readiness.label)}</h2>
      <p>${readiness.total} call-able tune${readiness.total === 1 ? "" : "s"} · bar: ${DATA.setRules.minTotal} tunes with ${DATA.setRules.minBallads}+ ballads, ${DATA.setRules.minUpbeat}+ upbeat and ${DATA.setRules.minPop}+ pop.</p>
      ${groups.length ? `<div class="callable-list">${groups.map(([key, label]) => `<section><h3>${label}</h3><ul>${readiness.callable[key].map(row).join("")}</ul></section>`).join("")}</div>` : '<p class="repertoire-empty">Nothing is call-able yet. Mark a tune Gig-ready when you could play it tonight.</p>'}
      ${order.length > 2 ? `<details class="set-order"><summary>Suggested set order</summary><ol>${order.map((tune) => `<li>${escapeHTML(tune.title)} <small>${escapeHTML(categoryLabel(tune.category))}</small></li>`).join("")}</ol></details>` : ""}`;
  }

  function humanizeSlug(tuneId) {
    return DATA.repertoire.find((item) => item.id === tuneId)?.title
      || tuneId.split("-").map((word) => word.charAt(0).toUpperCase() + word.slice(1)).join(" ");
  }

  function renderUnlinked() {
    const container = $("#repertoire-unlinked");
    if (!container) return;
    if (!state.unlinked.length) { container.innerHTML = ""; return; }
    container.innerHTML = `
      <section class="repertoire-unlinked" aria-labelledby="repertoire-unlinked-title">
        <h2 id="repertoire-unlinked-title">Unlinked practice</h2>
        <p>Practice tagged with a tune that is not in your repertoire.</p>
        <ul>${state.unlinked.map((item) => `<li><strong>${escapeHTML(humanizeSlug(item.tuneId))}</strong><span>${escapeHTML(item.tuneId)} · ${M.formatPracticeTime(item.totalPracticeMs)} · ${item.takeCount} take${item.takeCount === 1 ? "" : "s"} · last ${escapeHTML(item.lastPracticedDate || "—")}</span><button type="button" class="button button-quiet" data-adopt-tune="${escapeHTML(item.tuneId)}">Add to repertoire</button></li>`).join("")}</ul>
      </section>`;
  }

  function renderCategories() {
    const container = $("#repertoire-categories");
    if (!container) return;
    container.innerHTML = M.CATEGORIES.map(({ key, label }) => {
      const tunes = state.tunes.filter((tune) => tune.category === key);
      if (!tunes.length && key === "standard") return "";
      const chosen = tunes.filter((tune) => tune.chosen);
      const onDeck = tunes.filter((tune) => !tune.chosen);
      const goal = DATA.repertoireGoals[key];
      return `
        <section class="repertoire-category" data-category="${key}" aria-labelledby="repertoire-category-${key}">
          <header><h2 id="repertoire-category-${key}">${label}</h2><span>${goal ? `${chosen.length} chosen · goal ${goal.target}` : `${tunes.length} tune${tunes.length === 1 ? "" : "s"}`}</span></header>
          ${chosen.length ? `<div class="repertoire-grid" data-reorder-list="${key}">${chosen.map(tuneCardMarkup).join("")}</div>` : '<p class="repertoire-empty">No chosen tunes yet.</p>'}
          ${onDeck.length ? `<h3 class="on-deck-title">On deck</h3><div class="repertoire-grid on-deck" data-reorder-list="${key}">${onDeck.map(tuneCardMarkup).join("")}</div>` : ""}
        </section>`;
    }).join("");
  }

  function renderArchived() {
    const container = $("#repertoire-archived");
    if (!container) return;
    container.innerHTML = state.archived.length ? `
      <details class="repertoire-archived">
        <summary>Archived (${state.archived.length})</summary>
        <ul>${state.archived.map((tune) => `<li><a href="#repertoire/${encodeURIComponent(tune.tuneId)}">${escapeHTML(tune.title)}</a><span>${escapeHTML(practiceLine(tune))}</span><button type="button" class="button button-quiet" data-restore-tune="${escapeHTML(tune.tuneId)}">Restore</button></li>`).join("")}</ul>
      </details>` : "";
  }

  function render() {
    const screen = $("#repertoire");
    if (!screen) return;
    $$("[data-key-display]", screen).forEach((button) => button.setAttribute("aria-pressed", String(button.dataset.keyDisplay === state.display)));
    if (!state.loaded) {
      setStatus(state.error ? `Repertoire unavailable: ${state.error}` : "Loading your repertoire…", state.error ? "error" : "");
      return;
    }
    setStatus(online() ? "" : "Offline: changes not saved", online() ? "" : "error");
    $("#add-tune-button").disabled = !online();
    renderGoals();
    renderPace();
    renderSetReadiness();
    renderUnlinked();
    renderCategories();
    renderArchived();
    if (state.detailTuneId && $("#tune-dialog")?.open) renderDetail();
  }

  /* ---------- Practice now ---------- */

  function focusPreset(key) {
    return DATA.repertoireFocusPresets.find((preset) => preset.key === key) || DATA.repertoireFocusPresets[0];
  }

  function startPractice(tuneId, { focus = "", keepDaily = false } = {}) {
    const tune = byId(tuneId);
    if (!tune || tune.archivedAt) return Promise.reject(new Error("Restore this tune before practicing it"));
    const plan = globalThis.JazzTodayPlan;
    if (!plan?.addTuneBlock) return Promise.reject(new Error("Today's plan is still loading"));
    const definition = M.practiceBlockDefinition(tune, focusPreset(focus || M.suggestFocus(tune)), { keepDaily });
    return plan.addTuneBlock(definition, tune);
  }

  function openPracticeDialog(tuneId) {
    const tune = byId(tuneId);
    const dialog = $("#tune-practice-dialog");
    if (!tune || !dialog) return;
    const suggested = M.suggestFocus(tune);
    dialog.dataset.tuneId = tuneId;
    $("#tune-practice-title").textContent = tune.title;
    $("#tune-practice-focus").innerHTML = DATA.repertoireFocusPresets.map((preset) => `
      <label class="focus-option"><input type="radio" name="tune-focus" value="${preset.key}" ${preset.key === suggested ? "checked" : ""}><span><strong>${escapeHTML(preset.label)}${preset.key === suggested ? " · suggested" : ""}</strong><small>${escapeHTML(preset.instructions)}</small></span></label>`).join("");
    $("#tune-practice-keep").checked = false;
    $("#tune-practice-status").textContent = "";
    dialog.showModal();
    $("input[name='tune-focus']:checked", dialog)?.focus();
  }

  /* ---------- tune detail drawer ---------- */

  function openDetail(tuneId) {
    const dialog = $("#tune-dialog");
    if (!dialog) return;
    if (!dialog.open) state.detailOpener = document.activeElement;
    state.detailTuneId = tuneId;
    state.detailTab = "details";
    state.history = null;
    renderDetail();
    if (!dialog.open) dialog.showModal();
  }

  function closeDetailRoute() {
    if (location.hash.startsWith("#repertoire/")) history.replaceState(null, "", "#repertoire");
    const opener = state.detailOpener;
    const tuneId = state.detailTuneId;
    state.detailTuneId = "";
    state.detailOpener = null;
    if (opener?.isConnected) opener.focus();
    else $(`[data-tune-card="${CSS.escape(tuneId)}"] [data-tune-details]`)?.focus();
  }

  function milestoneSelector(tune, milestone, readonly) {
    const current = tune.milestones?.[milestone.key] || "not_started";
    return `
      <fieldset class="milestone-selector" data-milestone-field="${milestone.key}">
        <legend>${escapeHTML(milestone.label)}</legend>
        ${M.STATUSES.map((status) => `<button type="button" data-set-milestone="${milestone.key}" data-value="${status}" aria-pressed="${status === current}" ${readonly ? "disabled" : ""}>${{ not_started: "Not started", learning: "Learning", solid: "Solid" }[status]}</button>`).join("")}
      </fieldset>`;
  }

  function deepChecklist(tune) {
    const items = [
      ["Melody by ear", tune.milestones?.melodyByEar === "solid"],
      [`Two or more keys${tune.keysKnown?.length ? ` (${keysLabel(tune)})` : ""}`, (tune.keysKnown || []).length >= 2],
      ["Lyrics", tune.milestones?.lyrics === "solid"],
      ["One great recording transcribed", tune.milestones?.transcription === "solid"],
    ];
    return `<ul class="deep-checklist" aria-label="Deeply learned checklist">${items.map(([label, done]) => `<li data-done="${done}"><span aria-hidden="true">${done ? "✓" : "○"}</span>${escapeHTML(label)}<span class="visually-hidden">${done ? " done" : " not yet"}</span></li>`).join("")}</ul>`;
  }

  function keyEditor(tune, readonly) {
    return `
      <div class="key-editor" data-key-editor>
        <div class="key-chips">${(tune.keysKnown || []).map((key) => `<span class="key-chip">${escapeHTML(displayKey(key))}${readonly ? "" : `<button type="button" data-remove-key="${escapeHTML(key)}" aria-label="Remove ${escapeHTML(displayKey(key))}">×</button>`}</span>`).join("") || '<span class="key-empty">No keys yet</span>'}</div>
        ${readonly ? "" : `<form class="key-add" data-key-add><input type="text" maxlength="12" placeholder="+ key (concert)" aria-label="Add a concert key such as Bb or Gm" autocomplete="off"><button type="submit">Add</button></form>`}
      </div>`;
  }

  function renderDetail() {
    const dialog = $("#tune-dialog");
    const body = $("#tune-dialog-body");
    const tune = byId(state.detailTuneId);
    if (!dialog || !body) return;
    if (!tune) {
      body.innerHTML = state.loaded ? '<p class="repertoire-empty">This tune is not in your repertoire.</p>' : '<p class="repertoire-empty">Loading…</p>';
      return;
    }
    // Saves re-render the drawer; keep whatever is being typed and where the caret is.
    const focused = document.activeElement && body.contains(document.activeElement) ? document.activeElement : null;
    const focusKey = focused?.dataset?.focusKey || (focused?.name ? `name:${focused.name}` : "");
    const typing = focused?.matches("input[type='text'],input[type='url'],input:not([type]),textarea")
      ? { value: focused.value, start: focused.selectionStart, end: focused.selectionEnd } : null;
    const readonly = !online();
    $("#tune-dialog-title").textContent = tune.title;
    $("#tune-dialog-kicker").textContent = `${categoryLabel(tune.category)}${tune.archivedAt ? " · archived" : ""}`;
    $$("[data-tune-tab]", dialog).forEach((tab) => tab.setAttribute("aria-selected", String(tab.dataset.tuneTab === state.detailTab)));
    if (state.detailTab === "history") {
      body.innerHTML = historyMarkup(tune);
      wireHistory(body);
      if (!state.history || state.history.tuneId !== tune.tuneId) loadHistory(tune.tuneId);
      return;
    }
    const value = (field) => escapeHTML(tune[field] || "");
    body.innerHTML = `
      <div class="tune-detail-summary">
        <div class="tune-badges">${badgesMarkup(tune)}</div>
        <p>${escapeHTML(practiceLine(tune))} · ${referenceLink(tune)}</p>
        <div class="tune-detail-actions">
          ${tune.archivedAt ? `<button type="button" class="button button-primary" data-restore-tune="${escapeHTML(tune.tuneId)}">Restore</button>` : `<button type="button" class="button button-primary" data-practice-tune="${escapeHTML(tune.tuneId)}">Practice now</button>`}
          <span class="section-sync" data-tune-sync data-tone="saved">${readonly ? "Offline: not saved" : "Cloud synced"}</span>
        </div>
      </div>
      <form class="tune-detail-form" data-tune-form>
        <label class="wide"><span>Title</span><input name="title" maxlength="160" required value="${value("title")}" ${readonly ? "disabled" : ""}></label>
        <label><span>Category</span><select name="category" ${readonly ? "disabled" : ""}>${M.CATEGORIES.map((item) => `<option value="${item.key}" ${item.key === tune.category ? "selected" : ""}>${item.label}</option>`).join("")}</select></label>
        <label class="checkbox"><input type="checkbox" name="chosen" ${tune.chosen ? "checked" : ""} ${readonly ? "disabled" : ""}><span>Counts toward the goal</span></label>
        <label><span>Reference artist</span><input name="referenceArtist" maxlength="160" value="${value("referenceArtist")}" ${readonly ? "disabled" : ""}></label>
        <label><span>Reference recording</span><input name="referenceTitle" maxlength="160" value="${value("referenceTitle")}" ${readonly ? "disabled" : ""}></label>
        <label class="wide"><span>Reference link (https)</span><input name="referenceUrl" type="url" maxlength="500" pattern="https://.*" placeholder="https://" value="${value("referenceUrl")}" ${readonly ? "disabled" : ""}></label>
        <label><span>Original key (concert)</span><input name="concertKey" maxlength="12" placeholder="e.g. Bb" value="${value("concertKey")}" ${readonly ? "disabled" : ""}></label>
      </form>
      <section class="tune-detail-section"><h3>Keys known <small>${state.display === "bb-trumpet" ? "written for B♭ trumpet" : "concert"}</small></h3>${keyEditor(tune, readonly)}</section>
      <section class="tune-detail-section"><h3>Milestones</h3><div class="milestone-selectors">${M.MILESTONES.filter((item) => item.key !== "keysKnown").map((item) => milestoneSelector(tune, item, readonly)).join("")}</div></section>
      <section class="tune-detail-section"><h3>Deeply learned ${tune.deeplyLearned ? "· ✓" : ""}</h3>${deepChecklist(tune)}</section>
      <label class="tune-notes"><span>Notes</span><textarea name="notes" maxlength="8000" rows="5" ${readonly ? "disabled" : ""} placeholder="Form, tricky spots, what the reference recording does…">${escapeHTML(tune.notes || "")}</textarea></label>
      <div class="tune-archive-row">${tune.archivedAt ? "" : `<button type="button" class="button button-quiet" data-archive-tune="${escapeHTML(tune.tuneId)}">Archive tune</button><small>Archiving keeps all practice history and takes.</small>`}</div>`;
    wireDetail(body, tune);
    if (focusKey) {
      const target = focusKey.startsWith("name:") ? $(`[name="${focusKey.slice(5)}"]`, body) : $(`[data-focus-key="${focusKey}"]`, body);
      target?.focus();
      if (target && typing) {
        target.value = typing.value;
        target.setSelectionRange?.(typing.start, typing.end);
      }
    }
  }

  function wireDetail(body, tune) {
    const form = $("[data-tune-form]", body);
    $$("input,select", form).forEach((input) => {
      input.addEventListener("change", () => {
        const field = input.name;
        let next = input.type === "checkbox" ? input.checked : input.value.trim();
        if (field === "concertKey" && next) {
          next = M.normalizeKey(next);
          if (!next) { toast("Use a key like C, Bb, F# or Gm"); input.value = tune.concertKey || ""; return; }
        }
        if (field === "title" && !next) { input.value = tune.title; return; }
        if (field === "referenceUrl" && next && !/^https:\/\//.test(next)) { toast("Reference links must start with https://"); return; }
        if (next === (tune[field] ?? (input.type === "checkbox" ? false : ""))) return;
        patchTune(tune.tuneId, { [field]: next }).catch(() => {});
      });
    });
    form.addEventListener("submit", (event) => event.preventDefault());
    const notes = $("textarea[name='notes']", body);
    notes?.addEventListener("input", () => {
      setDetailSync("Saving…", "saving");
      clearTimeout(state.noteTimer);
      state.noteTimer = setTimeout(() => saveNotes(tune.tuneId, notes.value), 900);
    });
    notes?.addEventListener("blur", () => { clearTimeout(state.noteTimer); saveNotes(tune.tuneId, notes.value); });
    $$("[data-set-milestone]", body).forEach((button) => button.addEventListener("click", () => {
      button.dataset.focusKey = `${button.dataset.setMilestone}-${button.dataset.value}`;
      if (button.getAttribute("aria-pressed") === "true") return;
      patchTune(tune.tuneId, { milestones: { [button.dataset.setMilestone]: button.dataset.value } }).catch(() => {});
    }));
    $$("[data-set-milestone]", body).forEach((button) => { button.dataset.focusKey = `${button.dataset.setMilestone}-${button.dataset.value}`; });
    wireKeyEditor(body, tune, "");
    $("[data-archive-tune]", body)?.addEventListener("click", () => {
      if (!confirm(`Archive ${tune.title}? It leaves your goals and set list; practice history and takes stay.`)) return;
      patchTune(tune.tuneId, { archived: true }).then(() => toast(`${tune.title} archived`)).catch(() => {});
    });
  }

  function saveNotes(tuneId, value) {
    const tune = byId(tuneId);
    if (!tune || (tune.notes || "") === value) { setDetailSync("Saved", "saved"); return; }
    patchTune(tuneId, { notes: value }).catch(() => {});
  }

  function wireKeyEditor(root, tune, practiceBlockId) {
    $$("[data-remove-key]", root).forEach((button) => button.addEventListener("click", () => {
      patchTune(tune.tuneId, { keysKnown: tune.keysKnown.filter((key) => key !== button.dataset.removeKey) }, { practiceBlockId }).catch(() => {});
    }));
    const form = $("[data-key-add]", root);
    form?.addEventListener("submit", (event) => {
      event.preventDefault();
      const input = $("input", form);
      const result = M.addKey(tune.keysKnown || [], input.value);
      if (result.error) { toast(result.error); return; }
      input.value = "";
      patchTune(tune.tuneId, { keysKnown: result.keys }, { practiceBlockId }).then(() => toast(`${tune.title}: + ${displayKey(result.keys.at(-1))}`)).catch(() => {});
    });
  }

  async function loadHistory(tuneId, before = "") {
    const request = ++state.historyRequest;
    try {
      const result = await api(`/repertoire/tunes/${encodeURIComponent(tuneId)}/history?limit=30${before ? `&before=${before}` : ""}`);
      if (request !== state.historyRequest) return;
      const days = before && state.history?.tuneId === tuneId ? [...state.history.days, ...result.days] : result.days;
      state.history = { ...result, tuneId, days };
    } catch (error) {
      if (request !== state.historyRequest) return;
      state.history = { tuneId, error: error.message, days: [], events: [] };
    }
    if (state.detailTuneId === tuneId && state.detailTab === "history") renderDetail();
  }

  function eventLabel(event) {
    const p = event.payload || {};
    const statusLabel = (value) => ({ not_started: "Not started", learning: "Learning", solid: "Solid" }[value] || value);
    const milestone = M.MILESTONES.find((item) => item.key === p.field)?.label || p.field;
    switch (event.eventType) {
      case "milestone.changed": return `${milestone}: ${statusLabel(p.from)} → ${statusLabel(p.to)}`;
      case "key.added": return `+ key ${displayKey(p.key)}`;
      case "key.removed": return `− key ${displayKey(p.key)}`;
      case "tune.archived": return "Archived";
      case "tune.restored": return "Restored";
      case "tune.created": return "Added to repertoire";
      case "tune.seeded": return "Added from the starter list";
      case "tune.legacy_imported": return `Imported from the old roadmap (stage ${p.stage})`;
      case "tune.updated": return p.field === "notes" ? "Notes edited" : `${p.field}: ${p.from === "" ? "—" : p.from} → ${p.to === "" ? "—" : p.to}`;
      default: return event.eventType;
    }
  }

  function dayLabel(dateKey) {
    const date = new Date(`${dateKey}T12:00:00`);
    return Number.isNaN(date.valueOf()) ? dateKey : date.toLocaleDateString(undefined, { weekday: "short", month: "short", day: "numeric", year: "numeric" });
  }

  function historyMarkup(tune) {
    const history = state.history?.tuneId === tune.tuneId ? state.history : null;
    if (!history) return '<p class="repertoire-empty">Loading history…</p>';
    if (history.error) return `<p class="repertoire-empty">Could not load history: ${escapeHTML(history.error)}</p>`;
    const drills = history.drills?.drillCount ? `<p class="tune-drills">Guide-tone drills: ${history.drills.drillCount} sessions · ${history.drills.accuracy}% accuracy over ${history.drills.attemptCount} answers · ${(history.drills.averageResponseMs / 1000).toFixed(1)} s average.</p>` : "";
    const events = (history.events || []).filter((event) => !(event.eventType === "tune.updated" && event.payload?.field === "position"));
    const days = history.days.map((day) => `
      <article class="tune-history-day">
        <header><a href="#archive/${day.practiceDate}">${escapeHTML(dayLabel(day.practiceDate))}</a><span>${day.minutes} min · ${day.recordings.length} take${day.recordings.length === 1 ? "" : "s"}</span></header>
        ${day.blocks.map((block) => `<div class="tune-history-block"><strong>${escapeHTML(block.title)}${block.removed ? " <small>(removed from plan)</small>" : ""}</strong><span>${M.formatPracticeTime(block.elapsedMs)}</span>${block.notes ? `<p>${escapeHTML(block.notes)}</p>` : ""}</div>`).join("")}
        ${day.recordings.length ? `<div class="tune-history-takes">${day.recordings.map((recording) => `<a href="#archive/${day.practiceDate}/${recording.id}" ${recording.status === "ready" ? "" : 'aria-disabled="true"'}>▶ Take ${recording.takeNumber || ""} · ${Math.max(1, Math.round(recording.durationMs / 1000))} s${recording.notes ? ` · ${escapeHTML(recording.notes)}` : ""}</a>`).join("")}</div>` : ""}
      </article>`).join("");
    return `
      ${drills}
      <section class="tune-history-section"><h3>Practice days</h3>${days || '<p class="repertoire-empty">No practice linked to this tune yet. Use Practice now to start.</p>'}${history.nextBefore ? `<button type="button" class="button button-quiet" data-history-more="${history.nextBefore}">Load earlier days</button>` : ""}</section>
      <section class="tune-history-section"><h3>Changes</h3>${events.length ? `<ol class="tune-events">${events.map((event) => `<li><time datetime="${escapeHTML(event.occurredAt)}">${escapeHTML(new Date(event.occurredAt).toLocaleDateString(undefined, { month: "short", day: "numeric" }))}</time><span>${escapeHTML(eventLabel(event))}</span>${event.practiceDate ? `<a href="#archive/${event.practiceDate}">from practice</a>` : ""}</li>`).join("")}</ol>` : '<p class="repertoire-empty">No changes yet.</p>'}</section>`;
  }

  function wireHistory(body) {
    $("[data-history-more]", body)?.addEventListener("click", (event) => loadHistory(state.detailTuneId, event.currentTarget.dataset.historyMore));
    $$("a[href^='#archive']", body).forEach((link) => link.addEventListener("click", () => $("#tune-dialog")?.close()));
  }

  /* ---------- add tune ---------- */

  let createCallback = null;
  function openCreate({ tuneId = "", title: initialTitle = "", category = "ballad", onCreated = null } = {}) {
    const dialog = $("#tune-create-dialog");
    const form = $("#tune-create-form");
    if (!dialog || !form) return;
    form.reset();
    form.elements.tuneId.value = tuneId;
    form.elements.title.value = initialTitle;
    form.elements.category.value = category;
    form.elements.chosen.checked = category !== "standard";
    $("#tune-create-status").innerHTML = tuneId ? `Adopts existing practice tagged <code>${escapeHTML(tuneId)}</code>.` : "";
    createCallback = onCreated;
    dialog.showModal();
    form.elements.title.focus();
  }

  async function restoreTune(tuneId) {
    if (!state.loaded) await load();
    const tune = byId(tuneId);
    if (!tune) return;
    await patchTune(tuneId, { archived: false });
    toast(`${tune.title} restored`);
  }

  function setupCreateDialog() {
    const dialog = $("#tune-create-dialog");
    const form = $("#tune-create-form");
    if (!dialog || !form) return;
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const status = $("#tune-create-status");
      const submit = $("button[type='submit']", form);
      const fields = {
        title: form.elements.title.value.trim(),
        category: form.elements.category.value,
        chosen: form.elements.chosen.checked,
        referenceArtist: form.elements.referenceArtist.value.trim(),
        concertKey: M.normalizeKey(form.elements.concertKey.value),
      };
      if (form.elements.concertKey.value.trim() && !fields.concertKey) { status.textContent = "Use a key like C, Bb, F# or Gm."; return; }
      if (form.elements.tuneId.value) fields.tuneId = form.elements.tuneId.value;
      if (!fields.title) { status.textContent = "Give the tune a title."; return; }
      submit.disabled = true;
      status.textContent = "Adding…";
      try {
        const tune = await createTune(fields);
        dialog.close();
        toast(`${tune.title} added to ${categoryLabel(tune.category).toLowerCase()}`);
        const callback = createCallback;
        createCallback = null;
        if (callback) callback(tune);
      } catch (error) {
        if (error.status === 409 && error.body?.archived) {
          status.innerHTML = `${escapeHTML(error.body.title || "This tune")} is archived. <button type="button" class="button button-quiet" data-restore-conflict="${escapeHTML(error.body.tuneId)}">Restore it</button>`;
          $("[data-restore-conflict]", status).addEventListener("click", async () => {
            await restoreTune(error.body.tuneId).catch(() => {});
            dialog.close();
            const callback = createCallback;
            createCallback = null;
            if (callback && byId(error.body.tuneId)) callback(byId(error.body.tuneId));
          });
        } else {
          status.textContent = `Could not add: ${error.message}`;
        }
      } finally {
        submit.disabled = false;
      }
    });
    $("#cancel-tune-create")?.addEventListener("click", () => dialog.close());
  }

  /* ---------- reorder ---------- */

  async function persistOrder(category) {
    const ordered = $$(`[data-reorder-list="${category}"] [data-tune-card]`).map((card) => card.dataset.tuneCard);
    const changes = ordered.map((tuneId, position) => ({ tuneId, position })).filter(({ tuneId, position }) => byId(tuneId)?.position !== position);
    for (const change of changes) await patchTune(change.tuneId, { position: change.position }).catch(() => {});
  }

  function moveCard(card, delta) {
    const list = card.parentElement;
    const cards = [...list.children];
    const index = cards.indexOf(card);
    const target = Math.max(0, Math.min(cards.length - 1, index + delta));
    if (target === index) return false;
    list.insertBefore(card, delta > 0 ? cards[target].nextSibling : cards[target]);
    return true;
  }

  function wireReorder(screen) {
    screen.addEventListener("keydown", (event) => {
      const handle = event.target.closest?.("[data-tune-reorder]");
      if (!handle || !["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"].includes(event.key)) return;
      event.preventDefault();
      const card = handle.closest("[data-tune-card]");
      if (!moveCard(card, event.key === "ArrowUp" || event.key === "ArrowLeft" ? -1 : 1)) return;
      const category = card.dataset.category;
      const tuneId = card.dataset.tuneCard;
      persistOrder(category).then(() => {
        $(`[data-tune-card="${CSS.escape(tuneId)}"] [data-tune-reorder]`)?.focus();
        toast(`${title(tuneId)} moved`);
      });
    });
    screen.addEventListener("pointerdown", (event) => {
      const handle = event.target.closest?.("[data-tune-reorder]");
      if (!handle || event.button !== 0) return;
      const card = handle.closest("[data-tune-card]");
      const list = card.parentElement;
      event.preventDefault();
      handle.setPointerCapture(event.pointerId);
      card.classList.add("dragging");
      let moved = false;
      const move = (moveEvent) => {
        const over = document.elementFromPoint(moveEvent.clientX, moveEvent.clientY)?.closest("[data-tune-card]");
        if (!over || over === card || over.parentElement !== list) return;
        const bounds = over.getBoundingClientRect();
        const after = moveEvent.clientX > bounds.left + bounds.width / 2 || moveEvent.clientY > bounds.bottom - 8;
        list.insertBefore(card, after ? over.nextSibling : over);
        moved = true;
      };
      const end = () => {
        handle.removeEventListener("pointermove", move);
        handle.removeEventListener("pointerup", end);
        handle.removeEventListener("pointercancel", end);
        card.classList.remove("dragging");
        if (moved) persistOrder(card.dataset.category).then(() => toast(`${title(card.dataset.tuneCard)} moved`));
      };
      handle.addEventListener("pointermove", move);
      handle.addEventListener("pointerup", end);
      handle.addEventListener("pointercancel", end);
    });
  }

  /* ---------- Today integration ---------- */

  // A compact tune card for a linked section on Today. Milestone changes made
  // here carry the practice block so history links back to that day.
  function renderTuneCard(container, tuneId, practiceBlockId, { onUnlink = null } = {}) {
    if (!container) return;
    container.dataset.tuneProgress = tuneId;
    container.dataset.practiceBlockId = practiceBlockId || "";
    container._onUnlink = onUnlink;
    if (!state.loaded) {
      container.innerHTML = state.error
        ? `<p class="tune-progress-loading">Tune progress unavailable: ${escapeHTML(state.error)}</p>`
        : '<p class="tune-progress-loading">Loading tune progress…</p>';
      // Retry only after a successful load; a failure waits for the next refresh.
      if (!state.loading && state.error) return;
      load().then(() => { if (container.isConnected) renderTuneCard(container, tuneId, practiceBlockId, { onUnlink }); });
      return;
    }
    const tune = byId(tuneId);
    if (!tune) {
      container.innerHTML = `<div class="tune-progress-card"><span class="tune-progress-kicker">Tune progress</span><p>This section is linked to <code>${escapeHTML(tuneId)}</code>, which is not in your repertoire. <a href="#repertoire">Add it from Unlinked practice</a>.</p></div>`;
      return;
    }
    const readonly = Boolean(tune.archivedAt) || !online();
    container.innerHTML = `
      <div class="tune-progress-card${tune.archivedAt ? " archived" : ""}">
        <div class="tune-progress-head">
          <span class="tune-progress-kicker">Tune progress</span>
          <a href="#repertoire/${encodeURIComponent(tune.tuneId)}"><strong>${escapeHTML(tune.title)}${tune.archivedAt ? " (archived)" : ""}</strong></a>
          <small>${escapeHTML(practiceLine(tune))}</small>
        </div>
        ${tune.archivedAt ? `<p>This tune is archived, so its progress is read-only. <a href="#repertoire/${encodeURIComponent(tune.tuneId)}">Open it to restore</a>.</p>` : ""}
        <div class="milestone-chips">${M.MILESTONES.filter((item) => item.key !== "keysKnown").map((milestone) => readonly ? chipMarkup(tune, milestone) : chipMarkup(tune, milestone, { interactive: true })).join("")}</div>
        ${keyEditor(tune, readonly)}
        <details class="tune-progress-deep"><summary>Deeply learned ${tune.deeplyLearned ? "✓" : `· ${[tune.milestones.melodyByEar === "solid", tune.keysKnown.length >= 2, tune.milestones.lyrics === "solid", tune.milestones.transcription === "solid"].filter(Boolean).length}/4`}</summary>${deepChecklist(tune)}</details>
        <div class="tune-progress-foot"><span class="section-sync" data-tune-sync data-tone="saved">${!online() ? "Offline: changes not saved" : "Changes save to the tune and this day"}</span>${onUnlink ? '<button type="button" data-unlink-tune>Unlink</button>' : ""}</div>
      </div>`;
    $$("[data-milestone]", container).forEach((button) => button.addEventListener("click", () => {
      const key = button.dataset.milestone;
      const next = M.nextMilestoneStatus(tune.milestones[key]);
      patchTune(tune.tuneId, { milestones: { [key]: next } }, { practiceBlockId }).then(() => {
        $(`[data-milestone="${key}"]`, container)?.focus();
      }).catch(() => {});
    }));
    wireKeyEditor(container, tune, practiceBlockId);
    $("[data-unlink-tune]", container)?.addEventListener("click", () => onUnlink?.());
  }

  function refreshTuneCards() {
    $$("[data-tune-progress]").forEach((container) => {
      if (container.contains(document.activeElement) && document.activeElement.matches("input")) return;
      renderTuneCard(container, container.dataset.tuneProgress, container.dataset.practiceBlockId, { onUnlink: container._onUnlink });
    });
  }

  function tuneOptionsMarkup(selected = "", { includeNew = false, placeholder = "" } = {}) {
    const groups = M.CATEGORIES.map(({ key, label }) => {
      const tunes = state.tunes.filter((tune) => tune.category === key);
      return tunes.length ? `<optgroup label="${escapeHTML(label)}">${tunes.map((tune) => `<option value="${escapeHTML(tune.tuneId)}" ${tune.tuneId === selected ? "selected" : ""}>${escapeHTML(tune.title)}${tune.chosen ? "" : " (on deck)"}</option>`).join("")}</optgroup>` : "";
    }).join("");
    return `${placeholder ? `<option value="">${escapeHTML(placeholder)}</option>` : ""}${groups}${includeNew ? '<option value="__new">+ New tune…</option>' : ""}`;
  }

  // "Link to a tune" for a repertoire section that has no tune yet.
  function renderLinkPicker(container, onChoose) {
    if (!container) return;
    if (!state.loaded) {
      container.innerHTML = "";
      if (!state.loading && state.error) return;
      load().then(() => { if (container.isConnected) renderLinkPicker(container, onChoose); });
      return;
    }
    container.innerHTML = `<label class="tune-link-picker"><span>Link to a tune</span><select ${online() ? "" : "disabled"}>${tuneOptionsMarkup("", { includeNew: true, placeholder: "Not linked" })}</select></label>`;
    const select = $("select", container);
    select.addEventListener("change", () => {
      if (select.value === "__new") {
        select.value = "";
        openCreate({ onCreated: (tune) => onChoose(tune.tuneId) });
        return;
      }
      if (select.value) onChoose(select.value);
    });
  }

  /* ---------- routing ---------- */

  function route() {
    const match = /^#repertoire\/([a-z0-9-]{1,48})/.exec(location.hash);
    load().then(() => {
      if (match) openDetail(match[1]);
      else if ($("#tune-dialog")?.open) $("#tune-dialog").close();
    });
  }

  function setup() {
    const screen = $("#repertoire");
    if (!screen) return;
    screen.addEventListener("click", (event) => {
      const practice = event.target.closest("[data-tune-practice]");
      if (practice) { openPracticeDialog(practice.closest("[data-tune-card]").dataset.tuneCard); return; }
      const quick = event.target.closest("[data-practice-tune]");
      if (quick) {
        if (quick.dataset.focus) startPractice(quick.dataset.practiceTune, { focus: quick.dataset.focus }).catch((error) => toast(error.message));
        else openPracticeDialog(quick.dataset.practiceTune);
        return;
      }
      const adopt = event.target.closest("[data-adopt-tune]");
      if (adopt) { openCreate({ tuneId: adopt.dataset.adoptTune, title: humanizeSlug(adopt.dataset.adoptTune), category: "standard" }); return; }
      const restore = event.target.closest("[data-restore-tune]");
      if (restore) restoreTune(restore.dataset.restoreTune).catch(() => {});
      const display = event.target.closest("[data-key-display]");
      if (display) { writeDisplay(display.dataset.keyDisplay); render(); refreshTuneCards(); }
    });
    $("#add-tune-button")?.addEventListener("click", () => openCreate());
    wireReorder(screen);

    const dialog = $("#tune-dialog");
    dialog?.addEventListener("close", () => {
      clearTimeout(state.noteTimer);
      const notes = $("textarea[name='notes']", dialog);
      if (notes && state.detailTuneId) saveNotes(state.detailTuneId, notes.value);
      closeDetailRoute();
    });
    dialog?.addEventListener("click", (event) => {
      const tab = event.target.closest("[data-tune-tab]");
      if (tab) { state.detailTab = tab.dataset.tuneTab; renderDetail(); return; }
      const practice = event.target.closest("[data-practice-tune]");
      if (practice) { dialog.close(); openPracticeDialog(practice.dataset.practiceTune); return; }
      const restore = event.target.closest("[data-restore-tune]");
      if (restore) restoreTune(restore.dataset.restoreTune).catch(() => {});
    });

    const practiceDialog = $("#tune-practice-dialog");
    $("#tune-practice-form")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const status = $("#tune-practice-status");
      const focus = $("input[name='tune-focus']:checked", practiceDialog)?.value || "";
      status.textContent = "Adding to today’s plan…";
      try {
        await startPractice(practiceDialog.dataset.tuneId, { focus, keepDaily: $("#tune-practice-keep").checked });
        practiceDialog.close();
      } catch (error) {
        status.textContent = error.message;
      }
    });
    $("#cancel-tune-practice")?.addEventListener("click", () => practiceDialog.close());
    setupCreateDialog();
    addEventListener("online", () => { render(); refreshTuneCards(); });
    addEventListener("offline", () => { render(); refreshTuneCards(); });
    addEventListener("jazz:recordings-changed", () => invalidate());
    // Load quietly for Today's tune cards and stats; the view renders when opened.
    load();
  }

  globalThis.JazzRepertoire = {
    load,
    tunes: () => state.tunes,
    loaded: () => state.loaded,
    byId,
    title,
    invalidate,
    startPractice,
    route,
    openCreate,
    renderTuneCard,
    renderLinkPicker,
    tuneOptionsMarkup,
  };
  setup();
})();
