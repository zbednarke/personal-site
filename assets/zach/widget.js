// Embeddable ZACH spherical-harmonics widget.
//
// Usage:
//   <script src="data.js"></script>
//   <script src="sh.js"></script>
//   <script src="widget.js"></script>
//   const zh = ZachHarmonics.mount(container, { mode: "scroll", track: section });
//
// Modes:
//   "scroll" — l_max follows the scroll progress of `track` (an element,
//              typically a tall wrapper with the container sticky inside).
//   "auto"   — self-running sweep loop.
//   "manual" — nothing moves until you call .setL(l).
//
// Renderers:
//   "canvas" (default) — all dots drawn into one 2D canvas per frame.
//     One compositor layer, cheap on integrated GPUs.
//   "dom" — every dot is a CSS-3D-transformed div. The original novelty
//     renderer; heavy (thousands of composited layers), kept for demos.
//
// Returns { setL, getL, verifyError, destroy }.
(() => {
  "use strict";

  const DEFAULTS = {
    mode: "auto",
    renderer: "canvas",
    radius: 250,        // sphere radius, px (at scale 1)
    dot: 8,             // dot diameter, px (at scale 1)
    disp: 80,           // radial displacement per unit f, px
    gamma: 1.7,         // easing exponent progress -> degree
    tilt: 10,           // base camera pitch, deg
    yawRange: [-14, 0], // camera yaw drift across progress; ends face-on
    sweepSeconds: 11,   // auto mode
    holdSeconds: 2.5,   // auto mode
    vmin: -0.25,
    vmax: 1.15,
    perspective: 2200,
    drag: true,
    smooth: 0.14,       // per-frame lerp toward the target degree
  };

  const REDUCED_MOTION =
    typeof matchMedia === "function" &&
    matchMedia("(prefers-reduced-motion: reduce)").matches;

  const DEG = Math.PI / 180;

  function mount(container, opts = {}) {
    const o = { ...DEFAULTS, ...opts };
    const data = o.data || globalThis.ZACH_DATA;
    const SH = globalThis.SH;
    if (!data || !SH) throw new Error("ZachHarmonics: load data.js and sh.js first");

    const { thetas, phis } = data.patch;
    const LMAX = data.lmax;
    const nTh = thetas.length, nPh = phis.length, N = nTh * nPh;

    // -- verified synthesis --------------------------------------------------
    const fields = SH.buildFields(data.coeffs, LMAX, thetas, phis);
    const verifyError = SH.selfTest(data, fields);

    // -- per-dot geometry: lat/lon and their trig -----------------------------
    // CSS conventions: x right, y down, z toward viewer. A dot at (lat, lon)
    // sits at Ry(lon) Rx(lat) (0, 0, r):
    //   x = r cos(lat) sin(lon), y = -r sin(lat), z = r cos(lat) cos(lon)
    const sinLat = new Float64Array(N), cosLat = new Float64Array(N);
    const sinLon = new Float64Array(N), cosLon = new Float64Array(N);
    for (let i = 0; i < nTh; i++) {
      const lat = Math.PI / 2 - thetas[i];
      for (let j = 0; j < nPh; j++) {
        const k = i * nPh + j;
        const lon = phis[j] - Math.PI;
        sinLat[k] = Math.sin(lat); cosLat[k] = Math.cos(lat);
        sinLon[k] = Math.sin(lon); cosLon[k] = Math.cos(lon);
      }
    }

    // -- scene ----------------------------------------------------------------
    const scene = document.createElement("div");
    scene.style.cssText =
      "position:absolute;inset:0;overflow:hidden;touch-action:pan-y;";
    if (getComputedStyle(container).position === "static") {
      container.style.position = "relative";
    }
    container.appendChild(scene);

    const lut = data.magma.map(([r, g, b]) => `rgb(${r},${g},${b})`);

    let scale = 1;
    function computeScale() {
      const w = container.clientWidth || window.innerWidth;
      scale = Math.max(0.42, Math.min(1.1, w / 860));
    }

    // -- field evaluation with fractional degree -------------------------------
    const buf = new Float64Array(N);
    function fieldAt(lCont) {
      const L = Math.min(Math.floor(lCont), LMAX);
      const frac = lCont - L;
      const cum = fields.cum[L];
      if (frac > 1e-9 && L + 1 <= LMAX) {
        const nxt = fields.deg[L + 1];
        for (let k = 0; k < N; k++) buf[k] = cum[k] + frac * nxt[k];
      } else {
        buf.set(cum);
      }
      return buf;
    }

    // -- renderers --------------------------------------------------------------
    let paintScene; // (f, pitchDeg, yawDeg) => void
    let teardownRenderer = () => {};

    if (o.renderer === "canvas") {
      const canvas = document.createElement("canvas");
      canvas.style.cssText = "position:absolute;inset:0;width:100%;height:100%;";
      scene.appendChild(canvas);
      const ctx = canvas.getContext("2d");
      const order = new Int32Array(N);
      const zbuf = new Float64Array(N);
      const px = new Float64Array(N), py = new Float64Array(N);
      const ps = new Float64Array(N);

      let W = 0, H = 0, DPR = 1;
      function fit() {
        computeScale();
        DPR = Math.min(window.devicePixelRatio || 1, 2);
        W = scene.clientWidth; H = scene.clientHeight;
        canvas.width = Math.round(W * DPR);
        canvas.height = Math.round(H * DPR);
      }
      fit();
      window.addEventListener("resize", fit, { passive: true });
      teardownRenderer = () => window.removeEventListener("resize", fit);

      paintScene = (f, pitchDeg, yawDeg) => {
        const P = o.perspective;
        const cp = Math.cos(pitchDeg * DEG), sp = Math.sin(pitchDeg * DEG);
        const cy = Math.cos(yawDeg * DEG), sy = Math.sin(yawDeg * DEG);
        const R = o.radius * scale, D = o.disp * scale;
        const dotR = (o.dot / 2) * scale;

        for (let k = 0; k < N; k++) {
          const r = R + D * f[k];
          // dot position in globe frame
          let x = r * cosLat[k] * sinLon[k];
          let y = -r * sinLat[k];
          let z = r * cosLat[k] * cosLon[k];
          // globe rotation: Rx(pitch) then its parent Ry(yaw)... CSS order
          // rotateX(pitch) rotateY(yaw) means yaw is applied to the point
          // first, then pitch.
          const x1 = x * cy + z * sy;
          const z1 = -x * sy + z * cy;
          const y1 = y * cp - z1 * sp;
          const z2 = y * sp + z1 * cp;
          const w = P / (P - z2);
          px[k] = x1 * w; py[k] = y1 * w; ps[k] = w;
          zbuf[k] = z2;
          order[k] = k;
        }
        // painter's order: far to near
        order.sort((a, b) => zbuf[a] - zbuf[b]);

        ctx.setTransform(DPR, 0, 0, DPR, (W / 2) * DPR, (H / 2) * DPR);
        ctx.clearRect(-W / 2, -H / 2, W, H);
        const TWO_PI = 2 * Math.PI;
        const span = o.vmax - o.vmin;
        for (let n = 0; n < N; n++) {
          const k = order[n];
          const v = f[k];
          let c = ((v - o.vmin) / span) * 255;
          c = c < 0 ? 0 : c > 255 ? 255 : Math.round(c);
          const s = (0.75 + 0.5 * Math.max(0, Math.min(1, v))) * dotR * ps[k];
          ctx.fillStyle = lut[c];
          ctx.beginPath();
          ctx.arc(px[k], py[k], s, 0, TWO_PI);
          ctx.fill();
        }
      };
    } else {
      // ---- DOM renderer (original) ----
      scene.style.perspective = o.perspective + "px";
      scene.style.display = "flex";
      scene.style.alignItems = "center";
      scene.style.justifyContent = "center";
      const scaler = document.createElement("div");
      scaler.style.cssText =
        "position:relative;width:0;height:0;transform-style:preserve-3d;";
      const globe = document.createElement("div");
      globe.style.cssText =
        "position:relative;width:0;height:0;transform-style:preserve-3d;";
      scaler.appendChild(globe);
      scene.appendChild(scaler);

      function fit() {
        computeScale();
        scaler.style.transform = `scale(${scale.toFixed(3)})`;
      }
      fit();
      window.addEventListener("resize", fit, { passive: true });
      teardownRenderer = () => window.removeEventListener("resize", fit);

      const pre = new Array(N);
      const dots = new Array(N);
      const lastZ = new Float64Array(N).fill(1e9);
      const lastC = new Int16Array(N).fill(-1);
      const frag = document.createDocumentFragment();
      for (let i = 0; i < nTh; i++) {
        const lat = Math.PI / 2 - thetas[i];
        for (let j = 0; j < nPh; j++) {
          const k = i * nPh + j;
          const lon = phis[j] - Math.PI;
          const el = document.createElement("div");
          el.style.cssText =
            `position:absolute;left:0;top:0;width:${o.dot}px;height:${o.dot}px;` +
            `margin:${-o.dot / 2}px 0 0 ${-o.dot / 2}px;border-radius:50%;` +
            "will-change:transform;backface-visibility:hidden;pointer-events:none;";
          pre[k] =
            `rotateY(${lon.toFixed(5)}rad) rotateX(${lat.toFixed(5)}rad) translateZ(`;
          dots[k] = el;
          frag.appendChild(el);
        }
      }
      globe.appendChild(frag);

      paintScene = (f, pitchDeg, yawDeg) => {
        globe.style.transform =
          `rotateX(${pitchDeg.toFixed(2)}deg) rotateY(${yawDeg.toFixed(2)}deg)`;
        for (let k = 0; k < N; k++) {
          const v = f[k];
          const z = o.radius + o.disp * v;
          if (Math.abs(z - lastZ[k]) > 0.6) {
            lastZ[k] = z;
            const s = 0.75 + 0.5 * Math.max(0, Math.min(1, v));
            dots[k].style.transform =
              pre[k] + z.toFixed(1) + "px) scale(" + s.toFixed(2) + ")";
          }
          let c = Math.round(((v - o.vmin) / (o.vmax - o.vmin)) * 255);
          c = c < 0 ? 0 : c > 255 ? 255 : c;
          if (Math.abs(c - lastC[k]) >= 3) {
            lastC[k] = c;
            dots[k].style.background = lut[c];
          }
        }
      };
    }

    // -- interaction ------------------------------------------------------------
    let camX = 0, camY = 0, dragging = false, pxl = 0, pyl = 0;
    const onDown = (e) => {
      if (!o.drag || e.pointerType === "touch") return;
      dragging = true; pxl = e.clientX; pyl = e.clientY;
      scene.setPointerCapture(e.pointerId);
    };
    const onMove = (e) => {
      if (!dragging) return;
      camY += (e.clientX - pxl) * 0.15;
      camX -= (e.clientY - pyl) * 0.15;
      camX = Math.max(-40, Math.min(40, camX));
      camY = Math.max(-60, Math.min(60, camY));
      pxl = e.clientX; pyl = e.clientY;
    };
    const onUp = () => (dragging = false);
    scene.addEventListener("pointerdown", onDown);
    scene.addEventListener("pointermove", onMove);
    scene.addEventListener("pointerup", onUp);

    // -- visibility: do nothing while the stage is off-screen --------------------
    let visible = true;
    let observer = null;
    if (typeof IntersectionObserver === "function") {
      observer = new IntersectionObserver(
        (entries) => { visible = entries[0].isIntersecting; },
        { threshold: 0 }
      );
      observer.observe(container);
    }

    // -- degree control -----------------------------------------------------------
    let lTarget = o.mode === "auto" ? 0 : null;
    let lNow = -1;               // forces first paint
    let progress = 0;            // 0..1, drives yaw drift in scroll mode
    let tClock = 0, lastTs = null, alive = true;
    let needsPaint = true;
    let lastPose = "";

    const track = o.track || container.parentElement;
    function readScroll() {
      const r = track.getBoundingClientRect();
      const vh = window.innerHeight;
      const span = r.height - vh;
      progress = span > 1 ? Math.min(1, Math.max(0, -r.top / span)) : 1;
      lTarget = LMAX * Math.pow(progress, o.gamma);
    }
    if (o.mode === "scroll") {
      readScroll();
      window.addEventListener("scroll", readScroll, { passive: true });
      window.addEventListener("resize", readScroll, { passive: true });
    }

    function frame(ts) {
      if (!alive) return;
      if (lastTs === null) lastTs = ts;
      const dt = Math.min((ts - lastTs) / 1000, 0.1);
      lastTs = ts;

      if (o.mode === "auto" && visible) {
        tClock = (tClock + dt) % (o.sweepSeconds + o.holdSeconds);
        const t = Math.min(tClock / o.sweepSeconds, 1);
        progress = t;
        lTarget = LMAX * Math.pow(t, o.gamma);
      }

      if (visible) {
        if (lTarget !== null && lTarget !== lNow) {
          lNow =
            Math.abs(lTarget - lNow) < 0.02 || lNow < 0
              ? lTarget
              : lNow + (lTarget - lNow) * o.smooth;
          needsPaint = true;
          if (o.onProgress) o.onProgress(lNow, progress);
        }
        const yawDrift =
          o.yawRange[0] + (o.yawRange[1] - o.yawRange[0]) * progress;
        const sway = dragging || REDUCED_MOTION ? 0 : Math.sin(ts / 4200) * 3;
        const pitch = o.tilt + camX;
        const yaw = camY + yawDrift + sway;
        const pose = pitch.toFixed(2) + "," + yaw.toFixed(2);
        if (needsPaint || pose !== lastPose) {
          lastPose = pose;
          needsPaint = false;
          paintScene(fieldAt(lNow < 0 ? 0 : lNow), pitch, yaw);
        }
      }
      requestAnimationFrame(frame);
    }
    requestAnimationFrame(frame);

    return {
      verifyError,
      setL(l) { lTarget = Math.max(0, Math.min(LMAX, l)); },
      getL() { return lNow; },
      destroy() {
        alive = false;
        window.removeEventListener("scroll", readScroll);
        window.removeEventListener("resize", readScroll);
        teardownRenderer();
        if (observer) observer.disconnect();
        scene.remove();
      },
    };
  }

  globalThis.ZachHarmonics = { mount };
})();
