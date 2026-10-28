/* Real Clip Studio UI + playable synthetic video. No private data or cloud calls. */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { chromium } = require('playwright');
const root = path.resolve(__dirname, '../..');
const out = process.env.JAZZ_CROP_SCREENSHOT_DIR || fs.mkdtempSync(path.join(os.tmpdir(), 'vertical-crop-'));
fs.mkdirSync(out, { recursive: true });
const video = path.join(out, 'source.mp4');
execFileSync(process.env.FFMPEG_PATH || 'ffmpeg', ['-v','error','-y','-f','lavfi','-i','testsrc2=s=640x360:r=30:d=12','-c:v','libx264','-threads','1','-movflags','+faststart', video]);
const rotatedVideo=path.join(out,'rotated.mp4');
execFileSync(process.env.FFMPEG_PATH || 'ffmpeg',['-v','error','-y','-display_rotation:v:0','90','-i',video,'-c','copy','-movflags','+faststart',rotatedVideo]);
let html = fs.readFileSync(path.join(root, 'jazz/index.html'), 'utf8').replace(/<script\b[^>]*>[\s\S]*?<\/script>/g, '');
html = html.replace('</body>', ['archive-utils','media-playback','clip-studio-model','clip-studio-crop','clip-studio'].map(name => `<script src="/assets/jazz/${name}.js"></script>`).join('')+'</body>');
const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (url.pathname === '/jazz/') { res.setHeader('Content-Type','text/html'); res.end(html); return; }
  let file = url.pathname === '/source.mp4' ? video : url.pathname==='/rotated.mp4' ? rotatedVideo : path.resolve(root, '.'+url.pathname);
  if (file !== video && file!==rotatedVideo && !file.startsWith(root+path.sep)) { res.writeHead(403).end(); return; }
  if (!fs.existsSync(file) || !fs.statSync(file).isFile()) { res.writeHead(404).end(); return; }
  res.setHeader('Content-Type', file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.mp4') ? 'video/mp4' : 'application/octet-stream');
  res.setHeader('Content-Length',fs.statSync(file).size);
  fs.createReadStream(file).pipe(res);
});

(async () => {
  let browser;
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  try {
    browser = await chromium.launch({headless:true, executablePath:process.env.CHROMIUM_PATH || undefined, args:['--no-sandbox']});
    for (const width of [1440,390]) {
      const context = await browser.newContext({viewport:{width,height:1000},hasTouch:width===390,isMobile:width===390});
      const page = await context.newPage();
      const errors=[], requests=[];
      page.on('pageerror',error=>errors.push(error.message));
      await context.route('**/*', async route => {
        const url=new URL(route.request().url());
        if (url.origin!==base) return route.abort();
        if (!url.pathname.startsWith('/jazz/api/')) return route.continue();
        if (url.pathname.endsWith('/studio/renders')) {
          requests.push(route.request().postDataJSON());
          await new Promise(resolve=>setTimeout(resolve,150));
          return route.fulfill({json:{filename:'practice.mp4',url:base+'/full.mp4',quality:{audioLossless:true,audio:'AAC + ALAC'},outputs:[{label:'Full',filename:'practice.mp4',url:base+'/full.mp4'},{label:'Vertical',filename:'practice-vertical.mp4',url:base+'/vertical.mp4'}]}});
        }
        if (url.pathname.includes('/playback-url')) return route.fulfill({json:{url:base+(url.pathname.includes('take-two')?'/rotated.mp4':'/source.mp4'),expiresAt:new Date(Date.now()+3600000).toISOString()}});
        if (url.pathname.endsWith('/captions')) return route.fulfill({json:{status:'ready',segments:[]}});
        if (url.pathname.includes('/studio/days/')) return route.fulfill({json:{recordings:['take-one','take-two'].map((id,index)=>({id,mediaKind:'video',durationMs:12000,practiceBlockTitle:'Practice',takeNumber:index+1,videoPlaybackOptimized:true})),candidates:[],analysis:{needsScan:false}}});
        return route.fulfill({json:{}});
      });
      await context.addInitScript(() => {
        const date=new Date(), key=`${date.getFullYear()}-${String(date.getMonth()+1).padStart(2,'0')}-${String(date.getDate()).padStart(2,'0')}`;
        if (!localStorage.getItem('jazz.clip-studio.output.v1:'+key)) localStorage.setItem('jazz.clip-studio.output.v1:'+key,JSON.stringify({version:1,title:'Practice',clips:['one','two','other'].map((id,index)=>({id,recordingId:index===2?'take-two':'take-one',startMs:index*1000,endMs:index*1000+2000,title:'Practice',takeNumber:index===2?2:1}))}));
      });
      await page.goto(base+'/jazz/');
      await page.evaluate(()=>{
        document.querySelectorAll('main>section').forEach(s=>s.hidden=s.id!=='studio');
        document.body.className='virtuoso-studio';
        document.dispatchEvent(new CustomEvent('jazz:view-change',{detail:{view:'studio'}}));
      });
      const readProject=()=>page.evaluate(()=>JSON.parse(localStorage.getItem([...Object.keys(localStorage)].find(k=>k.startsWith('jazz.clip-studio.output.v1:')))));
      await page.waitForFunction(()=>document.querySelectorAll('[data-output-open]').length===3);
      await page.click('#studio-vertical');
      await page.waitForSelector('.studio-crop-frame',{state:'visible'});
      await page.locator('.studio-crop-frame').scrollIntoViewIfNeeded();
      let box=await page.locator('.studio-crop-frame').boundingBox();
      assert.ok(Math.abs(box.width/box.height-9/16)<.01,'box is 9:16');
      const x=box.x+box.width/2,y=box.y+box.height*.35;
      if(width===390){
        const cdp=await context.newCDPSession(page);
        await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x,y}]});
        await cdp.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{x:x+25,y}]});
        await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});
      }else{
        await page.mouse.move(x,y);await page.mouse.down();await page.mouse.move(x+35,y,{steps:5});await page.mouse.up();
      }
      assert.ok((await readProject()).clips[0].verticalCrop.centerX>.5,'drag saved');
      const edge=await page.locator('.studio-crop-handle.e').boundingBox();
      await page.mouse.move(edge.x+edge.width/2,edge.y+edge.height/2);await page.mouse.down();await page.mouse.move(edge.x+edge.width/2-10,edge.y+edge.height/2);await page.mouse.up();
      assert.ok((await readProject()).clips[0].verticalCrop.scale<1,'edge drag resizes');
      await page.locator('.studio-crop-frame').focus();
      await page.keyboard.press('-');
      const beforeCancel=(await readProject()).clips[0].verticalCrop;
      box=await page.locator('.studio-crop-frame').boundingBox();
      await page.mouse.move(box.x+box.width/2,box.y+box.height*.3);await page.mouse.down();await page.mouse.move(box.x+box.width/2-15,box.y+box.height*.3);
      await page.locator('.studio-crop-frame').dispatchEvent('pointercancel',{pointerId:1});await page.mouse.up();
      assert.deepEqual((await readProject()).clips[0].verticalCrop,beforeCancel,'pointer cancel restores');
      await page.click('#studio-crop-apply');
      let project=await readProject();assert.deepEqual(project.clips[0].verticalCrop,project.clips[1].verticalCrop);assert.equal(project.clips[2].verticalCrop,undefined);
      await page.click('[data-output-open="other"]');
      await page.waitForFunction(()=>{const video=document.querySelector('#studio-media video');return video?.dataset.recordingId==='take-two'&&video.videoWidth===360&&video.videoHeight===640;});
      await page.waitForSelector('.studio-crop-frame',{state:'visible'});
      box=await page.locator('.studio-crop-frame').boundingBox();assert.ok(Math.abs(box.width/box.height-9/16)<.01,'rotated video has upright 9:16 frame');
      await page.reload();
      await page.evaluate(()=>{document.querySelectorAll('main>section').forEach(s=>s.hidden=s.id!=='studio');document.body.className='virtuoso-studio';document.dispatchEvent(new CustomEvent('jazz:view-change',{detail:{view:'studio'}}));});
      await page.click('[data-output-open="one"]');
      await page.waitForSelector('.studio-crop-frame',{state:'visible'});
      assert.deepEqual((await readProject()).clips[0].verticalCrop,beforeCancel,'reload preserves framing');
      await page.setViewportSize({width:width===1440?1200:400,height:1000});
      box=await page.locator('.studio-crop-frame').boundingBox();assert.ok(Math.abs(box.width/box.height-9/16)<.01,'resize preserves geometry');
      await page.click('#studio-render');
      // Edits made during rendering must not change the submitted crop snapshot.
      await page.click('#studio-crop-reset');
      await page.waitForFunction(()=>document.querySelectorAll('#studio-render-outputs a').length===2);
      assert.equal(requests.length,1);assert.equal(requests[0].includeVertical,true);assert.deepEqual(requests[0].clips[0].verticalCrop,beforeCancel);
      assert.equal(await page.locator('#studio-render-outputs a').first().textContent(),'↓ Full · practice.mp4');
      await page.locator('.studio-crop-frame').scrollIntoViewIfNeeded();
      await page.screenshot({path:path.join(out,`crop-${width}.png`),fullPage:true});
      assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,'no horizontal overflow');
      assert.deepEqual(errors,[]);
      await context.close();
    }
    console.log('Vertical crop browser checks passed; screenshots: '+out);
  }finally{await browser?.close();await new Promise(resolve=>server.close(resolve));}
})().catch(error=>{console.error(error);process.exitCode=1;});
