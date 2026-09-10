(() => {
  'use strict';
  const header = document.querySelector('#studio .clip-studio-header');
  if (!header) return;
  const shelf = document.createElement('section');
  shelf.className = 'finished-films';
  shelf.setAttribute('aria-label', 'Finished films');
  shelf.hidden = true;
  header.after(shelf);
  const style = document.createElement('style');
  style.textContent = `.finished-films{margin:12px 0 30px;border:1px solid #515b46;background:#1b2018;border-radius:8px;overflow:hidden}.finished-films[hidden]{display:none}.finished-film-copy{padding:22px 26px}.finished-film-eyebrow{color:#c2d7a9;font-size:10px;letter-spacing:.18em;text-transform:uppercase;margin:0 0 10px}.finished-film-title{font:400 clamp(22px,3vw,34px)/1.2 Georgia,serif;color:#f0f3e9;margin:0 0 9px}.finished-film-description{color:#b9c3b0;font-size:13px;line-height:1.6;margin:0;max-width:65ch}.finished-film-player{display:block;width:100%;max-height:70vh;aspect-ratio:16/9;background:#0c0e0b;object-fit:contain}.finished-film-footer{display:flex;align-items:center;justify-content:space-between;gap:14px;flex-wrap:wrap;padding:16px 26px;color:#bac7ad;font-size:12px}.finished-film-footer a{display:inline-block;color:#e4f0d8;border:1px solid #667455;border-radius:4px;padding:9px 14px;text-decoration:none}.finished-film-footer a:focus-visible{outline:2px solid #d8eac3;outline-offset:3px}.finished-film+.finished-film{border-top:1px solid #515b46}@media(max-width:600px){.finished-film-copy{padding:18px}.finished-film-footer{padding:14px 18px}.finished-film-description{font-size:12px}}`;
  document.head.append(style);
  const el = (tag, className, text) => {
    const node = document.createElement(tag); node.className = className;
    if (text) node.textContent = text;
    return node;
  };
  let films = [];
  function renderDay() {
    const date = document.querySelector('#studio-date')?.value;
    shelf.querySelectorAll('video').forEach(player => player.pause());
    shelf.replaceChildren();
    for (const film of films) {
      if (!date || film.date !== date) continue;
      if (!/^\/jazz\/films\/[a-zA-Z0-9_-]+\.mp4$/.test(film.video) || !/^\/jazz\/films\/[a-zA-Z0-9_-]+\.jpg$/.test(film.poster)) continue;
      const card = el('article', 'finished-film');
      const copy = el('div', 'finished-film-copy');
      copy.append(el('p', 'finished-film-eyebrow', 'Finished films · The daily cut'), el('h2', 'finished-film-title', film.title), el('p', 'finished-film-description', film.description));
      const player = el('video', 'finished-film-player');
      player.controls = true; player.playsInline = true; player.preload = 'none';
      player.src = film.video; player.poster = film.poster;
      player.setAttribute('aria-label', film.title);
      player.addEventListener('play', () => shelf.querySelectorAll('video').forEach(other => { if (other !== player) other.pause(); }));
      const footer = el('div', 'finished-film-footer');
      const download = el('a', '', 'Download film');
      download.href = film.video; download.download = film.id + '.mp4';
      footer.append(el('span', '', `${film.duration} · Finished and ready to watch`), download);
      card.append(copy, player, footer); shelf.append(card);
    }
    shelf.hidden = !shelf.childElementCount;
  }
  document.addEventListener('jazz:studio-date-change', renderDay);
  fetch('../assets/jazz/finished-films.json', { cache: 'no-store' }).then(r => {
    if (!r.ok) throw new Error('Films unavailable');
    return r.json();
  }).then(result => {
    films = Array.isArray(result) ? result : [];
    renderDay();
  }).catch(() => {});
  document.addEventListener('jazz:view-change', event => {
    if (event.detail?.view !== 'studio') shelf.querySelectorAll('video').forEach(player => player.pause());
  });
})();
