(() => {
  "use strict";
  let opening;
  async function db() {
    return (opening ||= new Promise((resolve, reject) => {
      const r = indexedDB.open("jazz-training-takes", 1);
      r.onupgradeneeded = () => {
        r.result.createObjectStore("takes", { keyPath: "id" });
        const chunks = r.result.createObjectStore("chunks", {
          keyPath: ["id", "kind", "index"],
        });
        chunks.createIndex("take", "id");
      };
      r.onsuccess = () => resolve(r.result);
      r.onerror = () => reject(r.error);
    }));
  }
  async function tx(store, mode, action) {
    const connection = await db();
    return new Promise((resolve, reject) => {
      const t = connection.transaction(
        store,
        mode,
        mode === "readwrite" ? { durability: "strict" } : undefined,
      );
      const request = action(t.objectStore(store));
      t.oncomplete = () => resolve(request?.result);
      t.onabort = t.onerror = () =>
        reject(t.error || new Error("Could not save training take"));
    });
  }
  globalThis.JazzTrainingTakeStore = {
    put: (value) => tx("takes", "readwrite", (s) => s.put(value)),
    all: () => tx("takes", "readonly", (s) => s.getAll()),
    chunk: (id, kind, index, data) =>
      tx("chunks", "readwrite", (s) => s.put({ id, kind, index, data })),
    chunks: (id) => tx("chunks", "readonly", (s) => s.index("take").getAll(id)),
  };
})();
