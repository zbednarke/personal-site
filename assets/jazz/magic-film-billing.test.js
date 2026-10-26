const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const { JSDOM } = require("jsdom");

test("saved quota failures show billing guidance without raw provider JSON", async () => {
  const dom = new JSDOM('<input id="studio-date" value="2026-10-25"><div><button id="studio-render">Render</button></div>', {
    url: "https://zachbednarke.com/jazz/#studio", runScripts: "outside-only",
  });
  const w = dom.window;
  w.matchMedia = () => ({ matches: false, addEventListener() {} });
  let calls = 0;
  w.fetch = async () => {
    calls++;
    return { ok: true, json: async () => ({ jobs: [{
      id: "failed-job", status: "failed", message: "Magic Film failed",
      error: 'Astra returned 429: {"error":{"code":"credit_balance_exhausted"}}',
    }] }) };
  };
  try {
    w.eval(fs.readFileSync(__dirname + "/highlight-local.js", "utf8"));
    await new Promise(resolve => setTimeout(resolve, 20));
    const message = w.document.querySelector("#studio-magic-status").textContent;
    assert.match(message, /billing is separate from your ChatGPT\/Codex subscription/);
    assert.match(message, /earlier films are unchanged/);
    assert.doesNotMatch(message, /Astra returned|credit_balance_exhausted/);
    assert.equal(calls, 1, "a saved billing failure must not automatically resubmit");
  } finally { w.close(); }
});
