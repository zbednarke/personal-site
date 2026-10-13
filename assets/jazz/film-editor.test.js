const {test}=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const {JSDOM}=require('jsdom');
const fixture=()=>({id:'e',date:'2026-09-30',sourceFilmId:'f',revision:1,title:'Day',sources:{r:{durationMs:130000}},appearance:{intro:true,introSeconds:4,pitchTrail:true,pitchConvention:'written-bb'},clips:Array.from({length:13},(_,i)=>({id:'c'+i,recordingId:'r',title:'Study',startMs:i*10000,endMs:(i+1)*10000,originalPassage:i+1}))});
const flush=()=>new Promise(r=>setTimeout(r,20));
function mount(){const dom=new JSDOM('<body></body>',{url:'https://zachbednarke.com/jazz/',runScripts:'outside-only'}),w=dom.window;let edit=fixture();const calls=[];let conflict=false;
w.HTMLDialogElement.prototype.showModal=function(){this.open=true;};w.HTMLDialogElement.prototype.close=function(){this.open=false;};w.HTMLMediaElement.prototype.pause=function(){};w.HTMLMediaElement.prototype.play=async function(){};w.HTMLCanvasElement.prototype.getContext=()=>new Proxy({},{get:()=>()=>{}});
w.fetch=async(url,opts={})=>{const body=opts.body?JSON.parse(opts.body):null;calls.push({url,body,method:opts.method});let result;
if(url.includes('playback-url'))result={url:'https://storage.googleapis.com/test'};
else if(url.endsWith('/films/f/edit'))result={content:edit};
else if(url.endsWith('/edits')&&opts.method==='POST')result=edit;
else if(url.endsWith('/edits'))result=[];
else if(url.endsWith('/edits/e')&&opts.method==='PATCH'){if(conflict)return{ok:false,status:409,json:async()=>({error:'Conflict'})};edit={...edit,...body.content,revision:edit.revision+1};result=edit;}
else if(url.endsWith('/edits/e/renders'))result={id:'j',phase:'complete',editRevision:body.revision,message:'Ready',film:{video:'/jazz/films/test.mp4'}};
else if(url.endsWith('/jobs/j'))result={id:'j',phase:'complete',editRevision:edit.revision,message:'Ready',film:{video:'/jazz/films/test.mp4'}};
else if(url.endsWith('/jobs'))result=[];
else throw Error('Unexpected '+url);
return{ok:true,status:200,json:async()=>result};};
for(const name of ['film-edit-model.js','film-editor.js'])w.eval(fs.readFileSync(__dirname+'/'+name,'utf8'));
return{w,calls,$:id=>w.document.getElementById(id),close:()=>w.close(),conflict:()=>{conflict=true;}};}
test('user can keep passage 9 onward, undo, swap, save and render exact revision',async()=>{const m=mount();try{await m.w.JazzFilmEditor.open({id:'f',date:'2026-09-30',title:'Day'});await flush();m.w.document.querySelector('[data-index="8"]').click();await flush();m.w.document.querySelector('[data-op="start"]').click();await flush();assert.equal(m.$('fe-timeline').children.length,5);m.$('fe-undo').click();await flush();assert.equal(m.$('fe-timeline').children.length,13);m.$('fe-redo').click();await flush();m.w.document.querySelector('[data-index="2"]').click();await flush();m.w.document.querySelector('[data-op="earlier"]').click();await flush();m.$('fe-render').click();await flush();await flush();const patch=m.calls.find(c=>c.method==='PATCH');assert.deepEqual(patch.body.content.clips.map(c=>c.originalPassage),[9,11,10,12,13]);const request=m.calls.find(c=>c.url.endsWith('/renders'));assert.equal(request.body.revision,2);assert.ok(m.$('fe-job').textContent.includes('Download new film'));}finally{m.close();}});
test('conflict preserves changes and prevents render submission',async()=>{const m=mount();try{await m.w.JazzFilmEditor.open({id:'f',date:'2026-09-30',title:'Day'});await flush();m.conflict();m.w.document.querySelector('[data-op="remove"]').click();await flush();m.$('fe-render').click();await flush();assert.equal(m.$('fe-conflict').hidden,false);assert.equal(m.$('fe-timeline').children.length,12);assert.ok(!m.calls.some(c=>c.url.endsWith('/renders')));assert.ok(m.w.localStorage.getItem('jazz.film-edit.v1.e'));}finally{m.close();}});
test('delete shortcut never removes a passage while typing in title',async()=>{const m=mount();try{await m.w.JazzFilmEditor.open({id:'f',date:'2026-09-30',title:'Day'});await flush();m.$('fe-title').dispatchEvent(new m.w.KeyboardEvent('keydown',{key:'Backspace',bubbles:true}));assert.equal(m.$('fe-timeline').children.length,13);}finally{m.close();}});
