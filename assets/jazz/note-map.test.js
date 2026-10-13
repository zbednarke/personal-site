const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const {JSDOM}=require('jsdom');

const template=fs.readFileSync(path.join(__dirname,'../../tools/highlight/note-map.html'),'utf8');
const note=(startMs,endMs,midi=60)=>({startMs,endMs,midi,confidence:.95,spreadCents:12});
const fixture=()=>({
 first:{title:'Long tones',section:'Long tones',durationMs:60000,notes:[note(0,10000),note(55000,60000,62)]},
 second:{title:'Study 1',section:'Training',durationMs:120000,notes:[note(0,10000,64),note(119000,120000,67)]}
});
function mount(data){
 const encode=value=>JSON.stringify(value).replace(/</g,'\\u003c');
 const dom=new JSDOM(template.replace('__ANALYSIS__',encode(data)).replace('__METADATA__',encode({date:'2026-09-28'})),{runScripts:'outside-only',pretendToBeVisual:true});
 const w=dom.window,draws=[];
 w.HTMLElement.prototype.scrollIntoView=function(){};
 w.HTMLCanvasElement.prototype.getBoundingClientRect=function(){return {left:0,top:0,width:1000,height:350,right:1000,bottom:350};};
 w.HTMLCanvasElement.prototype.setPointerCapture=function(){};
 w.HTMLCanvasElement.prototype.getContext=function(){
  const id=this.id;
  return new Proxy({fillText(text){draws.push({id,text});}}, {get(target,key){return key in target?target[key]:()=>{};},set(target,key,value){target[key]=value;return true;}});
 };
 w.ResizeObserver=class {observe(){} disconnect(){}};
 try{w.eval([...w.document.querySelectorAll('script')].find(s=>!s.id).textContent);}catch(error){w.close();throw error;}
 const $=id=>w.document.getElementById(id);
 const openTable=()=>{$('note-data').open=true;$('note-data').dispatchEvent(new w.Event('toggle'));};
 return {dom,w,$,draws,openTable,close:()=>{$('note-data').ontoggle=null;w.close();},take:i=>w.document.querySelectorAll('.take')[i]};
}

test('initial map includes all takes and the last note, with whole-day scope',()=>{
 const m=mount(fixture());try{
  assert.equal(m.$('total-time').textContent,'3.0 min');assert.equal(m.$('take-count').textContent,'2');assert.equal(m.$('note-count').textContent,'4');
  assert.equal(m.$('focus').hidden,true);assert.match(m.$('data-scope').textContent,/whole day/);
  assert.equal(m.w.document.querySelectorAll('.take').length,2);
  assert.ok(m.draws.some(d=>d.id==='overview'&&d.text==='3:00'));
  m.openTable();assert.equal(m.$('notes').rows.length,4);
  assert.match(m.$('notes').rows[3].textContent,/1:59–2:00/);
 }finally{m.close();}
});

test('take selection uses cumulative time but table retains local time; reset restores all takes',()=>{
 const m=mount(fixture());try{
  m.take(1).click();m.openTable();
  assert.equal(m.$('focus').hidden,false);assert.match(m.$('focus-range').textContent,/1:00–3:00/);
  assert.equal(m.take(1).getAttribute('aria-pressed'),'true');assert.equal(m.take(0).getAttribute('aria-pressed'),'false');
  assert.equal(m.$('notes').rows.length,2);assert.match(m.$('notes').rows[0].textContent,/0:00–0:10/);
  assert.equal(m.$('later').disabled,true);
  m.$('reset').click();assert.equal(m.$('focus').hidden,true);assert.equal(m.$('notes').rows.length,4);assert.equal(m.take(1).getAttribute('aria-pressed'),'false');
  m.take(0).click();m.$('close-focus').click();assert.equal(m.w.document.activeElement,m.$('reset'));
 }finally{m.close();}
});

test('zoom and pan clamp to day boundaries and retain a minimum five-second window',()=>{
 const m=mount(fixture());try{
  m.take(0).click();for(let i=0;i<10;i++)m.$('zoom-in').click();
  assert.equal(m.$('zoom-in').disabled,true);assert.match(m.$('focus-range').textContent,/5 seconds shown/);
  for(let i=0;i<60;i++)m.$('earlier').click();assert.equal(m.$('earlier').disabled,true);assert.match(m.$('focus-range').textContent,/0:00–0:05/);
  for(let i=0;i<60;i++)m.$('later').click();assert.equal(m.$('later').disabled,true);assert.match(m.$('focus-range').textContent,/2:55–3:00/);
  for(let i=0;i<10;i++)m.$('zoom-out').click();assert.equal(m.$('zoom-out').disabled,true);assert.match(m.$('focus-range').textContent,/0:00–3:00/);
  assert.equal(m.$('fit-take').disabled,true);m.take(0).click();m.$('zoom-in').click();m.$('fit-take').click();assert.match(m.$('focus-range').textContent,/0:00–1:00/);
 }finally{m.close();}
});

test('transpose changes pitch names and distribution without changing timing',()=>{
 const m=mount(fixture());try{
  m.openTable();const before=m.$('notes').rows[0].cells[1].textContent;
  assert.equal(m.$('notes').rows[0].cells[2].textContent,'C4');
  m.$('transpose').value='2';m.$('transpose').dispatchEvent(new m.w.Event('change'));
  assert.equal(m.$('notes').rows[0].cells[2].textContent,'D4');assert.equal(m.$('notes').rows[0].cells[1].textContent,before);
  assert.match(m.$('distribution').children[2].getAttribute('aria-label'),/10.0 seconds/);
 }finally{m.close();}
});

test('empty and note-free recordings render explicit usable states',()=>{
 for(const data of [{},{silent:{durationMs:30000,notes:[]},zero:{durationMs:0,notes:[]}}]){
  const m=mount(data);try{
   assert.equal(m.$('note-count').textContent,'0');m.openTable();assert.equal(m.$('page-label').textContent,'No detected notes');
   assert.equal(m.$('previous-page').disabled,true);assert.equal(m.$('next-page').disabled,true);
   assert.equal(m.$('empty-day').hidden,Object.keys(data).length>0);
   if(data.silent){m.take(0).click();assert.match(m.$('focus-range').textContent,/30 seconds shown/);assert.equal(m.take(1).disabled,true);}
   assert.equal(m.draws.some(d=>/NaN|Infinity/.test(d.text)),false);
  }finally{m.close();}
 }
});

test('malformed records and notes are ignored, and valid intervals are clipped to the take',()=>{
 const m=mount({broken:null,good:{durationMs:10000,notes:[null,{},note(0,500,NaN),note(0,500,200),note(100,50),note(-100,11000),note(10000,12000)]},badDuration:{durationMs:'wrong',notes:[note(0,500)]}});
 try{m.openTable();assert.equal(m.$('note-count').textContent,'1');assert.equal(m.$('notes').rows.length,1);assert.match(m.$('notes').textContent,/0:00–0:10/);assert.equal(m.draws.some(d=>/NaN|Infinity/.test(d.text)),false);}finally{m.close();}
});

test('large note table is lazy and paginated, and titles cannot inject markup',()=>{
 const title='<img src=x onerror="window.injected=true">';
 const m=mount({many:{title,section:'<svg onload=alert(1)>',durationMs:150000,notes:Array.from({length:121},(_,i)=>note(i*1000,i*1000+500))}});
 try{
  assert.equal(m.$('notes').rows.length,0);assert.equal(m.w.document.querySelectorAll('img,svg').length,0);assert.equal(m.w.injected,undefined);
  assert.equal(m.w.document.querySelector('.take-title').textContent,title);
  m.openTable();assert.equal(m.$('notes').rows.length,50);assert.equal(m.$('page-label').textContent,'1–50 of 121 notes');
  m.$('next-page').click();assert.equal(m.$('notes').rows.length,50);assert.equal(m.$('page-label').textContent,'51–100 of 121 notes');
  m.$('next-page').click();assert.equal(m.$('notes').rows.length,21);assert.equal(m.$('next-page').disabled,true);
  m.$('previous-page').click();assert.equal(m.$('page-label').textContent,'51–100 of 121 notes');
  m.$('reset').click();assert.equal(m.$('page-label').textContent,'1–50 of 121 notes');assert.equal(m.w.document.querySelectorAll('img,svg').length,0);
 }finally{m.close();}
});

test('overview drag can select across take boundaries without dropping edge notes',()=>{
 const m=mount(fixture());try{
  const canvas=m.$('overview'),x=ms=>43+ms/180000*945;
  canvas.onpointerdown({button:0,pointerId:1,clientX:x(55000)});
  canvas.onpointerup({pointerId:1,clientX:x(65000)});m.openTable();
  assert.match(m.$('focus-title').textContent,/2 takes/);assert.match(m.$('focus-range').textContent,/0:55–1:05/);
  assert.equal(m.$('notes').rows.length,2);assert.equal(m.$('fit-take').disabled,true);
  canvas.onpointerdown({button:0,pointerId:2,clientX:x(150000)});canvas.onpointercancel();canvas.onpointerup({pointerId:2,clientX:x(170000)});
  assert.match(m.$('focus-range').textContent,/0:55–1:05/);
 }finally{m.close();}
});
