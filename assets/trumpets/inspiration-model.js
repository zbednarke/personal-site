/* Pure horn-inspiration model: capture parsing, link classification (mirrors
 * jazz-api/inspiration_links.go), filters, sorts and image plans. No DOM. */
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.InspirationModel = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  const priorities = ["inspiration", "someday", "want", "hunting"];
  const priorityLabels = {
    inspiration: "Just inspiration",
    someday: "Someday",
    want: "Want",
    hunting: "Actively hunting",
  };
  const tagGroups = {
    finish: ["raw brass", "lacquer", "silver", "gold plate", "black nickel", "mixed metals", "engraving", "patina", "satin"],
    bell: ["one-piece bell", "two-piece bell", "upswept bell", "flip bell", "rose brass bell", "sterling bell", "large bell", "small bell"],
    leadpipe: ["reverse leadpipe", "standard leadpipe", "heavy leadpipe"],
    build: ["heavyweight", "lightweight", "vintage", "one-off", "artist model", "unusual engineering", "mbs technology"],
    valves: ["heavy caps", "bottom sprung"],
    bore: ["ml bore", "large bore"],
  };
  const tagSuggestions = Object.values(tagGroups).flat();
  const sources = [
    ["youtube", "YouTube"],
    ["instagram", "Instagram"],
    ["tiktok", "TikTok"],
    ["facebook", "Facebook"],
    ["retailer", "Retailer"],
    ["photo", "Photo"],
  ];
  const imageTypes = ["image/jpeg", "image/png", "image/webp", "image/gif"];
  const maxUploadBytes = 10 * 1024 * 1024;
  const maxEdge = 2048;

  const trackingParams = ["fbclid", "gclid", "ref", "referrer", "srsltid", "itmmeta", "itmprp", "hash", "_trksid", "_trkparms"];
  const shareParams = ["si", "is", "igsh", "igshid", "mibextid", "mc_cid", "mc_eid", "_ga", "yclid", "msclkid"];
  const tiktokParams = ["_r", "_t", "is_from_webapp", "sender_device", "is_copy_url"];
  const facebookParams = ["mibextid", "rdid", "share_url"];
  const YT = /^[A-Za-z0-9_-]{11}$/;
  const IG = /^[A-Za-z0-9_-]{5,40}$/;

  // Go's url.QueryEscape: everything but A-Z a-z 0-9 - _ . ~ is escaped.
  function goEscape(s) {
    return encodeURIComponent(s)
      .replace(/[!'()*]/g, (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase())
      .replace(/%20/g, "+");
  }
  // Mirror of canonicalTrumpetURL followed by url.Values.Encode (sorted keys).
  function parseBase(raw) {
    const text = String(raw || "").trim();
    if (!text) return null;
    let u;
    try {
      u = new URL(text);
    } catch {
      return null;
    }
    if (!["http:", "https:"].includes(u.protocol) || !u.hostname || u.username || u.password) return null;
    const host = u.host.toLowerCase().replace(/^www\./, "");
    const path = u.pathname.replace(/\/+$/, "");
    const params = [];
    for (const [k, v] of u.searchParams) {
      const lower = k.toLowerCase();
      if (lower.startsWith("utm_") || trackingParams.includes(lower)) continue;
      params.push([k, v]);
    }
    return { host, path, params };
  }
  function encode(params) {
    return params
      .map((p, i) => [p, i])
      .sort((a, b) => (a[0][0] < b[0][0] ? -1 : a[0][0] > b[0][0] ? 1 : a[1] - b[1]))
      .map(([[k, v]]) => goEscape(k) + "=" + goEscape(v))
      .join("&");
  }
  function strip(params, names) {
    return params.filter(([k]) => !names.includes(k.toLowerCase()));
  }
  function build(host, path, params) {
    const q = encode(params || []);
    return `https://${host}${path}${q ? "?" + q : ""}`;
  }
  function segments(path) {
    return path.split("/").filter(Boolean);
  }
  function getParam(params, name) {
    const hit = params.find(([k]) => k === name);
    return hit ? hit[1] : "";
  }
  const result = (canonical, provider, mediaId, format) => ({ canonical, provider, mediaId, format });

  /** classifyURL(raw) → {canonical, provider, mediaId, format} or null. */
  function classifyURL(raw) {
    const base = parseBase(raw);
    if (!base) return null;
    const { host, path } = base;
    let params = base.params;
    const segs = segments(path);
    if (["youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com", "youtu.be"].includes(host)) {
      let id = "",
        kind = "";
      if (host === "youtu.be" && segs.length >= 1) [id, kind] = [segs[0], "video"];
      else if (segs.length >= 2 && segs[0] === "shorts") [id, kind] = [segs[1], "short"];
      else if (segs.length >= 2 && ["live", "embed", "v"].includes(segs[0])) [id, kind] = [segs[1], "video"];
      else if (segs.length === 1 && segs[0] === "watch") [id, kind] = [getParam(params, "v"), "video"];
      if (!kind) return result(build(host, path, strip(params, shareParams)), "youtube", "", "page");
      if (!YT.test(id)) return null;
      if (kind === "short") return result("https://youtube.com/shorts/" + id, "youtube", id, "short");
      const t = getParam(params, "t");
      return result(`https://youtube.com/watch?v=${id}${t && t.length <= 16 ? "&t=" + goEscape(t) : ""}`, "youtube", id, "video");
    }
    if (host === "instagram.com" || host === "m.instagram.com") {
      for (let i = 0; i < segs.length - 1 && i < 2; i++) {
        const kind = segs[i] === "p" ? "post" : ["reel", "reels", "tv"].includes(segs[i]) ? "reel" : "";
        if (!kind) continue;
        const code = segs[i + 1];
        if (!IG.test(code)) return null;
        return result(`https://instagram.com/${kind === "reel" ? "reel" : "p"}/${code}`, "instagram", code, kind);
      }
      return result(build(host, path, []), "instagram", "", "page");
    }
    if (["tiktok.com", "m.tiktok.com", "vm.tiktok.com", "vt.tiktok.com"].includes(host)) {
      params = strip(params, [...tiktokParams, ...shareParams]);
      let id = "",
        kind = "video";
      if (segs.length >= 3 && segs[0].startsWith("@") && ["video", "photo"].includes(segs[1])) {
        if (/^[0-9]{5,30}$/.test(segs[2])) id = segs[2];
        if (segs[1] === "photo") kind = "post";
      }
      return result(build(host, path, params), "tiktok", id, kind);
    }
    if (["facebook.com", "m.facebook.com", "web.facebook.com", "fb.com", "fb.watch"].includes(host)) {
      params = strip(params, [...facebookParams, ...shareParams]);
      const video = host === "fb.watch" || path.includes("/videos/") || path.includes("/reel/") || segs[0] === "watch";
      return result(build(host, path, params), "facebook", "", video ? "video" : "post");
    }
    if (host === "reverb.com" || host === "m.reverb.com") {
      const m = segs[0] === "item" && segs[1] ? /^([0-9]+)(?:-.*)?$/.exec(segs[1]) : null;
      if (m) return result("https://reverb.com/item/" + m[1], "reverb", "", "page");
      return result(build(host, path, strip(params, shareParams)), "reverb", "", "page");
    }
    if (/^(?:m\.)?ebay\.[a-z]{2,3}(?:\.[a-z]{2})?$/.test(host)) {
      const h = host.replace(/^m\./, "");
      if (segs[0] === "itm") {
        const id = segs.slice(1).find((s) => /^[0-9]{6,20}$/.test(s));
        if (id) return result(`https://${h}/itm/${id}`, "ebay", "", "page");
      }
      return result(build(host, path, strip(params, shareParams)), "ebay", "", "page");
    }
    return result(build(host, path, strip(params, shareParams)), "web", "", "page");
  }
  function youtubeID(raw) {
    const c = classifyURL(raw);
    return c && c.provider === "youtube" ? c.mediaId : "";
  }

  const urlPattern = /\bhttps?:\/\/[^\s<>"'`]+/gi;
  const barePattern = /^(?:www\.[^\s/]+\.[a-z]{2,}(?:[/?#]\S*)?|(?:[a-z0-9-]+\.)+[a-z]{2,}\/\S*)$/i;
  function trimURL(u) {
    return u.replace(/[)\].,;:!?'"»”]+$/, "");
  }
  /**
   * parseCaptureInput(text) → {url, why} | {error} | {empty: true}.
   * Share sheets add text around the link; that text becomes "why".
   */
  function parseCaptureInput(text) {
    const value = String(text || "").trim();
    if (!value) return { empty: true };
    let urls = value.match(urlPattern) || [];
    let rest = value;
    if (!urls.length) {
      const bare = value.split(/\s+/).filter((t) => barePattern.test(trimURL(t)));
      if (bare.length === 1) {
        urls = ["https://" + trimURL(bare[0])];
        rest = value.replace(bare[0], " ");
      }
    } else rest = value.replace(urlPattern, " ");
    if (!urls.length) return { error: "Paste a link or an image" };
    if (urls.length > 1) return { error: "Paste one link at a time" };
    const url = trimURL(urls[0]);
    if (!classifyURL(url)) return { error: "That link can't be added" };
    const why = rest.replace(/\s+/g, " ").trim().slice(0, 4000);
    return { url, why };
  }

  function normalizeTags(tags) {
    const out = [];
    for (const raw of tags || []) {
      const t = String(raw).toLowerCase().split(/\s+/).filter(Boolean).join(" ");
      if (t && t.length <= 40 && !out.includes(t)) out.push(t);
    }
    return out.slice(0, 20);
  }

  function sourceOf(item) {
    if (item.provider === "upload") return "photo";
    if (["reverb", "ebay", "web"].includes(item.provider)) return "retailer";
    return item.provider;
  }
  function domainOf(item) {
    try {
      return new URL(item.sourceUrl || item.canonicalUrl).hostname.replace(/^www\./, "");
    } catch {
      return "";
    }
  }
  function cardTitle(item) {
    return (
      item.title ||
      [item.maker, item.model].filter(Boolean).join(" ") ||
      domainOf(item) ||
      (item.kind === "image" ? "Photo" : "Link")
    );
  }
  function badgeLabel(item) {
    if (item.provider === "youtube") return item.mediaFormat === "short" ? "▶ Short" : "▶ Video";
    if (item.provider === "instagram") return item.mediaFormat === "reel" ? "Reel" : "Instagram";
    if (item.provider === "tiktok") return "TikTok";
    if (item.provider === "facebook") return "Facebook";
    if (item.provider === "reverb") return "Reverb";
    if (item.provider === "ebay") return "eBay";
    if (item.provider === "upload") return "Photo";
    return domainOf(item) || "Link";
  }
  function priorityLabel(p) {
    return priorityLabels[p] || priorityLabels.someday;
  }
  function priorityRank(p) {
    const i = priorities.indexOf(p);
    return i < 0 ? 1 : i;
  }
  function formatPriceSeen(item) {
    if (item.priceSeen == null) return "";
    let money;
    try {
      money = new Intl.NumberFormat("en-US", {
        style: "currency",
        currency: item.priceCurrency || "USD",
        maximumFractionDigits: item.priceSeen % 1 ? 2 : 0,
      }).format(item.priceSeen);
    } catch {
      money = `${item.priceSeen} ${item.priceCurrency}`;
    }
    const when = item.priceSeenOn
      ? new Date(item.priceSeenOn + "T00:00:00Z").toLocaleDateString("en-US", { month: "short", year: "numeric", timeZone: "UTC" })
      : "";
    return when ? `${money} · ${when}` : money;
  }

  /** Embeds are rebuilt from the validated media id only. */
  function embedFor(item) {
    if (item.provider === "youtube" && YT.test(item.providerMediaId || ""))
      return {
        kind: "youtube",
        src: `https://www.youtube-nocookie.com/embed/${item.providerMediaId}?autoplay=1&playsinline=1`,
        aspect: item.mediaFormat === "short" ? "9:16" : "16:9",
      };
    if (item.provider === "instagram" && IG.test(item.providerMediaId || ""))
      return {
        kind: "instagram",
        src: `https://www.instagram.com/${item.mediaFormat === "reel" ? "reel" : "p"}/${item.providerMediaId}/embed`,
        aspect: "4:5",
      };
    return null;
  }

  /** f: {query, maker, tags[], priorities[], source, hasPrice}. Tags are AND. */
  function filterInspirations(items, f = {}) {
    const q = String(f.query || "").trim().toLowerCase();
    const tags = f.tags || [];
    const prios = f.priorities || [];
    return (items || []).filter((x) => {
      const text = [x.title, x.maker, x.model, x.why, x.authorName, ...(x.tags || [])].join(" ").toLowerCase();
      return (
        (!q || text.includes(q)) &&
        (!f.maker || (x.maker || "").toLowerCase() === f.maker.toLowerCase()) &&
        tags.every((t) => (x.tags || []).includes(t)) &&
        (!prios.length || prios.includes(x.priority)) &&
        (!f.source || sourceOf(x) === f.source) &&
        (!f.hasPrice || x.priceSeen != null)
      );
    });
  }
  const time = (v) => (v ? new Date(v).getTime() : 0);
  /** Pinned first; then newest, priority, maker A–Z or price grouped by currency (no FX). */
  function sortInspirations(items, sort = "newest") {
    const newest = (a, b) => time(b.createdAt) - time(a.createdAt);
    const sorts = {
      newest,
      priority: (a, b) => priorityRank(b.priority) - priorityRank(a.priority) || newest(a, b),
      maker: (a, b) =>
        Number(!a.maker) - Number(!b.maker) ||
        (a.maker || "").localeCompare(b.maker || "", undefined, { sensitivity: "base" }) ||
        (a.model || "").localeCompare(b.model || "", undefined, { sensitivity: "base" }) ||
        newest(a, b),
      price: (a, b) =>
        Number(a.priceSeen == null) - Number(b.priceSeen == null) ||
        (a.priceCurrency || "").localeCompare(b.priceCurrency || "") ||
        (a.priceSeen ?? 0) - (b.priceSeen ?? 0) ||
        newest(a, b),
    };
    const by = sorts[sort] || newest;
    return [...(items || [])].sort((a, b) => Number(!!b.pinned) - Number(!!a.pinned) || by(a, b));
  }

  /**
   * resizePlan(width, height, type, size): JPEG q0.85 with the long edge
   * ≤ 2048 (never upscaled); GIFs keep their animation when ≤ 10 MB.
   */
  function resizePlan(width, height, type = "image/jpeg", size = 0) {
    if (type === "image/gif")
      return size <= maxUploadBytes ? { mode: "original" } : { mode: "reject", reason: "This GIF is larger than 10 MB." };
    if (!(width > 0 && height > 0)) return fallbackPlan(type, size);
    const scale = Math.min(1, maxEdge / Math.max(width, height));
    return {
      mode: "jpeg",
      type: "image/jpeg",
      quality: 0.85,
      width: Math.max(1, Math.round(width * scale)),
      height: Math.max(1, Math.round(height * scale)),
    };
  }
  /** When the browser cannot decode an image (e.g. HEIC outside Safari). */
  function fallbackPlan(type, size) {
    if (imageTypes.includes(type) && size > 0 && size <= maxUploadBytes) return { mode: "original" };
    return { mode: "reject", reason: "This image type isn't supported. Try a screenshot." };
  }

  /** Observatory suggestions: same maker, and a model prefix/contains match. */
  function hornSuggestions(item, horns) {
    const maker = String(item.maker || "").trim().toLowerCase();
    const model = String(item.model || "").trim().toLowerCase();
    if (!maker) return [];
    const seen = new Set();
    return (horns || []).filter((h) => {
      if (!h.id || seen.has(h.id) || String(h.maker || "").toLowerCase() !== maker) return false;
      const hm = String(h.model || "").toLowerCase();
      const ok = !model || hm.startsWith(model) || hm.includes(model) || model.includes(hm);
      if (ok) seen.add(h.id);
      return ok;
    });
  }

  return {
    priorities,
    priorityLabels,
    tagGroups,
    tagSuggestions,
    sources,
    imageTypes,
    maxUploadBytes,
    classifyURL,
    youtubeID,
    parseCaptureInput,
    normalizeTags,
    sourceOf,
    domainOf,
    cardTitle,
    badgeLabel,
    priorityLabel,
    priorityRank,
    formatPriceSeen,
    embedFor,
    filterInspirations,
    sortInspirations,
    resizePlan,
    fallbackPlan,
    hornSuggestions,
  };
});
