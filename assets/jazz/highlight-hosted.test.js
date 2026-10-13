const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const {JSDOM}=require('jsdom');
const tick=()=>new Promise(resolve=>setTimeout(resolve,10));

test('film button submits only date and length to the site, without a local worker or snapshot',async()=>{
 const dom=new JSDOM('<input id="studio-date" value="2026-09-28"><div><button id="studio-render">Render</button></div>',{url:'https://zachbednarke.com/jazz/#studio',runScripts:'outside-only'});
 const w=dom.window,calls=[];let jobs=[];
 w.fetch=async(url,options)=>{calls.push({url,options});if(options.method==='POST'){jobs=[{id:'one',date:'2026-09-28',phase:'preparing',message:'Preparing cloud recordings'}];return {ok:true,json:async()=>jobs[0]};}return {ok:true,json:async()=>jobs};};
 try{
  w.eval(fs.readFileSync(__dirname+'/highlight-hosted.js','utf8'));await tick();
  w.document.querySelector('#studio-make-server-film').click();await tick();
  const request=calls.find(c=>c.options.method==='POST');assert.equal(request.url,'./highlight-api/jobs');
  assert.deepEqual(JSON.parse(request.options.body),{date:'2026-09-28',targetSeconds:120});
  assert.equal(calls.some(c=>/localhost|127\.0\.0\.1/.test(c.url)),false);
  assert.equal(w.document.querySelector('#studio-make-server-film').disabled,true);
  jobs=[{...jobs[0],phase:'complete',message:'Ready',film:{video:'/jazz/films/test.mp4',noteMap:'/jazz/films/test.notes.html'}}];
  w.document.dispatchEvent(new w.Event('jazz:studio-date-change'));await tick();
  assert.equal(w.document.querySelector('#studio-make-server-film').disabled,false);
  assert.equal(w.document.querySelector('a[href="/jazz/films/test.notes.html"]').textContent,'Explore note map');
 }finally{w.close();}
});
