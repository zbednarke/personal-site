(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.JazzRepertoireModel = api;
})(globalThis, function () {
  "use strict";

  const STATUSES = ["not_started", "learning", "solid"];
  const MILESTONES = [
    { key: "melodyByEar", label: "Melody by ear", short: "Melody" },
    { key: "keysKnown", label: "Keys known", short: "Keys" },
    { key: "lyrics", label: "Lyrics learned", short: "Lyrics" },
    { key: "changes", label: "Changes memorized", short: "Changes" },
    { key: "transcription", label: "Solo transcribed", short: "Transcribed" },
    { key: "improvise", label: "Can improvise", short: "Improvise" },
    { key: "gigReady", label: "Gig-ready", short: "Gig-ready" },
  ];
  const CATEGORIES = [
    { key: "ballad", label: "Ballads" },
    { key: "upbeat", label: "Upbeat set tunes" },
    { key: "pop", label: "Current pop" },
    { key: "standard", label: "Other standards" },
  ];
  const DAY_MS = 24 * 60 * 60 * 1000;

  const nextMilestoneStatus = (status) => STATUSES[(STATUSES.indexOf(status) + 1) % STATUSES.length] || "learning";

  function keysKnownStatus(keys) {
    return keys.length >= 2 ? "solid" : keys.length === 1 ? "learning" : "not_started";
  }

  // Client mirror of deriveTuneState in jazz-api/repertoire.go, for optimistic UI.
  function deriveTuneState(tune) {
    const keysKnown = Array.isArray(tune.keysKnown) ? [...tune.keysKnown] : [];
    const milestones = { ...(tune.milestones || {}) };
    MILESTONES.forEach(({ key }) => { if (key !== "keysKnown" && !milestones[key]) milestones[key] = "not_started"; });
    milestones.keysKnown = keysKnownStatus(keysKnown);
    const deeplyLearned = milestones.melodyByEar === "solid" && keysKnown.length >= 2 && milestones.lyrics === "solid" && milestones.transcription === "solid";
    const practiced = Number(tune.totalPracticeMs) > 0 || Number(tune.takeCount) > 0 || Number(tune.sessionCount) > 0;
    const started = practiced || MILESTONES.some(({ key }) => milestones[key] !== "not_started");
    const practiceStatus = milestones.gigReady === "solid" ? "gig_ready" : started ? "learning" : "not_started";
    return { ...tune, keysKnown, milestones, deeplyLearned, practiceStatus };
  }

  const active = (tune) => !tune.archivedAt;
  const isGigReady = (tune) => tune.milestones?.gigReady === "solid";
  const meets = (tune, measure) => (measure === "deeplyLearned" ? Boolean(tune.deeplyLearned) : isGigReady(tune));

  // Progress per goal category. Only chosen tunes count, and a category never
  // reports more than its target even when more tunes are chosen.
  function categoryProgress(tunes, goals) {
    return Object.entries(goals).map(([category, goal]) => {
      const inCategory = tunes.filter((tune) => active(tune) && tune.category === category);
      const chosen = inCategory.filter((tune) => tune.chosen);
      const achieved = chosen.filter((tune) => meets(tune, goal.measure)).length;
      return {
        category,
        target: goal.target,
        measure: goal.measure,
        chosen: chosen.length,
        onDeck: inCategory.length - chosen.length,
        achieved,
        counted: Math.min(goal.target, achieved),
        deeplyLearned: chosen.filter((tune) => tune.deeplyLearned).length,
        gigReady: chosen.filter(isGigReady).length,
        overChosen: chosen.length > goal.target,
        percent: goal.target ? Math.round((Math.min(goal.target, achieved) / goal.target) * 100) : 0,
      };
    });
  }

  function plural(count, word) {
    return `${count} ${word}${count === 1 ? "" : "s"}`;
  }

  // Could I hold down a restaurant set tonight? Any gig-ready tune is call-able.
  function setReadiness(tunes, rules) {
    const callable = { ballad: [], upbeat: [], pop: [], standard: [] };
    tunes.filter((tune) => active(tune) && isGigReady(tune)).forEach((tune) => (callable[tune.category] || callable.standard).push(tune));
    const total = Object.values(callable).reduce((sum, list) => sum + list.length, 0);
    const missing = {
      ballad: Math.max(0, rules.minBallads - callable.ballad.length),
      upbeat: Math.max(0, rules.minUpbeat - callable.upbeat.length),
      pop: Math.max(0, rules.minPop - callable.pop.length),
    };
    const categoryShortfall = missing.ballad + missing.upbeat + missing.pop;
    const needed = Math.max(categoryShortfall, rules.minTotal - total);
    const parts = [];
    if (missing.upbeat) parts.push(`${missing.upbeat} more upbeat`);
    if (missing.ballad) parts.push(plural(missing.ballad, "ballad"));
    if (missing.pop) parts.push(`${missing.pop} pop`);
    const extra = needed - categoryShortfall;
    if (extra > 0) parts.push(`${extra} more of anything`);
    const verdict = needed === 0 ? "ready" : needed <= 3 ? "almost" : "not_yet";
    const label = verdict === "ready" ? "Ready to hold a set" : `${verdict === "almost" ? "Almost" : "Not yet"}: need ${parts.join(", ")}`;
    return { verdict, label, total, needed, missing, callable };
  }

  function parseDate(key) {
    const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(key || ""));
    return match ? Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])) : NaN;
  }

  function formatDate(ms) {
    return new Date(ms).toISOString().slice(0, 10);
  }

  // Linear schedule from startDate: about perWeek tunes a week until the goal total
  // is reached. "Done" is a chosen tune that meets its category's measure.
  function paceProjection(tunes, today, targetDate, perWeek, goals, startDate) {
    const now = parseDate(today);
    const target = parseDate(targetDate);
    const start = parseDate(startDate) || now;
    const goalTotal = Object.values(goals).reduce((sum, goal) => sum + goal.target, 0);
    const done = categoryProgress(tunes, goals).reduce((sum, item) => sum + item.counted, 0);
    const week = Math.max(1, Math.floor((now - start) / (7 * DAY_MS)) + 1);
    const expected = Math.min(goalTotal, Math.floor(((now - start) / (7 * DAY_MS)) * perWeek));
    const needed = Math.max(0, goalTotal - done);
    const weeksLeft = Math.max(0, Math.ceil((target - now) / (7 * DAY_MS)));
    const projected = needed && perWeek > 0 ? formatDate(now + Math.ceil(needed / perWeek) * 7 * DAY_MS) : today;
    return {
      week,
      done,
      goalTotal,
      expected: Math.max(0, expected),
      behind: Math.max(0, expected - done),
      onPace: done >= expected,
      needed,
      weeksLeft,
      pastTarget: now > target,
      requiredPerWeek: weeksLeft ? Math.round((needed / weeksLeft) * 10) / 10 : needed,
      projectedDate: projected,
      projectedLate: needed > 0 && parseDate(projected) > target,
    };
  }

  const LETTERS = ["C", "D", "E", "F", "G", "A", "B"];
  const NATURAL = { C: 0, D: 2, E: 4, F: 5, G: 7, A: 9, B: 11 };
  const ENHARMONIC = ["C", "Db", "D", "Eb", "E", "F", "F#", "G", "Ab", "A", "Bb", "B"];

  function normalizeKey(raw) {
    let value = String(raw || "").replace(/♭/g, "b").replace(/♯/g, "#").trim();
    if (!value) return "";
    const letter = value[0].toUpperCase();
    let rest = value.slice(1).trim().toLowerCase();
    let accidental = "";
    if (rest.startsWith("b") || rest.startsWith("#")) { accidental = rest[0]; rest = rest.slice(1).trim(); }
    if (["", "maj", "major"].includes(rest)) rest = "";
    else if (["m", "min", "minor", "-"].includes(rest)) rest = "m";
    const key = letter + accidental + rest;
    return /^[A-G](b|#)?m?$/.test(key) ? key : "";
  }

  function keyIdentity(key) {
    let pitch = NATURAL[key[0]];
    if (key.includes("b", 1)) pitch -= 1;
    if (key.includes("#")) pitch += 1;
    return ((pitch + 12) % 12) * 2 + (key.endsWith("m") ? 1 : 0);
  }

  // Adds a key unless an enharmonic spelling is already there.
  function addKey(keys, raw) {
    const key = normalizeKey(raw);
    if (!key) return { keys, error: "Use a key like C, Bb, F# or Gm" };
    if (keys.some((existing) => keyIdentity(existing) === keyIdentity(key))) return { keys, error: `${key} is already listed` };
    if (keys.length >= 12) return { keys, error: "At most 12 keys" };
    return { keys: [...keys, key], error: "" };
  }

  // Written pitch for B-flat trumpet is concert pitch up a major second: the
  // next letter name, with whatever accidental makes the interval two semitones.
  function transposeKey(concertKey, instrument = "bb-trumpet") {
    const key = normalizeKey(concertKey);
    if (!key || instrument !== "bb-trumpet") return key;
    const minor = key.endsWith("m") ? "m" : "";
    const root = minor ? key.slice(0, -1) : key;
    const pitch = (NATURAL[root[0]] + (root[1] === "b" ? -1 : root[1] === "#" ? 1 : 0) + 12) % 12;
    const letter = LETTERS[(LETTERS.indexOf(root[0]) + 1) % 7];
    const target = (pitch + 2) % 12;
    const offset = ((target - NATURAL[letter] + 18) % 12) - 6;
    const spelled = offset === 0 ? letter : offset === -1 ? `${letter}b` : offset === 1 ? `${letter}#` : ENHARMONIC[target];
    return spelled + minor;
  }

  // Mirrors slugifyTuneTitle in Go; testdata/repertoire_slugs.json keeps them in step.
  function slugify(title) {
    const folded = String(title || "").normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
    let slug = "";
    let dash = true;
    for (const character of folded) {
      if ("'’‘ʼ".includes(character)) continue;
      if (/[a-z0-9]/.test(character)) { slug += character; dash = false; }
      else if (!dash) { slug += "-"; dash = true; }
    }
    slug = slug.replace(/^-+|-+$/g, "");
    if (slug.length > 48) slug = slug.slice(0, 48).replace(/-+$/, "");
    return slug;
  }

  const EDITABLE = ["title", "category", "chosen", "position", "referenceArtist", "referenceTitle", "referenceUrl", "concertKey", "notes"];
  const STORED_MILESTONES = MILESTONES.map(({ key }) => key).filter((key) => key !== "keysKnown");
  const same = (a, b) => JSON.stringify(a ?? "") === JSON.stringify(b ?? "");

  // After a 409, keep local edits the server did not also change; for fields
  // both sides changed, the server wins and the field is reported as a conflict.
  function mergeServerTune(base, local, server) {
    const tune = { ...server, milestones: { ...(server.milestones || {}) }, keysKnown: [...(server.keysKnown || [])] };
    const patch = {};
    const conflicts = [];
    const consider = (name, from, mine, theirs, apply) => {
      if (same(mine, from)) return;
      if (!same(theirs, from) && !same(theirs, mine)) { conflicts.push(name); return; }
      apply(mine);
    };
    EDITABLE.forEach((field) => consider(field, base[field], local[field], server[field], (value) => { tune[field] = value; patch[field] = value; }));
    consider("keysKnown", base.keysKnown, local.keysKnown, server.keysKnown, (value) => { tune.keysKnown = [...value]; patch.keysKnown = [...value]; });
    STORED_MILESTONES.forEach((key) => consider(`milestones.${key}`, base.milestones?.[key], local.milestones?.[key], server.milestones?.[key], (value) => {
      tune.milestones[key] = value;
      patch.milestones = { ...(patch.milestones || {}), [key]: value };
    }));
    return { tune: deriveTuneState(tune), patch, conflicts };
  }

  // The next thing worth practicing on a tune, following the by-ear-first path.
  function suggestFocus(tune) {
    const m = tune.milestones || {};
    if (m.melodyByEar !== "solid") return "melody";
    if ((tune.keysKnown || []).length < 2) return "key";
    if (m.lyrics !== "solid" && tune.category !== "upbeat") return "lyrics";
    if (m.transcription !== "solid") return "transcribe";
    if (m.changes !== "solid") return "changes";
    if (m.improvise !== "solid") return "improvise";
    return "set";
  }

  // This week's tune: the chosen, unfinished tune in the category furthest behind its goal.
  function suggestTuneOfWeek(tunes, goals) {
    const progress = categoryProgress(tunes, goals).sort((a, b) => (a.counted / a.target) - (b.counted / b.target));
    for (const item of progress) {
      const goal = goals[item.category];
      const candidates = tunes
        .filter((tune) => active(tune) && tune.chosen && tune.category === item.category && !meets(tune, goal.measure))
        .sort((a, b) => (b.practiceStatus === "learning") - (a.practiceStatus === "learning") || a.position - b.position);
      if (candidates.length) return candidates[0];
    }
    return null;
  }

  function daysSince(dateKey, today) {
    const then = parseDate(dateKey);
    const now = parseDate(today);
    return Number.isFinite(then) && Number.isFinite(now) ? Math.round((now - then) / DAY_MS) : null;
  }

  // Gig-ready tunes left alone this long are flagged so they get a run-through.
  const isRusty = (tune, today, days = 14) => isGigReady(tune) && (!tune.lastPracticedDate || daysSince(tune.lastPracticedDate, today) >= days);

  // Alternate the feel: an upbeat opener, a ballad every few tunes, pop in the middle and near the end.
  function suggestedSetOrder(callable) {
    const queues = { U: [...callable.upbeat, ...callable.standard], B: [...callable.ballad], P: [...callable.pop] };
    const pattern = ["U", "U", "B", "U", "P", "U", "B", "U", "U", "P", "B", "U"];
    const order = [];
    let index = 0;
    while (Object.values(queues).some((queue) => queue.length)) {
      const want = pattern[index % pattern.length];
      const source = queues[want].length ? queues[want] : queues.U.length ? queues.U : queues.B.length ? queues.B : queues.P;
      order.push(source.shift());
      index += 1;
    }
    return order;
  }

  function randomSuffix(random = Math.random) {
    const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789";
    return Array.from({ length: 6 }, () => alphabet[Math.floor(random() * alphabet.length)]).join("");
  }

  // The practice block definition for "Practice now".
  function practiceBlockDefinition(tune, preset, { position = 0, keepDaily = false, suffix = randomSuffix() } = {}) {
    return {
      blockKey: `tune-${tune.tuneId}-${suffix}`,
      position: Math.min(99, Math.max(0, position)),
      title: `${tune.title}: ${preset.label.toLowerCase()}`.slice(0, 160),
      instructions: preset.instructions.slice(0, 2000),
      category: "repertoire",
      track: "musician",
      targetMinutes: preset.minutes || 15,
      tuneId: tune.tuneId,
      dayOnly: !keepDaily,
    };
  }

  function formatPracticeTime(ms) {
    const minutes = Math.round(Number(ms || 0) / 60000);
    if (minutes < 60) return `${minutes}m`;
    return `${Math.floor(minutes / 60)}h${minutes % 60 ? ` ${minutes % 60}m` : ""}`;
  }

  function lastPracticedLabel(dateKey, today) {
    const days = daysSince(dateKey, today);
    if (days === null) return "Not practiced yet";
    if (days <= 0) return "Last practiced today";
    if (days === 1) return "Last practiced yesterday";
    return `Last practiced ${days} days ago`;
  }

  return {
    STATUSES, MILESTONES, CATEGORIES,
    nextMilestoneStatus, deriveTuneState, categoryProgress, setReadiness, paceProjection,
    normalizeKey, keyIdentity, addKey, transposeKey, slugify, mergeServerTune,
    suggestFocus, suggestTuneOfWeek, isRusty, suggestedSetOrder, practiceBlockDefinition,
    formatPracticeTime, lastPracticedLabel, daysSince,
  };
});
