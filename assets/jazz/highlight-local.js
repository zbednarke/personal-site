(() => {
  'use strict';
  const anchor = document.querySelector('#studio-render');
  if (!anchor) return;
  const apiRoot = './highlight-api';
  const button = document.createElement('button');
  button.type = 'button'; button.id = 'studio-make-film'; button.textContent = "Make today's film";
  button.title = 'Analyze this day’s cloud recordings and render a film on the server';
  const label = document.createElement('label'); label.className = 'studio-magic-length';
  const text = document.createElement('span'); text.textContent = 'Film length';
  const length = document.createElement('select'); length.id = 'studio-magic-length';
  for (const minutes of [1,2,3,5,10]) {
    const option = new Option(`${minutes} min`,String(minutes*60)); option.selected = minutes===2; length.add(option);
  }
  label.append(text,length);anchor.before(label,button);
  const status = document.createElement('span');status.id='studio-magic-status';status.setAttribute('role','status');status.hidden=true;
  anchor.parentElement.append(status);
  const cancel = document.createElement('button');cancel.type='button';cancel.textContent='Cancel film';cancel.hidden=true;anchor.before(cancel);
  const results=document.createElement('span');anchor.parentElement.append(results);
  let busy=false, creating=false, active=null, timer=null;
  const running=j=>['preparing','editing','rendering','publishing'].includes(j.phase);
  const show=message=>{status.textContent=`Film · ${message}`;status.hidden=false;};
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
