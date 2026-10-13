/* Library view for /jazz/ (#library): the private PDF shelf served by the
 * highlight service on the site VM at ./highlight-api/library (list, upload,
 * read and download). #library?q=<text> opens it with the search filled in,
 * which is how Repertoire's "Sheet music" section links here. */
(() => {
  'use strict';
  const $ = id => document.getElementById(id), root = $('library');
  if (!root) return;
  const api = './highlight-api/library';
  let items = [], loading = false;
  const status = text => { $('library-status').textContent = text; };
  const fileURL = item => `${api}/${encodeURIComponent(item.id)}/file`;
  const isLibraryHash = () => location.hash === '#library' || location.hash.startsWith('#library?');
  function applyHashQuery() {
    const match = /^#library\?(.*)$/.exec(location.hash);
    const q = match ? new URLSearchParams(match[1]).get('q') : null;
    if (q !== null) $('library-search').value = q;
  }

  function open(item) {
    $('library-reader').hidden = false;
    $('library-reader-title').textContent = item.title;
    $('library-frame').src = fileURL(item);
    $('library-open-file').href = fileURL(item);
    $('library-reader-title').focus({ preventScroll: true });
    $('library-reader').scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  function listening(resources) {
    const panel = document.createElement('section'); panel.className = 'library-listening';
    const heading = document.createElement('h3'); heading.textContent = 'Listen & study'; panel.append(heading);
    for (const resource of resources) {
      // Like `source`, only https URLs become links; anything else is shown as plain text.
      const safe = /^https:\/\//i.test(String(resource.url || ''));
      const link = document.createElement(safe ? 'a' : 'div'); link.className = 'library-listening-item';
      if (safe) { link.href = resource.url; link.target = '_blank'; link.rel = 'noopener noreferrer'; }
      const title = document.createElement('strong'); title.textContent = resource.title;
      const artist = document.createElement('span'); artist.textContent = resource.artist || 'Recording';
      link.append(title, artist);
      if (resource.note) { const note = document.createElement('small'); note.textContent = resource.note; link.append(note); }
      panel.append(link);
    }
    return panel;
  }

  function render() {
    const query = $('library-search').value.trim().toLowerCase();
    const searchable = item => `${item.title} ${item.author} ${(item.resources || []).map(r => `${r.title} ${r.artist}`).join(' ')}`.toLowerCase();
    const shown = items.filter(item => searchable(item).includes(query));
    $('library-count').textContent = `${items.length} ${items.length === 1 ? 'document' : 'documents'} · private cloud library`;
    $('library-books').replaceChildren();
    for (const item of shown) {
      const card = document.createElement('article'); card.className = 'library-book';
      const cover = document.createElement('div'); cover.className = 'library-cover';
      const tag = document.createElement('small'); tag.textContent = 'PDF / MUSIC LIBRARY';
      const title = document.createElement('h2'); title.textContent = item.title;
      const author = document.createElement('p'); author.textContent = item.author || 'Personal collection';
      cover.append(tag, title, author);
      const body = document.createElement('div'); body.className = 'library-book-body';
      const meta = document.createElement('small'); meta.textContent = `${(item.sizeBytes / 1024 / 1024).toFixed(1)} MB · Added ${new Date(item.addedAt).toLocaleDateString()}`;
      const read = document.createElement('button'); read.type = 'button'; read.className = 'button'; read.textContent = 'Read PDF'; read.onclick = () => open(item);
      const download = document.createElement('a'); download.textContent = 'Download'; download.href = fileURL(item) + '?download=1';
      body.append(meta, read, download);
      if (item.source && /^https:\/\//.test(item.source)) {
        const source = document.createElement('a'); source.href = item.source; source.textContent = 'Original source ↗'; source.target = '_blank'; source.rel = 'noopener noreferrer'; body.append(source);
      }
      card.append(cover, body);
      if (item.resources?.length) card.append(listening(item.resources));
      $('library-books').append(card);
    }
    $('library-empty').hidden = shown.length > 0;
    $('library-empty').textContent = items.length ? 'No documents match your search.' : 'Your music bookshelf starts here. Add a PDF to keep it available on any device.';
  }

  async function load() {
    if (loading) return; loading = true; status('Loading library…');
    try { const r = await fetch(api, { credentials: 'same-origin', cache: 'no-store' }); if (!r.ok) throw Error('Could not load the library. Please try again.'); items = await r.json(); render(); status(''); }
    catch (e) { status(e.message); }
    finally { loading = false; }
  }

  $('library-add').onclick = () => { $('library-upload').hidden = !$('library-upload').hidden; if (!$('library-upload').hidden) $('library-file').focus(); };
  $('library-refresh').onclick = load;
  $('library-search').oninput = render;
  $('library-file').onchange = () => { const file = $('library-file').files[0]; if (file && !$('library-title').value) $('library-title').value = file.name.replace(/\.pdf$/i, ''); };
  $('library-form').onsubmit = event => {
    event.preventDefault(); const file = $('library-file').files[0], title = $('library-title').value.trim(); if (!file || !title) return;
    if (file.size > 100 * 1024 * 1024) { status('Choose a PDF up to 100 MB.'); return; }
    const submit = $('library-submit'); submit.disabled = true; status('Uploading PDF… Keep this tab open until it is saved.');
    const xhr = new XMLHttpRequest(); xhr.open('POST', api + '?' + new URLSearchParams({ title, author: $('library-author').value.trim() })); xhr.setRequestHeader('Content-Type', 'application/pdf'); xhr.timeout = 10 * 60 * 1000;
    xhr.upload.onprogress = e => { if (e.lengthComputable) status(`Uploading · ${Math.round(e.loaded / e.total * 100)}%${e.loaded === e.total ? ' · verifying cloud copy…' : ''}`); };
    const fail = () => { submit.disabled = false; status('Upload did not finish. Your original file is unchanged; retry when connected.'); };
    xhr.onerror = fail; xhr.ontimeout = fail;
    xhr.onload = async () => { submit.disabled = false; let result; try { result = JSON.parse(xhr.responseText); } catch { return fail(); } if (xhr.status !== 201) { status(result.error || 'Upload failed. Please retry.'); return; } await load(); $('library-form').reset(); $('library-upload').hidden = true; status(`Saved “${result.title}” to your library.`); };
    xhr.send(file);
  };
  $('library-close').onclick = () => { $('library-frame').removeAttribute('src'); $('library-reader').hidden = true; $('library-search').focus(); };
  document.addEventListener('jazz:view-change', e => { if (e.detail.view === 'library') { applyHashQuery(); if (items.length) render(); load(); } else { $('library-frame').removeAttribute('src'); $('library-reader').hidden = true; } });
  if (isLibraryHash()) { applyHashQuery(); load(); }
})();
