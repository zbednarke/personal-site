const test = require("node:test"),
  assert = require("node:assert/strict"),
  M = require("./model");

// All sample data is fictional.

const thread = { id: "t1", title: "Sample", lastEventId: 3, running: false, spendUsd: 0.01 };
const ev = (id, type, payload) => ({ id, type, payload });

test("a thread the API refuses for good is marked so the sheet offers a fresh one", () => {
  const s = M.applySnapshot(M.createState(), { thread: { ...thread }, messages: [], approvals: [] });
  M.applyEvent(s, ev(4, "thread.updated", { stuck: "HTTP 400: fake" }));
  assert.equal(s.thread.stuck, "HTTP 400: fake");
  const t = M.applySnapshot(M.createState(), { thread: { ...thread }, messages: [], approvals: [] });
  M.applyEvent(t, ev(4, "run.finished", { status: "error", message: "Start a fresh thread", freshThread: true }));
  assert.ok(t.thread.stuck);
  assert.equal(t.noticeStatus, "error");
});

test("layout: bottom sheet on phones, split view on tablets, side panel on desktops", () => {
  assert.equal(M.layoutFor(390), "sheet");
  assert.equal(M.layoutFor(768), "split");
  assert.equal(M.layoutFor(1440), "panel");
  assert.equal(M.deviceKind(390, true), "phone");
  assert.equal(M.deviceKind(768, true), "tablet");
  assert.equal(M.deviceKind(1366, true), "tablet");
  assert.equal(M.deviceKind(1440, false), "desktop");
});

test("page context describes the page, view, selection and device in plain terms", () => {
  const c = M.pageContext({ pathname: "/jazz/", hash: "#practice", title: "Jazz", selection: "x".repeat(3000), width: 390, height: 844, coarse: true, now: new Date(2026, 0, 2, 23, 30), timezone: "America/Los_Angeles" });
  assert.equal(c.page, "/jazz/");
  assert.equal(c.view, "practice");
  assert.equal(c.device, "phone");
  assert.equal(c.viewport, "390x844");
  assert.equal(c.pointer, "touch");
  assert.equal(c.localDate, "2026-01-02");
  assert.ok(c.selection.length <= 1500);
  const hooked = M.pageContext({ pathname: "/jazz/", width: 1440, height: 900, hook: () => ({ view: "repertoire", app: "Sample: 23 min logged today" }) });
  assert.equal(hooked.view, "repertoire");
  assert.equal(hooked.app, "Sample: 23 min logged today");
  assert.equal(M.pageContext({ pathname: "/x", hook: () => { throw new Error("boom"); } }).view, "", "a failing page hook never breaks sending");
});

test("the thread is a fold over events; replays are ignored", () => {
  const s = M.applySnapshot(M.createState(), { thread, messages: [{ id: "m1", role: "user", text: "Hi", createdAt: "2026-01-02T00:00:00Z" }], approvals: [], draft: { text: "", rev: 0 } });
  assert.equal(s.lastEventId, 3);
  assert.equal(M.applyEvent(s, ev(3, "message.created", { message: { id: "dup" } })), false, "ids at or below the snapshot are skipped");
  M.applyEvent(s, ev(4, "run.started", { runId: "r1" }));
  M.applyEvent(s, ev(5, "message.started", { message: { id: "a1", role: "assistant", text: "", status: "streaming" } }));
  M.applyEvent(s, ev(6, "message.delta", { id: "a1", text: "Blue Bossa " }));
  assert.equal(M.applyEvent(s, ev(6, "message.delta", { id: "a1", text: "Blue Bossa " })), false, "a resumed stream never doubles text");
  M.applyEvent(s, ev(7, "message.delta", { id: "a1", text: "is in C minor." }));
  assert.equal(s.byId.get("a1").text, "Blue Bossa is in C minor.");
  M.applyEvent(s, ev(8, "message.reset", { id: "a1" }));
  M.applyEvent(s, ev(9, "message.delta", { id: "a1", text: "C minor." }));
  M.applyEvent(s, ev(10, "message.completed", { id: "a1", text: "C minor.", status: "done", costUsd: 0.004 }));
  M.applyEvent(s, ev(11, "spend.updated", { threadUsd: 0.014, monthUsd: 1.2, capUsd: 25, blocked: false }));
  M.applyEvent(s, ev(12, "run.finished", { status: "done" }));
  assert.equal(s.running, false);
  assert.equal(s.byId.get("a1").text, "C minor.");
  assert.equal(M.spendLine(s.spend), "This thread $0.01 · month $1.20 of $25.00");
  assert.deepEqual(M.timeline(s).map((i) => i.id), ["m1", "a1"]);
});

test("two devices applying the same events in any delivery pattern converge", () => {
  const events = [
    ev(4, "message.created", { message: { id: "m2", role: "user", text: "File it", clientId: "c-1" } }),
    ev(5, "run.started", {}),
    ev(6, "message.started", { message: { id: "a2", role: "assistant", text: "" } }),
    ev(7, "message.delta", { id: "a2", text: "Proposed." }),
    ev(8, "tool.started", { toolUseId: "tu1", name: "github_create_issue" }),
    ev(9, "approval.requested", { approval: { id: "ap1", status: "pending", title: "Create issue: Sample" } }),
    ev(10, "tool.finished", { toolUseId: "tu1", name: "github_create_issue", ok: true }),
    ev(11, "message.completed", { id: "a2", text: "Proposed.", status: "done" }),
    ev(12, "approval.resolved", { id: "ap1", status: "approved", decidedBy: "phone" }),
    ev(13, "run.finished", { status: "done" }),
  ];
  const snap = { thread, messages: [], approvals: [] };
  const phone = M.applySnapshot(M.createState(), snap);
  events.forEach((e) => M.applyEvent(phone, e));
  // The laptop reconnects twice and receives overlapping batches.
  const laptop = M.applySnapshot(M.createState(), snap);
  [...events.slice(0, 5), ...events.slice(2, 8), ...events.slice(6)].forEach((e) => M.applyEvent(laptop, e));
  const view = (s) => JSON.stringify({ items: M.timeline(s).map((i) => [i.id, i.text || "", i.kind]), approvals: [...s.approvals.values()], last: s.lastEventId });
  assert.equal(view(laptop), view(phone));
  assert.equal(M.pendingApprovals(phone), 0);
  assert.equal(phone.approvals.get("ap1").decidedBy, "phone");
});

test("an optimistic bubble becomes the server's message", () => {
  const s = M.applySnapshot(M.createState(), { thread, messages: [], approvals: [] });
  M.addPending(s, { clientId: "c-9", text: "Sent offline", attachments: [{ kind: "audio", durationMs: 4000 }] });
  assert.equal(M.timeline(s)[0].pending, true);
  M.applyEvent(s, ev(4, "message.created", { message: { id: "m9", role: "user", text: "Sent offline", clientId: "c-9" } }));
  assert.equal(M.timeline(s).length, 1);
  assert.equal(M.timeline(s)[0].id, "m9");
  assert.equal(M.timeline(s)[0].pending, false);
});

test("snapshot places approval cards after the message that led to them", () => {
  const s = M.applySnapshot(M.createState(), {
    thread,
    messages: [
      { id: "m1", role: "user", text: "a", createdAt: "2026-01-02T00:00:00Z" },
      { id: "a1", role: "assistant", text: "b", createdAt: "2026-01-02T00:00:01Z" },
      { id: "m2", role: "user", text: "c", createdAt: "2026-01-02T00:05:00Z" },
    ],
    approvals: [{ id: "ap", status: "pending", createdAt: "2026-01-02T00:00:02Z" }],
  });
  assert.deepEqual(M.timeline(s).map((i) => i.id), ["m1", "a1", "approval:ap", "m2"]);
  assert.equal(M.pendingApprovals(s), 1);
});

test("drafts: unsaved local typing wins; otherwise the newest remote draft is adopted", () => {
  const remote = { text: "half a thought from the phone", deviceId: "phone", rev: 4 };
  assert.deepEqual(M.mergeDraft({ text: "", synced: "", focused: false }, remote, "laptop"), { text: remote.text, adopt: true });
  assert.deepEqual(M.mergeDraft({ text: "typing here", synced: "", focused: true }, remote, "laptop"), { text: "typing here", adopt: false });
  assert.deepEqual(M.mergeDraft({ text: "x", synced: "x", focused: true }, remote, "laptop"), { text: remote.text, adopt: true });
  assert.equal(M.mergeDraft({ text: "mine", synced: "", focused: false }, { ...remote, deviceId: "laptop" }, "laptop").adopt, false, "own echoes are ignored");
  const s = M.createState();
  M.applyEvent(s, ev(1, "draft.updated", { text: "hmm", deviceId: "phone", rev: 1 }), 1000);
  assert.equal(M.remoteTyping(s, "laptop", 3000), true);
  assert.equal(M.remoteTyping(s, "phone", 3000), false);
  assert.equal(M.remoteTyping(s, "laptop", 9000), false);
});

test("outbox: queues offline, replays in order with stable client ids, drops only on permanent errors", async () => {
  const storage = M.memoryStorage();
  let online = false;
  const posted = [], uploads = [];
  const transport = {
    upload: async (item, att) => {
      if (!online) throw Object.assign(new Error("offline"), { status: 0 });
      uploads.push(att.clientId);
      return { id: `srv-${att.clientId}` };
    },
    post: async (item, ids) => {
      if (!online) throw new TypeError("Failed to fetch");
      if (item.text === "bad") throw Object.assign(new Error("a message needs text"), { status: 422 });
      posted.push({ clientId: item.clientId, ids });
      return { id: `m-${item.clientId}` };
    },
  };
  const outbox = M.createOutbox(storage, transport);
  const a = await outbox.enqueue({ clientId: "c-a", threadId: "t1", text: "first", attachments: [{ kind: "audio", contentType: "audio/webm" }] });
  await outbox.enqueue({ clientId: "c-b", threadId: "t1", text: "second", createdAt: Date.now() + 1 });
  assert.equal(await outbox.flush(), 0);
  let items = await outbox.items();
  assert.equal(items.length, 2);
  assert.equal(M.outboxLabel(items), "2 waiting to send");
  assert.ok(items.find((i) => i.clientId === "c-a").notBefore > Date.now(), "backs off");
  // Back online: the retry clock is cleared by retry(); both go, in order, once.
  online = true;
  await outbox.retry("c-a");
  await outbox.flush();
  assert.deepEqual(posted.map((p) => p.clientId), ["c-a", "c-b"]);
  assert.deepEqual(posted[0].ids, [`srv-${a.attachments[0].clientId}`]);
  assert.equal(uploads.length, 1, "the voice note uploads once");
  assert.equal((await outbox.items()).length, 0);
  // A permanent error parks the item instead of retrying forever.
  await outbox.enqueue({ clientId: "c-c", threadId: "t1", text: "bad" });
  await outbox.flush();
  items = await outbox.items();
  assert.equal(items[0].status, "failed");
  assert.equal(M.outboxLabel(items), "1 could not be sent");
  await outbox.discard("c-c");
  assert.equal((await outbox.items()).length, 0);
});

test("outbox: an upload that succeeded before a crash is not repeated", async () => {
  const storage = M.memoryStorage();
  let uploads = 0, failPost = true;
  const outbox = M.createOutbox(storage, {
    upload: async () => ({ id: `srv-${++uploads}` }),
    post: async () => { if (failPost) throw Object.assign(new Error("busy"), { status: 503 }); return {}; },
  });
  await outbox.enqueue({ clientId: "c-v", threadId: "t1", text: "", attachments: [{ kind: "audio" }] });
  await outbox.flush();
  failPost = false;
  await outbox.retry("c-v");
  assert.equal(uploads, 1);
  assert.equal((await outbox.items()).length, 0);
});

// A minimal DOM stand-in: elements record their tag, attributes and children,
// so the tests can prove that only safe elements and attributes are created.
function fakeDocument() {
  const created = [];
  const node = (tag) => {
    const n = { tag, attrs: {}, children: [], appendChild(c) { this.children.push(c); return c; }, setAttribute(k, v) { this.attrs[k] = String(v); } };
    Object.defineProperty(n, "textContent", { set(v) { this.children = [{ tag: "#text", text: String(v) }]; } });
    created.push(n);
    return n;
  };
  return {
    created,
    createElement: (tag) => node(tag),
    createDocumentFragment: () => node("#fragment"),
    createTextNode: (text) => ({ tag: "#text", text: String(text) }),
  };
}

function serialize(n) {
  if (n.tag === "#text") return n.text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  const inner = n.children.map(serialize).join("");
  if (n.tag === "#fragment") return inner;
  const attrs = Object.entries(n.attrs).map(([k, v]) => ` ${k}="${v.replace(/&/g, "&amp;").replace(/"/g, "&quot;")}"`).join("");
  return n.tag === "br" ? "<br>" : `<${n.tag}${attrs}>${inner}</${n.tag}>`;
}

function render(text) {
  const doc = fakeDocument();
  const frag = M.markdownToDOM(text, doc);
  for (const el of doc.created) {
    assert.ok(["#fragment", "p", "br", "ul", "ol", "li", "pre", "code", "strong", "em", "a", "span"].includes(el.tag), `unexpected element ${el.tag}`);
    for (const [k, v] of Object.entries(el.attrs)) {
      assert.ok(["href", "target", "rel"].includes(k), `unexpected attribute ${k}`);
      if (k === "href") assert.ok(/^https?:\/\//.test(v) || (v.startsWith("/") && !v.startsWith("//")), `unsafe href ${v}`);
    }
  }
  return { html: serialize(frag), doc };
}

test("markdown is built as DOM nodes and supports a small safe subset", () => {
  const { html } = render("Hello <script>alert(1)</script>\n\n- **one**\n- `two`\n\n1. [docs](https://example.com)\n\n```\n<b>raw</b>\n```");
  assert.ok(!html.includes("<script>"));
  assert.ok(html.includes("&lt;script&gt;"));
  assert.ok(html.includes("<ul><li><strong>one</strong></li><li><code>two</code></li></ul>"));
  assert.ok(html.includes('<ol><li><a href="https://example.com/" target="_blank" rel="noopener noreferrer">docs</a></li></ol>'));
  assert.ok(html.includes("<pre><code>&lt;b&gt;raw&lt;/b&gt;</code></pre>"));
});

test("markdown: hostile links never become attributes or script URLs", () => {
  const payloads = [
    "[a](https://x.com/(https://y/onmouseover=document.title='PWNED:'+location.protocol+)",
    '[a](https://x.com/" onmouseover="alert(1))',
    "[x](javascript:alert(1))",
    "[x](JaVaScRiPt:alert(1))",
    "[x](data:text/html,<script>alert(1)</script>)",
    "[x](//evil.example/path)",
    "[x](/\\evil.example)",
    "see https://a.example/\"onmouseover=alert(1)// and https://b.example/<img src=x onerror=alert(1)>",
    "[https://x.example/a](https://x.example/b \"title\")",
    "**[a](https://x.example/**)** *https://y.example/*",
    "`[a](javascript:alert(1))` [b](vbscript:msgbox)",
  ];
  for (const p of payloads) {
    const { html, doc } = render(p);
    for (const el of doc.created) for (const k of Object.keys(el.attrs)) assert.ok(!/^on/i.test(k), `${p}: event handler attribute`);
    assert.ok(!/href="(javascript|data|vbscript):/i.test(html), `${p}: script URL`);
    assert.ok(!/href="\/\//.test(html), `${p}: protocol-relative`);
  }
  // The reported payload: the URL stays one href value (quotes are percent-encoded or attribute-escaped).
  const { doc } = render(payloads[0]);
  const a = doc.created.find((n) => n.tag === "a");
  assert.ok(a && a.attrs.href.startsWith("https://x.com/("));
  assert.deepEqual(Object.keys(a.attrs).sort(), ["href", "rel", "target"]);
  assert.equal(M.safeHref("javascript:alert(1)"), null);
  assert.equal(M.safeHref("/jazz/#today"), "/jazz/#today");
  assert.equal(M.safeHref("//evil.example"), null);
});

test("small helpers", () => {
  assert.equal(M.formatUSD(0.004), "<$0.01");
  assert.equal(M.formatUSD(12.5), "$12.50");
  assert.equal(M.lightFor({ status: "completed", conclusion: "failure" }), "red");
  assert.equal(M.lightFor({ status: "in_progress" }), "running");
  assert.equal(M.lightFor(null), "unknown");
  assert.equal(M.stepLabel({ name: "site_status", done: false }), "Checking CI, PRs and deploys…");
  assert.equal(M.streamURL("t1", 42), "/workbench/api/v1/workbench/threads/t1/events?after=42");
  assert.equal(M.isToggleKey({ key: "`", target: { tagName: "BODY" } }), true);
  assert.equal(M.isToggleKey({ key: "`", target: { tagName: "TEXTAREA" } }), false);
  assert.equal(M.backoff(1), 1000);
  assert.equal(M.backoff(20), 60000);
  assert.match(M.newId("c"), /^c-[a-z0-9]{8,}$/);
});
