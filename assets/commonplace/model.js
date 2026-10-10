/* Commonplace: pure logic shared by the browser and node tests. No DOM.
 * Time in the owner's zone, the hour's light, the wall label, capture parsing,
 * margin layout, thread geometry, search snippets and import batching. */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.CommonplaceModel = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  const KINDS = ["conversation", "dream", "idea", "quote"];
  const KIND_LABEL = { conversation: "Conversation", dream: "Dream", idea: "Idea", quote: "Quote" };
  const SOURCES = ["imessage", "discord", "voice", "text", "link", "other"];
  const SOURCE_LABEL = { imessage: "iMessage", discord: "Discord", voice: "Voice", text: "Text", link: "Link", other: "Other" };
  const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
  const WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

  // ---- Time in a zone ---------------------------------------------------------

  const partsCache = new Map();
  function formatterFor(tz) {
    let f = partsCache.get(tz);
    if (!f) {
      try {
        f = new Intl.DateTimeFormat("en-US", { timeZone: tz, hourCycle: "h23", year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric", weekday: "short" });
      } catch {
        f = new Intl.DateTimeFormat("en-US", { timeZone: "UTC", hourCycle: "h23", year: "numeric", month: "numeric", day: "numeric", hour: "numeric", minute: "numeric", weekday: "short" });
      }
      partsCache.set(tz, f);
    }
    return f;
  }

  /** Wall-clock parts of an instant in an IANA zone. */
  function localParts(iso, tz) {
    const d = iso instanceof Date ? iso : new Date(iso);
    if (Number.isNaN(d.getTime())) return null;
    const out = {};
    for (const p of formatterFor(tz || "UTC").formatToParts(d)) out[p.type] = p.value;
    const wd = { Sun: 0, Mon: 1, Tue: 2, Wed: 3, Thu: 4, Fri: 5, Sat: 6 }[out.weekday];
    const hour = Number(out.hour) % 24, minute = Number(out.minute);
    return { year: Number(out.year), month: Number(out.month), day: Number(out.day), hour, minute, weekday: wd, hourFloat: hour + minute / 60 };
  }

  function formatHour(p) {
    if (!p) return { hm: "–", ap: "" };
    const h12 = p.hour % 12 || 12;
    return { hm: `${h12}:${String(p.minute).padStart(2, "0")}`, ap: p.hour < 12 ? "am" : "pm" };
  }
  const hourText = (p) => {
    const h = formatHour(p);
    return `${h.hm} ${h.ap}`;
  };
  function formatDate(p, opts = {}) {
    if (!p) return "";
    const s = `${opts.short ? WEEKDAYS[p.weekday].slice(0, 3) : WEEKDAYS[p.weekday]} ${p.day} ${opts.short ? MONTHS[p.month - 1].slice(0, 3) : MONTHS[p.month - 1]}`;
    return opts.noYear ? s : `${s} ${p.year}`;
  }

  /** "23 min", "6 h 29 min", "3 days". */
  function duration(ms) {
    const min = Math.round(Math.abs(ms) / 60000);
    if (min < 1) return "less than a minute";
    if (min < 60) return `${min} min`;
    if (min < 48 * 60) {
      const h = Math.floor(min / 60), m = min % 60;
      return m ? `${h} h ${m} min` : `${h} h`;
    }
    const days = Math.round(min / 1440);
    return days < 60 ? `${days} days` : days < 730 ? `${Math.round(days / 30.4)} months` : `${Math.round(days / 365.25)} years`;
  }

  /** How long ago, by calendar day in the Moment's zone. */
  function relative(iso, tz, now = new Date()) {
    const a = localParts(iso, tz), b = localParts(now, tz);
    if (!a || !b) return "";
    const days = Math.round((Date.UTC(b.year, b.month - 1, b.day) - Date.UTC(a.year, a.month - 1, a.day)) / 86400000);
    if (days === 0) return "today";
    if (days === 1) return "yesterday";
    if (days < 0) return "ahead";
    if (days < 45) return `${days} days ago`;
    if (days < 365) return `${Math.round(days / 30.4)} months ago`;
    const y = Math.floor(days / 365.25);
    return y === 1 ? "a year ago" : `${y} years ago`;
  }

  const pad2 = (n) => String(n).padStart(2, "0");
  function tzOffsetMs(instantMs, tz) {
    const p = localParts(new Date(instantMs), tz);
    return Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute) - Math.floor(instantMs / 60000) * 60000;
  }
  /** A wall-clock "YYYY-MM-DDTHH:MM" in a zone to an ISO instant. */
  function zonedToISO(local, tz) {
    const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(String(local || ""));
    if (!m) return "";
    const guess = Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5]);
    let t = guess - tzOffsetMs(guess, tz);
    t = guess - tzOffsetMs(t, tz);
    return new Date(t).toISOString();
  }
  /** An ISO instant as a datetime-local value in a zone. */
  function isoToLocalInput(iso, tz) {
    const p = iso ? localParts(iso, tz) : null;
    return p ? `${p.year}-${pad2(p.month)}-${pad2(p.day)}T${pad2(p.hour)}:${pad2(p.minute)}` : "";
  }

  // ---- The hour lights the page ------------------------------------------------

  // Phases of the day. Night, predawn, lamplit and dream are dark in every
  // theme: 3 am is dark whatever the reader prefers.
  const PHASES = [
    [0, "night"], [4.5, "predawn"], [6.5, "dawn"], [8.5, "morning"], [12, "day"], [16.5, "golden"], [19, "dusk"], [21, "lamplit"],
  ];
  const PHASE_NOTE = {
    night: "Deep night.", predawn: "Before dawn.", dawn: "Dawn light.", morning: "Morning.", day: "Daylight.",
    golden: "Late afternoon, low sun.", dusk: "Dusk.", lamplit: "Lamplit evening.", dream: "A dream, kept in the dark it came from.",
  };
  const ALWAYS_DARK = new Set(["night", "predawn", "lamplit", "dream"]);

  function lightFor(hourFloat, kind) {
    if (kind === "dream") return "dream";
    if (hourFloat == null || Number.isNaN(hourFloat)) return "day";
    const h = ((hourFloat % 24) + 24) % 24;
    let phase = "night";
    for (const [start, name] of PHASES) if (h >= start) phase = name;
    return phase;
  }

  /** 24-hour dial: midnight at the bottom, noon at the top, night shaded. */
  function dialSVG(hourFloat) {
    const c = 22, r = 17;
    const pt = (hh, rr) => {
      const a = (hh / 24) * Math.PI * 2 + Math.PI;
      return [c + rr * Math.sin(a), c - rr * Math.cos(a)];
    };
    const f = (n) => n.toFixed(2);
    let ticks = "";
    for (let i = 0; i < 24; i++) {
      const big = i % 6 === 0;
      const [x1, y1] = pt(i, r + 3), [x2, y2] = pt(i, big ? r - 1 : r + 1);
      ticks += `<line x1="${f(x1)}" y1="${f(y1)}" x2="${f(x2)}" y2="${f(y2)}" stroke="currentColor" stroke-width="${big ? 1.2 : 0.7}" opacity="${big ? 0.9 : 0.5}"/>`;
    }
    const [ax, ay] = pt(18, r - 4), [bx, by] = pt(6, r - 4);
    const h = hourFloat == null ? 12 : hourFloat;
    const [hx, hy] = pt(h, r - 4);
    return `<svg class="dial" viewBox="0 0 44 44" aria-hidden="true"><circle cx="22" cy="22" r="21" fill="none" stroke="var(--hair)"/>${ticks}` +
      `<path d="M${f(ax)} ${f(ay)} A ${r - 4} ${r - 4} 0 0 0 ${f(bx)} ${f(by)}" fill="none" stroke="currentColor" stroke-width="3.5" opacity=".18" stroke-linecap="round"/>` +
      `<line x1="22" y1="22" x2="${f(hx)}" y2="${f(hy)}" stroke="var(--ink)" stroke-width="1.3" stroke-linecap="round"/>` +
      `<circle cx="${f(hx)}" cy="${f(hy)}" r="3.2" fill="var(--lamp)"/><circle cx="22" cy="22" r="1.6" fill="var(--ink)"/></svg>`;
  }

  // ---- The wall label ------------------------------------------------------------

  function momentInstant(m) {
    return m.occurredAt || m.createdAt;
  }

  /** What the label says about the hour, the span and what happened next. */
  function wallLabel(moment, lines = [], now = new Date()) {
    const tz = moment.timezone || "UTC";
    const at = momentInstant(moment);
    const p = localParts(at, tz);
    const phase = lightFor(p && p.hourFloat, moment.kind);
    const out = {
      phase, parts: p, hour: formatHour(p), date: formatDate(p), relative: relative(at, tz, now), tz,
      captured: !moment.occurredAt, note: PHASE_NOTE[phase], span: "", after: "",
    };
    if (moment.occurredAt && moment.endedAt) {
      const e = localParts(moment.endedAt, tz);
      const ms = new Date(moment.endedAt) - new Date(moment.occurredAt);
      if (ms >= 60000) {
        const sameDay = e && p && e.year === p.year && e.month === p.month && e.day === p.day;
        out.span = `${hourText(p)} to ${sameDay ? "" : formatDate(e, { short: true, noYear: e.year === p.year }) + ", "}${hourText(e)} · ${duration(ms)}`;
      }
    }
    // "You replied …": the first line of yours after someone else's, if it came later.
    const timed = lines.filter((l) => l.at).sort((a, b) => new Date(a.at) - new Date(b.at));
    const firstOther = timed.find((l) => l.speaker !== "me");
    if (firstOther) {
      const reply = timed.find((l) => l.speaker === "me" && new Date(l.at) > new Date(firstOther.at));
      if (reply) {
        const gap = new Date(reply.at) - new Date(firstOther.at);
        if (gap >= 30 * 60000) {
          const rp = localParts(reply.at, tz);
          out.after = `You replied at ${hourText(rp)}, ${duration(gap)} later${lightFor(rp.hourFloat) !== lightFor(localParts(firstOther.at, tz).hourFloat) ? `, by ${PHASE_NOTE[lightFor(rp.hourFloat)].replace(/\.$/, "").toLowerCase()}` : ""}`;
        }
      }
    }
    return out;
  }

  // ---- People ----------------------------------------------------------------------

  function initials(name) {
    const parts = String(name || "?").trim().split(/\s+/).filter(Boolean);
    if (!parts.length) return "?";
    return (parts[0][0] + (parts.length > 1 ? parts[parts.length - 1][0] : "")).toUpperCase();
  }
  /** A stable hue per person id, for avatars. */
  function hue(id) {
    let h = 2166136261;
    for (const ch of String(id || "")) h = Math.imul(h ^ ch.charCodeAt(0), 16777619) >>> 0;
    return h % 360;
  }
  function withLabel(people) {
    const names = (people || []).filter((p) => p.role !== "mentioned").map((p) => p.name);
    if (!names.length) return "Just you";
    if (names.length === 1) return `${names[0]} and you`;
    return `${names.slice(0, -1).join(", ")}, ${names[names.length - 1]} and you`;
  }
  const isSpecial = (moment) => (moment.people || []).some((p) => p.special);

  // ---- Capture ----------------------------------------------------------------------

  const URL_RE = /\bhttps?:\/\/[^\s<>"']+/i;
  /** Split pasted or shared text into a link and the words around it. */
  function parseCapture(input) {
    const raw = String(input || "").trim();
    if (!raw) return { url: "", text: "" };
    const m = raw.match(URL_RE);
    if (!m) {
      if (/^[\w-]+(\.[\w-]+)+\/\S*$/.test(raw) && !/\s/.test(raw)) return { url: "https://" + raw, text: "" };
      return { url: "", text: raw };
    }
    const url = m[0].replace(/[).,;!?]+$/, "");
    const rest = (raw.slice(0, m.index) + raw.slice(m.index + url.length)).trim();
    return { url, text: rest };
  }

  /** The phone share-sheet route: #add?url=…&text=…&title=… */
  function parseShareHash(hash) {
    const h = String(hash || "").replace(/^#/, "");
    if (!h.startsWith("add")) return null;
    const q = new URLSearchParams(h.slice(3).replace(/^\?/, ""));
    const shared = [q.get("text") || "", q.get("url") || ""].join(" ").trim();
    const parsed = parseCapture(shared);
    return { url: parsed.url, text: parsed.text, title: (q.get("title") || "").trim() };
  }

  function route(hash) {
    const h = String(hash || "").replace(/^#/, "");
    const [path, query = ""] = h.split("?");
    const q = Object.fromEntries(new URLSearchParams(query));
    if (path.startsWith("m/")) return { name: "moment", id: path.slice(2), q };
    if (path === "add") return { name: "add", q };
    if (path === "import") return { name: "import", q };
    if (path === "space") return { name: "space", q };
    return { name: "index", q };
  }

  // ---- Margin layout ----------------------------------------------------------------

  /** Notes sit beside their anchors, pushed down to avoid each other. */
  function layoutNotes(items, gap = 10) {
    let floor = -Infinity;
    return items
      .map((it, i) => ({ ...it, i }))
      .sort((a, b) => a.anchorTop - b.anchorTop || a.i - b.i)
      .map((it) => {
        const top = Math.max(it.anchorTop, floor);
        floor = top + it.height + gap;
        return { i: it.i, top };
      })
      .sort((a, b) => a.i - b.i)
      .map((x) => x.top);
  }

  function visibleNotes(annotations) {
    return (annotations || []).filter((n) => n.state !== "erased");
  }
  const pencilCount = (annotations) => visibleNotes(annotations).filter((n) => n.state === "pencil").length;

  // ---- Thread geometry ----------------------------------------------------------------

  /** Knots on a true time scale; long silences are drawn as dotted gaps. */
  function threadGeometry(knots, opts = {}) {
    const pad = opts.pad == null ? 4 : opts.pad;
    const gapMs = opts.gapMs || 3 * 3600000;
    const ts = knots.map((k) => (k.at ? new Date(k.at).getTime() : NaN));
    const valid = ts.filter((t) => !Number.isNaN(t));
    const min = valid.length ? Math.min(...valid) : 0, max = valid.length ? Math.max(...valid) : 1;
    const span = max - min;
    // Compress long silences so the knots stay readable: each gap counts as at most
    // a fifth of the total, and is marked.
    const order = knots.map((k, i) => i).sort((a, b) => (ts[a] || 0) - (ts[b] || 0) || a - b);
    const weights = [];
    for (let j = 1; j < order.length; j++) {
      const d = (ts[order[j]] || 0) - (ts[order[j - 1]] || 0);
      weights.push(Math.max(d, 0));
    }
    const total = weights.reduce((s, w) => s + w, 0);
    const cap = total * 0.2;
    const eased = weights.map((w) => (w > gapMs && w > cap ? cap + Math.log1p((w - cap) / 3600000) * 3600000 : w));
    const easedTotal = eased.reduce((s, w) => s + w, 0) || 1;
    const x = new Array(knots.length);
    let acc = 0;
    if (order.length) x[order[0]] = pad;
    for (let j = 1; j < order.length; j++) {
      acc += eased[j - 1];
      x[order[j]] = order.length === 1 ? 50 : pad + (acc / easedTotal) * (100 - pad * 2);
    }
    if (knots.length === 1) x[0] = 50;
    if (total === 0 && knots.length > 1) order.forEach((i, j) => (x[i] = pad + (j / (order.length - 1)) * (100 - pad * 2)));
    const gaps = [];
    for (let j = 1; j < order.length; j++) {
      const d = weights[j - 1];
      if (d > gapMs) gaps.push({ from: x[order[j - 1]], to: x[order[j]], label: duration(d) });
    }
    return { x, gaps, span };
  }

  /** The thread's wave: y (0–100) at x (0–100). */
  const waveY = (x) => 50 + Math.sin((x / 100) * Math.PI * 3.1 + 0.6) * 22;

  // ---- Search snippets ------------------------------------------------------------------

  /** The server marks hits with U+E000 … U+E001; split them for safe rendering. */
  function splitSnippet(s) {
    const out = [];
    let mark = false;
    let buf = "";
    for (const ch of String(s || "")) {
      if (ch === "" || ch === "") {
        if (buf) out.push({ text: buf, mark });
        buf = "";
        mark = ch === "";
      } else buf += ch;
    }
    if (buf) out.push({ text: buf, mark });
    return out;
  }

  // ---- Line metadata ----------------------------------------------------------------------

  function metaBits(meta) {
    const m = meta || {};
    const bits = [];
    if (m.edited) bits.push("Edited");
    if (m.pinned) bits.push("Pinned");
    if (m.replies) bits.push(typeof m.replies === "number" ? `${m.replies} ${m.replies === 1 ? "Reply" : "Replies"}` : String(m.replies));
    if (m.note) bits.push(String(m.note));
    const reactions = (Array.isArray(m.reactions) ? m.reactions : []).map((r) => (typeof r === "string" ? r : `${r.emoji || ""}${r.count > 1 ? " " + r.count : ""}`)).filter(Boolean);
    return { bits, reactions, replyTo: m.replyTo || "", replyToName: m.replyToName || "" };
  }

  // ---- Import helpers ------------------------------------------------------------------------

  /** Split files into upload batches under a byte budget (Cloud Run caps requests at 32 MB). */
  function batchFiles(files, limit = 24 * 1024 * 1024, reserve = 0) {
    const batches = [];
    let cur = [], size = reserve;
    for (const f of files) {
      if (cur.length && size + f.size > limit) {
        batches.push(cur);
        cur = [];
        size = reserve;
      }
      cur.push(f);
      size += f.size;
    }
    if (cur.length) batches.push(cur);
    return batches;
  }

  /** Pick the DiscordChatExporter JSON from a folder listing (shallowest .json). */
  function pickExportFile(paths) {
    return paths
      .filter((p) => /\.json$/i.test(p) && !/(^|\/)\./.test(p))
      .sort((a, b) => a.split("/").length - b.split("/").length || a.localeCompare(b))[0] || "";
  }

  function suggestKind(channelName) {
    return /dream/i.test(channelName || "") ? "dream" : "conversation";
  }

  function newId() {
    const b = new Uint8Array(16);
    (globalThis.crypto || require("node:crypto").webcrypto).getRandomValues(b);
    b[6] = (b[6] & 0x0f) | 0x40;
    b[8] = (b[8] & 0x3f) | 0x80;
    const h = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
    return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
  }

  /** A UUID derived from a string, so the same share link always names the same capture. */
  function captureIdFor(seed) {
    const words = [0, 1, 2, 3].map((salt) => {
      let h = 2166136261 ^ salt;
      const text = `${salt}|${seed}`;
      for (let i = 0; i < text.length; i++) h = Math.imul(h ^ text.charCodeAt(i), 16777619) >>> 0;
      h ^= h >>> 15; h = Math.imul(h, 0x2c1b3c6d) >>> 0; h ^= h >>> 12;
      return (h >>> 0).toString(16).padStart(8, "0");
    });
    const hex = words.join("").split("");
    hex[12] = "4";
    hex[16] = "89ab"[parseInt(hex[16], 16) & 3];
    const h = hex.join("");
    return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
  }

  function excerpt(s, n = 160) {
    s = String(s || "").replace(/\s+/g, " ").trim();
    if (s.length <= n) return s;
    const cut = s.lastIndexOf(" ", n - 1);
    return s.slice(0, cut > n * 0.5 ? cut : n - 1) + "…";
  }

  return {
    KINDS, KIND_LABEL, SOURCES, SOURCE_LABEL, MONTHS, WEEKDAYS, ALWAYS_DARK, PHASE_NOTE,
    localParts, zonedToISO, isoToLocalInput, formatHour, hourText, formatDate, duration, relative, lightFor, dialSVG, wallLabel, momentInstant,
    initials, hue, withLabel, isSpecial, parseCapture, parseShareHash, route, layoutNotes, visibleNotes, pencilCount,
    threadGeometry, waveY, splitSnippet, metaBits, batchFiles, pickExportFile, suggestKind, newId, captureIdFor, excerpt,
  };
});
