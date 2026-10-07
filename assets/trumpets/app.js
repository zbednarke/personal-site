/* Private data lives in the API only; no localStorage, analytics or notes in URLs. */
(() => {
  "use strict";
  const M = window.TrumpetModel,
    $ = (s) => document.querySelector(s),
    form = $("#filters");
  let board = M.shape({}),
    view = "today",
    selected = null,
    loading = false;
  function el(tag, className, text) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    if (text != null) n.textContent = text;
    return n;
  }
  function date(v) {
    return v
      ? new Date(v).toLocaleString(undefined, {
          month: "short",
          day: "numeric",
          hour: "2-digit",
          minute: "2-digit",
        })
      : "Never verified";
  }
  function report(message, error = false) {
    const n = $("#message");
    n.textContent = message;
    n.className = error ? "error" : "";
  }
  async function api(path, body, method = "POST") {
    const response = await fetch("/trumpets/api/v1/trumpets" + path, {
      credentials: "same-origin",
      cache: "no-store",
      ...(body !== undefined
        ? {
            method,
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
          }
        : {}),
    });
    if (!response.ok) {
      let error;
      try {
        error = (await response.json()).error;
      } catch {}
      throw new Error(
        response.status === 401
          ? "Your private session needs a login. Reload the page to sign in."
          : error || `Data service returned ${response.status}`,
      );
    }
    return response.json();
  }
  function populate(name, values) {
    const s = form.elements[name],
      old = s.value;
    s.replaceChildren(
      new Option(
        `All ${name === "interest" ? "interest states" : name + "s"}`,
        "",
      ),
    );
    for (const value of values) s.add(new Option(value, value));
    s.value = old;
  }
  async function load() {
    if (loading) return;
    loading = true;
    $("#refresh").disabled = true;
    report("");
    try {
      let data = await api("/listings");
      if (!(data.listings || []).length) {
        await api("/seed", {});
        data = await api("/listings");
      }
      board = M.shape(data);
      populate(
        "maker",
        [...new Set(board.listings.map((l) => l.maker))].sort(),
      );
      populate(
        "source",
        [...new Set(board.listings.map((l) => l.source))].sort(),
      );
      populate("status", M.statuses);
      populate("interest", M.interests);
      render();
    } catch (e) {
      report(e.message, true);
      $("#result-count").textContent = "Could not load private board";
    } finally {
      loading = false;
      $("#refresh").disabled = false;
    }
  }
  function setView(v) {
    view = v;
    for (const [id, val] of [
      ["today", "today"],
      ["all", "all"],
    ]) {
      const button = $("#" + id);
      button.classList.toggle("selected", v === val);
      button.setAttribute("aria-pressed", String(v === val));
    }
    render();
  }
  function filterState() {
    return {
      view,
      query: form.elements.query.value,
      maker: form.elements.maker.value,
      source: form.elements.source.value,
      status: form.elements.status.value,
      interest: form.elements.interest.value,
      sort: form.elements.sort.value,
      active: form.elements.active.checked,
      favorite: form.elements.favorite.checked,
    };
  }
  function card(l) {
    const article = el("article", "card");
    article.dataset.id = l.id;
    const visual = el("div", "card-visual");
    if (l.images.length) {
      const img = el("img");
      img.src = l.images[0];
      img.alt = l.title;
      img.loading = "lazy";
      img.referrerPolicy = "no-referrer";
      img.addEventListener(
        "error",
        () => {
          img.remove();
          visual.classList.add("no-photo");
          visual.append(el("span", "unverified", "PHOTO UNAVAILABLE"));
        },
        { once: true },
      );
      visual.append(img);
    } else {
      visual.classList.add("no-photo");
      visual.append(
        el("span", "unverified", "HISTORICAL REFERENCE / PHOTO UNVERIFIED"),
      );
    }
    const badges = el("div", "badges");
    for (const badge of M.badges(l, board.events))
      badges.append(
        el("span", "badge " + (badge === "PRICE DROP" ? "drop" : ""), badge),
      );
    visual.append(badges);
    const favorite = el("button", "favorite", l.feedback.favorite ? "★" : "☆");
    favorite.type = "button";
    favorite.setAttribute(
      "aria-label",
      `${l.feedback.favorite ? "Unfavorite" : "Favorite"} ${l.title}`,
    );
    favorite.setAttribute("aria-pressed", String(l.feedback.favorite));
    favorite.addEventListener("click", async () => {
      favorite.disabled = true;
      try {
        await api(
          `/listings/${l.id}/feedback`,
          { ...l.feedback, favorite: !l.feedback.favorite },
          "PUT",
        );
        await load();
      } catch (e) {
        report(e.message, true);
      } finally {
        favorite.disabled = false;
      }
    });
    visual.append(favorite);
    article.append(visual);
    const body = el("div", "card-body");
    const kicker = el("div", "card-kicker");
    kicker.append(
      el("span", "maker", l.maker),
      el("span", "status " + l.status, l.status),
    );
    body.append(kicker);
    body.append(el("h2", "model", l.model));
    const price = el("div", "price");
    price.append(el("strong", "", M.money(l.price, l.currency)));
    if (l.shipping != null)
      price.append(
        el("span", "shipping", `+ ${M.money(l.shipping, l.currency)} shipping`),
      );
    if (M.priceDrop(l) > 0)
      price.append(
        el(
          "span",
          "drop-note",
          `↓ ${M.money(M.priceDrop(l), l.currency)} recorded drop`,
        ),
      );
    body.append(price);
    body.append(
      el(
        "p",
        "source",
        `${l.seller || l.source}${l.seller ? " / " + l.source : ""}${l.location ? " · " + l.location : ""}`,
      ),
    );
    body.append(
      el("p", "rationale", l.searchRationale || "Research note pending."),
    );
    const tags = el("div", "tags");
    for (const tag of l.tags.slice(0, 5)) tags.append(el("span", "", tag));
    body.append(tags);
    const feedback = el("div", "card-feedback");
    const rating = el(
      "button",
      "rating",
      l.feedback.rating
        ? "★".repeat(l.feedback.rating) + "☆".repeat(5 - l.feedback.rating)
        : "☆☆☆☆☆",
    );
    rating.type = "button";
    rating.setAttribute(
      "aria-label",
      `Rate ${l.title}, current rating ${l.feedback.rating || "unrated"}`,
    );
    rating.addEventListener("click", () => openDetail(l));
    const interest = el("span", "interest", l.feedback.interestState);
    feedback.append(rating, interest);
    body.append(feedback);
    const actions = el("div", "card-actions");
    const notes = el(
      "button",
      "notes",
      l.feedback.notes ? "Edit notes ↗" : "Notes & history ↗",
    );
    notes.type = "button";
    notes.addEventListener("click", () => openDetail(l));
    actions.append(notes);
    const url = M.safeURL(l.url);
    if (url) {
      const a = el("a", "listing-link", "View listing ↗");
      a.href = url;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      actions.append(a);
    } else actions.append(el("span", "unknown-link", "URL unverified"));
    body.append(actions);
    const foot = el("div", "card-foot");
    foot.append(
      el("span", "", `SCORE ${Math.round(l.searchScore)} / 100`),
      el(
        "span",
        "",
        l.lastChecked ? `Checked ${date(l.lastChecked)}` : "Needs verification",
      ),
    );
    body.append(foot);
    if (l.possibleRelists?.length)
      body.append(
        el(
          "p",
          "relist-note",
          `${l.possibleRelists.length} possible relist match${l.possibleRelists.length === 1 ? "" : "es"} · identity unconfirmed`,
        ),
      );
    article.append(body);
    return article;
  }
  function render() {
    const filterCount =
      ["maker", "source", "status", "interest"].filter(
        (name) => form.elements[name].value,
      ).length +
      Number(form.elements.active.checked) +
      Number(form.elements.favorite.checked) +
      Number(form.elements.sort.value !== "newest");
    $("#filter-toggle").textContent =
      "Filters & sort" + (filterCount ? " · " + filterCount + " active" : "");
    const rows = M.select(board, filterState());
    $("#cards").replaceChildren(...rows.map(card));
    $("#result-count").textContent =
      `${rows.length} ${rows.length === 1 ? "instrument" : "instruments"} / ${view === "today" ? "today’s meaningful changes" : "accumulated market memory"}`;
    $("#today-count").textContent = new Set(
      board.events
        .filter((e) =>
          board.listings.some((l) => l.id === e.listingId && !l.acquired),
        )
        .map((e) => e.listingId),
    ).size;
    $("#all-count").textContent = board.listings.length;
    const search = M.lastSuccessFor(board, "search"),
      recheck = M.lastSuccessFor(board, "recheck");
    $("#last-search").textContent = search ? date(search) : "Not run yet";
    $("#last-recheck").textContent = recheck ? date(recheck) : "Not run yet";
    const active = board.listings.filter(
      (l) => l.status === "active" && !l.acquired,
    );
    const day = String(board.serverTime || new Date().toISOString()).slice(
      0,
      10,
    );
    const due = active.filter(
      (l) => !l.lastChecked || l.lastChecked.slice(0, 10) < day,
    );
    $("#active-count").textContent =
      `${active.length} active / ${due.length} due`;
    const run = board.latestRun,
      sources = run?.sources || [];
    $("#source-count").textContent =
      `${sources.filter((s) => s.status === "checked").length} checked / ${sources.length} reported`;
    $("#run-state").textContent = run
      ? `${run.status.toUpperCase()} · ${run.kind} · ${date(run.completedAt)}`
      : "No search submitted";
    $("#run-error").textContent = run?.error || "";
    const coverage = $("#coverage-list");
    coverage.replaceChildren();
    const reported = new Set(sources.map((s) => s.source));
    for (const s of sources) {
      const chip = el(
        "span",
        "coverage-chip " + s.status,
        `${s.source} · ${s.status}${s.candidates ? " / " + s.candidates : ""}`,
      );
      chip.title = s.note || "";
      coverage.append(chip);
    }
    for (const s of board.sourceCatalog)
      if (!reported.has(s))
        coverage.append(
          el("span", "coverage-chip unchecked", `${s} · not reported`),
        );
    $("#empty").hidden = rows.length > 0;
    $("#empty-copy").textContent =
      view === "today"
        ? "No meaningful changes reported today. Browse All tracked for your historical references."
        : "No listings match these filters. Clear a filter to broaden the board.";
    $("#browse-all").textContent =
      view === "today" ? "Browse all tracked" : "Clear filters";
  }
  function openDetail(l) {
    selected = l;
    const f = $("#feedback-form");
    $("#detail-title").textContent = l.title;
    f.elements.rating.value = l.feedback.rating ?? "";
    f.elements.interestState.replaceChildren(
      ...M.interests.map((v) => new Option(v, v)),
    );
    f.elements.interestState.value = l.feedback.interestState;
    f.elements.interestState.disabled = l.acquired;
    f.elements.favorite.checked = l.feedback.favorite;
    f.elements.notes.value = l.feedback.notes;
    f.elements.favoredAttributes.value = (
      l.feedback.favoredAttributes || []
    ).join(", ");
    f.elements.dislikedAttributes.value = (
      l.feedback.dislikedAttributes || []
    ).join(", ");
    $("#save-message").textContent = "";
    const facts = $("#detail-facts");
    facts.replaceChildren();
    for (const [key, value] of Object.entries({
      Status: l.status,
      Serial: l.serialNumber,
      Year: l.details.year,
      Finish: l.details.finish,
      Condition: l.details.condition,
      Bore: l.details.bore,
      Bell: l.details.bell,
      Provenance: l.details.provenance,
      "Notable features": (l.details.notableFeatures || []).join(", "),
      "First seen": date(l.firstSeen),
      "Last checked": date(l.lastChecked),
      "Posted date": l.postedAt ? date(l.postedAt) : "Unknown",
    })) {
      if (value) {
        const item = el("p");
        item.append(
          el("span", "", key),
          document.createTextNode(String(value)),
        );
        facts.append(item);
      }
    }
    if (l.description) facts.append(el("p", "description", l.description));
    const history = $("#history");
    history.replaceChildren();
    for (const event of (l.statusHistory || []).slice().reverse()) {
      const row = el("p");
      row.append(
        el("time", "", date(event.occurredAt)),
        el(
          "span",
          "",
          `${event.kind}${event.kind.startsWith("price") ? " · " + M.money(event.oldPrice, event.currency) + " → " + M.money(event.newPrice, event.currency) : " · " + (event.oldStatus || "untracked") + " → " + event.newStatus}`,
        ),
      );
      history.append(row);
    }
    if (l.priceHistory.length) {
      const observations = el("details");
      observations.append(
        el("summary", "", `All ${l.priceHistory.length} verified observations`),
      );
      for (const o of l.priceHistory.slice().reverse())
        observations.append(
          el(
            "p",
            "",
            `${date(o.checkedAt)} · ${M.money(o.price, o.currency)} · ${o.status}${o.evidence ? " · " + o.evidence : ""}`,
          ),
        );
      history.append(observations);
    }
    if (!l.statusHistory.length && !l.priceHistory.length)
      history.append(
        el(
          "p",
          "field-help",
          "No verified market observations yet. Historical references do not imply current availability.",
        ),
      );
    $("#detail").showModal();
  }
  $("#feedback-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!selected) return;
    const f = e.currentTarget,
      b = $("#save-feedback");
    b.disabled = true;
    $("#save-message").textContent = "Saving…";
    const attrs = (n) =>
      f.elements[n].value
        .split(",")
        .map((v) => v.trim())
        .filter(Boolean);
    try {
      await api(
        `/listings/${selected.id}/feedback`,
        {
          rating: f.elements.rating.value
            ? Number(f.elements.rating.value)
            : null,
          interestState: f.elements.interestState.value,
          notes: f.elements.notes.value,
          favorite: f.elements.favorite.checked,
          favoredAttributes: attrs("favoredAttributes"),
          dislikedAttributes: attrs("dislikedAttributes"),
        },
        "PUT",
      );
      $("#save-message").textContent = "Saved privately.";
      await load();
      selected = board.listings.find((l) => l.id === selected.id) || selected;
      f.elements.interestState.disabled = selected.acquired;
    } catch (e) {
      $("#save-message").textContent = e.message;
    } finally {
      b.disabled = false;
    }
  });
  $("#close-detail").addEventListener("click", () => $("#detail").close());
  $("#today").addEventListener("click", () => setView("today"));
  $("#all").addEventListener("click", () => setView("all"));
  $("#browse-all").addEventListener("click", () => {
    if (view === "today") setView("all");
    else {
      form.reset();
      render();
    }
  });
  $("#refresh").addEventListener("click", load);
  $("#filter-toggle").addEventListener("click", () => {
    const expanded = form.classList.toggle("is-expanded");
    $("#filter-toggle").setAttribute("aria-expanded", String(expanded));
  });
  form.addEventListener("submit", (e) => e.preventDefault());
  form.addEventListener("input", render);
  load();
})();
