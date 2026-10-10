(() => {
  'use strict';
  const origin = 'http://127.0.0.1:8765';
  let popup = null, sent = false, sending = false, requestedLength = 120;
  const button = document.createElement('button');
  button.type = 'button';
  button.id = 'studio-make-film';
  button.textContent = "Make today's film";
  button.title = 'Run the local Astra editor using existing manual, liked and suggested moments';
  const anchor = document.querySelector('#studio-render');
  if (!anchor) return;
  const lengthLabel = document.createElement('label');
  lengthLabel.className = 'studio-magic-length';
  const lengthText = document.createElement('span');
  lengthText.textContent = 'Film length';
  const length = document.createElement('select');
  length.id = 'studio-magic-length';
  for (const minutes of [1, 2, 3, 5, 10]) {
    const option = document.createElement('option');
    option.value = String(minutes * 60); option.textContent = `${minutes} min`;
    option.selected = minutes === 2; length.append(option);
  }
  lengthLabel.append(lengthText, length); anchor.before(lengthLabel);
  anchor.before(button);
  const restore = document.createElement('button');
  restore.id = 'studio-restore-local-backup'; restore.type = 'button'; restore.textContent = 'Restore previous timeline';
  restore.title = 'Restore the timeline saved before opening a local draft';
  anchor.before(restore);
  restore.addEventListener('click', () => {
    try { globalThis.JazzClipStudioLocal.restorePrevious(); status('Previous timeline restored.'); }
    catch (e) { status(e.message); }
  });
  const localStatus = document.createElement('span');
  localStatus.id = 'studio-magic-status';
  localStatus.setAttribute('role', 'status');
  localStatus.setAttribute('aria-label', 'Magic film status');
  localStatus.hidden = true;
  anchor.parentElement.append(localStatus);
  button.setAttribute('aria-describedby', localStatus.id);
  restore.setAttribute('aria-describedby', localStatus.id);
  const status = message => {
    localStatus.textContent = `Magic film · ${message}`;
    localStatus.hidden = !message;
  };
  button.addEventListener('click', () => {
    if (!globalThis.JazzClipStudioLocal) return;
    requestedLength = Number(length.value);
    sent = false;
    popup = window.open(origin, 'jazz-local-highlight');
    if (!popup) { status('Allow the local editor window to open, then try again.'); return; }
    // Existing named windows need a reload to perform a fresh handoff.
    status('Opening the local editor. If it cannot connect, start tools/highlight/start.ps1 on this PC.');
  });
  addEventListener('message', async event => {
    if (event.origin !== origin || event.source !== popup || !globalThis.JazzClipStudioLocal) return;
    if (event.data?.type === 'jazz:highlight-ready') popup.postMessage({ type: 'jazz:highlight-connect' }, origin);
    if (event.data?.type === 'jazz:highlight-ready' && !sent && !sending) {
      sending = true;
      button.disabled = true;
      try {
        status('Preparing the selected day’s existing candidate moments…');
        const snapshot = await globalThis.JazzClipStudioLocal.snapshot();
        snapshot.targetSeconds = requestedLength;
        popup.postMessage({ type: 'jazz:highlight-snapshot', snapshot }, origin);
        sent = true;
        status('Sent to your local Astra editor. Progress and the finished draft appear in its window.');
      } catch (e) { status(`Could not start the local edit: ${e.message}`); }
      finally { sending = false; button.disabled = false; }
    }
    if (event.data?.type === 'jazz:highlight-import') {
      try {
        globalThis.JazzClipStudioLocal.importDraft(event.data);
        status('Local draft opened. Your previous timeline was backed up on this device.');
        window.focus();
      } catch (e) { status(`Draft not opened: ${e.message}`); }
    }
  });
})();
