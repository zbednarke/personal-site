(() => {
  "use strict";
  const header = document.querySelector("#studio .clip-studio-header");
  if (!header) return;
  const shelf = document.createElement("section");
  shelf.className = "finished-films"; shelf.setAttribute("aria-label", "Finished films"); shelf.hidden = true;
  header.after(shelf);
  const style = document.createElement("style");
  style.textContent = `.finished-films{margin:12px 0 30px;border:1px solid #515b46;background:#1b2018;border-radius:8px;overflow:hidden}.finished-films[hidden]{display:none}.finished-film-copy{padding:22px 26px}.finished-film-eyebrow{color:#c2d7a9;font-size:10px;letter-spacing:.18em;text-transform:uppercase;margin:0 0 10px}.finished-film-title{font:400 clamp(22px,3vw,34px)/1.2 Georgia,serif;color:#f0f3e9;margin:0 0 9px}.finished-film-description{color:#b9c3b0;font-size:13px;line-height:1.6;margin:0;max-width:65ch}.finished-film-player{display:block;width:100%;max-height:70vh;aspect-ratio:16/9;background:#0c0e0b;object-fit:contain}.finished-film-footer{display:flex;align-items:center;justify-content:space-between;gap:14px;flex-wrap:wrap;padding:16px 26px;color:#bac7ad;font-size:12px}.finished-film-footer a{display:inline-block;color:#e4f0d8;border:1px solid #667455;border-radius:4px;padding:9px 14px;text-decoration:none}.finished-film+.finished-film{border-top:1px solid #515b46}.finished-film-direction{margin:12px 0 0;max-width:65ch;color:#d6dfcb;font-size:13px;line-height:1.6;white-space:pre-wrap;overflow-wrap:anywhere}.finished-film-direction b{color:#c2d7a9;font-weight:400;font-size:10px;letter-spacing:.14em;text-transform:uppercase;margin-right:8px}.finished-film-footer button{color:#e4f0d8;background:transparent;border:1px solid #667455;border-radius:4px;padding:9px 14px;font:inherit;cursor:pointer}.finished-film-clips{margin:0;padding:4px 26px 18px;list-style:none;border-top:1px solid #3a4233}.finished-film-clips[hidden]{display:none}.finished-film-clips li{display:grid;grid-template-columns:auto 1fr auto;align-items:center;gap:4px 14px;padding:12px 0;border-bottom:1px solid #2c3327}.finished-film-clips li:last-child{border-bottom:0}.finished-film-clip-order{color:#8f9c84;font-size:12px;font-variant-numeric:tabular-nums;min-width:2ch}.finished-film-clip-copy{min-width:0}.finished-film-clip-copy strong{display:block;color:#eef3e6;font-weight:400;font-size:14px;overflow-wrap:anywhere}.finished-film-clip-copy span{display:block;color:#aab69f;font-size:12px;margin-top:3px;overflow-wrap:anywhere}.finished-film-clips button{color:#e4f0d8;background:transparent;border:1px solid #667455;border-radius:4px;padding:8px 12px;font:inherit;font-size:12px;cursor:pointer;white-space:nowrap}.finished-film-clips button:disabled{opacity:.55;cursor:default}.finished-film-clip-status{grid-column:2/-1;color:#e3b7a2;font-size:12px}.finished-film-clip-status:empty{display:none}@media(max-width:600px){.finished-film-copy{padding:18px}.finished-film-footer{padding:14px 18px}.finished-film-description{font-size:12px}.finished-film-clips{padding:2px 18px 14px}.finished-film-clips li{grid-template-columns:auto 1fr}.finished-film-clips button{grid-column:2;justify-self:start}}`;
  document.head.append(style);
  const el = (tag, className, text) => { const node = document.createElement(tag); node.className = className; if (text) node.textContent = text; return node; };
  let films = [];
  let rendered = "";

  function durationLabel(ms) {
    const seconds = Math.max(0, Math.round(Number(ms || 0) / 1000));
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  }
  function cloudFilm(job) {
    const base = `./api/v1/studio/magic-films/${encodeURIComponent(job.id)}/media`;
    return { id: job.id, date: job.date, title: job.project?.title || "Magic Film", description: job.summary || "A finished practice-room edit.", duration: durationLabel(job.durationMs), video: `${base}?asset=video`, poster: `${base}?asset=poster`, download: `${base}?asset=video&download=1`, cloud: true, direction: job.direction || "", targetSeconds: job.targetSeconds, project: job.project || null };
  }
  function renderDay() {
    const date = document.querySelector("#studio-date")?.value;
    shelf.querySelectorAll("video").forEach(player => player.pause());
    shelf.replaceChildren();
    for (const film of films) {
      if (!date || film.date !== date) continue;
      if (!film.cloud && (!/^\/jazz\/films\/[a-zA-Z0-9_-]+\.mp4$/.test(film.video) || !/^\/jazz\/films\/[a-zA-Z0-9_-]+\.jpg$/.test(film.poster))) continue;
      const card = el("article", "finished-film");
      const copy = el("div", "finished-film-copy");
      copy.append(el("p", "finished-film-eyebrow", "Finished films · The daily cut"), el("h2", "finished-film-title", film.title), el("p", "finished-film-description", film.description));
      if (film.cloud && film.direction) {
        const asked = el("p", "finished-film-direction"); asked.append(el("b", "", "You asked"), document.createTextNode(film.direction)); copy.append(asked);
      }
      const player = el("video", "finished-film-player");
      player.controls = true; player.playsInline = true; player.preload = "none"; player.src = film.video; player.poster = film.poster; player.setAttribute("aria-label", film.title);
      player.addEventListener("play", () => shelf.querySelectorAll("video").forEach(other => { if (other !== player) other.pause(); }));
      const footer = el("div", "finished-film-footer");
      const download = el("a", "", "Download film"); download.href = film.download || film.video; download.download = `${film.id}.mp4`; download.rel = "noreferrer";
      if (!film.cloud && typeof film.noteMap === "string" && /^\/jazz\/films\/[a-zA-Z0-9_-]+\.notes\.html$/.test(film.noteMap)) {
        const map = document.createElement("a"); map.href = film.noteMap; map.textContent = "Explore note map"; map.target = "_blank"; map.rel = "noopener"; footer.append(map);
      }
      // The exact-edit film editor works on films published by the site VM's
      // film service; cloud Magic Films have no saved edit to open.
      if (!film.cloud && globalThis.JazzFilmEditor) {
        const edit = el("button", "", "Edit this film"); edit.type = "button"; edit.onclick = () => { player.pause(); globalThis.JazzFilmEditor.open(film); }; footer.append(edit);
      }
      if (film.cloud && globalThis.JazzMagicFilmComposer) {
        const remake = el("button", "", "Re-make with new direction"); remake.type = "button";
        remake.onclick = () => { player.pause(); globalThis.JazzMagicFilmComposer.remake(film); };
        footer.append(remake);
      }
      footer.append(el("span", "", `${film.duration} · Finished and ready to watch`), download);
      card.append(copy, player, footer);
      const clipCount = film.cloud ? (globalThis.JazzFilmClipsModel?.filmClipRows(film.project).length || 0) : 0;
      if (clipCount) {
        const list = el("ol", "finished-film-clips"); list.hidden = true; list.id = `finished-film-clips-${film.id}`;
        list.setAttribute("aria-label", `Clips used in ${film.title}`);
        const toggle = el("button", "", `Clips used (${clipCount})`); toggle.type = "button";
        toggle.setAttribute("aria-expanded", "false"); toggle.setAttribute("aria-controls", list.id);
        toggle.onclick = () => {
          const open = list.hidden;
          if (open) renderClips(list, film, player);
          list.hidden = !open; toggle.setAttribute("aria-expanded", String(open));
        };
        footer.prepend(toggle); card.append(list);
      }
      shelf.append(card);
    }
    shelf.hidden = !shelf.childElementCount;
    rendered = JSON.stringify(films.filter(film => film.date === date).map(film => film.id));
  }
  // Lists the film's source sub-clips in film order; each opens its original
  // take in the Studio player at the clip's start.
  function renderClips(list, film, player) {
    const sources = globalThis.JazzClipStudioSources;
    const rows = globalThis.JazzFilmClipsModel.filmClipRows(film.project, id => sources?.describe(id) || null);
    list.replaceChildren(...rows.map(row => {
      const item = el("li", "");
      const copy = el("div", "finished-film-clip-copy");
      copy.append(el("strong", "", row.title), el("span", "", [row.source || "Take not on this day", `${row.range} in the take`].join(" · ")));
      const status = el("p", "finished-film-clip-status");
      const play = el("button", "", row.playable ? "Play original" : "Unavailable"); play.type = "button"; play.disabled = !row.playable;
      play.setAttribute("aria-label", `Play original take for clip ${row.order}, ${row.title}, from ${row.range.split("–")[0]}`);
      play.onclick = async () => {
        status.textContent = ""; player.pause();
        try { await sources.play(row.recordingId, row.startMs); } catch (error) { status.textContent = error.message || "Could not open this take."; }
      };
      item.append(el("span", "finished-film-clip-order", String(row.order)), copy, play, status);
      return item;
    }));
  }
  async function refreshFilms() {
    const date = document.querySelector("#studio-date")?.value;
    if (!date) return;
    const next = [];
    try {
      const response = await fetch(`./api/v1/studio/magic-films?date=${encodeURIComponent(date)}`, { cache: "no-store" });
      if (response.ok) {
        const result = await response.json();
        next.push(...(result.jobs || []).filter(job => job.status === "complete" && job.hasVideo).map(cloudFilm));
      }
    } catch { /* older deployments have no cloud-film endpoint */ }
    try {
      let response = await fetch("./films/index.json", { cache: "no-store" });
      if (!response.ok) response = await fetch("../assets/jazz/finished-films.json", { cache: "no-store" });
      if (response.ok) {
        const legacy = await response.json();
        if (Array.isArray(legacy)) next.push(...legacy);
      }
    } catch { /* legacy shelf is optional */ }
    const changed = rendered !== JSON.stringify(next.filter(film => film.date === date).map(film => film.id));
    films = next;
    if (changed && !Array.from(shelf.querySelectorAll("video")).some(player => !player.paused)) renderDay();
  }
  document.addEventListener("jazz:studio-date-change", refreshFilms);
  document.addEventListener("jazz:magic-films-updated", refreshFilms);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refreshFilms(); });
  document.addEventListener("jazz:view-change", event => {
    if (event.detail?.view !== "studio") shelf.querySelectorAll("video").forEach(player => player.pause());
    else refreshFilms();
  });
  setInterval(() => { if (!document.hidden && !document.querySelector("#studio").hidden) refreshFilms(); }, 20000);
  refreshFilms();
})();
