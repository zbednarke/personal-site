const test=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const vm=require('node:vm');
function recorder(){let C;const messages=[];vm.runInNewContext(fs.readFileSync(require.resolve('./pcm-recorder-worklet.js'),'utf8'),{AudioWorkletProcessor:class{constructor(){this.port={postMessage:m=>messages.push(m)}}},registerProcessor:(_,c)=>C=c});const p=new C();return {messages,send:type=>p.port.onmessage({data:{type}}),audio:(n,value)=>p.process([[new Float32Array(n).fill(value)]],[[new Float32Array(n)]])};}
test('pause omits breaks, preserves partial buffers, and finish while paused flushes once',()=>{
 const r=recorder();r.audio(100,.25);r.send('pause');r.audio(48000,1);r.send('resume');r.audio(200,.5);r.send('pause');r.audio(96000,1);r.send('flush');r.audio(100,1);
 const samples=r.messages.filter(m=>m.type==='samples').flatMap(m=>Array.from(m.samples));assert.equal(samples.length,300);assert.ok(samples.slice(0,100).every(x=>x===.25));assert.ok(samples.slice(100).every(x=>x===.5));assert.equal(r.messages.filter(m=>m.type==='flushed').length,1);
});
test('repeated pause and resume preserve all active audio across full buffers',()=>{
 const r=recorder();for(let i=0;i<100;i++){r.send('resume');r.audio(5000,.25);r.send('pause');r.send('pause');r.audio(48000,.75);}r.send('flush');assert.equal(r.messages.filter(m=>m.type==='samples').reduce((n,m)=>n+m.samples.length,0),500000);
});
