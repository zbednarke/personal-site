/* Idea space: a deterministic, stable layout computed in the browser from the
 * API's Moments, Ideas (margin notes of type idea), Threads and People.
 * Regions are nameless groups (by person, then thread, then kind) with stable
 * keys; the owner names them. Pure functions, no DOM. */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.CommonplaceSpaceModel = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";
  const TAU = Math.PI * 2;
  const GOLDEN = 2.399963229728653;

  function hash32(s) {
    let h = 2166136261;
    for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619) >>> 0;
    // final avalanche so similar keys spread
    h ^= h >>> 16; h = Math.imul(h, 0x85ebca6b) >>> 0; h ^= h >>> 13; h = Math.imul(h, 0xc2b2ae35) >>> 0; h ^= h >>> 16;
    return h >>> 0;
  }
  const unit = (s) => hash32(s) / 4294967296;

  // Soft region tints (linear RGB 0..1), chosen by key hash.
  const REGION_COLORS = [
    [0.56, 0.62, 0.98], [0.92, 0.66, 0.5], [0.52, 0.86, 0.78], [0.86, 0.58, 0.86], [0.74, 0.84, 0.52], [0.96, 0.78, 0.46], [0.6, 0.78, 0.96],
  ];

  function yearOf(iso) {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return 0;
    const y = d.getUTCFullYear();
    return y + (d - Date.UTC(y, 0, 1)) / (Date.UTC(y + 1, 0, 1) - Date.UTC(y, 0, 1));
  }

  /** The stable region key of a Moment: its main person, else a thread, else its kind. */
  function regionKey(m, threadOf) {
    const person = (m.senders && m.senders[0]) || (m.people && m.people[0]);
    if (person) return "person:" + person;
    const t = threadOf && threadOf.get(m.id);
    if (t) return "thread:" + t;
    return "kind:" + (m.kind || "conversation");
  }

  /**
   * layout(data) -> { items, regions, threads, bounds }
   * items: moments then ideas, each with x, y, r, year, type, kind, region …
   */
  function layout(data) {
    const moments = [...(data.moments || [])].sort((a, b) => (a.occurredAt || a.createdAt || "").localeCompare(b.occurredAt || b.createdAt || "") || String(a.id).localeCompare(String(b.id)));
    const people = new Map((data.people || []).map((p) => [p.id, p]));
    const names = new Map((data.regions || []).map((r) => [r.key, r.name]));
    const threadOf = new Map();
    for (const t of data.threads || []) for (const k of t.knots || []) if (!threadOf.has(k.momentId)) threadOf.set(k.momentId, t.id);

    // Group into regions.
    const groups = new Map();
    for (const m of moments) {
      const key = regionKey(m, threadOf);
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(m);
    }
    const keys = [...groups.keys()].sort();
    const regions = keys.map((key) => {
      const n = groups.get(key).length;
      const person = key.startsWith("person:") ? people.get(key.slice(7)) : null;
      return {
        key, name: names.get(key) || "", count: n, radius: 50 + Math.sqrt(n) * 34, x: 0, y: 0,
        col: REGION_COLORS[hash32(key) % REGION_COLORS.length], special: !!(person && person.special),
        kind: key.startsWith("kind:") ? key.slice(5) : "",
      };
    });
    // Region anchors: angle and distance come from the key alone (stable as the
    // archive grows), then a short deterministic relaxation keeps them apart.
    if (regions.length === 1) regions[0].x = regions[0].y = 0;
    else {
      const spread = 140 + Math.sqrt(regions.length) * 95;
      for (const r of regions) {
        const a = unit(r.key) * TAU, d = spread * (0.55 + 0.45 * unit(r.key + "#d"));
        r.x = Math.cos(a) * d;
        r.y = Math.sin(a) * d * 0.82;
      }
      for (let it = 0; it < 160; it++) {
        for (let i = 0; i < regions.length; i++) {
          for (let j = i + 1; j < regions.length; j++) {
            const a = regions[i], b = regions[j];
            let dx = b.x - a.x, dy = b.y - a.y;
            const min = a.radius + b.radius + 60;
            let d = Math.hypot(dx, dy);
            if (d >= min) continue;
            if (d < 0.01) { dx = Math.cos(i + j); dy = Math.sin(i + j); d = 1; }
            const push = ((min - d) / d) * 0.5;
            a.x -= dx * push; a.y -= dy * push; b.x += dx * push; b.y += dy * push;
          }
        }
        // a gentle pull to the centre keeps the basin round
        for (const r of regions) { r.x *= 0.995; r.y *= 0.995; }
      }
    }
    const regionByKey = new Map(regions.map((r) => [r.key, r]));

    // Moments on a sunflower spiral around their region, oldest at the centre.
    // Small archives get larger bubbles so a handful of memories still reads.
    const items = [];
    const grow = 1 + 0.9 * Math.max(0, Math.min(1, (40 - moments.length) / 36));
    const ideasByMoment = new Map();
    for (const i of data.ideas || []) {
      if (!ideasByMoment.has(i.momentId)) ideasByMoment.set(i.momentId, []);
      ideasByMoment.get(i.momentId).push(i);
    }
    for (const key of keys) {
      const R = regionByKey.get(key);
      const phase = unit(key + "#p") * TAU;
      groups.get(key).forEach((m, k) => {
        const a = phase + k * GOLDEN;
        const rad = k === 0 && groups.get(key).length === 1 ? 0 : 34 * grow * Math.sqrt(k + 0.6);
        const ps = (m.people || []).map((id) => people.get(id)).filter(Boolean);
        const r = (14 + Math.min(6, (m.lines || 0) / 10) + (m.images ? 2 : 0) + Math.min(4, (ideasByMoment.get(m.id) || []).length * 1.5)) * grow;
        items.push({
          id: m.id, type: "moment", kind: m.kind, title: m.title || "", excerpt: m.excerpt || "", at: m.occurredAt || m.createdAt, timezone: m.timezone,
          source: m.source, sourceDetail: m.sourceDetail || "", people: ps, region: key, year: yearOf(m.occurredAt || m.createdAt),
          special: ps.some((p) => p.special), dream: m.kind === "dream", thread: threadOf.get(m.id) || "",
          x: R.x + Math.cos(a) * rad, y: R.y + Math.sin(a) * rad, ax: 0, ay: 0, r,
        });
      });
    }
    // Ideas orbit their Moment.
    const momentIndex = new Map(items.map((it, i) => [it.id, i]));
    for (const [momentId, list] of ideasByMoment) {
      const mi = momentIndex.get(momentId);
      if (mi == null) continue;
      const m = items[mi];
      const phase = unit(momentId + "#i") * TAU;
      list.forEach((idea, k) => {
        const a = phase + (k / list.length) * TAU;
        const rad = m.r + 30 + (k % 2) * 12;
        items.push({
          id: idea.id, type: "idea", kind: "idea", title: idea.title || idea.body || "", excerpt: idea.body || "", moment: momentId, state: idea.state,
          at: m.at, timezone: m.timezone, people: m.people, region: m.region, year: m.year, special: m.special, dream: false, thread: m.thread,
          x: m.x + Math.cos(a) * rad, y: m.y + Math.sin(a) * rad, ax: 0, ay: 0, r: 9,
        });
      });
    }
    for (const it of items) { it.ax = it.x; it.ay = it.y; }
    relax(items, items.length > 1500 ? 18 : items.length > 400 ? 40 : 90);

    // Region extents and label spots.
    for (const R of regions) {
      let maxd = 0;
      for (const it of items) if (it.region === R.key) maxd = Math.max(maxd, Math.hypot(it.x - R.x, it.y - R.y) + it.r);
      R.extent = Math.max(maxd, 60);
      R.labelX = R.x;
      R.labelY = R.y - R.extent - 22;
    }
    const bounds = { x0: Infinity, y0: Infinity, x1: -Infinity, y1: -Infinity };
    for (const it of items) {
      bounds.x0 = Math.min(bounds.x0, it.x - it.r); bounds.y0 = Math.min(bounds.y0, it.y - it.r);
      bounds.x1 = Math.max(bounds.x1, it.x + it.r); bounds.y1 = Math.max(bounds.y1, it.y + it.r);
    }
    for (const R of regions) { bounds.y0 = Math.min(bounds.y0, R.labelY - 20); }
    if (!items.length) Object.assign(bounds, { x0: -200, y0: -200, x1: 200, y1: 200 });
    const threads = (data.threads || []).map((t, ti) => currents(t, ti, items, momentIndex, regionByKey)).filter(Boolean);
    return { items, regions, threads, bounds };
  }

  /** Collision relaxation on a uniform grid: O(n) per pass, deterministic. */
  function relax(items, passes) {
    const cell = 64;
    for (let p = 0; p < passes; p++) {
      const grid = new Map();
      items.forEach((it, i) => {
        const k = Math.floor(it.x / cell) + "," + Math.floor(it.y / cell);
        if (!grid.has(k)) grid.set(k, []);
        grid.get(k).push(i);
      });
      let moved = false;
      items.forEach((a, i) => {
        const cx = Math.floor(a.x / cell), cy = Math.floor(a.y / cell);
        for (let gx = cx - 1; gx <= cx + 1; gx++) for (let gy = cy - 1; gy <= cy + 1; gy++) {
          const list = grid.get(gx + "," + gy);
          if (!list) continue;
          for (const j of list) {
            if (j <= i) continue;
            const b = items[j];
            let dx = b.x - a.x, dy = b.y - a.y;
            const pad = a.type === "idea" || b.type === "idea" ? 8 : a.region === b.region ? 10 : 30;
            const min = a.r + b.r + pad;
            const d2 = dx * dx + dy * dy;
            if (d2 >= min * min) continue;
            let d = Math.sqrt(d2);
            if (d < 0.01) { dx = Math.cos(i * 1.3 + j); dy = Math.sin(i * 1.3 + j); d = 1; }
            const push = ((min - d) / d) * 0.5;
            const wa = a.type === "idea" ? 1.4 : 1, wb = b.type === "idea" ? 1.4 : 1, ws = wa + wb;
            a.x -= dx * push * (wa / ws) * 2; a.y -= dy * push * (wa / ws) * 2;
            b.x += dx * push * (wb / ws) * 2; b.y += dy * push * (wb / ws) * 2;
            moved = true;
          }
        }
      });
      for (const it of items) { it.x += (it.ax - it.x) * 0.02; it.y += (it.ay - it.y) * 0.02; }
      if (!moved) break;
    }
  }

  function catmull(p0, p1, p2, p3, t) {
    const t2 = t * t, t3 = t2 * t;
    return [
      0.5 * (2 * p1[0] + (-p0[0] + p2[0]) * t + (2 * p0[0] - 5 * p1[0] + 4 * p2[0] - p3[0]) * t2 + (-p0[0] + 3 * p1[0] - 3 * p2[0] + p3[0]) * t3),
      0.5 * (2 * p1[1] + (-p0[1] + p2[1]) * t + (2 * p0[1] - 5 * p1[1] + 4 * p2[1] - p3[1]) * t2 + (-p0[1] + 3 * p1[1] - 3 * p2[1] + p3[1]) * t3),
    ];
  }

  /** A thread as a current: oldest to newest, cross-region legs through the basin's centre lanes. */
  function currents(t, ti, items, momentIndex, regionByKey) {
    const knots = [...(t.knots || [])].sort((a, b) => (a.at || "").localeCompare(b.at || ""));
    const members = [];
    for (const k of knots) {
      const i = momentIndex.get(k.momentId);
      if (i == null) continue;
      if (members.length && members[members.length - 1] === i) continue;
      members.push(i);
    }
    if (!members.length) return null;
    const ctrl = [];
    members.forEach((mi, k) => {
      const m = items[mi];
      if (k > 0) {
        const p = items[members[k - 1]];
        const dist = Math.hypot(m.x - p.x, m.y - p.y);
        if (p.region !== m.region) {
          const aP = Math.atan2(p.y, p.x), aM = Math.atan2(m.y, m.x);
          let da = aM - aP;
          da = ((((da + Math.PI) % TAU) + TAU) % TAU) - Math.PI;
          const rc = 120 + (ti % 6) * 34;
          const steps = Math.max(1, Math.round(Math.abs(da) / 0.9));
          for (let s = 0; s <= steps; s++) {
            const a = aP + da * (s / steps), rr = rc * (1 + 0.06 * Math.sin(s * 1.7 + ti));
            ctrl.push([Math.cos(a) * rr, Math.sin(a) * rr]);
          }
        } else if (dist > 50) {
          const nx = -(m.y - p.y) / dist, ny = (m.x - p.x) / dist;
          ctrl.push([(p.x + m.x) / 2 + nx * dist * 0.22, (p.y + m.y) / 2 + ny * dist * 0.22]);
        }
      }
      ctrl.push([m.x, m.y]);
    });
    if (ctrl.length === 1) {
      // A thread of one Moment: a small eddy around it.
      const m = items[members[0]];
      for (let s = 0; s <= 12; s++) ctrl.push([m.x + Math.cos((s / 12) * TAU) * (m.r + 18), m.y + Math.sin((s / 12) * TAU) * (m.r + 18)]);
    }
    const pts = [];
    const P = [ctrl[0], ...ctrl, ctrl[ctrl.length - 1]];
    for (let i = 1; i < P.length - 2; i++) for (let s = 0; s < 12; s++) pts.push(catmull(P[i - 1], P[i], P[i + 1], P[i + 2], s / 12));
    pts.push(ctrl[ctrl.length - 1]);
    const sArr = [0];
    for (let i = 1; i < pts.length; i++) sArr.push(sArr[i - 1] + Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1]));
    const mid = pts[Math.floor(pts.length / 2)];
    const h = hash32(t.id);
    const col = [0.75 + (h % 50) / 200, 0.7 + ((h >> 8) % 40) / 200, 0.95];
    return { id: t.id, title: t.title || "", revision: t.revision, members: members.map((i) => items[i].id), pts, s: sArr, len: sArr[sArr.length - 1], label: mid, col };
  }

  /** Lexical match over what a bubble shows. */
  function matches(it, terms) {
    if (!terms.length) return true;
    if (!it._hay) {
      it._hay = [it.title, it.excerpt, it.kind, it.sourceDetail, it.source, Math.floor(it.year), ...(it.people || []).map((p) => p.name)].join(" ").toLowerCase();
    }
    return terms.every((t) => it._hay.includes(t));
  }

  /** Home camera: phones open on the most recent memory; desktops on the whole map. */
  function homeCamera(lay, view, phone) {
    const { bounds } = lay;
    const w = Math.max(1, bounds.x1 - bounds.x0), h = Math.max(1, bounds.y1 - bounds.y0);
    const margin = phone ? 40 : 120;
    const fit = Math.min((view.w - 40) / w, (view.h - margin * 2) / h);
    const z = Math.min(Math.max(fit, 0.12), 1.6);
    if (phone) {
      const recent = [...lay.items].filter((i) => i.type === "moment").sort((a, b) => b.year - a.year)[0];
      // The bubble sits a little above centre so its card fits beneath it.
      if (recent) return { x: recent.x, y: recent.y + (view.h * 0.15) / 2.6, z: 2.6, focus: recent.id };
    }
    return { x: (bounds.x0 + bounds.x1) / 2, y: (bounds.y0 + bounds.y1) / 2, z, focus: "" };
  }

  return { hash32, unit, regionKey, layout, relax, currents, matches, homeCamera, yearOf, REGION_COLORS };
});
