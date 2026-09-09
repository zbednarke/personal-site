const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {normalizeSettings,tapTempo,MetronomeEngine} = require('./metronome.js');
function processor() {
 let Processor; const events=[];
 vm.runInNewContext(fs.readFileSync(require.resolve('./metronome-worklet.js'),'utf8'),{AudioWorkletProcessor:class {constructor(){this.port={postMessage:m=>events.push(m)}}},sampleRate:48000,registerProcessor:(_,p)=>Processor=p});
 const p=new Processor();
 return {p,events,send:data=>p.port.onmessage({data}),render:n=>{const output=new Float32Array(n);p.process([],[[output]]);return output}};
}
test('sample clock produces exact tempo and bar accents without UI timers',()=>{
 const x=processor();x.send({running:true,bpm:120,beats:4,volume:.35});const out=x.render(480000);
 assert.equal(x.events.length,20);assert.deepEqual(x.events.map(e=>e.beat),Array.from({length:20},(_,i)=>i%4));
 assert.equal(x.events.filter(e=>e.accent).length,5);assert.ok(out.some(v=>v!==0));assert.ok(out.every(v=>Number.isFinite(v)&&Math.abs(v)<=.35));
 x.send({running:false});assert.ok(x.render(48000).every(v=>v===0));assert.equal(x.events.length,20);
 x.send({running:true,volume:0});assert.ok(x.render(48000).every(v=>v===0));assert.equal(x.events[20].beat,0);assert.equal(x.events.length,22);
});
test('tempo changes preserve fractional position within the beat',()=>{
 const x=processor();x.send({running:true,bpm:120});x.render(12000);x.send({bpm:240});x.render(6000);assert.equal(x.events.length,1);x.render(1);assert.equal(x.events.length,2);
});
test('settings and tap tempo are bounded and handle new tapping sessions',()=>{
 assert.deepEqual(normalizeSettings(null),{bpm:120,beats:4,volume:.35});assert.deepEqual(normalizeSettings({bpm:900,beats:-1,volume:3}),{bpm:300,beats:1,volume:1});
 assert.equal(tapTempo([0,500],1000).bpm,120);assert.equal(tapTempo([0],3000).bpm,null);
});
function engineFixture(modulePromise=Promise.resolve()){
 const nodes=[];let context;
 class Context {constructor(){context=this;this.destination={};this.audioWorklet={addModule:()=>modulePromise}}resume(){return Promise.resolve()}close(){this.closed=true;return Promise.resolve()}}
 class Node {constructor(ctx,name,options){this.options=options;this.messages=[];this.port={postMessage:m=>this.messages.push(m)};this.connections=0;nodes.push(this)}connect(dest){this.dest=dest;this.connections++}disconnect(){this.connections--}}
 return {engine:new MetronomeEngine({AudioContextClass:Context,NodeClass:Node,moduleURL:'test.js'}),nodes,get context(){return context}};
}
test('stop cancels pending initialization and restart connects only to output',async()=>{
 let resolve;const x=engineFixture(new Promise(r=>resolve=r));const start=x.engine.start({});x.engine.stop();resolve();assert.equal(await start,false);assert.equal(x.nodes.length,0);
 assert.equal(await x.engine.start({bpm:90}),true);assert.equal(x.nodes[0].options.numberOfInputs,0);assert.equal(x.nodes[0].dest,x.context.destination);
 await x.engine.start({bpm:100});assert.equal(x.nodes[0].connections,1);x.engine.stop();assert.equal(x.nodes[0].connections,0);await x.engine.close();assert.equal(x.context.closed,true);
});
