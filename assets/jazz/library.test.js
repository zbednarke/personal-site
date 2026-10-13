// Library view (library.js) against a small DOM stand-in, so it runs under
// plain `node --test` without a browser. Every title here is fictional.
const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const source = fs.readFileSync(path.join(__dirname, "library.js"), "utf8");
const page = fs.readFileSync(path.join(__dirname, "../../jazz/index.html"), "utf8");
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

const IDS = ["library", "library-search", "library-add", "library-refresh", "library-upload", "library-form", "library-file",
  "library-title", "library-author", "library-submit", "library-status", "library-count", "library-reader",
  "library-reader-title", "library-close", "library-open-file", "library-frame", "library-empty", "library-books"];

function element(tag, id) {
  return {
    tagName: tag.toUpperCase(), id, children: [], attributes: {}, hidden: false, value: "", className: "",
    _text: "", get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); },
    set textContent(v) { this._text = String(v); this.children = []; },
    set innerHTML(v) { throw new Error("library.js must not write HTML"); },
    get src() { return this.attributes.src ?? ""; }, set src(v) { this.attributes.src = String(v); },
    append(...kids) { this.children.push(...kids); },
    replaceChildren(...kids) { this.children = kids; },
    setAttribute(k, v) { this.attributes[k] = String(v); },
    getAttribute(k) { return this.attributes[k] ?? null; },
    hasAttribute(k) { return k in this.attributes; },
    removeAttribute(k) { delete this.attributes[k]; },
    focus() {}, scrollIntoView() {}, reset() { this.resetCalled = true; },
    all(pred, out = []) { for (const c of this.children) { if (pred(c)) out.push(c); c.all(pred, out); } return out; },
  };
}

function load({ hash = "#library", items = [] } = {}) {
  for (const id of IDS) assert.ok(page.includes(`id="${id}"`), `jazz/index.html is missing #${id}`);
  const nodes = Object.fromEntries(IDS.map((id) => [id, element("div", id)]));
  nodes["library-upload"].hidden = true;
  nodes["library-reader"].hidden = true;
  const listeners = {};
  const fetches = [];
  const xhrs = [];
  class FakeXHR {
    constructor() { this.upload = {}; this.headers = {}; xhrs.push(this); }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader(k, v) { this.headers[k] = v; }
    send(body) { this.body = body; }
  }
  const context = {
    location: { hash },
    document: {
      getElementById: (id) => nodes[id] || null,
      createElement: (tag) => element(tag),
      addEventListener: (type, fn) => { listeners[type] = fn; },
    },
    fetch: async (url, init) => { fetches.push({ url, init }); return { ok: true, json: async () => items }; },
    XMLHttpRequest: FakeXHR,
    URLSearchParams, Error, JSON, Math, Date,
  };
  vm.runInNewContext(source, context);
  const view = (name) => listeners["jazz:view-change"]({ detail: { view: name } });
  return { nodes, fetches, xhrs, view, context };
}

const ITEMS = [
  { id: "study-a", title: "<img src=x> Test Study A", author: "Fictional Author", sizeBytes: 2 * 1024 * 1024, addedAt: "2026-09-30",
    source: "https://example.com/test-study-a", resources: [{ title: "Test Recording One", artist: "Test Ensemble", note: "Listen first.", url: "https://example.com/listen" }] },
  { id: "method b", title: "Test Method Book B", author: "", sizeBytes: 1024, addedAt: "2026-09-29", source: "javascript:alert(1)" },
];

test("lists from the highlight API, renders titles as text, and opens the reader", async () => {
  const { nodes, fetches } = load({ items: ITEMS });
  await settle();
  assert.equal(fetches.length, 1);
  assert.equal(fetches[0].url, "./highlight-api/library");
  assert.equal(fetches[0].init.credentials, "same-origin");
  const books = nodes["library-books"].children;
  assert.equal(books.length, 2);
  assert.equal(books[0].all((n) => n.tagName === "H2")[0].textContent, "<img src=x> Test Study A");
  assert.equal(nodes["library-count"].textContent, "2 documents · private cloud library");
  assert.equal(nodes["library-empty"].hidden, true);

  const listen = books[0].all((n) => n.className === "library-listening");
  assert.equal(listen.length, 1);
  const link = listen[0].all((n) => n.tagName === "A")[0];
  assert.equal(link.rel, "noopener noreferrer");
  assert.equal(link.all((n) => n.tagName === "STRONG")[0].textContent, "Test Recording One");

  // Only https sources become links.
  assert.equal(books[0].all((n) => n.textContent === "Original source ↗").length, 1);
  assert.equal(books[1].all((n) => n.textContent === "Original source ↗").length, 0);
  const download = books[1].all((n) => n.textContent === "Download")[0];
  assert.equal(download.href, "./highlight-api/library/method%20b/file?download=1");

  books[0].all((n) => n.tagName === "BUTTON")[0].onclick();
  assert.equal(nodes["library-reader"].hidden, false);
  assert.equal(nodes["library-frame"].getAttribute("src"), "./highlight-api/library/study-a/file");
  assert.equal(nodes["library-open-file"].href, "./highlight-api/library/study-a/file");
  assert.equal(nodes["library-reader-title"].textContent, "<img src=x> Test Study A");

  nodes["library-close"].onclick();
  assert.equal(nodes["library-frame"].hasAttribute("src"), false);
  assert.equal(nodes["library-reader"].hidden, true);
});

test("search filters by title, author and listening resources", async () => {
  const { nodes } = load({ items: ITEMS });
  await settle();
  nodes["library-search"].value = "test ensemble";
  nodes["library-search"].oninput();
  assert.equal(nodes["library-books"].children.length, 1);
  nodes["library-search"].value = "nothing like this";
  nodes["library-search"].oninput();
  assert.equal(nodes["library-books"].children.length, 0);
  assert.equal(nodes["library-empty"].hidden, false);
  assert.equal(nodes["library-empty"].textContent, "No documents match your search.");
});

test("#library?q= fills the search, and leaving the view closes the reader", async () => {
  const { nodes, view, context } = load({ hash: "#repertoire", items: ITEMS });
  await settle();
  assert.equal(nodes["library-books"].children.length, 0, "does not load until the view opens");
  context.location.hash = "#library?q=Method%20Book";
  view("library");
  await settle();
  assert.equal(nodes["library-search"].value, "Method Book");
  assert.equal(nodes["library-books"].children.length, 1);
  nodes["library-reader"].hidden = false;
  nodes["library-frame"].setAttribute("src", "x");
  view("today");
  assert.equal(nodes["library-reader"].hidden, true);
  assert.equal(nodes["library-frame"].hasAttribute("src"), false);
});

test("an empty shelf invites the first upload", async () => {
  const { nodes } = load({ items: [] });
  await settle();
  assert.equal(nodes["library-count"].textContent, "0 documents · private cloud library");
  assert.equal(nodes["library-empty"].hidden, false);
  assert.match(nodes["library-empty"].textContent, /Add a PDF/);
});

test("upload posts the PDF with its title and author to the same endpoint", async () => {
  const { nodes, xhrs, fetches } = load({ items: [] });
  await settle();
  nodes["library-add"].onclick();
  assert.equal(nodes["library-upload"].hidden, false);
  const file = { name: "Test Study A.pdf", size: 1024 };
  nodes["library-file"].files = [file];
  nodes["library-file"].onchange();
  assert.equal(nodes["library-title"].value, "Test Study A");
  nodes["library-author"].value = "Fictional Author";
  nodes["library-form"].onsubmit({ preventDefault() {} });
  assert.equal(xhrs.length, 1);
  assert.equal(xhrs[0].method, "POST");
  assert.equal(xhrs[0].url, "./highlight-api/library?title=Test+Study+A&author=Fictional+Author");
  assert.equal(xhrs[0].headers["Content-Type"], "application/pdf");
  assert.equal(xhrs[0].body, file);
  assert.equal(nodes["library-submit"].disabled, true);

  xhrs[0].status = 201;
  xhrs[0].responseText = JSON.stringify({ id: "new", title: "Test Study A" });
  await xhrs[0].onload();
  assert.equal(fetches.length, 2, "reloads the list after saving");
  assert.equal(nodes["library-upload"].hidden, true);
  assert.equal(nodes["library-status"].textContent, "Saved “Test Study A” to your library.");
});

test("an oversized PDF is refused before uploading", async () => {
  const { nodes, xhrs } = load({ items: [] });
  await settle();
  nodes["library-file"].files = [{ name: "big.pdf", size: 101 * 1024 * 1024 }];
  nodes["library-title"].value = "Big";
  nodes["library-form"].onsubmit({ preventDefault() {} });
  assert.equal(xhrs.length, 0);
  assert.equal(nodes["library-status"].textContent, "Choose a PDF up to 100 MB.");
});

test("a non-https listening link is shown as text, not a link", async () => {
  const items = [{ id: "c", title: "Test Lead Sheet C", author: "", sizeBytes: 1024, addedAt: "2026-09-20",
    resources: [
      { title: "Test Recording Safe", artist: "Test Ensemble", url: "https://example.com/safe" },
      { title: "Test Recording Script", artist: "Test Ensemble", url: "javascript:alert(1)" },
      { title: "Test Recording Plain", artist: "Test Ensemble", url: "http://example.com/plain" },
    ] }];
  const { nodes } = load({ items });
  await settle();
  const entries = nodes["library-books"].children[0].all((n) => n.className === "library-listening-item");
  assert.deepEqual(entries.map((n) => n.tagName), ["A", "DIV", "DIV"]);
  assert.equal(entries[0].href, "https://example.com/safe");
  assert.equal(entries[0].rel, "noopener noreferrer");
  for (const entry of entries.slice(1)) {
    assert.equal(entry.href, undefined, "no href on a non-https resource");
    assert.equal(entry.target, undefined);
  }
  assert.match(entries[1].textContent, /Test Recording Script/, "the title still shows");
});

async function startUpload() {
  const page = load({ items: [] });
  await settle();
  page.nodes["library-file"].files = [{ name: "Test Upload E.pdf", size: 2048 }];
  page.nodes["library-file"].onchange();
  page.nodes["library-form"].onsubmit({ preventDefault() {} });
  assert.equal(page.xhrs.length, 1);
  assert.equal(page.nodes["library-submit"].disabled, true);
  return { ...page, xhr: page.xhrs[0] };
}

test("an upload the server refuses shows its error and keeps the form", async () => {
  const { nodes, xhr, fetches } = await startUpload();
  xhr.status = 413;
  xhr.responseText = JSON.stringify({ error: "That PDF is too large." });
  await xhr.onload();
  assert.equal(nodes["library-status"].textContent, "That PDF is too large.");
  assert.equal(nodes["library-submit"].disabled, false, "the button is usable again");
  assert.equal(nodes["library-form"].resetCalled, undefined, "the chosen file and title stay for a retry");
  assert.equal(fetches.length, 1, "no reload after a failed upload");
});

test("a non-201 without a JSON error falls back to a generic message", async () => {
  const { nodes, xhr } = await startUpload();
  xhr.status = 500;
  xhr.responseText = JSON.stringify({});
  await xhr.onload();
  assert.equal(nodes["library-status"].textContent, "Upload failed. Please retry.");
  assert.equal(nodes["library-submit"].disabled, false);
});

test("a non-JSON reply, a network error and a timeout all say the upload did not finish", async () => {
  const message = "Upload did not finish. Your original file is unchanged; retry when connected.";
  const failures = {
    "non-JSON 502": async (xhr) => { xhr.status = 502; xhr.responseText = "<html>Bad gateway</html>"; await xhr.onload(); },
    onerror: async (xhr) => xhr.onerror(),
    ontimeout: async (xhr) => xhr.ontimeout(),
  };
  for (const [name, fail] of Object.entries(failures)) {
    const { nodes, xhr, fetches } = await startUpload();
    await fail(xhr);
    assert.equal(nodes["library-status"].textContent, message, name);
    assert.equal(nodes["library-submit"].disabled, false, name);
    assert.equal(nodes["library-upload"].hidden, true, `${name}: the form was never opened in this test, and nothing changed it`);
    assert.equal(nodes["library-form"].resetCalled, undefined, name);
    assert.equal(fetches.length, 1, name);
  }
});
