/* Idea space: the Commonplace as a Pensieve basin. Raw WebGL2 (fluid, thread
 * currents, instanced glass bubbles) with a flat Canvas2D fallback. Every
 * bubble comes from the API; the layout is computed by space-model.js.
 * Opening a Moment dives into its bubble and hands off to the Moment view. */
(() => {
  "use strict";
  const SM = globalThis.CommonplaceSpaceModel;
  const M = globalThis.CommonplaceModel;
  const TAU = Math.PI * 2;
  const clamp = (v, a, b) => (v < a ? a : v > b ? b : v), lerp = (a, b, t) => a + (b - a) * t;
  const smooth = (a, b, x) => { const t = clamp((x - a) / (b - a), 0, 1); return t * t * (3 - 2 * t); };
  const easeIO = (t) => (t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2);
  const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
  const KIND_TINT = { conversation: [0.98, 0.86, 0.62], dream: [0.62, 0.52, 1.0], idea: [0.66, 0.92, 1.0], quote: [1.0, 0.76, 0.82] };
  const KIND_SHORT = { conversation: "Talk", dream: "Dream", idea: "Idea", quote: "Quote" };
  const WARM = [1.0, 0.7, 0.36];
  const MAXREG = 8;
  const ZMIN = 0.08, ZMAX = 7, LEVELS = [0.42, 1.1, 4.2];
  const reduceMQ = matchMedia("(prefers-reduced-motion: reduce)");
  let reduced = reduceMQ.matches;
  reduceMQ.addEventListener?.("change", (e) => (reduced = e.matches));

  let root = null, api = null, built = false, running = false, raf = 0;
  let el = {};
  let gl = null, ctx2 = null, P = {}, fbo = null, fboTex = null, fboW = 0, fboH = 0;
  let vaoBub = null, vaoRib = null, vaoFull = null, quadBuf = null, instA = null, instDyn = null, ribBuf = null, ribRanges = [];
  let items = [], regions = [], threads = [], bounds = null, people = [], data = null;
  let anim = [], screen = [], order = [], STATIC = new Float32Array(0), DYN = new Float32Array(0);
  let byId = new Map();
  let theme = {};
  let regionEls = [], ideaEls = [], threadEls = [], glimpses = [], edgeEls = [];
  const S = {
    view: { w: innerWidth, h: innerHeight }, cam: { x: 0, y: 0, z: 0.5 }, fly: null, vel: { x: 0, y: 0 },
    query: "", lens: "", follow: null, surface: 2026, surfaceT: 2026, depthOn: 0, depthOnT: 0, yearMin: 2020, yearMax: 2026.85,
    hover: -1, focus: -1, touchSel: -1, dive: null, divePhase: 0, diveW: [0, 0], diveId: -1, t0: performance.now(), time: 0, cardFor: -1,
  };
  const $ = (s) => root.querySelector(s);

  // ---- DOM --------------------------------------------------------------------------
  function build() {
    built = true;
    root.innerHTML = `
      <div class="sp-stage" tabindex="0" role="application" aria-roledescription="memory map" aria-label="Idea space. Your Moments as bubbles in a basin, grouped by who and what." aria-describedby="sp-kbd">
        <canvas class="sp-gl" aria-hidden="true"></canvas><div class="sp-labels"></div>
      </div>
      <header class="top">
        <div class="brand"><div class="brand-row"><a class="book" href="#">Commonplace</a><span class="slash">/</span><h1>Idea space</h1></div><p class="counts" id="sp-counts"></p></div>
        <div class="top-actions">
          <label class="search glass"><svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><circle cx="7" cy="7" r="5" fill="none" stroke="currentColor" stroke-width="1.5"/><path d="M11 11l3.5 3.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
            <input id="sp-q" type="search" placeholder="Search memories, people, years" autocomplete="off" aria-label="Search memories" /><kbd>/</kbd></label>
          <button id="sp-listBtn" class="pill-btn glass" type="button" aria-expanded="false"><svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><path d="M2 4h12M2 8h12M2 12h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>List</button>
        </div>
      </header>
      <div class="status glass" id="sp-status" hidden><span class="s-text" id="sp-status-text"></span><span id="sp-status-ctl" style="display:flex;gap:6px;align-items:center"></span></div>
      <nav class="gauge" aria-label="Zoom level"><div class="gauge-track"></div><div class="gauge-mark" id="sp-gmark"></div>
        <button type="button" data-level="2">Moments</button><button type="button" data-level="1">Ideas</button><button type="button" data-level="0">Regions</button></nav>
      <div class="dock glass" role="toolbar" aria-label="Lenses and time">
        <div class="lens" role="group" aria-label="People lens" id="sp-lens"></div>
        <div class="time">
          <label class="time-label" for="sp-surface">Surface</label>
          <div class="time-track"><canvas class="spark" id="sp-spark" aria-hidden="true"></canvas>
            <input id="sp-surface" class="surface" type="range" step="0.05" aria-describedby="sp-time-read" /><div class="years" id="sp-years" aria-hidden="true"></div></div>
          <div style="display:flex;align-items:center;gap:8px"><span class="time-read" id="sp-time-read" aria-live="polite"></span>
            <button class="chip" type="button" id="sp-level" aria-pressed="true" title="Every year at the same depth">Level</button></div>
        </div>
        <div class="zoom">
          <button class="icon-btn" type="button" id="sp-zout" aria-label="Zoom out"><svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true"><path d="M3 7h8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></button>
          <button class="icon-btn" type="button" id="sp-zin" aria-label="Zoom in"><svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true"><path d="M3 7h8M7 3v8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></button>
          <button class="icon-btn" type="button" id="sp-zhome" aria-label="Show the whole space"><svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true"><circle cx="7" cy="7" r="5" fill="none" stroke="currentColor" stroke-width="1.4"/><circle cx="7" cy="7" r="1.6" fill="currentColor"/></svg></button>
        </div>
      </div>
      <div class="card glass" id="sp-card" hidden></div>
      <aside class="list glass" id="sp-list" hidden aria-labelledby="sp-list-h"><div class="l-head"><h2 id="sp-list-h">Every memory</h2>
        <button class="icon-btn" type="button" id="sp-listClose" aria-label="Close list"><svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true"><path d="M2 2l8 8M10 2l-8 8" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></button></div>
        <div class="l-body" id="sp-list-body"></div></aside>
      <div class="nogl glass" id="sp-nogl" hidden>WebGL is unavailable here, so the space is drawn flat. Everything else works.</div>
      <div class="empty glass" id="sp-empty" hidden></div>
      <div id="sp-live" class="sr-only" aria-live="polite"></div>
      <p id="sp-kbd" class="sr-only">Arrow keys move between neighbouring memories. Shift with arrows pans. Plus and minus zoom. Zero shows everything. Enter opens the focused memory. Escape steps back. Slash searches. L opens the list.</p>`;
    el.stage = $(".sp-stage");
    el.canvas = $(".sp-gl");
    el.labels = $(".sp-labels");
    el.card = $("#sp-card");
    bindStatic();
    const forceFlat = /[?&]flat=1/.test(location.hash);
    if (!forceFlat) {
      try { gl = el.canvas.getContext("webgl2", { antialias: false, alpha: false, premultipliedAlpha: true, powerPreference: "high-performance" }); } catch { gl = null; }
    }
    if (gl) {
      try { initPrograms(); } catch (err) { console.error(err); gl = null; }
    }
    if (!gl) {
      ctx2 = el.canvas.getContext("2d");
      $("#sp-nogl").hidden = false;
      setTimeout(() => { $("#sp-nogl").hidden = true; }, 7000);
    }
    matchMedia("(prefers-color-scheme: dark)").addEventListener?.("change", readTheme);
    new MutationObserver(readTheme).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    addEventListener("resize", () => { if (running) resize(); });
  }

  // ---- Data --------------------------------------------------------------------------
  async function load(focus) {
    let d;
    try {
      d = await api("/space");
    } catch (err) {
      showEmpty(esc(err.message));
      return;
    }
    if (!running) return;
    setData(d, focus);
  }
  function showEmpty(html) {
    const e = $("#sp-empty");
    e.innerHTML = html;
    e.hidden = false;
  }

  function setData(d, focus) {
    data = d;
    people = d.people || [];
    const lay = SM.layout(d);
    items = lay.items;
    regions = lay.regions;
    threads = lay.threads;
    bounds = lay.bounds;
    byId = new Map(items.map((it, i) => [it.id, i]));
    items.forEach((it, i) => { it.idx = i; });
    anim = items.map(() => ({ lit: 0, dim: 0, sel: 0, hov: 0, tl: 0, td: 0 }));
    screen = items.map(() => ({ x: 0, y: 0, R: 0, a: 1, vis: false }));
    STATIC = new Float32Array(Math.max(1, items.length) * 12);
    DYN = new Float32Array(Math.max(1, items.length) * 4);
    const moments = items.filter((i) => i.type === "moment");
    $("#sp-empty").hidden = moments.length > 0;
    if (!moments.length) showEmpty('Nothing here yet. Keep a Moment and it will surface here.<br /><a href="#">Back to the Commonplace</a>');
    const years = moments.map((m) => m.year).filter((y) => y > 1900);
    S.yearMin = years.length ? Math.floor(Math.min(...years)) : new Date().getFullYear();
    S.yearMax = Math.max(S.yearMin + 1, (years.length ? Math.floor(Math.max(...years)) : S.yearMin) + 0.85);
    S.surface = S.surfaceT = S.yearMax;
    S.depthOn = S.depthOnT = 0; // Level by default: every year at the same depth.
    const surf = $("#sp-surface");
    surf.min = String(S.yearMin);
    surf.max = String(S.yearMax);
    surf.value = String(S.yearMax);
    $("#sp-level").setAttribute("aria-pressed", "true");
    const ys = $("#sp-years");
    ys.innerHTML = "";
    const span = S.yearMax - S.yearMin;
    const stepY = Math.max(1, Math.ceil(span / 6));
    for (let y = S.yearMin; y <= Math.floor(S.yearMax); y += stepY) ys.insertAdjacentHTML("beforeend", `<span style="left:${((y - S.yearMin) / span) * 100}%">${y}</span>`);
    const nIdeas = items.length - moments.length;
    $("#sp-counts").textContent = `${moments.length} ${moments.length === 1 ? "Moment" : "Moments"} · ${nIdeas} ${nIdeas === 1 ? "idea" : "ideas"} · ${threads.length} ${threads.length === 1 ? "thread" : "threads"}`;
    buildLens();
    buildLabels();
    buildList();
    if (gl) uploadGeometry();
    readTheme();
    resize();
    S.query = "";
    $("#sp-q").value = "";
    S.lens = "";
    S.follow = null;
    recompute();
    // Camera: a focused Moment rises out of its bubble; otherwise home.
    const phone = S.view.w < 760;
    const home = SM.homeCamera(lay, S.view, phone);
    const fi = focus ? byId.get(focus) : undefined;
    if (fi != null) {
      const it = items[fi];
      Object.assign(S.cam, { x: it.x, y: it.y, z: reduced ? 2.6 : clamp((Math.hypot(S.view.w, S.view.h) * 0.62) / it.r, ZMIN, 40) });
      S.divePhase = reduced ? 0 : 0.8;
      S.diveW = [it.x, it.y];
      S.dive = { i: fi, dir: -1, t0: performance.now(), dur: reduced ? 200 : 1300, from: { ...S.cam }, saved: { x: it.x, y: it.y, z: 2.6 }, rise: true };
      setFocus(fi, false);
    } else {
      Object.assign(S.cam, { x: home.x, y: home.y, z: home.z });
      if (home.focus) {
        const hi = byId.get(home.focus);
        setFocus(hi, false);
        setTimeout(() => { if (running && S.focus === hi) showCard(hi, true); }, 300);
      }
    }
    updateStatus();
  }

  // ---- Theme -------------------------------------------------------------------------
  function hexToRgb(h) {
    h = h.trim();
    if (h.startsWith("rgb")) { const v = h.match(/[\d.]+/g).map(Number); return [v[0] / 255, v[1] / 255, v[2] / 255]; }
    h = h.replace("#", "");
    if (h.length === 3) h = h.split("").map((c) => c + c).join("");
    const n = parseInt(h, 16);
    return [((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255];
  }
  function readTheme() {
    if (!root || !built) return;
    const cs = getComputedStyle(root);
    const g = (k) => hexToRgb(cs.getPropertyValue(k) || "#000");
    const bg = g("--bg");
    const light = (bg[0] + bg[1] + bg[2]) / 3 > 0.5;
    theme = {
      light, deep: g("--fluid-deep"), mid: g("--fluid-mid"), silver: g("--fluid-silver"), accent: g("--accent-glow"), ink: g("--ink"),
      veil: light ? [0.93, 0.92, 0.96] : [0.05, 0.04, 0.1],
    };
    theme.kind = {};
    for (const k in KIND_TINT) theme.kind[k] = light ? KIND_TINT[k].map((c) => c * 0.42) : KIND_TINT[k];
    theme.warm = light ? WARM.map((c) => c * 0.62) : WARM;
    if (gl) uploadStatic();
    else computeOrder();
    drawSpark();
  }
  function tintOf(it) {
    if (it.special) return theme.warm;
    if (it.type === "idea") return theme.light ? [0.3, 0.26, 0.5] : [0.92, 0.9, 1];
    return theme.kind[it.kind] || theme.kind.conversation;
  }

  // ---- Size model (mirrors the vertex shader) ----------------------------------------
  function depthOf(it) {
    return it.type === "idea" ? 0 : S.depthOn * clamp(Math.abs(it.year - S.surface) / 4, 0, 1) * (1 - 0.6 * smooth(2.2, 4.5, S.cam.z));
  }
  function screenR(it, i) {
    const a = anim[i];
    let R = it.r * S.cam.z * (it.type === "moment" ? 1 + 0.6 * smooth(1.4, 4.5, S.cam.z) : 1) * lerp(1, 0.55, depthOf(it));
    R *= 1 + 0.12 * a.lit + 0.1 * a.hov;
    const sf = it.type === "idea" ? 0 : S.depthOn * (1 - smooth(0.15, 0.7, Math.abs(it.year - S.surface)));
    return Math.max(R, it.type === "idea" ? 3 : 1.6 + a.lit * 2.6 + sf * 2.2);
  }
  function itemAlpha(it, i) {
    let al = it.type === "idea" ? lerp(1, 0.55, smooth(2.2, 4, S.cam.z)) : lerp(0.8, 1, smooth(0.3, 0.8, S.cam.z)) * lerp(1, 0.4, depthOf(it));
    if (S.divePhase > 0 && i !== S.diveId) al *= 1 - S.divePhase;
    return al;
  }
  const toScreen = (x, y) => [(x - S.cam.x) * S.cam.z + S.view.w / 2, (y - S.cam.y) * S.cam.z + S.view.h / 2];
  const toWorld = (sx, sy) => [(sx - S.view.w / 2) / S.cam.z + S.cam.x, (sy - S.view.h / 2) / S.cam.z + S.cam.y];

  // ---- WebGL2 ------------------------------------------------------------------------
  const VS_FULL = `#version 300 es
const vec2 P[3]=vec2[3](vec2(-1,-1),vec2(3,-1),vec2(-1,3));
void main(){gl_Position=vec4(P[gl_VertexID],0,1);}`;
  const NOISE = `
float h(vec2 p){vec3 p3=fract(vec3(p.xyx)*.1031);p3+=dot(p3,p3.yzx+33.33);return fract((p3.x+p3.y)*p3.z);}
float n(vec2 p){vec2 i=floor(p),f=fract(p);vec2 u=f*f*(3.-2.*f);return mix(mix(h(i),h(i+vec2(1,0)),u.x),mix(h(i+vec2(0,1)),h(i+vec2(1,1)),u.x),u.y);}
const mat2 M=mat2(1.6,1.2,-1.2,1.6);
float fbm(vec2 p){float v=0.,a=.5;for(int i=0;i<5;i++){v+=a*n(p);p=M*p;a*=.5;}return v;}
float fbm3(vec2 p){float v=0.,a=.5;for(int i=0;i<3;i++){v+=a*n(p);p=M*p;a*=.5;}return v;}
mat2 rot(float a){float c=cos(a),s=sin(a);return mat2(c,-s,s,c);}`;
  const FS_FLUID = `#version 300 es
precision highp float;
uniform vec2 uRes,uView,uCam,uDiveW;uniform float uZoom,uTime,uDive,uLight;uniform int uNReg;
uniform vec2 uReg[${MAXREG}];uniform vec3 uRegCol[${MAXREG}];uniform vec3 uDeep,uMid,uSilver;
out vec4 o;${NOISE}
void main(){
  vec2 uv=gl_FragCoord.xy/uRes;
  vec2 sp=(uv-.5)*uView; sp.y=-sp.y;
  vec2 w=uCam+sp/uZoom;
  vec2 dv=(w-uDiveW)*uZoom; float dd=length(dv)/length(uView);
  w=uDiveW+rot(uDive*4.5*exp(-dd*2.6))*(w-uDiveW);
  float t=uTime; vec2 p=w/560.;
  vec2 q=vec2(fbm(p+vec2(0.,t*.03)),fbm(p+vec2(5.2,1.3)-t*.026));
  vec2 r=vec2(fbm(p+3.4*q+vec2(1.7,9.2)+t*.045),fbm(p+3.4*q+vec2(8.3,2.8)-t*.038));
  float f=fbm(p+2.6*r);
  float L=log2(max(uZoom,.01))+1.5; float b=floor(L); float fr=L-b;
  vec2 pd=w/260.;
  float d1=fbm3(pd*exp2(b)+r*2.5+t*.015);
  float d2=fbm3(pd*exp2(b+1.)+r*2.5-t*.015);
  float det=mix(d1,d2,smoothstep(0.,1.,fr));
  float band=abs(sin((f*4.6+r.x*2.2)*3.14159));
  float fil=pow(1.-band,7.);
  float fil2=pow(1.-abs(sin(det*12.+f*7.)),16.)*smoothstep(.35,1.3,uZoom);
  vec3 col=mix(uDeep,uMid,smoothstep(.22,.9,f));
  for(int i=0;i<${MAXREG};i++){ if(i>=uNReg) break; vec2 d=(w-uReg[i])/430.;float k=exp(-dot(d,d));col=mix(col,uRegCol[i],k*(.5-.28*uLight)*smoothstep(.1,.8,f+.2));}
  float sheen=fil*.34*(.35+.9*length(q))+fil2*(.04+.04*uLight);
  col=mix(col,uSilver,clamp(sheen,0.,1.));
  vec2 vc=uv-.5; float vig=smoothstep(.2,.9,dot(vc,vc)*2.4);
  col*=1.-vig*mix(.5,.07,uLight);
  col=mix(col,uSilver,uDive*uDive*.12);
  o=vec4(col,1);
}`;
  const VS_RIB = `#version 300 es
layout(location=0) in vec2 aPos;layout(location=1) in vec2 aNrm;layout(location=2) in float aS;layout(location=3) in float aSide;
uniform vec2 uCam,uView;uniform float uZoom,uWidth;
out float vS;out float vSide;
void main(){vec2 w=aPos+aNrm*aSide*(uWidth/uZoom);vec2 s=(w-uCam)*uZoom;gl_Position=vec4(s.x/(uView.x*.5),-s.y/(uView.y*.5),0,1);vS=aS;vSide=aSide;}`;
  const FS_RIB = `#version 300 es
precision highp float;
in float vS;in float vSide;uniform float uTime,uAlpha,uLight;uniform vec3 uCol;out vec4 o;
void main(){
  float across=exp(-vSide*vSide*3.2);float core=exp(-vSide*vSide*26.)*.32;
  float flow=.5+.5*sin(vS*.028-uTime*1.1);float pulse=pow(flow,5.);
  float a=(across*.42+core)*(.4+.55*pulse)*uAlpha;
  o=vec4(uCol*a,a*uLight*.85);
}`;
  const FS_BLIT = `#version 300 es
precision highp float;
uniform sampler2D uTex;uniform vec2 uDev;uniform float uTime;out vec4 o;
float h(vec2 p){vec3 p3=fract(vec3(p.xyx)*.1031);p3+=dot(p3,p3.yzx+33.33);return fract((p3.x+p3.y)*p3.z);}
void main(){vec2 uv=gl_FragCoord.xy/uDev;vec3 c=texture(uTex,uv).rgb;c+=(h(gl_FragCoord.xy+fract(uTime)*100.)-.5)*.022;o=vec4(c,1);}`;
  // aA: x,y,r,year  aB: tint rgb, seed  aC: type, lit, dim, sel  aD: hover, diving, warm, dream
  const VS_BUB = `#version 300 es
layout(location=0) in vec2 aQ;layout(location=1) in vec4 aA;layout(location=2) in vec4 aB;layout(location=3) in vec4 aC;layout(location=4) in vec4 aD;
uniform vec2 uCam,uView;uniform float uZoom,uTime,uSurface,uDepthOn,uMotion,uDive;
out vec2 vUv;out vec3 vTint;out float vSeed,vType,vLit,vDim,vSel,vHov,vDepth,vAlpha,vR,vGl,vSurf,vWarm,vDream;
void main(){
  float type=aC.x;
  float depth=type<.5?0.:uDepthOn*clamp(abs(aA.w-uSurface)/4.,0.,1.)*(1.-.6*smoothstep(2.2,4.5,uZoom));
  float surf=type<.5?0.:uDepthOn*(1.-smoothstep(.15,.7,abs(aA.w-uSurface)));
  vec2 c=aA.xy+uMotion*vec2(sin(uTime*.37+aB.w*41.),cos(uTime*.29+aB.w*23.))*aA.z*.09;
  vec2 s=(c-uCam)*uZoom;
  float R=aA.z*uZoom*(type>.5?1.+.6*smoothstep(1.4,4.5,uZoom):1.)*mix(1.,.55,depth);
  R*=1.+.12*aC.y+.1*aD.x;
  R=max(R,type<.5?3.:1.6+aC.y*2.6+surf*2.2);
  float al=type<.5?mix(1.,.55,smoothstep(2.2,4.,uZoom)):mix(.8,1.,smoothstep(.3,.8,uZoom));
  al*=1.-uDive*(1.-aD.y);
  if(type>.5) al*=mix(1.,.4,depth);
  float E=1.5; vec2 p=s+aQ*R*E;
  gl_Position=vec4(p.x/(uView.x*.5),-p.y/(uView.y*.5),0,1);
  vUv=vec2(aQ.x,-aQ.y)*E;vTint=aB.rgb;vSeed=aB.w;vType=type;vLit=aC.y;vDim=aC.z;vSel=aC.w;vHov=aD.x;vDepth=depth;vAlpha=al;vR=R;vSurf=surf;vWarm=aD.z;vDream=aD.w;
  vGl=step(.5,type)*smoothstep(26.,64.,R)*(1.-aD.y*uDive);
}`;
  const FS_BUB = `#version 300 es
precision highp float;
in vec2 vUv;in vec3 vTint;in float vSeed,vType,vLit,vDim,vSel,vHov,vDepth,vAlpha,vR,vGl,vSurf,vWarm,vDream;
uniform sampler2D uScene;uniform vec2 uDev,uView;uniform float uTime,uLight,uDive;uniform vec3 uAccent,uSilver,uInk,uVeil,uWarmCol;
out vec4 o;
void main(){
  float d=length(vUv); if(d>1.5)discard;
  float aa=1.4/max(vR,1.);
  float body=1.-smoothstep(1.-aa,1.+aa*.4,d);
  float glow=clamp(vLit*.8+vSel+vHov*.55+vSurf*.4+vWarm*.35,0.,1.4);
  vec3 gcol=mix(mix(uSilver,uWarmCol,vWarm),uAccent,clamp(vLit+vSel,0.,1.));
  if(d>1.){float halo=exp(-(d-1.)*6.5)*smoothstep(1.5,1.15,d)*glow*vAlpha*(1.-vDim);float ha=halo*.85;o=vec4(gcol*ha,ha*uLight*.7);return;}
  vec2 suv=gl_FragCoord.xy/uDev;
  float z=sqrt(max(0.,1.-d*d));
  float Rm=min(vR,min(uView.x,uView.y)*.55);
  vec2 off=-vUv*Rm*(.22+.8*(1.-z))/uView;
  vec3 refr=vec3(texture(uScene,suv+off*1.07).r,texture(uScene,suv+off).g,texture(uScene,suv+off*.93).b);
  vec3 plain=texture(uScene,suv).rgb;
  float a=atan(vUv.y,vUv.x); float sw=uTime*.42+vSeed*6.2831;
  vec3 col=refr; col=mix(col,vTint,.07+.05*z);
  // Dreams: a night inside the glass, with a few slow stars.
  col=mix(col,vec3(.05,.04,.14),vDream*.55*(1.-uLight*.3));
  float star=pow(max(0.,sin(vUv.x*37.+vSeed*20.)*sin(vUv.y*41.-vSeed*13.)),40.)*vDream*smoothstep(.95,.3,d);
  col+=vec3(.8,.78,1.)*star*.8;
  if(vType>.5){
    float w1=sin(a*2.+d*6.5-sw+1.7*sin(d*4.2+sw*.6));
    float w2=sin(a*3.-d*9.+sw*1.25+vSeed*4.);
    float wisp=pow(max(0.,1.-abs(w1)),9.)*.95+pow(max(0.,1.-abs(w2)),15.)*.55;
    wisp*=smoothstep(1.,.25,d)*smoothstep(0.,.3,d+.05)*smoothstep(14.,42.,vR);
    col=mix(col,mix(vTint,uSilver,.3*(1.-uLight)),clamp(wisp*(.55+uDive*.3),0.,1.));
    float pearl=(1.-smoothstep(10.,40.,vR))*exp(-d*d*2.5);
    col=mix(col,vTint,pearl*.42);
  }else{
    float core=exp(-d*d*10.);
    float rings=pow(.5+.5*sin(d*20.-uTime*.7+vSeed*6.),8.)*.16*smoothstep(1.,.2,d);
    col=mix(col,mix(vTint,uSilver,.5*(1.-uLight)),clamp(core*.75+rings,0.,1.));
  }
  float fres=pow(1.-z,2.1);
  float film=vSeed*3.+vUv.x*.7+vUv.y*1.1+uTime*.035;
  vec3 irid=.5+.5*cos(6.2831*(film+vec3(0.,.33,.67)));
  vec3 rimCol=mix(mix(uSilver,uInk,uLight*.55),uWarmCol,vWarm*.8);
  rimCol=mix(rimCol,vec3(.7,.62,1.),vDream*.6);
  col=mix(col,irid*mix(1.,.55,uLight)*.8+rimCol*.35,fres*.42);
  col*=1.-.14*smoothstep(.1,1.,-vUv.y*.8+d*.25)*(1.-uLight*.6);
  col=mix(col,plain,vDim*.84);
  col=mix(col,plain,vDepth*.5);
  col=mix(col,uVeil,vGl*.5*smoothstep(1.,.15,d)*(1.-vDim)*(1.-vDream*.7));
  vec2 h1=mat2(.8,-.6,.6,.8)*(vUv-vec2(-.34,.44));
  float s1=exp(-(h1.x*h1.x*13.+h1.y*h1.y*46.));
  float s2=exp(-dot(vUv-vec2(.44,-.5),vUv-vec2(.44,-.5))*55.);
  float spec=(s1*.75+s2*.28)*(1.-vDim*.7)*(1.-vDepth*.5);
  col+=vec3(1.)*spec*mix(1.,.8,uLight);
  float rim=smoothstep(1.-aa*3.2,1.-aa*.8,d);
  col=mix(col,rimCol,rim*(.42-.2*vDim));
  col=mix(col,gcol,rim*glow*.75);
  float A=body*vAlpha; o=vec4(col*A,A);
}`;
  function compile(vs, fs) {
    const sh = (t, src) => { const s = gl.createShader(t); gl.shaderSource(s, src); gl.compileShader(s); if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s)); return s; };
    const p = gl.createProgram();
    gl.attachShader(p, sh(gl.VERTEX_SHADER, vs));
    gl.attachShader(p, sh(gl.FRAGMENT_SHADER, fs));
    gl.linkProgram(p);
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(p));
    const u = {};
    const n = gl.getProgramParameter(p, gl.ACTIVE_UNIFORMS);
    for (let i = 0; i < n; i++) { const info = gl.getActiveUniform(p, i); u[info.name.replace("[0]", "")] = gl.getUniformLocation(p, info.name); }
    return { p, u };
  }
  function initPrograms() {
    P.fluid = compile(VS_FULL, FS_FLUID);
    P.rib = compile(VS_RIB, FS_RIB);
    P.blit = compile(VS_FULL, FS_BLIT);
    P.bub = compile(VS_BUB, FS_BUB);
    vaoFull = gl.createVertexArray();
    quadBuf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, quadBuf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
    fboTex = gl.createTexture();
    fbo = gl.createFramebuffer();
  }
  function uploadGeometry() {
    if (vaoBub) gl.deleteVertexArray(vaoBub);
    if (instA) gl.deleteBuffer(instA);
    if (instDyn) gl.deleteBuffer(instDyn);
    vaoBub = gl.createVertexArray();
    gl.bindVertexArray(vaoBub);
    gl.bindBuffer(gl.ARRAY_BUFFER, quadBuf);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 0, 0);
    instA = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, instA);
    gl.bufferData(gl.ARRAY_BUFFER, STATIC.byteLength, gl.DYNAMIC_DRAW);
    for (let k = 0; k < 3; k++) { const loc = 1 + k; gl.enableVertexAttribArray(loc); gl.vertexAttribPointer(loc, 4, gl.FLOAT, false, 48, k * 16); gl.vertexAttribDivisor(loc, 1); }
    instDyn = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, instDyn);
    gl.bufferData(gl.ARRAY_BUFFER, DYN.byteLength, gl.DYNAMIC_DRAW);
    gl.enableVertexAttribArray(4);
    gl.vertexAttribPointer(4, 4, gl.FLOAT, false, 16, 0);
    gl.vertexAttribDivisor(4, 1);
    const verts = [];
    ribRanges = [];
    threads.forEach((T) => {
      const start = verts.length / 6, pts = T.pts;
      for (let i = 0; i < pts.length; i++) {
        const a = pts[Math.max(0, i - 1)], b = pts[Math.min(pts.length - 1, i + 1)];
        const tx = b[0] - a[0], ty = b[1] - a[1], L = Math.hypot(tx, ty) || 1, nx = -ty / L, ny = tx / L;
        verts.push(pts[i][0], pts[i][1], nx, ny, T.s[i], -1, pts[i][0], pts[i][1], nx, ny, T.s[i], 1);
      }
      ribRanges.push([start, verts.length / 6 - start]);
    });
    if (vaoRib) gl.deleteVertexArray(vaoRib);
    if (ribBuf) gl.deleteBuffer(ribBuf);
    vaoRib = gl.createVertexArray();
    gl.bindVertexArray(vaoRib);
    ribBuf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, ribBuf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(verts.length ? verts : [0, 0, 0, 0, 0, 0]), gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0); gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 24, 0);
    gl.enableVertexAttribArray(1); gl.vertexAttribPointer(1, 2, gl.FLOAT, false, 24, 8);
    gl.enableVertexAttribArray(2); gl.vertexAttribPointer(2, 1, gl.FLOAT, false, 24, 16);
    gl.enableVertexAttribArray(3); gl.vertexAttribPointer(3, 1, gl.FLOAT, false, 24, 20);
    gl.bindVertexArray(null);
  }
  let lastOrderSurface = -99, lastOrderDepth = -1;
  function computeOrder() {
    order = items.map((_, i) => i).sort((a, b) => {
      const A = items[a], B = items[b];
      if (A.type !== B.type) return A.type === "idea" ? 1 : -1;
      return depthOf(B) - depthOf(A);
    });
    lastOrderSurface = S.surface;
    lastOrderDepth = S.depthOn;
  }
  function uploadStatic() {
    if (!items.length) return;
    computeOrder();
    order.forEach((i, p) => {
      const it = items[i], tint = tintOf(it);
      STATIC.set([it.x, it.y, it.r, it.year, tint[0], tint[1], tint[2], (i * 0.6180339) % 1, it.type === "idea" ? 0 : 1, 0, 0, 0], p * 12);
    });
    gl.bindBuffer(gl.ARRAY_BUFFER, instA);
    gl.bufferSubData(gl.ARRAY_BUFFER, 0, STATIC);
  }
  function sizeFBO() {
    const sc = S.view.w < 760 ? 0.42 : 0.5;
    const w = Math.max(2, Math.round(el.canvas.width * sc)), h = Math.max(2, Math.round(el.canvas.height * sc));
    if (w === fboW && h === fboH) return;
    fboW = w; fboH = h;
    gl.bindTexture(gl.TEXTURE_2D, fboTex);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, w, h, 0, gl.RGBA, gl.UNSIGNED_BYTE, null);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    gl.bindFramebuffer(gl.FRAMEBUFFER, fbo);
    gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, fboTex, 0);
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
  }
  function threadState(T) {
    const fol = S.follow && S.follow.id === T.id;
    const lensOk = S.lens ? T.members.some((id) => { const it = items[byId.get(id)]; return it && it.people.some((p) => p.id === S.lens); }) : true;
    let a = fol ? 1.3 : S.follow ? 0.12 : S.lens ? (lensOk ? 0.62 : 0.12) : 0.55;
    if (S.query && !S.follow) a *= 0.55;
    a *= lerp(1, 0.22, smooth(1.4, 3.4, S.cam.z));
    return a * (1 - S.divePhase);
  }
  function nearRegions() {
    return [...regions].sort((a, b) => Math.hypot(a.x - S.cam.x, a.y - S.cam.y) - Math.hypot(b.x - S.cam.x, b.y - S.cam.y)).slice(0, MAXREG);
  }
  function renderGL() {
    const { w, h } = S.view, z = S.cam.z, t = S.time * (reduced ? 0.18 : 1);
    gl.bindFramebuffer(gl.FRAMEBUFFER, fbo);
    gl.viewport(0, 0, fboW, fboH);
    gl.disable(gl.BLEND);
    gl.useProgram(P.fluid.p);
    const u = P.fluid.u;
    gl.uniform2f(u.uRes, fboW, fboH); gl.uniform2f(u.uView, w, h); gl.uniform2f(u.uCam, S.cam.x, S.cam.y);
    gl.uniform1f(u.uZoom, z); gl.uniform1f(u.uTime, t); gl.uniform1f(u.uDive, S.divePhase); gl.uniform2f(u.uDiveW, S.diveW[0], S.diveW[1]);
    gl.uniform1f(u.uLight, theme.light ? 1 : 0);
    const nr = nearRegions();
    const regPos = new Float32Array(MAXREG * 2), regCol = new Float32Array(MAXREG * 3);
    nr.forEach((r, i) => {
      regPos[i * 2] = r.x; regPos[i * 2 + 1] = r.y;
      const c = r.special ? WARM : r.kind === "dream" ? [0.42, 0.34, 0.9] : r.col;
      const cc = theme.light ? c.map((v) => lerp(v, 0.55, 0.25)) : c.map((v) => v * 0.32);
      regCol.set(cc, i * 3);
    });
    gl.uniform1i(u.uNReg, nr.length);
    gl.uniform2fv(u.uReg, regPos); gl.uniform3fv(u.uRegCol, regCol);
    gl.uniform3fv(u.uDeep, theme.deep); gl.uniform3fv(u.uMid, theme.mid); gl.uniform3fv(u.uSilver, theme.silver);
    gl.bindVertexArray(vaoFull);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);
    if (threads.length) {
      gl.useProgram(P.rib.p);
      const ur = P.rib.u;
      gl.uniform2f(ur.uCam, S.cam.x, S.cam.y); gl.uniform2f(ur.uView, w, h); gl.uniform1f(ur.uZoom, z); gl.uniform1f(ur.uTime, t);
      gl.uniform1f(ur.uLight, theme.light ? 1 : 0);
      gl.uniform1f(ur.uWidth, clamp(24 * z, 7, 24));
      gl.bindVertexArray(vaoRib);
      threads.forEach((T, i) => {
        const a = threadState(T);
        if (a < 0.01) return;
        gl.uniform1f(ur.uAlpha, a);
        gl.uniform3fv(ur.uCol, theme.light ? T.col.map((c) => c * 0.45) : T.col);
        gl.drawArrays(gl.TRIANGLE_STRIP, ribRanges[i][0], ribRanges[i][1]);
      });
    }
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    gl.viewport(0, 0, el.canvas.width, el.canvas.height);
    gl.disable(gl.BLEND);
    gl.useProgram(P.blit.p);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, fboTex);
    gl.uniform1i(P.blit.u.uTex, 0); gl.uniform2f(P.blit.u.uDev, el.canvas.width, el.canvas.height); gl.uniform1f(P.blit.u.uTime, t);
    gl.bindVertexArray(vaoFull);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
    if (!items.length) return;
    if (Math.abs(S.surface - lastOrderSurface) > 0.2 || Math.abs(S.depthOn - lastOrderDepth) > 0.2) uploadStatic();
    order.forEach((i, p) => {
      const a = anim[i], it = items[i];
      DYN[p * 4] = a.hov; DYN[p * 4 + 1] = i === S.diveId ? 1 : 0; DYN[p * 4 + 2] = it.special ? 1 : 0; DYN[p * 4 + 3] = it.dream ? 1 : 0;
      STATIC[p * 12 + 9] = a.lit; STATIC[p * 12 + 10] = a.dim; STATIC[p * 12 + 11] = a.sel;
    });
    gl.bindBuffer(gl.ARRAY_BUFFER, instA); gl.bufferSubData(gl.ARRAY_BUFFER, 0, STATIC);
    gl.bindBuffer(gl.ARRAY_BUFFER, instDyn); gl.bufferSubData(gl.ARRAY_BUFFER, 0, DYN);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);
    gl.useProgram(P.bub.p);
    const ub = P.bub.u;
    gl.uniform2f(ub.uCam, S.cam.x, S.cam.y); gl.uniform2f(ub.uView, w, h); gl.uniform1f(ub.uZoom, z); gl.uniform1f(ub.uTime, t);
    gl.uniform1f(ub.uSurface, S.surface); gl.uniform1f(ub.uDepthOn, S.depthOn); gl.uniform1f(ub.uMotion, reduced ? 0 : 1); gl.uniform1f(ub.uDive, S.divePhase);
    gl.uniform1i(ub.uScene, 0); gl.uniform2f(ub.uDev, el.canvas.width, el.canvas.height); gl.uniform1f(ub.uLight, theme.light ? 1 : 0);
    gl.uniform3fv(ub.uAccent, theme.accent); gl.uniform3fv(ub.uSilver, theme.silver); gl.uniform3fv(ub.uInk, theme.ink); gl.uniform3fv(ub.uVeil, theme.veil); gl.uniform3fv(ub.uWarmCol, theme.warm);
    gl.bindVertexArray(vaoBub);
    gl.drawArraysInstanced(gl.TRIANGLE_STRIP, 0, 4, items.length);
    gl.bindVertexArray(null);
  }

  // ---- Flat fallback ------------------------------------------------------------------
  const rgb = (c, a = 1) => `rgba(${c.map((v) => Math.round(clamp(v, 0, 1) * 255)).join(",")},${a})`;
  function render2D() {
    const c = ctx2, { w, h } = S.view, dpr = el.canvas.width / w;
    c.setTransform(dpr, 0, 0, dpr, 0, 0);
    c.fillStyle = rgb(theme.mid);
    c.fillRect(0, 0, w, h);
    nearRegions().forEach((r) => {
      const [x, y] = toScreen(r.x, r.y), R = 430 * S.cam.z;
      const col = r.special ? WARM : r.col;
      const g = c.createRadialGradient(x, y, 0, x, y, R);
      g.addColorStop(0, rgb(theme.light ? col.map((v) => lerp(v, 0.55, 0.25)) : col.map((v) => v * 0.32), 0.7));
      g.addColorStop(1, rgb(col, 0));
      c.fillStyle = g;
      c.fillRect(0, 0, w, h);
    });
    c.lineCap = "round";
    c.lineJoin = "round";
    threads.forEach((T) => {
      const a = threadState(T);
      if (a < 0.02) return;
      c.strokeStyle = rgb(theme.light ? T.col.map((v) => v * 0.45) : T.col, 0.35 * a);
      c.lineWidth = clamp(14 * S.cam.z, 3, 12);
      c.beginPath();
      T.pts.forEach((p, k) => { const [x, y] = toScreen(p[0], p[1]); k ? c.lineTo(x, y) : c.moveTo(x, y); });
      c.stroke();
    });
    order.forEach((i) => {
      const it = items[i], s = screen[i], a = anim[i];
      if (!s.vis) return;
      const al = s.a * (1 - a.dim * 0.8) * (1 - depthOf(it) * 0.4);
      const tint = tintOf(it);
      const g = c.createRadialGradient(s.x - s.R * 0.35, s.y - s.R * 0.4, s.R * 0.05, s.x, s.y, s.R);
      g.addColorStop(0, `rgba(255,255,255,${0.55 * al})`);
      g.addColorStop(0.6, rgb(it.dream ? [0.12, 0.1, 0.3] : tint, (it.dream ? 0.5 : 0.12) * al));
      g.addColorStop(1, rgb(tint, 0.45 * al));
      c.fillStyle = g;
      c.beginPath(); c.arc(s.x, s.y, s.R, 0, TAU); c.fill();
      const glow = clamp(a.lit * 0.8 + a.sel + a.hov * 0.5 + (it.special ? 0.35 : 0), 0, 1);
      c.lineWidth = 1 + glow * 1.5;
      c.strokeStyle = glow > 0.05 ? rgb(it.special && !a.lit && !a.sel ? theme.warm : theme.accent, 0.4 + glow * 0.6) : rgb(theme.silver, 0.5 * al);
      c.stroke();
    });
  }

  // ---- Labels -----------------------------------------------------------------------
  function buildLabels() {
    el.labels.innerHTML = "";
    regionEls = regions.map((r) => {
      const e = document.createElement("div");
      e.className = "rlabel" + (r.special ? " special" : "");
      e.setAttribute("aria-hidden", "true");
      fillRegionLabel(e, r);
      el.labels.appendChild(e);
      return e;
    });
    const ideas = items.filter((i) => i.type === "idea");
    ideaEls = ideas.map((I) => {
      const e = document.createElement("div");
      e.className = "ilabel" + (I.state === "pencil" ? " pencil" : "");
      e.setAttribute("aria-hidden", "true");
      e.textContent = M.excerpt(I.title, 60);
      e._i = I.idx;
      el.labels.appendChild(e);
      return e;
    });
    threadEls = threads.map((T) => {
      const e = document.createElement("button");
      e.type = "button";
      e.className = "tlabel";
      e.tabIndex = -1;
      e.innerHTML = T.title ? `Thread<i>${esc(T.title)}</i>` : `Thread · ${T.members.length}`;
      e.setAttribute("aria-label", `Follow ${T.title ? "thread " + T.title : "an unnamed thread"}`);
      e.addEventListener("pointerdown", (ev) => ev.stopPropagation());
      e.addEventListener("click", () => startFollow(T.id));
      el.labels.appendChild(e);
      return e;
    });
    glimpses = [];
    for (let i = 0; i < 38; i++) {
      const g = document.createElement("div");
      g.className = "glimpse";
      g.setAttribute("aria-hidden", "true");
      g.hidden = true;
      g._id = -1;
      g._lvl = -1;
      el.labels.appendChild(g);
      glimpses.push(g);
    }
    edgeEls = regions.map((r) => {
      const e = document.createElement("button");
      e.type = "button";
      e.className = "edge glass";
      e.tabIndex = -1;
      e.setAttribute("aria-hidden", "true");
      e.innerHTML = `<svg width="12" height="12" viewBox="0 0 12 12"><path d="M2 6h8M7 3l3 3-3 3" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/></svg><span>${r.name ? esc(r.name) : r.count}</span>`;
      e.hidden = true;
      e.addEventListener("pointerdown", (ev) => ev.stopPropagation());
      e.addEventListener("click", () => flyTo(r.x, r.y, Math.max(S.cam.z * 0.85, 1.05), 900));
      el.labels.appendChild(e);
      return e;
    });
  }
  function fillRegionLabel(e, r) {
    e.classList.toggle("unnamed", !r.name);
    e.innerHTML = r.name
      ? `${esc(r.name)}<small>${r.count} ${r.count === 1 ? "moment" : "moments"}</small>`
      : `<small>· ${r.count}</small><button type="button" class="namebtn" tabindex="-1">Name this…</button>`;
    e._w = 0;
    const btn = e.querySelector(".namebtn");
    if (btn) {
      btn.addEventListener("pointerdown", (ev) => ev.stopPropagation());
      btn.addEventListener("click", () => nameRegion(e, r));
    }
  }
  function nameRegion(e, r) {
    e.innerHTML = `<input aria-label="Name this region" placeholder="Name this region" maxlength="120" />`;
    const input = e.querySelector("input");
    input.addEventListener("pointerdown", (ev) => ev.stopPropagation());
    input.focus();
    let done = false;
    const finish = async (save) => {
      if (done) return;
      done = true;
      const name = input.value.trim();
      if (save && name) {
        try {
          await api(`/regions/${encodeURIComponent(r.key)}`, { name }, "PUT");
          r.name = name;
          announce(`Region named ${name}.`);
          buildList();
        } catch (err) { announce(err.message); }
      }
      fillRegionLabel(e, r);
    };
    input.addEventListener("keydown", (ev) => {
      ev.stopPropagation();
      if (ev.key === "Enter") finish(true);
      if (ev.key === "Escape") finish(false);
    });
    input.addEventListener("blur", () => finish(true));
  }

  function fmtWhen(it) {
    const p = M.localParts(it.at, it.timezone);
    return p ? `${M.formatDate(p, { short: true })} · ${M.hourText(p)}` : "";
  }
  function updateLabels() {
    const { w, h } = S.view, z = S.cam.z, diving = S.divePhase > 0.02;
    el.labels.style.opacity = diving ? String(Math.max(0, 1 - S.divePhase * 3)) : "1";
    const rFade = 1 - smooth(1.25, 2.1, z);
    const rSize = clamp(z * 58, 14, 30) * (1 - smooth(0.6, 1.6, z) * 0.4);
    const anyFilter = !!(S.lens || S.query || S.follow);
    regions.forEach((r, i) => {
      const e = regionEls[i];
      if (e.querySelector("input")) { const [x, y] = toScreen(r.labelX, r.labelY); e.style.opacity = "1"; e.style.transform = `translate(${x}px,${y}px) translate(-50%,-50%)`; return; }
      const [x, y] = toScreen(r.labelX, r.labelY);
      if (rFade < 0.02) { e.style.opacity = "0"; e.style.pointerEvents = "none"; return; }
      if (e._fs !== Math.round(rSize)) { e._fs = Math.round(rSize); e.style.fontSize = e._fs + "px"; e._w = 0; }
      e.style.opacity = String(rFade * (anyFilter ? 0.55 : 1));
      const hw = (e._w || (e._w = e.offsetWidth)) / 2;
      const cx = hw * 2 < w - 24 && x > 0 && x < w ? clamp(x, hw + 12, w - hw - 12) : x;
      e.style.transform = `translate(${cx}px,${y}px) translate(-50%,-50%)`;
    });
    const iFade = smooth(0.62, 0.85, z);
    ideaEls.forEach((e) => {
      const i = e._i, s = screen[i];
      const show = iFade > 0.02 && s.x > -150 && s.x < w + 150 && s.y > -60 && s.y < h + 60 && anim[i].dim < 0.7;
      if (!show) { if (e.style.opacity !== "0") e.style.opacity = "0"; return; }
      e.style.opacity = String(iFade * (1 - anim[i].dim * 0.8));
      e.style.transform = `translate(${s.x}px,${s.y + s.R + 6}px) translate(-50%,0)`;
      e.classList.toggle("lit", anim[i].lit > 0.5);
    });
    const tFade = (1 - smooth(1.4, 2.4, z)) * smooth(0.2, 0.3, z);
    threads.forEach((T, i) => {
      const e = threadEls[i], a = threadState(T), [x, y] = toScreen(T.label[0], T.label[1]);
      const o = tFade * clamp(a * 1.2, 0, 1);
      e.style.opacity = String(o);
      e.style.pointerEvents = o > 0.15 ? "auto" : "none";
      e.style.transform = `translate(${x}px,${y}px) translate(-50%,-50%)`;
      e.classList.toggle("on", !!(S.follow && S.follow.id === T.id));
    });
    const cands = [];
    if (!diving) items.forEach((m, i) => {
      if (m.type !== "moment") return;
      const s = screen[i];
      if (s.R < 27 || !s.vis || anim[i].dim > 0.6) return;
      if (s.x < -s.R || s.x > w + s.R || s.y < -s.R || s.y > h + s.R) return;
      cands.push(i);
    });
    cands.sort((a, b) => screen[b].R - screen[a].R);
    const assign = cands.slice(0, glimpses.length), want = new Set(assign), used = new Set();
    glimpses.forEach((g) => { if (g._id >= 0 && !want.has(g._id)) { g._id = -1; g.hidden = true; } });
    assign.forEach((i) => {
      const m = items[i];
      let g = glimpses.find((x) => x._id === i) || glimpses.find((x) => x._id < 0 && !used.has(x));
      if (!g) return;
      used.add(g);
      const s = screen[i], lvl = s.R > 96 ? 2 : s.R > 44 ? 1 : 0;
      if (g._id !== i || g._lvl !== lvl) {
        g._id = i;
        g._lvl = lvl;
        const p = M.localParts(m.at, m.timezone);
        const meta = lvl === 0 ? KIND_SHORT[m.kind] : `${KIND_SHORT[m.kind]} · ${p ? (lvl === 1 ? "’" + String(p.year).slice(2) : M.formatDate(p, { short: true, noYear: true }) + " ’" + String(p.year).slice(2)) : ""}`;
        const title = m.title || M.excerpt(m.excerpt, 60);
        g.innerHTML = `<div class="g-meta">${esc(meta)}</div>${lvl >= 1 ? `<div class="g-title">${esc(title)}</div>` : ""}${lvl >= 2 && m.title && m.excerpt ? `<div class="g-text">${esc(M.excerpt(m.excerpt, 110))}</div>` : ""}`;
        g.className = "glimpse" + (m.dream ? " dream" : "") + (m.special ? " special" : "");
        g.hidden = false;
      }
      const d = s.R * 1.7;
      g.style.width = d + "px";
      g.style.height = d + "px";
      g.style.fontSize = clamp(s.R / (lvl === 2 ? 7 : 4.2), 11, 17) + "px";
      g.style.transform = `translate(${s.x - d / 2}px,${s.y - d / 2}px)`;
      g.style.opacity = String(smooth(27, 40, s.R) * (1 - anim[i].dim) * (1 - depthOf(m) * 0.35));
      g.classList.toggle("lit", anim[i].lit > 0.5);
    });
    const showEdges = z > 0.95 && !diving && regions.length > 1;
    const phone = w < 760, top = phone ? 160 : 132, bot = phone ? 190 : 120, side = phone ? 14 : 24, rside = phone ? 14 : 150;
    const off = [];
    regions.forEach((r, i) => {
      const [x, y] = toScreen(r.x, r.y);
      const inside = x > -40 && x < w + 40 && y > -40 && y < h + 40;
      if (!showEdges || inside) { edgeEls[i].hidden = true; return; }
      off.push({ i, x, y, d: Math.hypot(x - w / 2, y - h / 2) });
    });
    off.sort((a, b) => a.d - b.d);
    off.forEach((o, k) => {
      const e = edgeEls[o.i];
      if (k >= (phone ? 2 : 4)) { e.hidden = true; return; }
      e.hidden = false;
      const cx = w / 2, cy = h / 2, dx = o.x - cx, dy = o.y - cy;
      const sx = ((dx < 0 ? w / 2 - side : w / 2 - rside) - 90) / Math.abs(dx || 1e-6), sy = (dy < 0 ? h / 2 - top : h / 2 - bot) / Math.abs(dy || 1e-6);
      const sc = Math.min(sx, sy);
      e.querySelector("svg").style.transform = `rotate(${Math.atan2(dy, dx)}rad)`;
      e.style.transform = `translate(${cx + dx * sc}px,${cy + dy * sc}px) translate(-50%,-50%)`;
    });
  }

  // ---- Filters: search, lens, thread follow -------------------------------------------
  let matches = [];
  function recompute() {
    const terms = S.query.trim().toLowerCase().split(/\s+/).filter(Boolean);
    const fol = S.follow ? threads.find((t) => t.id === S.follow.id) : null;
    const folSet = fol ? new Set(fol.members) : null;
    const any = terms.length || S.lens || fol;
    matches = [];
    items.forEach((m, i) => {
      if (m.type !== "moment") return;
      let ok = true;
      if (S.lens) ok = ok && m.people.some((p) => p.id === S.lens);
      if (folSet) ok = ok && folSet.has(m.id);
      if (terms.length) ok = ok && SM.matches(m, terms);
      anim[i].tl = any && ok ? 1 : 0;
      anim[i].td = any && !ok ? 1 : 0;
      if (any && ok) matches.push(i);
    });
    items.forEach((I, i) => {
      if (I.type !== "idea") return;
      const mi = byId.get(I.moment);
      let lit = 0;
      if (any) lit = terms.length && SM.matches(I, terms) && !S.lens && !fol ? 1 : mi != null && anim[mi].tl > 0 ? 0.55 : 0;
      anim[i].tl = lit;
      anim[i].td = any && !lit ? 1 : 0;
    });
    if (terms.length) matches.sort((a, b) => items[b].year - items[a].year);
    updateStatus();
    filterList();
    drawSpark();
  }
  function btn(label, path, fn) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "icon-btn";
    b.setAttribute("aria-label", label);
    b.innerHTML = `<svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true"><path d="${path}" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>`;
    b.addEventListener("click", fn);
    return b;
  }
  function updateStatus() {
    const st = $("#sp-status"), tx = $("#sp-status-text"), ctl = $("#sp-status-ctl");
    const parts = [];
    ctl.innerHTML = "";
    if (S.follow) {
      const T = threads.find((t) => t.id === S.follow.id);
      const m = items[byId.get(T.members[S.follow.idx])];
      parts.push(`Thread <b>${T.title ? esc(T.title) : "unnamed"}</b> <span class="s-num">${S.follow.idx + 1}/${T.members.length}${m ? " · " + esc(fmtWhen(m)) : ""}</span>`);
      ctl.append(btn("Earlier memory in thread", "M8 2L4 6l4 4", () => stepFollow(-1)), btn("Later memory in thread", "M4 2l4 4-4 4", () => stepFollow(1)));
      if (!T.title) {
        const nb = document.createElement("button");
        nb.type = "button";
        nb.className = "chip";
        nb.textContent = "Name this…";
        nb.addEventListener("click", () => nameThread(T));
        ctl.append(nb);
      }
    }
    if (S.lens) {
      const p = people.find((x) => x.id === S.lens);
      parts.push(`With <b>${esc(p ? p.name : "someone")}</b> <span class="s-num">${matches.length} ${matches.length === 1 ? "moment" : "moments"}</span>`);
    }
    if (S.query.trim()) parts.push(`“${esc(S.query.trim())}” <span class="s-num">${matches.length} ${matches.length === 1 ? "match" : "matches"}${matches.length ? " · Enter to visit" : ""}</span>`);
    if (!parts.length) {
      const moments = items.filter((i) => i.type === "moment");
      if (!moments.length) { st.hidden = true; return; }
      tx.innerHTML = `<span class="s-num">${moments.length} ${moments.length === 1 ? "Moment" : "Moments"}${S.depthOnT < 0.5 ? " · every year level" : ""}</span>`;
      const go = document.createElement("button");
      go.type = "button";
      go.className = "chip";
      go.textContent = "Newest";
      go.addEventListener("click", () => {
        const recent = moments.sort((a, b) => b.year - a.year)[0];
        focusItem(recent.idx, true, 2.6);
      });
      ctl.append(go);
      st.hidden = false;
      return;
    }
    tx.innerHTML = parts.join(' <span style="opacity:.4">/</span> ');
    ctl.append(btn("Clear filters", "M3 3l6 6M9 3l-6 6", clearFilters));
    st.hidden = false;
  }
  function nameThread(T) {
    const ctl = $("#sp-status-ctl");
    ctl.innerHTML = `<input aria-label="Name this thread" placeholder="Name this thread" maxlength="300" />`;
    const input = ctl.querySelector("input");
    input.focus();
    input.addEventListener("keydown", async (e) => {
      e.stopPropagation();
      if (e.key === "Escape") return updateStatus();
      if (e.key !== "Enter" || !input.value.trim()) return;
      try {
        const d = await api(`/threads/${T.id}`, { expectedRevision: T.revision, title: input.value.trim() }, "PATCH");
        T.title = d.thread.title;
        T.revision = d.thread.revision;
        const e2 = threadEls[threads.indexOf(T)];
        e2.innerHTML = `Thread<i>${esc(T.title)}</i>`;
        announce(`Thread named ${T.title}.`);
        buildList();
      } catch (err) { announce(err.message); }
      updateStatus();
    });
  }
  function clearFilters() {
    S.query = "";
    $("#sp-q").value = "";
    setLens("");
    S.follow = null;
    recompute();
    announce("Filters cleared.");
  }
  function buildLens() {
    const lens = $("#sp-lens");
    const top = [...people].filter((p) => p.moments > 0).sort((a, b) => (b.special - a.special) || b.moments - a.moments).slice(0, 8);
    lens.innerHTML = `<span class="lens-label">Lens</span><button class="chip" type="button" data-person="" aria-pressed="true">Everyone</button>` +
      top.map((p) => `<button class="chip ${p.special ? "special" : ""}" type="button" data-person="${esc(p.id)}" aria-pressed="false"><span class="dot"></span>${esc(p.name)}</button>`).join("");
    lens.querySelectorAll(".chip").forEach((c) => c.addEventListener("click", () => setLens(S.lens === c.dataset.person ? "" : c.dataset.person)));
  }
  function setLens(p) {
    S.lens = p;
    root.querySelectorAll("#sp-lens .chip").forEach((c) => c.setAttribute("aria-pressed", String(c.dataset.person === p)));
    recompute();
    if (p) announce(`Lens on ${people.find((x) => x.id === p)?.name || "someone"}. ${matches.length} moments light up.`);
  }
  function startFollow(id) {
    const T = threads.find((t) => t.id === id);
    if (!T) return;
    S.follow = { id, idx: 0 };
    recompute();
    goFollow();
    announce(`Following ${T.title || "an unnamed thread"}, ${T.members.length} moments.`);
  }
  function stepFollow(d) {
    const T = threads.find((t) => t.id === S.follow.id);
    S.follow.idx = (S.follow.idx + d + T.members.length) % T.members.length;
    goFollow();
  }
  function goFollow() {
    const T = threads.find((t) => t.id === S.follow.id);
    const i = byId.get(T.members[S.follow.idx]);
    if (S.depthOnT > 0.5) { S.surfaceT = Math.min(S.yearMax, items[i].year + 0.15); $("#sp-surface").value = String(S.surfaceT); }
    focusItem(i, true, 2.4);
    updateStatus();
  }

  // ---- Camera --------------------------------------------------------------------------
  function flyTo(x, y, z, dur = 900) {
    z = clamp(z, ZMIN, ZMAX);
    if (reduced) dur = Math.min(dur, 160);
    const from = { ...S.cam };
    const dist = Math.hypot(x - from.x, y - from.y) * Math.min(from.z, z);
    const dip = clamp(dist / Math.max(S.view.w, S.view.h) - 0.6, 0, 1.2);
    S.fly = { from, to: { x, y, z }, t0: performance.now(), dur, dip };
    S.vel.x = S.vel.y = 0;
  }
  function stepFly(now) {
    const f = S.fly;
    if (!f) return;
    const p = clamp((now - f.t0) / f.dur, 0, 1), e = easeIO(p);
    S.cam.x = lerp(f.from.x, f.to.x, e);
    S.cam.y = lerp(f.from.y, f.to.y, e);
    S.cam.z = Math.exp(lerp(Math.log(f.from.z), Math.log(f.to.z), e) - f.dip * Math.sin(Math.PI * p) * 0.9);
    if (p >= 1) S.fly = null;
  }
  function zoomAt(sx, sy, factor) {
    const [wx, wy] = toWorld(sx, sy);
    const nz = clamp(S.cam.z * factor, ZMIN, ZMAX);
    S.cam.x = wx - (sx - S.view.w / 2) / nz;
    S.cam.y = wy - (sy - S.view.h / 2) / nz;
    S.cam.z = nz;
    S.fly = null;
  }
  function clampCam() {
    if (!bounds) return;
    const mx = (bounds.x1 - bounds.x0) * 0.5 + 200, my = (bounds.y1 - bounds.y0) * 0.5 + 200, cx = (bounds.x0 + bounds.x1) / 2, cy = (bounds.y0 + bounds.y1) / 2;
    S.cam.x = clamp(S.cam.x, cx - mx, cx + mx);
    S.cam.y = clamp(S.cam.y, cy - my, cy + my);
  }
  function homeCam() {
    return SM.homeCamera({ items, bounds }, S.view, false);
  }
  let drifted = true;
  function maybeDrift() {
    if (drifted || S.dive || S.fly || S.cam.z < 2.2 || pointers.size) return;
    drifted = true;
    let best = -1, bd = 1e9;
    items.forEach((m, i) => {
      if (m.type !== "moment") return;
      const [sx, sy] = toScreen(m.x, m.y), d = Math.hypot(sx - S.view.w / 2, sy - S.view.h / 2);
      if (d < bd && anim[i].dim < 0.5) { bd = d; best = i; }
    });
    if (best >= 0 && bd < Math.min(S.view.w, S.view.h) * 0.22 && bd > 6) flyTo(items[best].x, items[best].y, S.cam.z, reduced ? 0 : 700);
  }

  // ---- Pointer ---------------------------------------------------------------------------
  const pointers = new Map();
  let pinch = null, dragMoved = 0, downAt = null, gScale = 1;
  function bindStatic() {
    const stage = el.stage;
    stage.addEventListener("pointerdown", (e) => {
      if (S.dive) return;
      stage.setPointerCapture(e.pointerId);
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY, t: performance.now() });
      S.fly = null; S.vel.x = S.vel.y = 0; drifted = true;
      if (pointers.size === 1) { dragMoved = 0; downAt = { x: e.clientX, y: e.clientY, type: e.pointerType }; }
      if (pointers.size === 2) { const [a, b] = [...pointers.values()]; pinch = { d: Math.hypot(a.x - b.x, a.y - b.y), mx: (a.x + b.x) / 2, my: (a.y + b.y) / 2 }; }
    });
    stage.addEventListener("pointermove", (e) => {
      if (S.dive) return;
      const p = pointers.get(e.pointerId);
      if (!p) { if (e.pointerType === "mouse") setHover(pick(e.clientX, e.clientY)); return; }
      const now = performance.now(), dx = e.clientX - p.x, dy = e.clientY - p.y;
      if (pointers.size === 1) {
        dragMoved += Math.abs(dx) + Math.abs(dy);
        if (dragMoved > 4) {
          stage.classList.add("dragging");
          S.cam.x -= dx / S.cam.z; S.cam.y -= dy / S.cam.z;
          const dt = Math.max(8, now - p.t);
          S.vel.x = (-dx / S.cam.z / dt) * 16; S.vel.y = (-dy / S.cam.z / dt) * 16;
          hideCard();
        }
      }
      p.x = e.clientX; p.y = e.clientY; p.t = now;
      if (pointers.size === 2 && pinch) {
        const [a, b] = [...pointers.values()];
        const d = Math.hypot(a.x - b.x, a.y - b.y), mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2;
        S.cam.x -= (mx - pinch.mx) / S.cam.z; S.cam.y -= (my - pinch.my) / S.cam.z;
        zoomAt(mx, my, d / pinch.d);
        pinch = { d, mx, my };
        dragMoved = 99;
        hideCard();
      }
    });
    const endPointer = (e) => {
      if (!pointers.has(e.pointerId)) return;
      pointers.delete(e.pointerId);
      stage.classList.remove("dragging");
      if (pointers.size < 2) pinch = null;
      if (pointers.size === 0) {
        if (dragMoved <= 4 && downAt) tapAt(e.clientX, e.clientY, downAt.type);
        else { if (reduced) S.vel.x = S.vel.y = 0; drifted = false; }
        downAt = null;
      }
    };
    stage.addEventListener("pointerup", endPointer);
    stage.addEventListener("pointercancel", endPointer);
    stage.addEventListener("pointerleave", (e) => { if (e.pointerType === "mouse" && !pointers.size) setHover(-1); });
    stage.addEventListener("wheel", (e) => {
      e.preventDefault();
      if (S.dive) return;
      hideCard();
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? S.view.h : 1;
      const dx = e.deltaX * unit, dy = e.deltaY * unit;
      const mouseWheel = e.deltaMode !== 0 || (Math.abs(dy) >= 40 && dx === 0 && Number.isInteger(dy));
      if (e.ctrlKey || mouseWheel) zoomAt(e.clientX, e.clientY, Math.exp(-dy * (e.ctrlKey ? 0.012 : 0.0016)));
      else { S.cam.x += dx / S.cam.z; S.cam.y += dy / S.cam.z; S.fly = null; }
      drifted = false;
    }, { passive: false });
    stage.addEventListener("gesturestart", (e) => { e.preventDefault(); gScale = 1; });
    stage.addEventListener("gesturechange", (e) => { e.preventDefault(); zoomAt(e.clientX, e.clientY, e.scale / gScale); gScale = e.scale; });
    stage.addEventListener("dblclick", (e) => { if (pick(e.clientX, e.clientY) < 0) flyTo(...toWorld(e.clientX, e.clientY), S.cam.z * 2, 500); });
    stage.addEventListener("keydown", onStageKey);
    stage.addEventListener("blur", () => { if (!el.card.classList.contains("interactive")) hideCard(); });
    const q = $("#sp-q");
    let qt = 0;
    q.addEventListener("input", () => { clearTimeout(qt); qt = setTimeout(() => { S.query = q.value; recompute(); announce(S.query.trim() ? `${matches.length} matches.` : ""); }, 90); });
    q.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && matches.length) { e.preventDefault(); focusItem(matches[0], true); el.stage.focus({ preventScroll: true }); }
      if (e.key === "Escape") { q.value = ""; S.query = ""; recompute(); el.stage.focus({ preventScroll: true }); }
    });
    $("#sp-listBtn").addEventListener("click", () => toggleList());
    $("#sp-listClose").addEventListener("click", () => toggleList(false));
    const surf = $("#sp-surface");
    surf.addEventListener("input", () => {
      S.surfaceT = +surf.value;
      if (S.depthOnT < 0.5) { S.depthOnT = 1; $("#sp-level").setAttribute("aria-pressed", "false"); }
      setTimeRead();
      updateStatus();
    });
    $("#sp-level").addEventListener("click", (e) => {
      const on = e.currentTarget.getAttribute("aria-pressed") !== "true";
      e.currentTarget.setAttribute("aria-pressed", String(on));
      S.depthOnT = on ? 0 : 1;
      setTimeRead();
      updateStatus();
      announce(on ? "Every year at the same depth." : "Time depth on: the chosen year rises.");
    });
    $("#sp-zin").addEventListener("click", () => flyTo(S.cam.x, S.cam.y, S.cam.z * 1.7, reduced ? 0 : 420));
    $("#sp-zout").addEventListener("click", () => flyTo(S.cam.x, S.cam.y, S.cam.z / 1.7, reduced ? 0 : 420));
    $("#sp-zhome").addEventListener("click", () => { const h = homeCam(); flyTo(h.x, h.y, h.z); });
    root.querySelectorAll(".gauge button").forEach((b) => b.addEventListener("click", () => { const L = +b.dataset.level; flyTo(S.cam.x, S.cam.y, L === 0 ? homeCam().z : LEVELS[L], 800); }));
    document.addEventListener("keydown", (e) => {
      if (!running) return;
      const typing = /INPUT|TEXTAREA/.test(document.activeElement.tagName);
      if (e.key === "/" && !typing && !S.dive) { e.preventDefault(); $("#sp-q").focus(); }
      else if ((e.key === "l" || e.key === "L") && !typing && !S.dive && !e.metaKey && !e.ctrlKey) toggleList();
      else if (e.key === "Escape" && !$("#sp-list").hidden) toggleList(false);
    });
  }
  function pick(sx, sy) {
    let best = -1, bestScore = 1e9;
    for (let i = 0; i < items.length; i++) {
      const s = screen[i];
      if (!s.vis || s.a < 0.3) continue;
      const it = items[i], d = Math.hypot(sx - s.x, sy - s.y), hit = Math.max(s.R, it.type === "moment" ? 8 : 6) + 3;
      if (d > hit) continue;
      const score = d / hit + anim[i].dim * 2 + (it.type === "idea" && S.cam.z < 1.4 ? 0.6 : 0);
      if (score < bestScore) { bestScore = score; best = i; }
    }
    return best;
  }
  function setHover(i) {
    if (S.hover === i) return;
    S.hover = i;
    el.stage.classList.toggle("over", i >= 0);
    if (i >= 0) showCard(i, false);
    else if (S.focus >= 0 && document.activeElement === el.stage) showCard(S.focus, true);
    else hideCard();
  }
  function tapAt(x, y, type) {
    const i = pick(x, y);
    if (i < 0) { if (type !== "mouse") { S.touchSel = -1; setFocus(-1); hideCard(); } return; }
    if (type !== "mouse" && S.touchSel !== i) { S.touchSel = i; setFocus(i, false); showCard(i, true); return; }
    activate(i);
  }
  function activate(i) {
    const it = items[i];
    if (it.type === "idea") {
      const mi = byId.get(it.moment);
      if (S.cam.z < 2 && mi != null) { flyTo(it.x, it.y, 2.6, 900); setFocus(i, false); return; }
      startDive(mi != null ? mi : i, `?at=note:${it.id}`);
    } else startDive(i);
  }

  // ---- Card ---------------------------------------------------------------------------
  function showCard(i, interactive) {
    const it = items[i], s = screen[i];
    if (!it || !s) return;
    S.cardFor = i;
    const who = it.people.length ? "With " + it.people.map((p) => p.name).join(", ") : "Just you";
    const region = regions.find((r) => r.key === it.region);
    if (it.type === "idea") {
      const m = items[byId.get(it.moment)];
      el.card.innerHTML = `<div class="c-meta">Idea · ${it.state === "pencil" ? "in pencil" : "kept"}</div><div class="c-title">${esc(M.excerpt(it.title, 120))}</div>
        ${it.excerpt && it.excerpt !== it.title ? `<p class="c-text">${esc(M.excerpt(it.excerpt, interactive ? 300 : 120))}</p>` : ""}
        <div class="c-prov">From ${esc(m ? m.title || M.excerpt(m.excerpt, 60) : "a Moment")}</div>
        ${interactive ? `<button class="pill-btn c-go" type="button" id="sp-go">Open its Moment</button>` : ""}`;
    } else {
      el.card.innerHTML = `<div class="c-meta">${it.special ? "<b>●</b> " : ""}${esc(M.KIND_LABEL[it.kind] || it.kind)} · ${esc(fmtWhen(it))}</div>
        ${it.title ? `<div class="c-title">${esc(it.title)}</div>` : ""}
        ${it.excerpt ? `<p class="c-text">${esc(M.excerpt(it.excerpt, interactive ? 320 : 130))}</p>` : ""}
        <div class="c-prov">${esc(who)} · ${esc(M.SOURCE_LABEL[it.source] || "")}${it.sourceDetail ? " · " + esc(it.sourceDetail) : ""}${region && region.name ? " · in " + esc(region.name) : ""}</div>
        ${interactive ? `<button class="pill-btn c-go" type="button" id="sp-go">Enter this memory</button>` : ""}`;
    }
    el.card.classList.toggle("interactive", !!interactive);
    el.card.hidden = false;
    placeCard();
    const go = $("#sp-go");
    if (go) {
      go.addEventListener("pointerdown", (e) => e.stopPropagation());
      go.addEventListener("click", () => activate(i));
    }
  }
  function placeCard() {
    if (el.card.hidden || S.cardFor < 0) return;
    const s = screen[S.cardFor], { w, h } = S.view, cw = el.card.offsetWidth, ch = el.card.offsetHeight;
    let x, y;
    if (w < 760) { x = (w - cw) / 2; y = s.y < h * 0.55 ? Math.min(h - ch - 150, s.y + s.R + 12) : Math.max(130, s.y - s.R - 12 - ch); }
    else { x = s.x + s.R + 18; if (x + cw > w - 16) x = s.x - s.R - 18 - cw; y = clamp(s.y - ch / 2, 84, h - ch - 120); }
    el.card.style.transform = `translate(${Math.round(clamp(x, 16, Math.max(16, w - cw - 16)))}px,${Math.round(y)}px)`;
  }
  function hideCard() {
    if (!el.card) return;
    el.card.hidden = true;
    S.cardFor = -1;
  }

  // ---- Keyboard and spatial navigation ------------------------------------------------
  let liveT = 0;
  function announce(t) { clearTimeout(liveT); liveT = setTimeout(() => { const l = $("#sp-live"); if (l) l.textContent = t; }, 60); }
  function describe(it) {
    if (it.type === "idea") return `Idea: ${it.title}. Enter opens its Moment.`;
    const who = it.people.length ? "with " + it.people.map((p) => p.name).join(" and ") : "on your own";
    return `${M.KIND_LABEL[it.kind]}, ${fmtWhen(it)}, ${who}, ${M.SOURCE_LABEL[it.source] || ""}. ${it.title || M.excerpt(it.excerpt, 140)}. Enter opens it.`;
  }
  function setFocus(i, speak = true) {
    S.focus = i;
    if (i >= 0 && speak) announce(describe(items[i]));
  }
  function focusItem(i, fly, zoom) {
    const it = items[i];
    if (!it) return;
    setFocus(i);
    if (fly) flyTo(it.x, it.y, zoom || (it.type === "idea" ? Math.max(S.cam.z, 1.6) : Math.max(S.cam.z, 2.6)), 1000);
    setTimeout(() => { if (S.focus === i && running) showCard(i, "ontouchstart" in window || matchMedia("(pointer:coarse)").matches); }, fly && !reduced ? 1020 : 10);
  }
  function ensureVisible(i) {
    const s = screen[i], { w, h } = S.view, mx = w * 0.18, my = h * 0.2;
    if (s.x < mx || s.x > w - mx || s.y < my + 40 || s.y > h - my - 60) flyTo(items[i].x, items[i].y, S.cam.z, reduced ? 0 : 520);
  }
  function spatialNext(dx, dy) {
    const pool = items.filter((it, i) => anim[i].dim < 0.5);
    if (S.focus < 0) {
      let best = null, bd = 1e9;
      pool.forEach((it) => { const s = screen[it.idx], d = Math.hypot(s.x - S.view.w / 2, s.y - S.view.h / 2); if (d < bd) { bd = d; best = it; } });
      return best;
    }
    const f = items[S.focus];
    let best = null, bs = 1e9;
    pool.forEach((it) => {
      if (it === f) return;
      const vx = it.x - f.x, vy = it.y - f.y, along = vx * dx + vy * dy;
      if (along <= 4) return;
      const sc = along + Math.abs(vx * dy - vy * dx) * 2.4;
      if (sc < bs) { bs = sc; best = it; }
    });
    return best;
  }
  function onStageKey(e) {
    if (S.dive) return;
    const k = e.key, pan = 80 / S.cam.z;
    const dirs = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] };
    if (dirs[k]) {
      e.preventDefault();
      const [dx, dy] = dirs[k];
      if (e.shiftKey) { flyTo(S.cam.x + dx * pan * 2, S.cam.y + dy * pan * 2, S.cam.z, reduced ? 0 : 260); return; }
      const n = spatialNext(dx, dy);
      if (n) { setFocus(n.idx); ensureVisible(n.idx); setTimeout(() => { if (S.focus === n.idx) showCard(n.idx, false); }, reduced ? 0 : 540); }
      return;
    }
    if (k === "+" || k === "=") { e.preventDefault(); flyTo(S.cam.x, S.cam.y, S.cam.z * 1.6, reduced ? 0 : 320); return; }
    if (k === "-" || k === "_") { e.preventDefault(); flyTo(S.cam.x, S.cam.y, S.cam.z / 1.6, reduced ? 0 : 320); return; }
    if (k === "0") { const h = homeCam(); flyTo(h.x, h.y, h.z); return; }
    if ((k === "Enter" || k === " ") && S.focus >= 0) { e.preventDefault(); activate(S.focus); return; }
    if (k === "Escape") { if (S.focus >= 0) { setFocus(-1); hideCard(); } else if (S.query || S.lens || S.follow) clearFilters(); }
  }

  // ---- The dive: into the bubble, then the Moment view takes over ----------------------
  function startDive(i, suffix = "") {
    const it = items[i];
    hideCard();
    setFocus(i, false);
    const target = (Math.hypot(S.view.w, S.view.h) * 0.62) / it.r;
    S.dive = { i, dir: 1, t0: performance.now(), dur: reduced ? 380 : 1500, from: { ...S.cam }, target, shown: false, suffix };
    S.diveId = i;
    S.diveW = [it.x, it.y];
    S.fly = null;
    S.vel.x = S.vel.y = 0;
    root.classList.add("diving");
    announce(`Opening ${it.title || M.excerpt(it.excerpt, 60)}.`);
  }
  function stepDive(now) {
    const d = S.dive;
    if (!d) return;
    const p = clamp((now - d.t0) / d.dur, 0, 1), it = items[d.i];
    if (d.dir === 1) {
      const pc = 1 - Math.pow(1 - clamp(p * 1.7, 0, 1), 3);
      S.cam.x = lerp(d.from.x, it.x, pc);
      S.cam.y = lerp(d.from.y, it.y, pc);
      S.cam.z = Math.exp(lerp(Math.log(d.from.z), Math.log(d.target), reduced ? p : Math.pow(p, 2.2)));
      S.divePhase = reduced ? Math.min(p, 0.6) : smooth(0.05, 0.95, p);
      if (p > (reduced ? 0.6 : 0.82) && !d.shown) {
        d.shown = true;
        // Hand-off: the Moment view takes over from inside the glass.
        location.hash = `#m/${it.id}${d.suffix}`;
      }
    } else {
      const e = easeIO(p);
      S.cam.x = lerp(d.from.x, d.saved.x, e);
      S.cam.y = lerp(d.from.y, d.saved.y, e);
      S.cam.z = Math.exp(lerp(Math.log(d.from.z), Math.log(d.saved.z), e));
      S.divePhase = lerp(d.rise ? (reduced ? 0 : 0.8) : 1, 0, e);
      if (p >= 1) {
        S.dive = null;
        S.divePhase = 0;
        S.diveId = -1;
        root.classList.remove("diving");
        if (d.rise) { setFocus(d.i); showCard(d.i, true); }
      }
    }
  }

  // ---- List alternative, time control ------------------------------------------------------
  function buildList() {
    const body = $("#sp-list-body");
    let h = `<p style="margin:0;color:var(--ink-dim);font-size:13.5px;line-height:1.5">The same memories as the map, by region. Choosing one shows it on the map.</p>`;
    if (threads.length) h += `<h3>Threads</h3><ul>${threads.map((T) => `<li data-thread-li="${esc(T.id)}"><button type="button" data-thread="${esc(T.id)}">${T.title ? esc(T.title) : "<em>Unnamed thread</em>"}</button><span class="m">${T.members.length} moments</span></li>`).join("")}</ul>`;
    for (const R of [...regions].sort((a, b) => b.count - a.count)) {
      h += `<h3>${R.name ? esc(R.name) : `<span style="opacity:.6">Unnamed region</span>`} <span class="m" style="font-family:var(--font-mono);font-size:11px;font-style:normal">${R.count}</span></h3><ul>`;
      items.filter((it) => it.region === R.key && it.type === "moment").sort((a, b) => b.year - a.year).forEach((m) => {
        const p = M.localParts(m.at, m.timezone);
        h += `<li data-mid="${m.idx}"><button type="button" data-id="${m.idx}">${esc(m.title || M.excerpt(m.excerpt, 70) || M.KIND_LABEL[m.kind])}</button><span class="m">${esc(KIND_SHORT[m.kind].toLowerCase())} · ${p ? p.year : ""}</span></li>`;
      });
      h += "</ul>";
    }
    body.innerHTML = h;
    body.querySelectorAll("[data-id]").forEach((b) => b.addEventListener("click", () => { toggleList(false); focusItem(+b.dataset.id, true); el.stage.focus({ preventScroll: true }); }));
    body.querySelectorAll("[data-thread]").forEach((b) => b.addEventListener("click", () => { toggleList(false); startFollow(b.dataset.thread); el.stage.focus({ preventScroll: true }); }));
  }
  function filterList() {
    const any = S.query.trim() || S.lens || S.follow;
    root.querySelectorAll("#sp-list-body li[data-mid]").forEach((li) => { li.hidden = !!any && !(anim[+li.dataset.mid] && anim[+li.dataset.mid].tl); });
  }
  function toggleList(force) {
    const l = $("#sp-list"), open = force === undefined ? l.hidden : force;
    l.hidden = !open;
    $("#sp-listBtn").setAttribute("aria-expanded", String(open));
    if (open) { hideCard(); $("#sp-listClose").focus(); } else $("#sp-listBtn").focus({ preventScroll: true });
  }
  function setTimeRead() {
    const r = $("#sp-time-read");
    r.innerHTML = S.depthOnT < 0.5 ? "<em>All years</em> level" : S.surfaceT > S.yearMax - 0.25 ? "<em>Now</em> on top" : `<em>${Math.floor(S.surfaceT)}</em> surfaces`;
  }
  function drawSpark() {
    const c = $("#sp-spark");
    if (!c || !items.length) return;
    const w = c.clientWidth, h = c.clientHeight;
    if (!w) return;
    const dpr = Math.min(devicePixelRatio || 1, 2);
    c.width = w * dpr;
    c.height = h * dpr;
    const x = c.getContext("2d");
    x.scale(dpr, dpr);
    x.clearRect(0, 0, w, h);
    const cs = getComputedStyle(root), faint = cs.getPropertyValue("--ink-faint").trim(), acc = cs.getPropertyValue("--accent").trim();
    const any = !!(S.query.trim() || S.lens || S.follow), pad = 9;
    items.forEach((m, i) => {
      if (m.type !== "moment") return;
      const px = pad + ((m.year - S.yearMin) / (S.yearMax - S.yearMin)) * (w - pad * 2), on = any && anim[i].tl > 0;
      x.globalAlpha = on ? 1 : any ? 0.25 : 0.5;
      x.fillStyle = on ? acc : faint;
      const th = m.dream ? h - 2 : h * 0.62;
      x.fillRect(Math.round(px), h - th, 1.2, th);
    });
    x.globalAlpha = 1;
  }
  function updateGauge() {
    const btns = [...root.querySelectorAll(".gauge button")];
    if (!btns[0] || !btns[0].offsetHeight) return;
    const ys = btns.map((b) => b.offsetTop + b.offsetHeight / 2);
    const lz = Math.log(S.cam.z), lv = LEVELS.map(Math.log);
    let y;
    if (lz <= lv[0]) y = ys[2]; else if (lz >= lv[2]) y = ys[0]; else if (lz < lv[1]) y = lerp(ys[2], ys[1], (lz - lv[0]) / (lv[1] - lv[0])); else y = lerp(ys[1], ys[0], (lz - lv[1]) / (lv[2] - lv[1]));
    $("#sp-gmark").style.top = y + "px";
    const lvl = S.cam.z < 0.75 ? 0 : S.cam.z < 2 ? 1 : 2;
    btns.forEach((b) => b.setAttribute("aria-current", String(+b.dataset.level === lvl)));
  }

  // ---- Frame loop ------------------------------------------------------------------------
  function resize() {
    S.view.w = innerWidth;
    S.view.h = innerHeight;
    const dpr = Math.min(devicePixelRatio || 1, 2);
    el.canvas.width = Math.round(S.view.w * dpr);
    el.canvas.height = Math.round(S.view.h * dpr);
    if (gl && fboTex) sizeFBO();
    drawSpark();
  }
  let lastNow = performance.now();
  function frame(now) {
    if (!running) return;
    const dt = Math.min(50, now - lastNow);
    lastNow = now;
    S.time = (now - S.t0) / 1000;
    stepFly(now);
    stepDive(now);
    if (!S.fly && !S.dive && !pointers.size) {
      if (Math.abs(S.vel.x) + Math.abs(S.vel.y) > 0.0005) { S.cam.x += (S.vel.x * dt) / 16; S.cam.y += (S.vel.y * dt) / 16; const k = Math.pow(0.93, dt / 16); S.vel.x *= k; S.vel.y *= k; }
      else { S.vel.x = S.vel.y = 0; maybeDrift(); }
      clampCam();
    }
    const ek = 1 - Math.pow(0.88, dt / 16);
    S.surface = lerp(S.surface, S.surfaceT, reduced ? 1 : ek);
    S.depthOn = lerp(S.depthOn, S.depthOnT, reduced ? 1 : ek);
    const ak = reduced ? 1 : 1 - Math.pow(0.82, dt / 16);
    for (let i = 0; i < items.length; i++) {
      const a = anim[i], it = items[i];
      a.lit = lerp(a.lit, a.tl, ak); a.dim = lerp(a.dim, a.td, ak);
      a.sel = lerp(a.sel, i === S.focus || i === S.diveId ? 1 : 0, ak); a.hov = lerp(a.hov, i === S.hover ? 1 : 0, ak);
      const [x, y] = toScreen(it.x, it.y), R = screenR(it, i), s = screen[i];
      s.x = x; s.y = y; s.R = R; s.a = itemAlpha(it, i);
      s.vis = x > -R * 1.6 && x < S.view.w + R * 1.6 && y > -R * 1.6 && y < S.view.h + R * 1.6 && s.a > 0.02;
    }
    if (gl) renderGL();
    else if (ctx2) { if (Math.abs(S.surface - lastOrderSurface) > 0.2 || Math.abs(S.depthOn - lastOrderDepth) > 0.2) computeOrder(); render2D(); }
    if (items.length) { updateLabels(); updateGauge(); placeCard(); }
    raf = requestAnimationFrame(frame);
  }

  function open(opts) {
    api = opts.api;
    if (!root) root = opts.root;
    if (!built) build();
    if (running) {
      if (opts.focus && byId.has(opts.focus)) focusItem(byId.get(opts.focus), true, 2.6);
      return;
    }
    running = true;
    root.classList.remove("diving");
    S.dive = null; S.divePhase = 0; S.diveId = -1; S.fly = null;
    hideCard();
    $("#sp-list").hidden = true;
    resize();
    readTheme();
    setTimeRead();
    lastNow = performance.now();
    raf = requestAnimationFrame(frame);
    load(opts.focus);
  }
  function close() {
    running = false;
    cancelAnimationFrame(raf);
    hideCard();
    pointers.clear();
  }
  document.addEventListener("visibilitychange", () => { lastNow = performance.now(); });

  globalThis.CommonplaceSpace = { open, close, get state() { return { S, items, regions, threads, gl: !!gl }; }, focusItem: (id) => focusItem(byId.get(id), true), startFollow, setLens, startDive: (id) => startDive(byId.get(id)) };
})();
