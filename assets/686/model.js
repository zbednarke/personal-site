import * as THREE from "./vendor/three.module.js";

const TAU = Math.PI * 2;
const mix = THREE.MathUtils.lerp;
const COLORS = {
  idle: new THREE.Color(0x308fff),
  listening: new THREE.Color(0x1ae4f0),
  thinking: new THREE.Color(0x976bff),
  speaking: new THREE.Color(0x78caff),
  error: new THREE.Color(0xff5c28),
};

function metal(color, roughness = .34) {
  return new THREE.MeshStandardMaterial({ color, roughness, metalness: .76, side: THREE.DoubleSide });
}

function part(parent, geometry, material, x = 0, y = 0, z = 0) {
  const mesh = new THREE.Mesh(geometry, material);
  mesh.position.set(x, y, z);
  parent.add(mesh);
  return mesh;
}

function block(parent, material, x, y, z, w, h, d) {
  return part(parent, new THREE.BoxGeometry(w, h, d), material, x, y, z);
}

function tube(parent, material, points, radius) {
  const curve = new THREE.CatmullRomCurve3(points);
  return part(parent, new THREE.TubeGeometry(curve, Math.max(6, points.length * 2), radius, 6, false), material);
}

function randomSequence(seed) {
  return () => {
    seed = (Math.imul(1664525, seed) + 1013904223) >>> 0;
    return seed / 4294967296;
  };
}

function paintedTexture(width, height, paint) {
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  paint(canvas.getContext("2d"));
  const texture = new THREE.CanvasTexture(canvas);
  texture.colorSpace = THREE.SRGBColorSpace;
  return texture;
}

// Long softboxes reflect across the curved armor as the monitor turns.
function studioLight() {
  const map = paintedTexture(512, 256, ctx => {
    const sky = ctx.createLinearGradient(0, 0, 0, 256);
    for (const [stop, color] of [[0, "#9fb3c0"], [.38, "#536679"], [.57, "#182735"], [1, "#212527"]]) {
      sky.addColorStop(stop, color);
    }
    ctx.fillStyle = sky;
    ctx.fillRect(0, 0, 512, 256);
    for (const [x, y, w, h, color] of [[15, 32, 118, 50, "#e3edfa"], [165, 65, 22, 110, "#badbff"], [319, 20, 105, 54, "#fff5dc"], [460, 106, 32, 75, "#849cae"]]) {
      ctx.fillStyle = color;
      ctx.fillRect(x, y, w, h);
    }
  });
  map.mapping = THREE.EquirectangularReflectionMapping;
  return map;
}

// Cross sections run along the axle joining the two recessed side ports.
const shoulder = [[0, 1.61], [.83, 1.61], [1.12, 1.57], [1.26, 1.53], [1.40, 1.45], [1.53, 1.30], [1.63, 1.08], [1.68, .87]];
function radiusAt(x) {
  x = Math.abs(x);
  for (let i = 1; i < shoulder.length; i++) {
    const [b, rb] = shoulder[i];
    if (x <= b) {
      const [a, ra] = shoulder[i - 1];
      return mix(ra, rb, (x - a) / (b - a));
    }
  }
  return .87;
}

function signedPower(x, p) {
  return Math.sign(x) * Math.pow(Math.abs(x), p);
}

const sectionPower = 2.42;
function onHull(x, angle) {
  const r = radiusAt(x);
  return new THREE.Vector3(x, r * signedPower(Math.sin(angle), 2 / sectionPower), r * signedPower(Math.cos(angle), 2 / sectionPower));
}

function frontDepth(x, y) {
  return Math.pow(Math.max(.002, Math.pow(radiusAt(x), sectionPower) - Math.pow(Math.abs(y), sectionPower)), 1 / sectionPower);
}

function frontAngle(x, y) {
  return Math.asin(signedPower(y / radiusAt(x), sectionPower / 2));
}

// Give each armor sheet a solid return at its perimeter. Separate sheets expose
// the darker chassis in their seams and cast a bevel highlight at the rim.
function panel(parent, material, edgeMaterial, across, along, surface) {
  const vertices = [];
  const faces = [];
  const edgeFaces = [];
  for (let j = 0; j <= along; j++) {
    for (let i = 0; i <= across; i++) {
      surface(i / across, j / along).toArray(vertices, vertices.length);
      if (i && j) {
        const d = j * (across + 1) + i;
        const a = d - across - 2;
        faces.push(a, a + 1, d, a, d, d - 1);
      }
    }
  }
  const perimeter = [];
  for (let i = 0; i < across; i++) perimeter.push(i);
  for (let j = 0; j < along; j++) perimeter.push(j * (across + 1) + across);
  for (let i = across; i > 0; i--) perimeter.push(along * (across + 1) + i);
  for (let j = along; j > 0; j--) perimeter.push(j * (across + 1));
  const edgeStart = vertices.length / 3;
  for (const index of perimeter) {
    const point = new THREE.Vector3().fromArray(vertices, index * 3);
    point.toArray(vertices, vertices.length);
    point.multiplyScalar(.986).toArray(vertices, vertices.length);
    point.multiplyScalar(.94).toArray(vertices, vertices.length);
  }
  for (let i = 0; i < perimeter.length; i++) {
    const a = edgeStart + 3 * i;
    const b = edgeStart + 3 * ((i + 1) % perimeter.length);
    edgeFaces.push(a, a + 1, b, a + 1, b + 1, b, a + 1, a + 2, b + 1, a + 2, b + 2, b + 1);
  }
  const geometry = new THREE.BufferGeometry();
  geometry.setAttribute("position", new THREE.Float32BufferAttribute(vertices, 3));
  geometry.setIndex([...faces, ...edgeFaces]);
  geometry.addGroup(0, faces.length, 0);
  geometry.addGroup(faces.length, edgeFaces.length, 1);
  geometry.computeVertexNormals();
  return part(parent, geometry, [material, edgeMaterial]);
}

function armor(root, m) {
  // The saddle bridges the face from left to right and wraps to the underside.
  for (const [left, right] of [[-1.12, -.415], [-.402, .402], [.415, 1.12]]) {
    for (const [start, end, finish] of [[0, .32, m.silver], [.325, .70, m.titanium], [.705, 1, m.silver]]) {
      panel(root, finish, m.edge, 7, 16, (u, v) => {
        const x = mix(left, right, u);
        const top = frontAngle(x, 1.315);
        const bottom = TAU + frontAngle(x, -1.475);
        return onHull(x, mix(top, bottom, mix(start, end, v)));
      });
    }
  }
  for (const sign of [-1, 1]) {
    // Each shoulder is a wraparound fairing, terminating in a deep circular bore.
    const seams = [-.98, -.14, .74, 1.61, 2.6, 3.58, 4.57, TAU - .98];
    for (const [inner, outer, finish] of [[1.13, 1.23, m.edge], [1.24, 1.675, m.silver]]) {
      for (let i = 1; i < seams.length; i++) {
        panel(root, finish, m.edge, 10, 15, (u, v) =>
          onHull(sign * mix(inner, outer, u), mix(seams[i - 1] + .012, seams[i] - .012, v)));
      }
    }
    // The opening has a rounded brow and an angular, open channel below the eye.
    const rimX = y => {
      if (y >= 0) return .79 * Math.sqrt(Math.max(0, 1 - (y / .88) ** 2));
      if (y > -.34) return .79;
      if (y > -.87) return mix(.79, .43, (-y - .34) / .53);
      return mix(.43, .27, (-y - .87) / .60);
    };
    for (const [low, high, finish] of [
      [-1.46, -.94, m.titanium], [-.922, -.64, m.edge], [-.62, -.445, m.silver],
      [-.43, -.265, m.dark], [-.25, .145, m.titanium], [.158, .86, m.silver],
      [.86, 1.25, m.silver],
    ]) {
      panel(root, finish, m.edge, 9, 20, (u, v) => {
        const y = mix(low, high, v);
        const x = sign * mix(rimX(y), 1.119, u);
        return new THREE.Vector3(x, y, frontDepth(x, y));
      });
    }
    // A raised horseshoe follows the opening, then breaks into separate jaw fins.
    for (const [start, end] of [[-.04, .41], [.422, 1.566]]) {
      panel(root, m.rim, m.edge, 4, 20, (u, v) => {
        const a = mix(start, end, v);
        const x = sign * (.79 + .15 * u) * Math.cos(a);
        const y = (.88 + .15 * u) * Math.sin(a);
        return new THREE.Vector3(x, y, frontDepth(x, y) + .032 + .025 * Math.sin(Math.PI * u));
      });
    }
    for (const [low, high, width, lift] of [[-.38, -.047, .15, .055], [-.88, -.405, .21, .09], [-1.41, -.91, .27, .115]]) {
      panel(root, m.rim, m.edge, 4, 8, (u, v) => {
        const y = mix(low, high, v);
        const x = sign * (rimX(y) + width * u);
        return new THREE.Vector3(x, y, frontDepth(x, y) + lift * (1 - .4 * u));
      });
    }
    // Recess the port's barrel behind its machined annular lip. The stationary
    // vertical rails remain visible through its mouth at a three-quarter angle.
    const port = new THREE.Group();
    port.rotation.y = sign * Math.PI / 2;
    root.add(port);
    function sleeve(outer, inner, front, back, material) {
      const profile = [[inner, back], [inner, front - .025], [inner + .03, front], [outer - .025, front], [outer, front - .025], [outer, back]];
      const mesh = part(port, new THREE.LatheGeometry(profile.map(([r, z]) => new THREE.Vector2(r, z)), 64), material);
      mesh.rotation.x = Math.PI / 2;
    }
    sleeve(.91, .731, 1.70, 1.46, m.rim);
    sleeve(.748, .665, 1.56, 1.33, m.dark);
    part(port, new THREE.CircleGeometry(.685, 48), m.dark, 0, 0, 1.326);
    for (const x of [-.50, -.23, 0, .23, .50]) {
      const height = 2 * Math.sqrt(.67 ** 2 - x ** 2);
      block(port, m.titanium, x, 0, 1.365, x === 0 ? .095 : .042, height, .065);
    }
    for (const angle of [.48, 2.64, 3.65, 5.79]) {
      part(port, new THREE.CircleGeometry(.023, 6), m.dark, .823 * Math.cos(angle), .823 * Math.sin(angle), 1.705);
    }
    // The slightly proud longitudinal runner is visible in silhouette.
    panel(root, m.rim, m.edge, 5, 12, (u, v) =>
      onHull(sign * mix(.80, 1.035, u), mix(1.21, 1.93, v)).multiplyScalar(1.025));
  }

  // Slotted heat sink behind the face and the open throat.
  block(root, m.dark, 0, -.16, .67, 1.94, 2.54, .36);
  for (let i = 0; i < 7; i++) {
    block(root, i % 2 ? m.dark : m.edge, 0, -.78 - i * .095, .95, .57 - i * .018, .052, .23);
  }
  for (const sign of [-1, 1]) {
    block(root, m.edge, sign * .65, -.08, .97, .09, 1.76, .29);
    for (const y of [-.62, -.42, .40, .60]) {
      block(root, m.titanium, sign * .61, y, 1.13, .18, .038, .13);
    }
    block(root, m.dark, sign * .58, -1.28, .72, .57, .44, .57);
  }
}

function irisTexture() {
  return paintedTexture(512, 512, ctx => {
    const rand = randomSequence(686);
    ctx.translate(256, 256);
    const gradient = ctx.createRadialGradient(-10, 12, 3, 0, 0, 255);
    for (const [stop, color] of [[0, "#ffffff"], [.16, "#f1ffff"], [.32, "#8ef5ff"], [.44, "#2dbefe"],
      [.49, "#1479be"], [.51, "#b3faff"], [.55, "#3bd7ff"], [.75, "#168bd4"], [.91, "#06519c"], [1, "#122e52"]]) gradient.addColorStop(stop, color);
    ctx.fillStyle = gradient;
    ctx.fillRect(-256, -256, 512, 512);
    // Cut crystal facets reflect in broad sectors beneath the finer iris filaments.
    for (let i = 0; i < 29; i++) {
      const a = i * TAU / 29;
      const b = a + TAU / 29 - .018;
      const r = 142 + 40 * rand();
      ctx.fillStyle = i % 3 ? "rgba(172,235,255,.17)" : "rgba(6,27,84,.34)";
      ctx.beginPath();
      ctx.moveTo(r * Math.cos(a), r * Math.sin(a));
      ctx.lineTo(234 * Math.cos(a + .035), 234 * Math.sin(a + .035));
      ctx.lineTo(239 * Math.cos(b), 239 * Math.sin(b));
      ctx.lineTo((r + 26) * Math.cos(b - .035), (r + 26) * Math.sin(b - .035));
      ctx.closePath();
      ctx.fill();
    }
    for (let i = 0; i < 420; i++) {
      const a = rand() * TAU;
      const r = 63 + rand() * 177;
      const length = 3 + rand() * 24;
      ctx.strokeStyle = i % 3 ? "rgba(187,248,255,.35)" : "rgba(3,51,126,.48)";
      ctx.lineWidth = .8 + rand() * 2;
      ctx.beginPath();
      ctx.moveTo(Math.cos(a) * r, Math.sin(a) * r);
      ctx.lineTo(Math.cos(a + .012) * (r + length), Math.sin(a + .012) * (r + length));
      ctx.stroke();
    }
  });
}

function optic(root, m) {
  const eye = new THREE.Group();
  eye.position.set(0, .015, 1.16);
  root.add(eye);
  const radius = .62;
  const depth = .34;
  const light = new THREE.MeshBasicMaterial({ map: irisTexture(), color: COLORS.idle, toneMapped: false });
  // The painted channels encode deep glass, colored refraction and the white
  // emitter separately, so the same core can glow through its full state palette.
  light.onBeforeCompile = shader => {
    shader.fragmentShader = shader.fragmentShader.replace("#include <map_fragment>", `
      vec3 lensLayers = texture2D(map, vMapUv).rgb;
      vec3 coloredGlass = diffuse * (0.075 * lensLayers.b + 1.25 * lensLayers.g);
      diffuseColor.rgb = mix(coloredGlass, vec3(1.0), lensLayers.r);
    `);
  };
  const glass = new THREE.SphereGeometry(radius, 56, 32);
  const p = glass.attributes.position;
  const uv = glass.attributes.uv;
  for (let i = 0; i < p.count; i++) uv.setXY(i, .5 + p.getX(i) / (2 * radius), .5 + p.getY(i) / (2 * radius));
  const lens = part(eye, glass, light);
  lens.scale.z = depth / radius;

  const cage = new THREE.Group();
  eye.add(cage);
  const ringRadius = .275;
  const onLens = (r, a) => new THREE.Vector3(r * Math.cos(a), r * Math.sin(a), depth * Math.sqrt(Math.max(0, 1 - (r / radius) ** 2)) + .007);
  const ring = part(cage, new THREE.TorusGeometry(ringRadius, .012, 8, 64), m.edge);
  ring.position.z = onLens(ringRadius, 0).z;
  for (const a of [.13, 2.27, 4.36]) {
    const points = [];
    for (let i = 0; i <= 12; i++) points.push(onLens(mix(ringRadius, radius, i / 12), a + .09 * Math.sin(i / 12 * Math.PI)));
    tube(cage, m.edge, points, .012);
  }
  const bezel = part(eye, new THREE.TorusGeometry(.628, .046, 10, 64), m.titanium);
  bezel.position.z = -.024;
  const glowMap = paintedTexture(128, 128, ctx => {
    const g = ctx.createRadialGradient(64, 64, 0, 64, 64, 64);
    for (const [stop, opacity] of [[0, .85], [.20, .36], [.45, .11], [1, 0]]) g.addColorStop(stop, 'rgba(255,255,255,' + opacity + ')');
    ctx.fillStyle = g;
    ctx.fillRect(0, 0, 128, 128);
  });
  const glow = part(eye, new THREE.PlaneGeometry(1, 1), new THREE.MeshBasicMaterial({ map: glowMap, color: COLORS.idle, blending: THREE.AdditiveBlending, transparent: true, depthWrite: false, toneMapped: false }));
  glow.position.z = depth + .019;
  glow.scale.setScalar(1.55);
  return { eye, light, cage, glow };
}

function identification(root) {
  const texture = paintedTexture(256, 80, ctx => {
    ctx.fillStyle = "#dda55d";
    ctx.fillRect(10, 16, 9, 49);
    ctx.fillRect(25, 16, 4, 49);
    ctx.font = "bold 58px monospace";
    ctx.fillStyle = "#182632";
    ctx.fillText("686", 42, 61);
  });
  const mark = part(root, new THREE.PlaneGeometry(.405, .127), new THREE.MeshBasicMaterial({ map: texture, transparent: true, depthWrite: false, polygonOffset: true, polygonOffsetFactor: -1 }));
  mark.position.set(-.50, 1.42, frontDepth(-.50, 1.42) + .012);
  mark.rotation.x = -Math.atan2(1.42 ** (sectionPower - 1), mark.position.z ** (sectionPower - 1));
}

export function createEmberModel(canvas) {
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true, powerPreference: "high-performance" });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 2));
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  renderer.toneMapping = THREE.ACESFilmicToneMapping;
  const scene = new THREE.Scene();
  scene.environment = studioLight();
  scene.add(new THREE.HemisphereLight(0xe5f2ff, 0x48413a, 1.18));
  for (const [color, power, x, y, z] of [[0xfff4da, 2.3, -3, 5, 4], [0x8dcbff, 1.4, -4, 1, -2], [0xb9dcff, 1.35, 4, 2, 2]]) {
    const lamp = new THREE.DirectionalLight(color, power);
    lamp.position.set(x, y, z);
    scene.add(lamp);
  }
  const camera = new THREE.PerspectiveCamera(33, 1, .1, 55);
  const root = new THREE.Group();
  root.rotation.order = "YXZ";
  root.rotation.set(.105, .51, -.025);
  scene.add(root);
  const m = { silver: metal(0xa8b0b5), titanium: metal(0x87919a, .37), rim: metal(0xb3bac0, .29), edge: metal(0x4a5661, .38), dark: metal(0x202a34, .53) };
  armor(root, m);
  identification(root);
  const { eye, light, cage, glow } = optic(root, m);

  const dust = new THREE.BufferGeometry();
  const rand = randomSequence(1686);
  const points = [];
  for (let i = 0; i < 150; i++) points.push((rand() - .5) * 17, (rand() - .5) * 13, -2 - 5 * rand());
  dust.setAttribute("position", new THREE.Float32BufferAttribute(points, 3));
  scene.add(new THREE.Points(dust, new THREE.PointsMaterial({ color: 0x72a2b6, size: .015, transparent: true, opacity: .43 })));

  let state = "idle";
  let input = 0;
  let level = 0;
  let pointerX = 0;
  let pointerY = 0;
  let hasPointer = false;
  let time = 0;
  let last = performance.now();
  let frame = 0;
  let width = 0;
  let height = 0;
  const motionPreference = window.matchMedia("(prefers-reduced-motion: reduce)");
  function animate(now) {
    const dt = Math.min(.06, (now - last) / 1000);
    last = now;
    time += dt;
    const w = Math.max(1, canvas.clientWidth);
    const h = Math.max(1, canvas.clientHeight);
    if (w !== width || h !== height) {
      width = w;
      height = h;
      renderer.setSize(w, h, false);
      camera.aspect = w / h;
      camera.position.z = 1 + 2.50 / (Math.tan(THREE.MathUtils.degToRad(16.5)) * Math.min(1, camera.aspect));
      camera.updateProjectionMatrix();
    }
    const motion = motionPreference.matches ? 0 : 1;
    const listen = state === "listening";
    const think = state === "thinking";
    const speak = state === "speaking";
    const damping = 1 - Math.exp(-dt * 3);
    level = mix(level, input, 1 - Math.exp(-dt * 11));
    const voice = speak || listen ? level : 0;
    const beat = speak ? .25 + .17 * Math.sin(8 * time) : 0;
    const scan = Math.tanh(2.5 * Math.sin(time * (think ? 1.25 : .43)));
    const gaze = hasPointer ? pointerX : scan * (think ? .85 : .35);
    const yaw = (listen ? .20 : .50) + motion * (.32 * gaze + (think ? .14 * scan : 0));
    const pitch = (listen ? -.035 : .105) + motion * (pointerY * .16 + .045 * Math.sin(1.1 * time) + (speak ? .11 * level : 0));
    root.rotation.y = mix(root.rotation.y, yaw, damping);
    root.rotation.x = mix(root.rotation.x, pitch, damping);
    root.rotation.z = mix(root.rotation.z, motion * ((listen ? -.105 : -.025) + (think ? .075 * scan : .025 * Math.sin(.76 * time))), damping);
    root.position.y = mix(root.position.y, motion * (.09 * Math.sin(1.37 * time) + (listen ? .09 : 0) + (speak ? .065 * level : 0)), damping);
    root.position.x = motion * .06 * Math.sin(.73 * time);
    root.position.z = mix(root.position.z, listen ? .15 * motion : 0, damping);
    eye.rotation.y = mix(eye.rotation.y, motion * .20 * gaze, damping);
    eye.rotation.x = mix(eye.rotation.x, motion * (.13 * pointerY + .03 * Math.cos(time)), damping);
    cage.rotation.z += motion * dt * (think ? .85 : speak ? .15 + .7 * level : .035);
    light.color.lerp(COLORS[state], damping);
    glow.material.color.copy(light.color);
    glow.material.opacity = .52 + .36 * voice + beat * .16;
    glow.scale.setScalar(1.5 + .28 * voice);
    renderer.render(scene, camera);
    frame = requestAnimationFrame(animate);
  }
  const onPointer = event => {
    const bounds = canvas.getBoundingClientRect();
    if (!bounds.width || !bounds.height) return;
    hasPointer = true;
    pointerX = 2 * (event.clientX - bounds.left) / bounds.width - 1;
    pointerY = 2 * (event.clientY - bounds.top) / bounds.height - 1;
  };
  const onLeave = () => { hasPointer = false; pointerX = 0; pointerY = 0; };
  canvas.addEventListener("pointermove", onPointer);
  canvas.addEventListener("pointerleave", onLeave);
  frame = requestAnimationFrame(animate);

  return {
    setState(next) {
      if (next !== state) input = 0;
      state = Object.hasOwn(COLORS, next) ? next : "idle";
    },
    setAudioLevel(value) { input = Number.isFinite(value) ? THREE.MathUtils.clamp(value, 0, 1) : 0; },
    dispose() {
      cancelAnimationFrame(frame);
      canvas.removeEventListener("pointermove", onPointer);
      canvas.removeEventListener("pointerleave", onLeave);
      const materials = new Set();
      const geometries = new Set();
      scene.traverse(object => {
        if (object.geometry) geometries.add(object.geometry);
        if (object.material) {
          for (const material of [].concat(object.material)) materials.add(material);
        }
      });
      for (const geometry of geometries) geometry.dispose();
      for (const material of materials) {
        material.map?.dispose();
        material.dispose();
      }
      scene.environment.dispose();
      renderer.dispose();
    },
  };
}
