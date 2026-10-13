// Site-VM film service (/jazz/highlight-api, tools/highlight/hosted.py): note-map
// films with saved exact edits that the film editor can reopen. Restored from
// the out-of-band Jazz line beside the cloud Magic Film button (highlight-local.js).
(() => {
  'use strict';
  const anchor = document.querySelector('#studio-render');
  if (!anchor) return;
  const apiRoot = './highlight-api';
  const button = document.createElement('button');
  button.type = 'button'; button.id = 'studio-make-server-film'; button.textContent = 'Make note-map film';
  button.title = 'Analyze every take for this day on the site server, then render a film with a whole-day note map you can edit';
  const label = document.createElement('label'); label.className = 'studio-magic-length studio-server-film-length';
  const text = document.createElement('span'); text.textContent = 'Note-map film length';
  const length = document.createElement('select'); length.id = 'studio-server-film-length';
  for (const minutes of [1,2,3,5,10]) {
    const option = new Option(`${minutes} min`,String(minutes*60)); option.selected = minutes===2; length.add(option);
  }
  label.append(text,length);anchor.before(label,button);
  const status = document.createElement('span');status.id='studio-server-film-status';status.setAttribute('role','status');status.hidden=true;
  anchor.parentElement.append(status);
  const cancel = document.createElement('button');cancel.type='button';cancel.id='studio-cancel-server-film';cancel.textContent='Cancel note-map film';cancel.hidden=true;anchor.before(cancel);
  const results=document.createElement('span');anchor.parentElement.append(results);
  let busy=false, creating=false, active=null, timer=null;
  const running=j=>['preparing','editing','rendering','publishing'].includes(j.phase);
  const show=message=>{status.textContent=`Note-map film · ${message}`;status.hidden=false;};
  async function api(path,body) {
    const response=await fetch(apiRoot+path,{method:body?'POST':'GET',credentials:'same-origin',headers:body?{'Content-Type':'application/json'}:{},body:body?JSON.stringify(body):undefined,cache:'no-store'});
    const data=await response.json();if(!response.ok)throw Error(data.error||'Film service unavailable');return data;
  }
  function render(job) {
    results.replaceChildren();
    if (!job) return;
    show(`${job.date} · ${job.message}`);
    if (job.phase==='complete' && job.film) {
      for (const [name,url] of [['Watch / download film',job.film.video],['Explore note map',job.film.noteMap]]) {
        if(typeof url!=='string'||!/^\/jazz\/films\/[a-zA-Z0-9_.-]+$/.test(url))continue;
        const a=document.createElement('a');a.className='button';a.textContent=name;a.href=url;a.target='_blank';a.rel='noopener';results.append(a);
      }
      const open=document.createElement('button');open.type='button';open.textContent='Open draft in timeline';open.onclick=()=>{try{globalThis.JazzClipStudioLocal.importDraft({date:job.date,project:job.project});show('Draft opened; previous timeline backed up.');}catch(e){show(e.message);}};results.append(open);
    }
  }
  async function refresh() {
    clearTimeout(timer);
    try {
      const jobs=await api('/jobs');active=jobs.find(running)||null;busy=Boolean(active);
      const date=document.querySelector('#studio-date').value;
      render(active||jobs.find(j=>j.date===date&&!j.preview&&j.kind!=="day-analysis"));
      button.disabled=busy||creating;cancel.hidden=!active;
    } catch(e) {show(`${e.message}. A running server job can continue; reconnect to check progress.`);}
    timer=setTimeout(refresh,busy?3000:20000);
  }
  button.onclick=async()=>{
    creating=true;button.disabled=true;show('Preparing cloud recordings…');
    try {const job=await api('/jobs',{date:document.querySelector('#studio-date').value,targetSeconds:Number(length.value)});active=job;busy=true;render(job);}
    catch(e){show(e.message);}
    finally{creating=false;button.disabled=busy;if(busy)refresh();}
  };
  cancel.onclick=async()=>{if(!active)return;cancel.disabled=true;try{await api('/cancel',{id:active.id});show('Cancellation requested; original recordings are unchanged.');}catch(e){show(e.message);}finally{cancel.disabled=false;refresh();}};
  document.addEventListener('jazz:studio-date-change',refresh);
  addEventListener('hashchange',()=>{if(location.hash==='#studio')refresh();});
  if(location.hash==='#studio')refresh();
})();
