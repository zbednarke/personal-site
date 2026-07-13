// Embeddable ZACH spherical-harmonics widget. Pure DOM/CSS rendering.
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
// Returns { setL, getL, verifyError, destroy }.
(() => {
  "use strict";

  const DEFAULTS = {
    mode: "auto",
    radius: 250,        // sphere radius, px
    dot: 8,             // dot diameter, px
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

    // -- scene DOM -----------------------------------------------------------
    const scene = document.createElement("div");
    scene.style.cssText =
      `position:absolute;inset:0;display:flex;align-items:center;` +
      `justify-content:center;perspective:${o.perspective}px;` +
      `overflow:hidden;touch-action:pan-y;`;
    const globe = document.createElement("div");
    globe.style.cssText =
      "position:relative;width:0;height:0;transform-style:preserve-3d;";
    scene.appendChild(globe);
    if (getComputedStyle(container).position === "static") {
      container.style.position = "relative";
    }
    container.appendChild(scene);

    const lut = data.magma.map(([r, g, b]) => `rgb(${r},${g},${b})`);
    const pre = new Array(N);
    const dots = new Array(N);
    const lastZ = new Float64Array(N).fill(1e9);
    const lastC = new Int16Array(N).fill(-1);
    {
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
          // CSS y-axis points down the screen, so +lat (north) needs +rotateX
          pre[k] =
            `rotateY(${lon.toFixed(5)}rad) rotateX(${lat.toFixed(5)}rad) translateZ(`;
          dots[k] = el;
          frag.appendChild(el);
        }
      }
      globe.appendChild(frag);
    }

    // -- painting --------------------------------------------------------------
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

    function paint(f) {
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
    }

    // -- interaction ------------------------------------------------------------
    let camX = 0, camY = 0, dragging = false, px = 0, py = 0;
    const onDown = (e) => {
      if (!o.drag || e.pointerType === "touch") return;
      dragging = true; px = e.clientX; py = e.clientY;
      scene.setPointerCapture(e.pointerId);
    };
    const onMove = (e) => {
      if (!dragging) return;
      camY += (e.clientX - px) * 0.15;
      camX -= (e.clientY - py) * 0.15;
      camX = Math.max(-40, Math.min(40, camX));
      camY = Math.max(-60, Math.min(60, camY));
      px = e.clientX; py = e.clientY;
    };
    const onUp = () => (dragging = false);
    scene.addEventListener("pointerdown", onDown);
    scene.addEventListener("pointermove", onMove);
    scene.addEventListener("pointerup", onUp);

    // -- degree control -----------------------------------------------------------
    let lTarget = o.mode === "auto" ? 0 : null;
    let lNow = -1;               // forces first paint
    let progress = 0;            // 0..1, drives yaw drift in scroll mode
    let tClock = 0, lastTs = null, frameNo = 0, alive = true;

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

      if (o.mode === "auto") {
        tClock = (tClock + dt) % (o.sweepSeconds + o.holdSeconds);
        const t = Math.min(tClock / o.sweepSeconds, 1);
        progress = t;
        lTarget = LMAX * Math.pow(t, o.gamma);
      }

      const yawDrift = o.yawRange[0] + (o.yawRange[1] - o.yawRange[0]) * progress;
      const sway = dragging ? 0 : Math.sin(ts / 4200) * 3;
      globe.style.transform =
        `rotateX(${(o.tilt + camX).toFixed(2)}deg) ` +
        `rotateY(${(camY + yawDrift + sway).toFixed(2)}deg)`;

      if (lTarget !== null && (frameNo++ & 1) === 0) {
        const next =
          Math.abs(lTarget - lNow) < 0.02
            ? lTarget
            : lNow < 0
              ? lTarget
              : lNow + (lTarget - lNow) * o.smooth;
        if (next !== lNow) {
          lNow = next;
          paint(fieldAt(lNow));
          if (o.onProgress) o.onProgress(lNow, progress);
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
        scene.remove();
      },
    };
  }

  globalThis.ZachHarmonics = { mount };
})();
