(() => {
  'use strict';
  const header = document.querySelector('#studio .clip-studio-header');
  if (!header) return;
  const panel = document.createElement('section');
  panel.className = 'clip-downloads'; panel.hidden = true;
  panel.setAttribute('aria-label', 'Clip downloads');
  const heading = document.createElement('h2'); heading.textContent = 'Clip downloads';
  panel.append(heading); header.after(panel);
  const pending = new Map();
  document.addEventListener('click', async event => {
    const button = event.target.closest('[data-download-candidate], [data-download-current]');
    if (!button) return;
    let payload;
    try { payload = globalThis.JazzClipStudioDownloads.selection(button.dataset.downloadCandidate); }
    catch (error) {
      panel.hidden = false;
      const message = document.createElement('p'); message.setAttribute('role', 'status');
      message.textContent = error.message; panel.append(message); return;
    }
    const key = JSON.stringify(payload.clips);
    if (pending.has(key)) { pending.get(key).scrollIntoView({ block: 'nearest' }); return; }
    const row = document.createElement('div'); row.className = 'clip-download-row';
    const name = document.createElement('strong'); name.textContent = payload.title;
    const status = document.createElement('span'); status.setAttribute('role', 'status');
    status.textContent = 'Preparing clip… Keep this page open.';
    row.append(name, status); panel.append(row); panel.hidden = false;
    pending.set(key, row); button.disabled = true;
    try {
      const response = await fetch('./api/v1/studio/renders', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload),
      });
      const result = await response.json();
      if (!response.ok) throw new Error(result.error || 'Could not prepare this clip.');
      const link = document.createElement('a');
      link.href = result.url; link.download = result.filename || 'practice-clip.mp4';
      link.rel = 'noreferrer'; link.textContent = 'Download MP4';
      const until = result.expiresAt ? new Date(result.expiresAt).toLocaleTimeString([], {hour:'numeric',minute:'2-digit'}) : null;
      status.textContent = until ? `Ready · link available until ${until}` : 'Ready';
      row.append(link); link.click();
    } catch (error) {
      status.textContent = `Download failed · ${error.message} Try Download clip again.`;
    } finally {
      pending.delete(key); button.disabled = false;
    }
  });
})();
