(() => {
  "use strict";
  const header = document.querySelector("#studio .clip-studio-header");
  if (!header) return;
  const shelf = document.createElement("section");
  shelf.className = "finished-films"; shelf.setAttribute("aria-label", "Finished films"); shelf.hidden = true;
  header.after(shelf);
  const style = document.createElement("style");
  style.textContent = `.finished-films{margin:12px 0 30px;border:1px solid #515b46;background:#1b2018;border-radius:8px;overflow:hidden}.finished-films[hidden]{display:none}.finished-film-copy{padding:22px 26px}.finished-film-eyebrow{color:#c2d7a9;font-size:10px;letter-spacing:.18em;text-transform:uppercase;margin:0 0 10px}.finished-film-title{font:400 clamp(22px,3vw,34px)/1.2 Georgia,serif;color:#f0f3e9;margin:0 0 9px}.finished-film-description{color:#b9c3b0;font-size:13px;line-height:1.6;margin:0;max-width:65ch}.finished-film-player{display:block;width:100%;max-height:70vh;aspect-ratio:16/9;background:#0c0e0b;object-fit:contain}.finished-film-footer{display:flex;align-items:center;justify-content:space-between;gap:14px;flex-wrap:wrap;padding:16px 26px;color:#bac7ad;font-size:12px}.finished-film-footer a{display:inline-block;color:#e4f0d8;border:1px solid #667455;border-radius:4px;padding:9px 14px;text-decoration:none}.finished-film+.finished-film{border-top:1px solid #515b46}.finished-film-direction{margin:12px 0 0;max-width:65ch;color:#d6dfcb;font-size:13px;line-height:1.6;white-space:pre-wrap;overflow-wrap:anywhere}.finished-film-direction b{color:#c2d7a9;font-weight:400;font-size:10px;letter-spacing:.14em;text-transform:uppercase;margin-right:8px}.finished-film-footer button{color:#e4f0d8;background:transparent;border:1px solid #667455;border-radius:4px;padding:9px 14px;font:inherit;cursor:pointer}@media(max-width:600px){.finished-film-copy{padding:18px}.finished-film-footer{padding:14px 18px}.finished-film-description{font-size:12px}}`;
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
    return { id: job.id, date: job.date, title: job.project?.title || "Magic Film", description: job.summary || "A finished practice-room edit.", duration: durationLabel(job.durationMs), video: `${base}?asset=video`, poster: `${base}?asset=poster`, download: `${base}?asset=video&download=1`, cloud: true, direction: job.direction || "", targetSeconds: job.targetSeconds };
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
      card.append(copy, player, footer); shelf.append(card);
    }
    shelf.hidden = !shelf.childElementCount;
    rendered = JSON.stringify(films.filter(film => film.date === date).map(film => film.id));
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
