/* Cloud film editor. Existing automatic selection and local timeline stay independent. */
(() => {
  'use strict';
  const M = globalThis.JazzFilmEditModel;
  if (!M) return;
  const dialog = document.createElement('dialog'); dialog.className = 'film-editor'; dialog.setAttribute('aria-labelledby', 'film-edit-heading');
  dialog.innerHTML = `<header><div><p class="fe-eyebrow">CLIP STUDIO · FILM EDITOR</p><h2 id="film-edit-heading">Edit your film</h2><p id="fe-version"></p></div><button id="fe-close" aria-label="Close film editor">Close</button></header>
  <p id="fe-status" role="status" aria-live="polite"></p><div id="fe-recovery" hidden><p>A saved draft is available. Choose how to continue.</p><button id="fe-resume">Resume cloud draft</button><button id="fe-new">Start separate edit</button><button id="fe-local" hidden>Restore local changes</button></div>
  <div id="fe-conflict" hidden><p>Both versions are safe. Choose which to continue.</p><button id="fe-reload">Reload cloud draft</button><button id="fe-copy">Save local changes as separate edit</button></div>
  <div id="fe-work" hidden><div class="fe-layout"><main><video id="fe-player" controls playsinline preload="metadata"></video><canvas id="fe-pitch" width="1000" height="130" aria-label="Estimated pitch trail"></canvas><p id="fe-player-label">Source passage preview · use Preview intro to see the final animation</p>
  <label class="fe-scrub">Film position <input id="fe-scrub" type="range" min="0" value="0" step="0.05"><output id="fe-position"></output></label>
  <div class="fe-history"><button id="fe-undo">Undo</button><button id="fe-redo">Redo</button></div><div id="fe-timeline" role="group" aria-label="Film passages"></div></main>
  <aside><label>Film title<input id="fe-title" maxlength="120"></label><h3 id="fe-selection">Selected passage</h3><p id="fe-source"></p>
  <div class="fe-trims"><label>Source in (seconds)<input id="fe-in" type="number" min="0" step="0.1"></label><label>Source out (seconds)<input id="fe-out" type="number" min="0.5" step="0.1"></label></div>
  <label>Trim start<input id="fe-in-range" type="range" step="0.1"></label><label>Trim end<input id="fe-out-range" type="range" step="0.1"></label><button id="fe-trim">Apply trim</button>
  <div class="fe-actions"><button data-op="start">Start film here</button><button data-op="end">End film here</button><button data-op="earlier">Move earlier</button><button data-op="later">Move later</button><button data-op="remove">Remove passage</button></div>
  <h3>Film appearance</h3><label class="fe-check"><input id="fe-intro" type="checkbox"> Day overview intro</label><label>Intro length<select id="fe-intro-length"><option value="3">3 seconds</option><option value="4">4 seconds</option><option value="5">5 seconds</option></select></label><p class="fe-help">Whole practice day over the opening audio. Film length stays the same.</p>
  <label class="fe-check"><input id="fe-trail" type="checkbox"> Pitch trail</label><label>Pitch labels<select id="fe-convention"><option value="written-bb">Written B♭ trumpet</option><option value="concert">Concert pitch</option></select></label></aside></div>
  <footer><strong id="fe-total"></strong><span id="fe-saved" role="status"></span><button id="fe-preview">Preview intro</button><button id="fe-render" class="fe-primary">Render this edit</button></footer><div id="fe-job" aria-live="polite"></div></div>`;
  document.body.append(dialog);
  const $ = s => dialog.querySelector(s), esc = value => { const e = document.createElement('span'); e.textContent = value; return e.innerHTML; };
  const clock = ms => `${Math.floor(ms / 60000)}:${(ms / 1000 % 60).toFixed(1).padStart(4, '0')}`;
  let edit = null, history = null, selected = 0, sourceFilm = null, maps = {}, saved = '', timer, saving = null, conflict = false, loadToken = 0, mediaToken = 0, mode = 'source', jobTimer, job = null, operation = false;
  const player = $('#fe-player');
  async function api(path, body, method) {
    const r = await fetch('./highlight-api' + path, { method: method || (body ? 'POST' : 'GET'), credentials: 'same-origin', cache: 'no-store', headers: body ? { 'Content-Type': 'application/json' } : {}, body: body ? JSON.stringify(body) : undefined });
    const data = await r.json(); if (!r.ok) { const e = Error(data.error || 'Request failed'); e.status = r.status; throw e; } return data;
  }
  const status = text => { $('#fe-status').textContent = text; };
  function localKey(id) { return 'jazz.film-edit.v1.' + id; }
  function localSave() { try { localStorage.setItem(localKey(edit.id), JSON.stringify({ revision: edit.revision, content: history.value })); } catch { status('Browser backup unavailable; keep this window open until cloud save completes.'); } }
  function changed() {
    localSave(); clearTimeout(timer);
    if (JSON.stringify(history.value) === saved) $('#fe-saved').textContent = 'Saved to cloud';
    else { $('#fe-saved').textContent = 'Saving…'; timer = setTimeout(() => save().catch(() => {}), 650); }
    render();
  }
  async function save() {
    clearTimeout(timer);
    if (saving) { await saving; return save(); }
    if (!edit || JSON.stringify(history.value) === saved) return;
    if (conflict) throw Error('Resolve the draft conflict before rendering');
    const id = edit.id, content = M.content(history.value), revision = edit.revision;
    saving = (async () => {
      try {
        const result = await api('/edits/' + id, { revision, content }, 'PATCH');
        if (edit.id !== id) return;
        edit = result; saved = JSON.stringify(content); localSave(); $('#fe-saved').textContent = JSON.stringify(history.value) === saved ? 'Saved to cloud' : 'Saving newer changes…';
      } catch (e) {
        if (e.status === 409) { conflict = true; $('#fe-conflict').hidden = false; }
        $('#fe-saved').textContent = e.status === 409 ? 'Save conflict' : 'Not saved to cloud · local backup kept'; status(e.message); throw e;
      } finally { saving = null; }
    })();
    await saving;
    if (JSON.stringify(history.value) !== saved) return save();
  }
  function install(value, local = null) {
    status('Edit passages below. Original film stays unchanged.');
    edit = value; history = new M.History(local || value); selected = 0; saved = JSON.stringify(M.content(value)); conflict = false;
    $('#fe-recovery').hidden = true; $('#fe-conflict').hidden = true; $('#fe-work').hidden = false;
    $('#fe-saved').textContent = local ? 'Restored local changes' : 'Saved to cloud'; render(); select(0, false);
    if (local) changed();
    const installedId = edit.id;
    api('/jobs').then(jobs => {
      if (edit?.id !== installedId) return;
      const latest = jobs.find(j => j.editId === installedId);
      if (latest) { job = latest; showJob(); if (!['complete','failed','cancelled'].includes(job.phase)) pollJob(); }
    }).catch(() => {});
  }
  async function open(film) {
    if (edit && JSON.stringify(history.value) !== saved) { if (!dialog.open) dialog.showModal(); status('Your previous edit has unsaved changes. Save it or resolve its conflict before opening another film.'); return; }
    const token = ++loadToken; sourceFilm = film; edit = null; history = null; clearTimeout(timer); clearTimeout(jobTimer); job = null; player.pause();
    $('#fe-work').hidden = true; $('#fe-recovery').hidden = true; $('#fe-conflict').hidden = true; $('#fe-job').replaceChildren(); status('Loading saved film edit…');
    if (!dialog.open) dialog.showModal();
    try {
      const [base, drafts] = await Promise.all([api('/films/' + film.id + '/edit'), api('/edits')]);
      if (token !== loadToken) return;
      maps = {}; if (film.analysis) fetch(film.analysis).then(r => r.json()).then(value => { if (token === loadToken) { maps = value; if(history) $('#fe-timeline').querySelectorAll('button').forEach(b=>paintSpark(b,history.value.clips[+b.dataset.index])); } }).catch(() => {});
      $('#fe-version').textContent = `${film.date} · ${film.title} · exact saved selections`;
      const prior = drafts.find(d => d.sourceFilmId === film.id);
      if (!prior) { install(await api('/edits', { sourceFilmId: film.id })); status('Edit passages below. Original film stays unchanged.'); }
      else {
        $('#fe-recovery').hidden = false; status('Resume your saved work or begin another version.');
        $('#fe-resume').onclick = () => run(async () => install(await api('/edits/' + prior.id)));
        $('#fe-new').onclick = () => run(async () => install(await api('/edits', { sourceFilmId: film.id })));
        let local; try { local = JSON.parse(localStorage.getItem(localKey(prior.id))); } catch {}
        $('#fe-local').hidden = !local;
        $('#fe-local').onclick = () => run(async () => {
          const remote = await api('/edits/' + prior.id);
          if (local.revision !== remote.revision) { install(remote, local.content); conflict = true; $('#fe-conflict').hidden = false; clearTimeout(timer); status('Local changes are from an older revision. Save as a separate edit or reload cloud.'); }
          else install(remote, local.content);
        });
      }
    } catch (e) { status(e.message); }
  }
  async function run(fn) { try { await fn(); } catch (e) { status(e.message); } }
  function paintSpark(b,c) {
    if (!c) return;
    const ctx=b.querySelector('canvas').getContext('2d'),notes=(maps[c.recordingId]?.notes||[]).filter(n=>n.endMs>c.startMs&&n.startMs<c.endMs);
      const lo=Math.min(48,...notes.map(n=>n.midi)),hi=Math.max(72,...notes.map(n=>n.midi));ctx.fillStyle=c.title.toLowerCase().includes('long tone')?'#a5d3be':'#e5b979';
      for(const n of notes){const x=(Math.max(c.startMs,n.startMs)-c.startMs)/(c.endMs-c.startMs)*160,w=(Math.min(c.endMs,n.endMs)-Math.max(c.startMs,n.startMs))/(c.endMs-c.startMs)*160;ctx.fillRect(x,28-(n.midi-lo)/(hi-lo)*25,Math.max(1,w),2);}
  }
  function render() {
    const v = history.value; selected = Math.max(0, Math.min(selected, v.clips.length - 1));
    $('#fe-title').value = v.title; $('#fe-total').textContent = `${v.clips.length} passages · ${clock(M.duration(v))}`;
    $('#fe-intro').checked = v.appearance.intro; $('#fe-trail').checked = v.appearance.pitchTrail; $('#fe-intro-length').value = v.appearance.introSeconds; $('#fe-convention').value = v.appearance.pitchConvention;
    $('#fe-intro').disabled = M.duration(v) < v.appearance.introSeconds * 1000;
    $('#fe-preview').disabled = $('#fe-render').disabled = !v.clips.length || operation;
    $('[data-op=earlier]').disabled = !v.clips.length || selected === 0; $('[data-op=later]').disabled = !v.clips.length || selected === v.clips.length-1;
    $('#fe-undo').disabled = !history.past.length; $('#fe-redo').disabled = !history.future.length;
    $('#fe-scrub').max = M.duration(v)/1000;
    let offset = 0;
    $('#fe-timeline').innerHTML = v.clips.map((c, i) => {
      const start = offset; offset += c.endMs - c.startMs;
      return `<button draggable="true" data-index="${i}" aria-pressed="${i === selected}" class="${c.title.toLowerCase().includes('long tone') ? 'fe-mint' : 'fe-amber'}"><small>${i+1} · ORIGINAL ${c.originalPassage}</small><strong>${esc(c.title)}</strong><canvas class="fe-spark" width="160" height="32" aria-hidden="true"></canvas><span>${clock(start)}–${clock(offset)}</span></button>`;
    }).join('') || '<p>No passages. Undo to restore your edit.</p>';
    $('#fe-timeline').querySelectorAll('button').forEach(b => {
      paintSpark(b,v.clips[+b.dataset.index]);
      b.onclick = () => select(+b.dataset.index, false);
      b.ondragstart = e => e.dataTransfer.setData('text/plain', b.dataset.index);
      b.ondragover = e => e.preventDefault();
      b.ondrop = e => { e.preventDefault(); const from = Number(e.dataTransfer.getData('text/plain')); if (Number.isInteger(from)) mutate('move', +b.dataset.index, from); };
    });
    const c = v.clips[selected]; $('#fe-selection').textContent = c ? `Passage ${selected+1} · original ${c.originalPassage}` : 'No passage selected';
    $('#fe-source').textContent = c ? `${c.title} · source ${clock(c.startMs)}–${clock(c.endMs)}` : '';
    for (const id of ['#fe-in','#fe-out','#fe-in-range','#fe-out-range']) { $(id).disabled = !c; $(id).max = c ? edit.sources[c.recordingId].durationMs/1000 : 0; }
    if (c) { $('#fe-in').value = $('#fe-in-range').value = c.startMs/1000; $('#fe-out').value = $('#fe-out-range').value = c.endMs/1000; }
    $('#fe-version').textContent = `${edit.date} · cloud revision ${edit.revision} · original film preserved`;
  }
  function mutate(action, value, index = selected) {
    run(async () => {
      history.change(M.apply(history.value, action, index, value));
      if (action === 'move') selected = value;
      else if (action === 'start') selected = 0;
      changed(); select(selected, false);
    });
  }
  async function select(index, play, sourceMs) {
    player.pause(); const token = ++mediaToken; mode = 'source'; selected = index; render();
    const strip=$('#fe-timeline'),button=strip.querySelector('button[aria-pressed=true]');
    if(button){const delta=button.getBoundingClientRect().left-strip.getBoundingClientRect().left;if(delta<0)strip.scrollLeft+=delta;else if(delta+button.offsetWidth>strip.clientWidth)strip.scrollLeft+=delta+button.offsetWidth-strip.clientWidth;}
    const c = history.value.clips[selected]; if (!c) { player.removeAttribute('src'); return; }
    $('#fe-pitch').hidden = !history.value.appearance.pitchTrail;
    $('#fe-player-label').textContent = 'Loading source passage…';
    try {
      // API remains authoritative for source ownership and expiring playback URLs.
      const response = await fetch(`./api/v1/recordings/${encodeURIComponent(c.recordingId)}/playback-url?asset=video`, { method:'POST',headers:{'Content-Type':'application/json'},body:'{}' });
      let data = await response.json();
      if (!response.ok) {
        const fallback = await fetch(`./api/v1/recordings/${encodeURIComponent(c.recordingId)}/playback-url?asset=audio`, { method:'POST',headers:{'Content-Type':'application/json'},body:'{}' });
        data = await fallback.json(); if (!fallback.ok) throw Error('Source playback unavailable');
      }
      if (token !== mediaToken) return;
      player.src = data.url;
      player.onloadedmetadata = () => { if (token !== mediaToken) return; player.currentTime = (sourceMs ?? c.startMs)/1000; if (play) player.play().catch(e => status(e.message)); };
      $('#fe-player-label').textContent = 'Source passage preview · use Preview intro for the rendered animation';
    } catch (e) { if (token === mediaToken) status(e.message); }
  }
  player.ontimeupdate = () => {
    if (!edit || mode !== 'source') return;
    const c = history.value.clips[selected]; if (!c) return;
    let time = player.currentTime * 1000;
    if (time < c.startMs - 80) { player.currentTime = c.startMs/1000; return; }
    if (time >= c.endMs - 25 && !player.paused) {
      if (selected + 1 < history.value.clips.length) select(selected + 1, true);
      else player.pause();
    }
    const offset = history.value.clips.slice(0,selected).reduce((n,c) => n+c.endMs-c.startMs,0);
    const position = offset+Math.max(0,Math.min(time-c.startMs,c.endMs-c.startMs)); $('#fe-scrub').value=position/1000; $('#fe-position').textContent=clock(position);
    drawPitch(c,time);
  };
  function drawPitch(c,t) {
    const canvas=$('#fe-pitch');canvas.hidden=mode!=='source'||!history.value.appearance.pitchTrail;
    const ctx=canvas.getContext('2d');ctx.clearRect(0,0,1000,130);ctx.fillStyle='#101e1c';ctx.fillRect(0,0,1000,130);
    const notes=maps[c.recordingId]?.notes || [], shift=history.value.appearance.pitchConvention==='written-bb'?2:0;
    const visible=notes.filter(n => n.endMs>t-2000 && n.startMs<t+4000);const pitches=visible.map(n=>n.midi+shift),lo=Math.min(48,...pitches),hi=Math.max(72,...pitches);
    ctx.fillStyle='#a5d3be';for(const n of visible){const x=340+(n.startMs-t)*.15,y=110-(n.midi+shift-lo)/(hi-lo)*85;ctx.fillRect(Math.max(0,x),y,Math.max(2,(n.endMs-n.startMs)*.15),4);}
    ctx.fillStyle='#eeefdd';ctx.fillRect(340,15,2,105);ctx.font='13px sans-serif';ctx.fillText(notes.length?'Estimated pitch · '+(shift?'written B♭':'concert'):'Pitch unavailable',15,18);
  }
  $('#fe-scrub').onchange = e => { const target=M.at(history.value,+e.target.value*1000);if(target)select(target.index,!player.paused,target.sourceMs); };
  $('#fe-title').onchange=e=>mutate('title',e.target.value);
  $('#fe-in-range').oninput=e=>{$('#fe-in').value=e.target.value;};$('#fe-out-range').oninput=e=>{$('#fe-out').value=e.target.value;};
  $('#fe-trim').onclick=()=>{const c=history.value.clips[selected];if(c)mutate('trim',[+$('#fe-in').value*1000,+$('#fe-out').value*1000,edit.sources[c.recordingId].durationMs]);};
  dialog.querySelectorAll('[data-op]').forEach(b=>b.onclick=()=>{const op=b.dataset.op;mutate(op==='earlier'||op==='later'?'move':op,op==='earlier'?selected-1:selected+1);});
  $('#fe-undo').onclick=()=>{history.undo();changed();select(selected,false);};$('#fe-redo').onclick=()=>{history.redo();changed();select(selected,false);};
  for(const id of ['#fe-intro','#fe-trail','#fe-intro-length','#fe-convention'])$(id).onchange=()=>mutate('settings',{intro:$('#fe-intro').checked,pitchTrail:$('#fe-trail').checked,introSeconds:+$('#fe-intro-length').value,pitchConvention:$('#fe-convention').value});
  $('#fe-reload').onclick=()=>run(async()=>install(await api('/edits/'+edit.id)));
  $('#fe-copy').onclick=()=>run(async()=>install(await api('/edits',{sourceFilmId:edit.sourceFilmId,content:history.value})));
  function showJob(){
    if(!job)return;
    const host=$('#fe-job');host.replaceChildren();const p=document.createElement('p');p.textContent=`${job.preview?'Preview':'Film'} · revision ${job.editRevision} · ${job.message}`+(history && (edit.revision!==job.editRevision || JSON.stringify(history.value)!==saved)?' · New changes are not in this render':'');host.append(p);
    if(!['complete','failed','cancelled'].includes(job.phase)){const b=document.createElement('button');b.textContent='Cancel render';b.onclick=()=>run(()=>api('/cancel',{id:job.id}));host.append(b);}
    if(job.phase==='complete'&&job.previewUrl){const b=document.createElement('button');b.textContent='Play rendered intro preview';b.onclick=()=>{player.pause();++mediaToken;player.onloadedmetadata=null;mode='rendered';player.src=job.previewUrl;$('#fe-pitch').hidden=true;$('#fe-player-label').textContent=`Rendered preview · revision ${job.editRevision}`;player.play().catch(e=>status(e.message));};host.append(b);}
    if(job.phase==='complete'&&job.film){const a=document.createElement('a');a.href=job.film.video;a.textContent='Download new film';a.download=`${edit.date}-practice-revision-${job.editRevision}.mp4`;host.append(a);const b=document.createElement('button');b.textContent='Watch new version';b.onclick=()=>{++mediaToken;player.pause();player.onloadedmetadata=null;mode='rendered';player.src=job.film.video;$('#fe-pitch').hidden=true;player.play().catch(e=>status(e.message));};host.append(b);}
    if(['failed','cancelled'].includes(job.phase)){const b=document.createElement('button');b.textContent='Retry saved edit';b.onclick=()=>submit(job.preview);host.append(b);}
  }
  async function pollJob(){clearTimeout(jobTimer);const id=job.id;try{const result=await api('/jobs/'+id);if(job?.id!==id)return;job=result;showJob();if(!['complete','failed','cancelled'].includes(job.phase))jobTimer=setTimeout(pollJob,2500);}catch(e){status('Render may still be running. Reconnecting…');jobTimer=setTimeout(pollJob,5000);}}
  async function submit(preview){
    if(operation)return;operation=true;status(preview?'Preparing intro preview…':'Preparing your saved film edit…');render();
    try{await save();const revision=edit.revision;let pending;const keyName=localKey(edit.id)+'.request';try{pending=JSON.parse(localStorage.getItem(keyName));}catch{}
      if(!pending||pending.revision!==revision||pending.preview!==preview)pending={revision,preview,idempotencyKey:crypto.randomUUID()};
      localStorage.setItem(keyName,JSON.stringify(pending));
      job=await api('/edits/'+edit.id+(preview?'/previews':'/renders'),{revision,idempotencyKey:pending.idempotencyKey});localStorage.removeItem(keyName);showJob();pollJob();
    }catch(e){status(e.message);}finally{operation=false;render();}
  }
  $('#fe-preview').onclick=()=>submit(true);$('#fe-render').onclick=()=>submit(false);
  function close(){++mediaToken;player.onloadedmetadata=null;player.pause();dialog.close();if(edit)save().catch(()=>{});}
  $('#fe-close').onclick=close;dialog.addEventListener('cancel',e=>{e.preventDefault();close();});
  dialog.addEventListener('keydown',e=>{if(/INPUT|TEXTAREA|SELECT/.test(e.target.tagName)||e.target.isContentEditable)return;if((e.key==='Delete'||e.key==='Backspace')&&e.target.closest('#fe-timeline')){e.preventDefault();mutate('remove');setTimeout(()=>$('#fe-timeline button[aria-pressed=true]')?.focus(),0);}if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()==='z'){e.preventDefault();$(e.shiftKey?'#fe-redo':'#fe-undo').click();}});
  window.addEventListener('beforeunload',e=>{if(edit&&JSON.stringify(history.value)!==saved){localSave();e.preventDefault();e.returnValue='';}});
  globalThis.JazzFilmEditor={open};
})();
