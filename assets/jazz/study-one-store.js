(() => {
  "use strict";
  let dbPromise;
  function open() {
    return (dbPromise ||= new Promise((resolve, reject) => {
      const req = indexedDB.open("jazz-study-one-v1", 1);
      req.onupgradeneeded = () => {
        const db = req.result;
        db.createObjectStore("attempts", { keyPath: "id" });
        const chunks = db.createObjectStore("chunks", {
          keyPath: ["id", "index"],
        });
        chunks.createIndex("attempt", "id");
      };
      req.onsuccess = () => {
        req.result.onversionchange = () => req.result.close();
        resolve(req.result);
      };
      req.onerror = () => reject(req.error);
      req.onblocked = () =>
        reject(new Error("Close other Training tabs to open local storage."));
    }));
  }
  async function transaction(stores, mode, run) {
    const db = await open();
    return new Promise((resolve, reject) => {
      const tx = db.transaction(
        stores,
        mode,
        mode === "readwrite" ? { durability: "strict" } : undefined,
      );
      let result;
      tx.oncomplete = () =>
        resolve(typeof result === "function" ? result() : result);
      tx.onerror = () => reject(tx.error);
      tx.onabort = () =>
        reject(tx.error || new Error("Local save was interrupted"));
      try {
        result = run(tx);
      } catch (error) {
        tx.abort();
        reject(error);
      }
    });
  }
  const put = (a) =>
    transaction(["attempts"], "readwrite", (tx) => {
      tx.objectStore("attempts").put(a);
    });
  const all = () =>
    transaction(["attempts"], "readonly", (tx) => {
      const req = tx.objectStore("attempts").getAll();
      return () => req.result;
    });
  const chunk = (id, index, samples) =>
    transaction(["chunks"], "readwrite", (tx) => {
      tx.objectStore("chunks").put({ id, index, samples });
    });
  const chunks = (id) =>
    transaction(["chunks"], "readonly", (tx) => {
      const req = tx.objectStore("chunks").index("attempt").getAll(id);
      return () =>
        req.result.sort((a, b) => a.index - b.index).map((c) => c.samples);
    });
  async function recover() {
    for (const a of await all()) {
      if (a.finalized) continue;
      const data = await chunks(a.id);
      const frames = data.reduce((n, c) => n + c.length, 0);
      a.durationMs = Math.max(
        0,
        Math.min(
          Math.round(960000 / a.tempo),
          Math.round((frames / a.sampleRate) * 1000 - 240000 / a.tempo),
        ),
      );
      a.audioBytes = 44 + frames * 2;
      a.result = {
        status: "interrupted",
        correct: 0,
        notes: [],
        feedback:
          "Recovered after the tab closed or recording stopped unexpectedly.",
      };
      a.finalized = true;
      a.synced = false;
      await put(a);
    }
  }
  globalThis.JazzStudyStore = { open, put, all, chunk, chunks, recover };
})();
