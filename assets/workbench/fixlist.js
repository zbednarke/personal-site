/* Workbench: the daily fix list page section (#fixlist on any private page).
 * The pure page model (fixListModel) is shared with the node tests; the view
 * is a full-screen panel inside the Workbench root, built with createElement
 * and textContent only. workbench.js loads this file when the hash is
 * #fixlist. All data comes from GET /workbench/api/v1/workbench/fixlist. */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.WorkbenchFixList = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  const SECTIONS = [
    ["run", "Trumpet runs"],
    ["api", "API: 5xx and slow responses"],
    ["checks", "Failed checks"],
    ["sources", "Search sources"],
    ["market", "Budgets and caps"],
    ["films", "Magic Film"],
  ];

  /** How a count moved against the previous 24 hours. */
  function change(count, previous) {
    count = Number(count) || 0;
    previous = Number(previous) || 0;
    if (count === 0 && previous > 0) return { trend: "fixed", text: `fixed (was ${previous})` };
    if (previous === 0 && count > 0) return { trend: "new", text: "new" };
    if (count > previous) return { trend: "up", text: `+${count - previous}` };
    if (count < previous) return { trend: "down", text: `−${previous - count}` };
    return { trend: "same", text: "same" };
  }

  function utcTime(iso) {
    const t = Date.parse(iso || "");
    if (!t) return "";
    const d = new Date(t);
    const p = (n) => String(n).padStart(2, "0");
    return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())} UTC`;
  }

  function item(raw) {
    const c = change(raw.count, raw.previous);
    return {
      key: String(raw.key || ""), label: String(raw.label || ""), count: Number(raw.count) || 0, previous: Number(raw.previous) || 0,
      trend: c.trend, change: c.text, domains: Array.isArray(raw.domains) ? raw.domains.map(String) : [], info: !!raw.info, score: Number(raw.score) || 0,
    };
  }

  function runLine(run) {
    if (!run) return "";
    if (run.missing) return `No daily run for ${run.expectedDay} (expected at 13:17 UTC).`;
    if (run.running) return `Daily run ${run.expectedDay}: running since ${utcTime(run.startedAt)}.`;
    if (run.unfinished) return `Daily run ${run.expectedDay}: started ${utcTime(run.startedAt)} but never stored.`;
    if (!run.found) return "";
    const status = { succeeded: "succeeded", partial: "partial", failed: "failed" }[run.status] || String(run.status || "unknown");
    return `Daily run ${run.expectedDay}: ${status}, started ${utcTime(run.startedAt)}.`;
  }

  /** The page model: verdict, Fix next, the run, sections with counts and changes. */
  function fixListModel(res) {
    const d = (res && res.digest) || {};
    const items = (Array.isArray(d.items) ? d.items : []).map((raw) => Object.assign(item(raw), { section: String(raw.section || "") }));
    const sections = SECTIONS.map(([id, title]) => {
      const list = items.filter((i) => i.section === id);
      // Things to fix first (by value), then fixed ones, then context.
      list.sort((a, b) => (a.info - b.info) || ((b.count > 0) - (a.count > 0)) || (b.score - a.score) || a.label.localeCompare(b.label));
      return { id, title, items: list, open: list.filter((i) => !i.info && i.count > 0).reduce((n, i) => n + i.count, 0) };
    }).filter((s) => s.items.length || s.id === "run");
    const next = d.fixNext ? item(d.fixNext) : null;
    return {
      day: String(d.day || ""),
      title: d.day ? `Fix list – ${d.day}` : "Fix list",
      verdict: String(d.verdict || ""),
      allClear: !!d.allClear,
      toFix: Number(d.toFix) || 0,
      live: !!(res && res.live),
      generated: utcTime(d.generatedAt),
      fixNext: next,
      run: { line: runLine(d.run), notes: ((d.run && d.run.notes) || []).map(String).slice(0, 12), missing: !!(d.run && d.run.missing), running: !!(d.run && d.run.running), unfinished: !!(d.run && d.run.unfinished), status: (d.run && d.run.status) || "" },
      sections,
      threadId: (res && res.threadId) || "",
      history: ((res && res.history) || []).map((h) => ({ day: String(h.day || ""), verdict: String(h.verdict || ""), allClear: !!h.allClear })),
    };
  }

  // ---- View (browser only) ------------------------------------------------------------

  function show(opts) {
    const { api, mount, el, openThread, onClose } = opts;
    let panel = mount.querySelector(".wb-fixlist");
    if (!panel) {
      panel = el("div", { class: "wb-fixlist", role: "region", "aria-labelledby": "wb-fixlist-title", tabindex: "-1" });
      mount.prepend(panel);
    }
    panel.hidden = false;
    document.documentElement.classList.add("wb-fixlist-open");
    const close = () => {
      panel.hidden = true;
      document.documentElement.classList.remove("wb-fixlist-open");
      if (onClose) onClose();
    };
    const head = (title) => el("header", { class: "wb-fl-head" },
      el("h1", { class: "wb-fl-title", id: "wb-fixlist-title" }, title),
      el("button", { class: "wb-icon wb-fl-close", type: "button", "aria-label": "Close the fix list", onclick: close }, "×"));
    panel.replaceChildren(head("Fix list"), el("p", { class: "wb-fl-muted" }, "Loading…"));
    api("/fixlist").then((res) => {
      const m = fixListModel(res);
      const body = el("div", { class: "wb-fl-body" });
      body.append(el("p", { class: `wb-fl-verdict${m.allClear ? " wb-fl-clear" : ""}` }, m.verdict || "No digest yet."));
      body.append(el("p", { class: "wb-fl-muted" }, m.live ? `Live view, ${m.generated}; not sent yet today.` : `Last 24 hours to ${m.generated}, against the 24 hours before.`));
      if (m.fixNext) {
        body.append(el("div", { class: "wb-fl-next" },
          el("div", { class: "wb-fl-kicker" }, "Fix next"),
          el("div", { class: "wb-fl-next-label" }, m.fixNext.label),
          el("div", { class: "wb-fl-muted" }, `${m.fixNext.count} in 24 h · ${m.fixNext.change}`)));
      }
      if (m.threadId && openThread) body.append(el("button", { class: "wb-chip wb-fl-thread", type: "button", onclick: () => openThread(m.threadId) }, "Ask Workbench to fix one"));
      for (const s of m.sections) {
        const sec = el("div", { class: "wb-fl-section", "data-section": s.id },
          el("h2", {}, s.title, s.open ? el("span", { class: "wb-fl-badge" }, String(s.open)) : null));
        if (s.id === "run") {
          if (m.run.line) sec.append(el("p", { class: `wb-fl-run${m.run.missing || m.run.unfinished || (m.run.status && !["succeeded", "running"].includes(m.run.status)) ? " wb-fl-warn" : ""}` }, m.run.line));
          if (m.run.notes.length) sec.append(el("ul", { class: "wb-fl-notes" }, m.run.notes.map((n) => el("li", {}, n))));
        }
        if (s.items.length) {
          sec.append(el("ul", { class: "wb-fl-items" }, s.items.map((i) => el("li", { class: `wb-fl-item${i.info ? " wb-fl-info" : ""}`, "data-trend": i.trend },
            el("span", { class: "wb-fl-count" }, String(i.count)),
            el("span", { class: "wb-fl-label" }, i.label, i.domains.length ? el("span", { class: "wb-fl-domains" }, i.domains.join(", ")) : null),
            el("span", { class: "wb-fl-change" }, i.change)))));
        } else if (s.id !== "run" || !m.run.line) sec.append(el("p", { class: "wb-fl-muted" }, "Nothing failed."));
        body.append(sec);
      }
      if (m.history.length > 1) {
        body.append(el("div", { class: "wb-fl-section" }, el("h2", {}, "Recent days"),
          el("ul", { class: "wb-fl-history" }, m.history.map((h) => el("li", { "data-clear": String(h.allClear) }, el("span", { class: "wb-fl-day" }, h.day), el("span", {}, h.verdict))))));
      }
      panel.replaceChildren(head(m.title), body);
      panel.focus({ preventScroll: true });
    }).catch((err) => {
      panel.replaceChildren(head("Fix list"), el("p", { class: "wb-fl-muted" }, `Could not load the fix list: ${err.message}`));
    });
    return { close };
  }

  return { fixListModel, change, show, SECTIONS };
});
