(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.JazzPracticeLayout = api;
})(globalThis, function () {
  "use strict";
  const create = (ids) => ({ base: [...ids], order: [...ids], removed: [], renames: {} });
  const kept = (draft) => draft.order.filter((id) => !draft.removed.includes(id));
  const dirty = (draft) => Object.keys(draft.renames || {}).some(id => !draft.removed.includes(id)) || draft.removed.length > 0 || draft.order.some((id, index) => id !== draft.base[index]);
  function move(draft, id, index) {
    if (!draft.order.includes(id) || draft.removed.includes(id)) return draft;
    const order = draft.order.filter((item) => item !== id);
    order.splice(Math.max(0, Math.min(order.length, index)), 0, id);
    return { ...draft, order };
  }
  function remove(draft, id) {
    if (!draft.order.includes(id)) return draft;
    return { ...draft, removed: draft.removed.includes(id) ? draft.removed.filter((item) => item !== id) : [...draft.removed, id] };
  }
  function reconcile(draft, ids) {
    if (!dirty(draft)) return create(ids);
    return {
      renames: Object.fromEntries(Object.entries(draft.renames || {}).filter(([id]) => ids.includes(id))),
      base: [...ids],
      order: [...draft.order.filter((id) => ids.includes(id)), ...ids.filter((id) => !draft.order.includes(id))],
      removed: draft.removed.filter((id) => ids.includes(id)),
    };
  }
  // Pointer position is compared to row centers, including rows of different heights.
  function insertionIndex(y, rows) {
    const index = rows.findIndex((row) => y < row.top + row.height / 2);
    return index < 0 ? rows.length : index;
  }
  function rename(draft, id, original, title) {
    const renames = { ...draft.renames };
    const from = renames[id]?.from ?? original;
    if (title.trim() === from) delete renames[id];
    else renames[id] = { from, to: title.trim() };
    return { ...draft, renames };
  }
  return { rename, create, kept, dirty, move, remove, reconcile, insertionIndex };
});
