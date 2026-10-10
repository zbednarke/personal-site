/* Workbench: pure logic shared by the browser and node tests. No DOM.
 * The thread state is a fold over the server's event log (applyEvent), so
 * every device that has seen the same events shows the same thing. The
 * outbox queues sends while offline and replays them with the same client
 * ids, which the server deduplicates. */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.WorkbenchModel = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  const API = "/workbench/api/v1/workbench";

  // ---- Layout and device --------------------------------------------------------

  /** Bottom sheet on phones, split view on tablets, side panel on desktops. */
  function layoutFor(width) {
    if (width < 700) return "sheet";
    if (width < 1100) return "split";
    return "panel";
  }

  function deviceKind(width, coarse) {
    if (width < 700) return "phone";
    if (width < 1100 || (coarse && width < 1400)) return "tablet";
    return "desktop";
  }

  function newId(prefix) {
    const rand = (globalThis.crypto && globalThis.crypto.randomUUID) ? globalThis.crypto.randomUUID() : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`;
    return `${prefix || "c"}-${rand.replace(/-/g, "")}`.slice(0, 64);
  }

  function clip(text, n) {
    text = String(text || "").replace(/\s+/g, " ").trim();
    return text.length > n ? `${text.slice(0, n - 1)}…` : text;
  }

  function localDate(d) {
    const p = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }

  /** The page context sent with each message: where the owner is and what he sees. */
  function pageContext(env) {
    const w = env.width || 0, h = env.height || 0;
    const extra = (env.hook && typeof env.hook === "function" && safe(env.hook)) || {};
    return {
      page: clip(env.pathname || "/", 200),
      title: clip(env.title, 200),
      view: clip(extra.view || (env.hash || "").replace(/^#/, "") || "", 200),
      selection: clip(extra.selection || env.selection || "", 1500),
      app: clip(extra.app || "", 1500),
      device: deviceKind(w, env.coarse),
      viewport: `${w}x${h}`,
      pointer: env.coarse ? "touch" : "mouse",
      localDate: localDate(env.now || new Date()),
      timezone: clip(env.timezone || "", 64),
    };
  }

  function safe(fn) {
    try {
      const v = fn();
      return v && typeof v === "object" ? v : null;
    } catch {
      return null;
    }
  }

  // ---- Thread state as a fold over events --------------------------------------------

  function createState() {
    return { thread: null, lastEventId: 0, items: [], byId: new Map(), approvals: new Map(), draft: { text: "", rev: 0, deviceId: "", at: 0 }, running: false, steps: [], notice: "", spend: null, seq: 0 };
  }

  function addItem(state, item) {
    state.seq += 1;
    item.seq = item.seq || state.seq;
    state.items.push(item);
    if (item.id) state.byId.set(item.id, item);
    return item;
  }

  function messageItem(m) {
    return { kind: "message", id: m.id, role: m.role, text: m.text || "", status: m.status || "done", clientId: m.clientId || "", attachments: m.attachments || [], createdAt: m.createdAt, effort: m.effort, costUsd: m.costUsd };
  }

  /** Load a snapshot (GET /threads/{id}); events after lastEventId follow. */
  function applySnapshot(state, snap) {
    const fresh = createState();
    Object.assign(state, fresh);
    state.thread = snap.thread;
    state.lastEventId = snap.thread.lastEventId || 0;
    state.running = !!snap.thread.running;
    for (const m of snap.messages || []) addItem(state, messageItem(m));
    const approvals = (snap.approvals || []).slice().sort((a, b) => String(a.createdAt).localeCompare(String(b.createdAt)));
    for (const a of approvals) {
      state.approvals.set(a.id, a);
      // Place each card after the last message created before it.
      const at = state.items.filter((i) => i.kind === "message" && String(i.createdAt) <= String(a.createdAt)).pop();
      const item = { kind: "approval", id: `approval:${a.id}`, approvalId: a.id };
      if (at) {
        const idx = state.items.indexOf(at);
        item.seq = at.seq + 0.5;
        state.items.splice(idx + 1, 0, item);
        state.byId.set(item.id, item);
      } else addItem(state, item);
    }
    state.items.sort((a, b) => a.seq - b.seq);
    if (snap.draft) state.draft = { text: snap.draft.text || "", rev: snap.draft.rev || 0, deviceId: snap.draft.deviceId || "", at: Date.parse(snap.draft.updatedAt) || 0 };
    if (snap.spend) state.spend = { monthUsd: snap.spend.spentUsd, capUsd: snap.spend.capUsd, blocked: snap.spend.blocked, threadUsd: snap.thread.spendUsd };
    return state;
  }

  /** Apply one event. Returns false for an event already applied (dedupe by id). */
  function applyEvent(state, ev, now) {
    if (!ev || typeof ev.id !== "number" || ev.id <= state.lastEventId) return false;
    state.lastEventId = ev.id;
    const p = ev.payload || {};
    switch (ev.type) {
      case "message.created": {
        const m = p.message;
        if (!m) break;
        const optimistic = state.items.find((i) => i.kind === "message" && i.pending && i.clientId && i.clientId === m.clientId);
        if (optimistic) {
          state.byId.delete(optimistic.id);
          Object.assign(optimistic, messageItem(m), { pending: false });
          state.byId.set(m.id, optimistic);
        } else if (!state.byId.has(m.id)) addItem(state, messageItem(m));
        break;
      }
      case "message.started":
        if (p.message && !state.byId.has(p.message.id)) addItem(state, messageItem(p.message));
        break;
      case "message.delta": {
        const m = state.byId.get(p.id);
        if (m) m.text += p.text || "";
        break;
      }
      case "message.reset": {
        const m = state.byId.get(p.id);
        if (m) m.text = "";
        break;
      }
      case "message.completed": {
        const m = state.byId.get(p.id);
        if (m) Object.assign(m, { text: p.text != null ? p.text : m.text, status: p.status || "done", costUsd: p.costUsd });
        break;
      }
      case "tool.started":
        state.steps.push({ toolUseId: p.toolUseId, name: p.name, done: false });
        break;
      case "tool.finished": {
        const step = state.steps.find((s) => s.toolUseId === p.toolUseId);
        if (step) Object.assign(step, { done: true, ok: p.ok, error: p.error });
        if (p.card) addItem(state, { kind: "card", id: `card:${p.toolUseId}`, card: p.card, ok: p.ok });
        break;
      }
      case "approval.requested":
        if (p.approval) {
          state.approvals.set(p.approval.id, p.approval);
          if (!state.byId.has(`approval:${p.approval.id}`)) addItem(state, { kind: "approval", id: `approval:${p.approval.id}`, approvalId: p.approval.id });
        }
        break;
      case "approval.resolved": {
        const a = state.approvals.get(p.id);
        if (a) Object.assign(a, { status: p.status, decidedBy: p.decidedBy });
        break;
      }
      case "approval.result":
        if (p.approval) state.approvals.set(p.approval.id, p.approval);
        break;
      case "draft.updated":
        state.draft = { text: p.text || "", rev: p.rev || 0, deviceId: p.deviceId || "", at: now || Date.now() };
        break;
      case "run.started":
        state.running = true;
        state.steps = [];
        state.notice = "";
        break;
      case "run.finished":
        state.running = false;
        state.notice = p.status === "done" ? (p.message || "") : (p.message || `Stopped (${p.status}).`);
        state.noticeStatus = p.status;
        break;
      case "spend.updated":
        state.spend = { monthUsd: p.monthUsd, capUsd: p.capUsd, blocked: p.blocked, threadUsd: p.threadUsd };
        if (state.thread) state.thread.spendUsd = p.threadUsd;
        break;
      case "thread.updated":
        if (state.thread && p.title != null) state.thread.title = p.title;
        break;
    }
    return true;
  }

  /** An optimistic bubble for a message that is queued or in flight. */
  function addPending(state, item) {
    return addItem(state, { kind: "message", id: `pending:${item.clientId}`, role: "user", text: item.text, clientId: item.clientId, pending: true, status: item.status || "queued", attachments: (item.attachments || []).map((a) => ({ kind: a.kind, contentType: a.contentType, durationMs: a.durationMs, local: true })) });
  }

  /** Visible timeline: hides empty finished assistant turns (pure tool steps). */
  function timeline(state) {
    return state.items.filter((i) => !(i.kind === "message" && i.role === "assistant" && i.status === "done" && !i.text.trim()));
  }

  function pendingApprovals(state) {
    let n = 0;
    for (const a of state.approvals.values()) if (a.status === "pending") n++;
    return n;
  }

  // ---- Drafts ----------------------------------------------------------------------

  /**
   * Decide what the composer shows when another device saves the draft.
   * local: { text, synced (last text we saved or adopted), focused }.
   * The owner's unsaved typing on this device always wins; otherwise the
   * newest remote draft is adopted.
   */
  function mergeDraft(local, remote, ownDevice) {
    if (!remote || remote.deviceId === ownDevice) return { text: local.text, adopt: false };
    const dirty = local.text !== local.synced;
    if (dirty && local.focused) return { text: local.text, adopt: false };
    return { text: remote.text, adopt: remote.text !== local.text };
  }

  /** "Typing on another device" while a remote draft changed in the last few seconds. */
  function remoteTyping(state, ownDevice, now) {
    const d = state.draft;
    return !!(d && d.deviceId && d.deviceId !== ownDevice && d.text && now - d.at < 6000);
  }

  // ---- Outbox ----------------------------------------------------------------------

  const RETRYABLE = new Set([408, 425, 429, 500, 502, 503, 504]);

  function backoff(attempt) {
    return Math.min(60000, 1000 * Math.pow(2, Math.max(0, attempt - 1)));
  }

  /**
   * The offline outbox. storage: { all(): Promise<item[]>, put(item), delete(id) }.
   * transport: { upload(item, att) → {id}, post(item, attachmentIds) → message }.
   * Items keep their client ids for life, so replays are deduplicated server-side.
   */
  function createOutbox(storage, transport, opts) {
    opts = opts || {};
    const listeners = new Set();
    let flushing = null;
    const emit = async () => {
      const items = await storage.all();
      for (const fn of listeners) fn(items);
    };
    async function enqueue(item) {
      const full = Object.assign({ status: "queued", attempts: 0, createdAt: Date.now(), attachments: [] }, item);
      for (const a of full.attachments) a.clientId = a.clientId || newId("a");
      await storage.put(full);
      await emit();
      return full;
    }
    async function flushOnce() {
      const items = (await storage.all()).filter((i) => i.status !== "failed").sort((a, b) => a.createdAt - b.createdAt);
      let sent = 0;
      for (const item of items) {
        if (item.notBefore && item.notBefore > Date.now()) break; // keep order
        try {
          item.status = "sending";
          await storage.put(item);
          await emit();
          const ids = [];
          for (const att of item.attachments) {
            if (!att.serverId) {
              const res = await transport.upload(item, att);
              att.serverId = res.id;
              await storage.put(item);
            }
            ids.push(att.serverId);
          }
          const message = await transport.post(item, ids);
          await storage.delete(item.clientId);
          sent++;
          if (opts.onSent) opts.onSent(item, message);
        } catch (err) {
          const status = err && err.status;
          item.attempts += 1;
          if (status && !RETRYABLE.has(status)) {
            item.status = "failed";
            item.error = (err && err.message) || `HTTP ${status}`;
          } else {
            item.status = "queued";
            item.notBefore = Date.now() + backoff(item.attempts);
          }
          await storage.put(item);
          await emit();
          if (item.status === "queued") break; // offline: later items wait
        }
      }
      await emit();
      return sent;
    }
    function flush() {
      if (!flushing) flushing = flushOnce().finally(() => { flushing = null; });
      return flushing;
    }
    async function retry(clientId) {
      const items = await storage.all();
      const item = items.find((i) => i.clientId === clientId);
      if (!item) return;
      Object.assign(item, { status: "queued", notBefore: 0, error: "" });
      await storage.put(item);
      return flush();
    }
    async function discard(clientId) {
      await storage.delete(clientId);
      await emit();
    }
    return { enqueue, flush, retry, discard, items: () => storage.all(), subscribe: (fn) => (listeners.add(fn), () => listeners.delete(fn)) };
  }

  function memoryStorage() {
    const map = new Map();
    const copy = (v) => JSON.parse(JSON.stringify(v));
    return {
      all: async () => [...map.values()].map(copy),
      put: async (item) => { map.set(item.clientId, copy(item)); },
      delete: async (id) => { map.delete(id); },
    };
  }

  function outboxLabel(items) {
    const waiting = items.filter((i) => i.status !== "failed").length;
    const failed = items.length - waiting;
    const parts = [];
    if (waiting) parts.push(`${waiting} waiting to send`);
    if (failed) parts.push(`${failed} could not be sent`);
    return parts.join(" · ");
  }

  // ---- Event stream bookkeeping ----------------------------------------------------------

  /** URL for the event stream: resume after the last applied event. */
  function streamURL(threadId, lastEventId) {
    return `${API}/threads/${encodeURIComponent(threadId)}/events?after=${Math.max(0, lastEventId | 0)}`;
  }

  // ---- Text --------------------------------------------------------------------------

  function escapeHTML(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }

  function inline(s) {
    return s
      .replace(/`([^`]+)`/g, "<code>$1</code>")
      .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
      .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>")
      .replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+|\/[^\s)]*)\)/g, (m, label, href) => `<a href="${href}" target="${href.startsWith("/") ? "_self" : "_blank"}" rel="noopener noreferrer">${label}</a>`)
      .replace(/(^|[\s(])(https?:\/\/[^\s<)]+)/g, (m, pre, href) => `${pre}<a href="${href}" target="_blank" rel="noopener noreferrer">${href}</a>`);
  }

  /** A small, safe Markdown subset: paragraphs, lists, code, emphasis, links. Input is escaped first. */
  function renderMarkdown(text) {
    const lines = escapeHTML(text || "").split("\n");
    const out = [];
    let list = null, para = [], code = null;
    const flushPara = () => { if (para.length) out.push(`<p>${inline(para.join("<br>"))}</p>`); para = []; };
    const flushList = () => { if (list) out.push(`<${list.tag}>${list.items.map((i) => `<li>${inline(i)}</li>`).join("")}</${list.tag}>`); list = null; };
    for (const line of lines) {
      if (code !== null) {
        if (/^```/.test(line)) { out.push(`<pre><code>${code.join("\n")}</code></pre>`); code = null; } else code.push(line);
        continue;
      }
      if (/^```/.test(line)) { flushPara(); flushList(); code = []; continue; }
      const ul = line.match(/^\s*[-*]\s+(.*)$/), ol = line.match(/^\s*\d+[.)]\s+(.*)$/);
      if (ul || ol) {
        flushPara();
        const tag = ul ? "ul" : "ol";
        if (!list || list.tag !== tag) { flushList(); list = { tag, items: [] }; }
        list.items.push((ul || ol)[1]);
        continue;
      }
      if (!line.trim()) { flushPara(); flushList(); continue; }
      const h = line.match(/^#{1,4}\s+(.*)$/);
      if (h) { flushPara(); flushList(); out.push(`<p><strong>${inline(h[1])}</strong></p>`); continue; }
      flushList();
      para.push(line);
    }
    if (code !== null) out.push(`<pre><code>${code.join("\n")}</code></pre>`);
    flushPara();
    flushList();
    return out.join("");
  }

  function formatUSD(v) {
    if (v == null || isNaN(v)) return "";
    return v < 0.01 && v > 0 ? "<$0.01" : `$${v.toFixed(2)}`;
  }

  function spendLine(spend) {
    if (!spend) return "";
    const parts = [];
    if (spend.threadUsd != null) parts.push(`This thread ${formatUSD(spend.threadUsd)}`);
    if (spend.monthUsd != null) parts.push(`month ${formatUSD(spend.monthUsd)} of ${formatUSD(spend.capUsd)}`);
    return parts.join(" · ") + (spend.blocked ? " · paused: budget used" : "");
  }

  const TOOL_LABEL = {
    triage_idea: "Sorting the idea", github_list_issues: "Reading issues", github_create_issue: "Drafting an issue", github_comment: "Drafting a comment",
    site_status: "Checking CI, PRs and deploys", jazz_today: "Reading today's plan", jazz_repertoire: "Reading the repertoire",
    jazz_add_practice_block: "Proposing a practice section", jazz_mark_tune: "Proposing a tune update", jazz_add_note: "Adding a note", commonplace_capture: "Saving a Moment",
  };

  function stepLabel(step) {
    const label = TOOL_LABEL[step.name] || step.name;
    return step.done ? (step.ok ? label : `${label}: failed`) : `${label}…`;
  }

  const TRIAGE = { do_now: "Do now", spec_it: "Spec it", keep_it: "Keep it", ask_me: "Ask me" };

  function lightFor(run) {
    if (!run) return "unknown";
    if (run.status && run.status !== "completed") return "running";
    if (run.conclusion === "success") return "green";
    if (run.conclusion === "failure" || run.conclusion === "timed_out" || run.conclusion === "startup_failure") return "red";
    return "neutral";
  }

  /** Is this keypress the Workbench shortcut (backtick outside text fields)? */
  function isToggleKey(e) {
    if (e.key !== "`" || e.metaKey || e.ctrlKey || e.altKey) return false;
    const t = e.target;
    const tag = t && t.tagName ? t.tagName.toLowerCase() : "";
    return !(tag === "input" || tag === "textarea" || tag === "select" || (t && t.isContentEditable));
  }

  return {
    API, layoutFor, deviceKind, newId, clip, pageContext, localDate,
    createState, applySnapshot, applyEvent, addPending, timeline, pendingApprovals,
    mergeDraft, remoteTyping, backoff, createOutbox, memoryStorage, outboxLabel, streamURL,
    escapeHTML, renderMarkdown, formatUSD, spendLine, stepLabel, TRIAGE, lightFor, isToggleKey,
  };
});
