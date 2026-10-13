/* Pure edit operations shared by the UI and regression tests. */
((root) => {
  const copy = value => JSON.parse(JSON.stringify(value));
  const content = edit => copy({ title: edit.title, clips: edit.clips, appearance: edit.appearance });
  const duration = edit => edit.clips.reduce((n, c) => n + c.endMs - c.startMs, 0);
  function at(edit, ms) {
    let offset = 0;
    for (let i = 0; i < edit.clips.length; i++) {
      const c = edit.clips[i], end = offset + c.endMs - c.startMs;
      if (ms < end || i === edit.clips.length - 1) return { index: i, sourceMs: c.startMs + Math.max(0, Math.min(ms - offset, c.endMs - c.startMs)), offset };
      offset = end;
    }
    return null;
  }
  function apply(edit, action, index, value) {
    const next = content(edit);
    if (action === 'settings') next.appearance = { ...next.appearance, ...value };
    else if (action === 'title') next.title = value;
    else {
      if (!Number.isInteger(index) || index < 0 || index >= next.clips.length) throw Error('Select a passage');
      if (action === 'remove') next.clips.splice(index, 1);
      else if (action === 'start') next.clips = next.clips.slice(index);
      else if (action === 'end') next.clips = next.clips.slice(0, index + 1);
      else if (action === 'move') {
        if (!Number.isInteger(value) || value < 0 || value >= next.clips.length) return next;
        next.clips.splice(value, 0, next.clips.splice(index, 1)[0]);
      } else if (action === 'trim') {
        const [startMs, endMs, limit] = value;
        if (![startMs, endMs, limit].every(Number.isFinite) || startMs < 0 || endMs > limit || endMs - startMs < 500) throw Error('Keep at least 0.5 seconds inside the source take');
        Object.assign(next.clips[index], { startMs: Math.round(startMs), endMs: Math.round(endMs) });
      }
    }
    if (duration(next) < next.appearance.introSeconds * 1000) next.appearance.intro = false;
    return next;
  }
  class History {
    constructor(value) { this.value = content(value); this.past = []; this.future = []; }
    change(value) { if (JSON.stringify(value) === JSON.stringify(this.value)) return; this.past.push(copy(this.value)); this.past = this.past.slice(-100); this.value = copy(value); this.future = []; }
    undo() { if (this.past.length) { this.future.push(this.value); this.value = this.past.pop(); } return copy(this.value); }
    redo() { if (this.future.length) { this.past.push(this.value); this.value = this.future.pop(); } return copy(this.value); }
  }
  const api = { content, duration, at, apply, History };
  if (typeof module !== 'undefined') module.exports = api;
  root.JazzFilmEditModel = api;
})(globalThis);
