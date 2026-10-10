const test = require("node:test"),
  assert = require("node:assert/strict"),
  SM = require("./space-model");

// Fictional sample data only.
function sample(n, opts = {}) {
  const people = [{ id: "p-a", name: "Sample Friend", special: !!opts.special }, { id: "p-b", name: "Example Cousin" }];
  const moments = [];
  for (let i = 0; i < n; i++) {
    const day = String(1 + (i % 28)).padStart(2, "0");
    moments.push({
      id: `m-${i}`, kind: i % 5 === 0 ? "dream" : "conversation", title: `Sample ${i}`, excerpt: `fixture words ${i}`,
      occurredAt: `${2020 + (i % 6)}-0${1 + (i % 9)}-${day}T0${i % 10}:00:00Z`, createdAt: "2026-01-01T00:00:00Z", timezone: "UTC",
      source: "text", people: i % 3 === 0 ? [] : [i % 3 === 1 ? "p-a" : "p-b"], senders: [], lines: i % 7, images: i % 2,
    });
  }
  return { moments, people, ideas: [], threads: [], regions: [] };
}

test("one Moment sits alone at the centre", () => {
  const lay = SM.layout(sample(1));
  assert.equal(lay.items.length, 1);
  assert.equal(lay.regions.length, 1);
  assert.equal(lay.regions[0].name, "", "regions stay nameless until the owner names them");
  assert.deepEqual([Math.round(lay.items[0].x), Math.round(lay.items[0].y)], [0, 0]);
});

test("layout is deterministic and keyed by person, then thread, then kind", () => {
  const data = sample(30);
  data.threads = [{ id: "t-1", title: "", knots: [{ momentId: "m-0", at: "2020-01-01T00:00:00Z" }, { momentId: "m-1", at: "2021-01-01T00:00:00Z" }] }];
  data.regions = [{ key: "person:p-a", name: "Owner-given name" }];
  const a = SM.layout(data), b = SM.layout(JSON.parse(JSON.stringify(data)));
  assert.deepEqual(a.items.map((i) => [i.x, i.y]), b.items.map((i) => [i.x, i.y]));
  const keys = a.regions.map((r) => r.key).sort();
  assert.deepEqual(keys, ["kind:conversation", "kind:dream", "person:p-a", "person:p-b", "thread:t-1"]);
  assert.equal(a.regions.find((r) => r.key === "person:p-a").name, "Owner-given name");
  assert.equal(a.threads.length, 1);
  assert.equal(a.threads[0].title, "");
  assert.ok(a.threads[0].pts.length > 10 && a.threads[0].len > 0);
});

test("bubbles do not overlap after relaxation and Ideas orbit their Moment", () => {
  const data = sample(40);
  data.ideas = [{ id: "i-1", momentId: "m-4", title: "A fixture idea", state: "pencil" }, { id: "i-2", momentId: "m-4", title: "Another", state: "ink" }];
  const lay = SM.layout(data);
  let worst = 0;
  for (let i = 0; i < lay.items.length; i++)
    for (let j = i + 1; j < lay.items.length; j++) {
      const a = lay.items[i], b = lay.items[j];
      worst = Math.max(worst, a.r + b.r - Math.hypot(a.x - b.x, a.y - b.y));
    }
  assert.ok(worst < 4, `overlap ${worst}`);
  const m = lay.items.find((i) => i.id === "m-4"), idea = lay.items.find((i) => i.id === "i-1");
  assert.ok(Math.hypot(m.x - idea.x, m.y - idea.y) < 110);
  assert.equal(idea.type, "idea");
});

test("special people and dreams are flagged for their own look", () => {
  const lay = SM.layout(sample(6, { special: true }));
  assert.ok(lay.items.some((i) => i.special));
  assert.ok(lay.items.find((i) => i.id === "m-0").dream);
  assert.ok(lay.regions.find((r) => r.key === "person:p-a").special);
});

test("home camera: phones open on the newest memory, desktops on the whole map", () => {
  const lay = SM.layout(sample(12));
  const phone = SM.homeCamera(lay, { w: 390, h: 844 }, true);
  const newest = lay.items.filter((i) => i.type === "moment").sort((a, b) => b.year - a.year)[0];
  assert.equal(phone.focus, newest.id);
  const desk = SM.homeCamera(lay, { w: 1440, h: 900 }, false);
  assert.equal(desk.focus, "");
  assert.ok(desk.z <= 1.6 && desk.z > 0.1);
});

test("scales to thousands of Moments", () => {
  const t0 = Date.now();
  const lay = SM.layout(sample(3000));
  assert.equal(lay.items.length, 3000);
  assert.ok(Date.now() - t0 < 4000, `layout took ${Date.now() - t0} ms`);
  assert.ok(Number.isFinite(lay.bounds.x0) && Number.isFinite(lay.bounds.y1));
});

test("lexical matching covers titles, words, people and years", () => {
  const lay = SM.layout(sample(4));
  const it = lay.items.find((i) => i.id === "m-1");
  assert.ok(SM.matches(it, ["sample", "friend"]));
  assert.ok(SM.matches(it, [String(Math.floor(it.year))]));
  assert.ok(!SM.matches(it, ["nowhere"]));
});
