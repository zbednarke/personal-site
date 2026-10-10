(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.TrumpetModel = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  const statuses = ["active", "sold", "removed", "stale", "acquired"];
  const interests = [
    "pass",
    "watch",
    "interested",
    "contact",
    "buy",
    "acquired",
  ];
  function money(value, currency = "USD") {
    if (value == null) return "Price unverified";
    try {
      return new Intl.NumberFormat("en-US", {
        style: "currency",
        currency,
        maximumFractionDigits: 2,
      }).format(value);
    } catch {
      return `${value} ${currency}`;
    }
  }
  function safeURL(value) {
    try {
      const u = new URL(value);
      return ["https:", "http:"].includes(u.protocol) &&
        !u.username &&
        !u.password
        ? u.href
        : "";
    } catch {
      return "";
    }
  }
  function priceDrop(l) {
    return Math.max(
      0,
      ...(l.statusHistory || [])
        .filter(
          (e) =>
            e.kind === "price drop" &&
            e.currency === l.currency &&
            e.oldPrice != null &&
            e.newPrice != null,
        )
        .map((e) => e.oldPrice - e.newPrice),
    );
  }
  function biggestDrop(l) {
    return Math.max(
      0,
      ...(l.statusHistory || [])
        .filter(
          (e) =>
            e.kind === "price drop" && e.oldPrice > 0 && e.newPrice != null,
        )
        .map((e) => (e.oldPrice - e.newPrice) / e.oldPrice),
    );
  }
  function lastSuccessFor(board, kind) {
    const s = board.lastSuccess || {};
    return (
      [s[kind], s.combined]
        .filter(Boolean)
        .sort((a, b) => new Date(b) - new Date(a))[0] || null
    );
  }
  function shape(board) {
    return {
      ...board,
      listings: (board.listings || []).map((l) => ({
        ...l,
        verificationState: l.verificationState || "verified",
        details: l.details || {},
        images: (l.images || []).filter(safeURL),
        tags: l.tags || [],
        feedback: {
          rating: null,
          interestState: l.acquired ? "acquired" : "watch",
          notes: "",
          favorite: false,
          favoredAttributes: [],
          dislikedAttributes: [],
          ...l.feedback,
        },
      })),
      events: board.events || [],
      sourceCatalog: board.sourceCatalog || [],
      sourceUniverse: board.sourceUniverse || [],
    };
  }
  function select(board, f) {
    const todayIds = new Set((board.alerts ?? board.events ?? []).map((e) => e.listingId));
    let rows = board.listings.filter((l) => {
      const fb = l.feedback || {};
      const text = [
        l.maker,
        l.model,
        l.title,
        l.description,
        l.details?.finish,
        ...(l.tags || []),
      ]
        .join(" ")
        .toLowerCase();
      return (
        (f.view === "candidates"
          ? l.verificationState === "candidate" &&
            !l.acquired &&
            (fb.interestState !== "pass" || f.interest === "pass")
          : l.verificationState !== "candidate" || l.acquired) &&
        (f.view !== "today" ||
          (todayIds.has(l.id) && !l.acquired && l.status !== "acquired")) &&
        (!f.active || l.status === "active") &&
        (!f.status || l.status === f.status) &&
        (!f.maker || l.maker === f.maker) &&
        (!f.source || l.source === f.source) &&
        (!f.interest || fb.interestState === f.interest) &&
        (!f.favorite || fb.favorite) &&
        (!f.query || text.includes(f.query.toLowerCase()))
      );
    });
    const time = (v) => (v ? new Date(v).getTime() : 0);
    const sorts = {
      newest: (a, b) => time(b.firstSeen) - time(a.firstSeen),
      rating: (a, b) => (b.feedback.rating || 0) - (a.feedback.rating || 0),
      score: (a, b) => b.searchScore - a.searchScore,
      drop: (a, b) => biggestDrop(b) - biggestDrop(a),
      changed: (a, b) => time(b.changedAt) - time(a.changedAt),
      unchecked: (a, b) => time(a.lastChecked) - time(b.lastChecked),
      price: (a, b) =>
        a.currency.localeCompare(b.currency) ||
        (a.price ?? Infinity) - (b.price ?? Infinity),
    };
    return groupOffers(rows, board.listings, f.view).sort(sorts[f.sort] || sorts.newest);
  }
  function groupOffers(rows, baseline = rows, view = "all") {
    const grouped = new Map();
    for (const listing of rows) {
      const key = listing.hornId || listing.id;
      if (!grouped.has(key)) grouped.set(key, []);
      grouped.get(key).push(listing);
    }
    const marketplace = (l) => /reverb\.com|ebay\.|marktplaats\./i.test(l.url || "");
    return [...grouped.entries()].map(([key, offers]) => {
      if (view !== "today") offers.sort((a, b) =>
        Number(b.status === "active") - Number(a.status === "active") ||
        Number(marketplace(a)) - Number(marketplace(b)) ||
        (a.currency === b.currency ? (a.price ?? Infinity) - (b.price ?? Infinity) : 0));
      const primary = offers[0];
      const alternates = baseline.filter((l) => (l.hornId || l.id) === key && l.id !== primary.id && safeURL(l.url));
      return { ...primary, alternateOffers: alternates };
    });
  }
  function sourceMetrics(board) {
    const checks = board.latestRun?.sources || [];
    return {
      searched: new Set(checks.filter((s) => s.domain && s.query && s.status === "checked").map((s) => s.domain)).size,
      live: new Set(checks.filter((s) => s.domain && s.pagesOpened > 0).map((s) => s.domain)).size,
      universe: (board.sourceUniverse || []).length,
    };
  }
  function badges(l, events) {
    const out = [];
    if (l.verificationState === "candidate") out.push("UNVERIFIED");
    const kinds = new Set(
      events.filter((e) => e.listingId === l.id).map((e) => e.kind),
    );
    if (kinds.has("new listing")) out.push("NEW");
    if (kinds.has("newly discovered") || kinds.has("rediscovered"))
      out.push("FOUND");
    if (kinds.has("price drop")) out.push("PRICE DROP");
    if (kinds.has("status change")) out.push("STATUS CHANGE");
    if (kinds.has("details change")) out.push("UPDATED");
    const text = [
      l.details?.finish,
      l.details?.provenance,
      l.searchRationale,
      ...(l.tags || []),
    ]
      .join(" ")
      .toLowerCase();
    for (const [re, b] of [
      [/gold/, "GOLD"],
      [/raw/, "RAW"],
      [/patina|aged|antique/, "PATINA"],
      [/provenance|artist|one-off/, "PROVENANCE"],
      [/weird|upswept|geometry|engineering/, "WEIRD"],
      [/good value|bargain|deal/, "DEAL"],
    ])
      if (re.test(text)) out.push(b);
    return [...new Set(out)];
  }
  return {
    statuses,
    interests,
    money,
    safeURL,
    lastSuccessFor,
    shape,
    select,
    badges,
    priceDrop,
    biggestDrop,
    groupOffers,
    sourceMetrics,
  };
});
