(() => {
  "use strict";
  const E = globalThis.JazzStudyOne,
    S = globalThis.JazzStudyStore,
    A = globalThis.JazzAudioAnalysis,
    $ = (s) => document.querySelector(s);
  if (!$("#training") || !E || !S || !A) return;
  const API = "./api/v1/training/attempts";
  let initialized = false,
    running = false,
    starting = false,
    ctx,
    stream,
    node,
    source,
    sink,
    trial = null,
    sequence = [],
    round = 1,
    position = 0,
    sessionId,
    settings,
    feedbackTimer,
    pendingRepeat = false,
    repeatProgress = {},
    reviewPaused = false,
    uiTimer,
    releaseLock,
    cloud = [],
    syncing = false,
    syncRequested = false,
    playURL,
    emergencyURL;
  const oscillators = new Set(),
    escape = (s) =>
      String(s).replace(
        /[&<>"']/g,
        (c) =>
          ({
            "&": "&amp;",
            "<": "&lt;",
            ">": "&gt;",
            '"': "&quot;",
            "'": "&#39;",
          })[c],
      );
  const msg = (id, text) => {
    $(id).textContent = text;
  };
  const selected = () =>
    [...$("#study-keys").querySelectorAll("input:checked")].map((i) => i.value);
  const statusLabel = (s) =>
    ({
      pass: "Pass",
      "needs-work": "Needs work",
      "not-graded": "Not graded",
      interrupted: "Interrupted",
    })[s] || s;
  E.keys.forEach((k) => {
    const label = document.createElement("label"),
      box = document.createElement("input");
    box.type = "checkbox";
    box.value = k.name;
    box.checked = true;
    box.disabled = true;
    label.append(box, document.createTextNode(k.name));
    $("#study-keys").append(label);
  });
  function modeChanged() {
    const lesson = $("#study-mode").value === "lesson";
    $("#study-keys").hidden = lesson;
    $("#study-keys")
      .querySelectorAll("input")
      .forEach((b) => {
        b.disabled = lesson || running;
        if (lesson) b.checked = true;
      });
  }
  $("#study-mode").addEventListener("change", modeChanged);
  $("#study-instrument").addEventListener("change", () =>
    msg(
      "#study-convention",
      $("#study-instrument").value === "bb-trumpet"
        ? "Keys and notes are written for B♭ trumpet; the microphone checks sounding pitch."
        : "Keys and notes are in concert pitch.",
    ),
  );
  async function api(path = "", options = {}) {
    const response = await fetch(API + path, options);
    if (!response.ok)
      throw new Error(`Cloud sync unavailable (${response.status})`);
    return response.json();
  }
  const publicAttempt = (a) => {
    const { finalized, synced, ...payload } = a;
    return payload;
  };
  async function sync() {
    if (syncing) {
      syncRequested = true;
      return;
    }
    syncing = true;
    $("#study-sync").disabled = true;
    try {
      for (const a of await S.all()) {
        if (!a.finalized || a.synced) continue;
        await api("/" + a.id, {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(publicAttempt(a)),
        });
        const blob = E.wav(await S.chunks(a.id), a.sampleRate);
        if (blob.size !== a.audioBytes)
          throw new Error(
            "Local audio size mismatch; download your retained recording.",
          );
        await api("/" + a.id + "/audio", {
          method: "PUT",
          headers: { "Content-Type": "audio/wav" },
          body: blob,
        });
        a.synced = true;
        await S.put(a);
      }
      const remote = [];
      let after = "";
      do {
        const result = await api(
          after ? "?after=" + encodeURIComponent(after) : "",
        );
        remote.push(...result.attempts);
        after = result.next;
      } while (after);
      cloud = remote;
      if (!trial) msg("#study-save", "Exercise audio and results uploaded to cloud.");
      msg(
        "#study-cloud",
        "Exercise audio and results are up to date in the cloud.",
      );
    } catch (error) {
      msg(
        "#study-cloud",
        error.message + " Locally saved attempts retry when connected.",
      );
    } finally {
      syncing = false;
      await renderHistory().catch(() => {});
      $("#study-sync").disabled = false;
      if (syncRequested) {
        syncRequested = false;
        sync();
      }
    }
  }
  async function history() {
    const map = new Map(
      cloud.map((r) => [
        r.attempt.id,
        { ...r.attempt, synced: r.audioReady, remote: true },
      ]),
    );
    for (const a of await S.all()) map.set(a.id, { ...a, remote: false });
    return [...map.values()]
      .filter((a) => a.finalized !== false)
      .sort((a, b) => b.startedAt.localeCompare(a.startedAt));
  }
  function reportHTML(result) {
    const r = result.report;
    if (!r) return "";
    return `<p class="study-next-step"><strong>Next try:</strong> ${escape(r.nextStep)}</p><details class="study-diagnostic"><summary>Detailed report · pitch ${escape(r.pitchScore)} · timing ${escape(r.timing)} · hold ${escape(r.hold)}</summary><ul>${r.issues.map(issue=>`<li><strong>${escape(issue.location)}</strong> — ${escape(issue.message)} <span>${escape(issue.advice)}</span></li>`).join("") || "<li>All grading checks passed.</li>"}</ul><div class="study-report-scroll"><table><thead><tr><th>Where</th><th>Expected</th><th>Heard</th><th>Target sustained</th><th>Entry</th></tr></thead><tbody>${result.notes.map(n=>`<tr><td>${escape(n.location)}</td><td>${escape(n.name)}</td><td>${escape(n.heard || "Unclear / none")}</td><td>${Math.round(n.coverage*100)}%</td><td>${n.offsetMs===null?"Not detected":`${Math.round(Math.abs(n.offsetMs)/10)*10} ms ${n.offsetMs<0?"early":"late"}`}</td></tr>`).join("")}</tbody></table></div><p>Pitch names follow your instrument setting. Timing and pitch coverage are estimates; attack transients are excluded. Only the first four tonic beats are graded.</p></details>`;
  }
  async function renderHistory() {
    const list = await history(),
      today = new Date().toLocaleDateString(),
      week = Date.now() - 7 * 86400000;
    const minutes = (items) =>
        (items.reduce((s, a) => s + a.durationMs, 0) / 60000).toFixed(1),
      sessions = new Set(list.map((a) => a.sessionId)),
      rounds = new Map();
    for (const a of list.filter(
      (a) =>
        a.mode === "lesson" &&
        !["interrupted", "not-graded"].includes(a.result.status),
    )) {
      const key = a.sessionId + ":" + a.round;
      if (!rounds.has(key)) rounds.set(key, new Set());
      rounds.get(key).add(a.key);
    }
    msg(
      "#study-totals",
      `${minutes(list.filter((a) => new Date(a.startedAt).toLocaleDateString() === today))} min today · ${minutes(list.filter((a) => Date.parse(a.startedAt) >= week))} min in 7 days · ${list.length} attempts · ${sessions.size} sessions · ${[...rounds.values()].filter((s) => s.size === 12).length} full lesson rounds. Exercise time excludes count-ins and feedback.`,
    );
    const mode = $("#study-history-mode").value,
      tempo = Number($("#study-history-tempo").value),
      filtered = list.filter(
        (a) =>
          a.mode === mode &&
          a.tempo === tempo &&
          a.instrument === $("#study-history-instrument").value,
      );
    $("#study-key-metrics").innerHTML = E.keys
      .map((k) => {
        const attempts = filtered.filter(
            (a) =>
              a.key === k.name &&
              !["not-graded", "interrupted"].includes(a.result.status),
          ),
          passes = attempts.filter((a) => a.result.status === "pass"),
          recent = attempts.slice(0, 3),
          ready =
            mode === "lesson" &&
            recent.length === 3 &&
            recent.every((a) => a.result.status === "pass") &&
            new Set(recent.map((a) => a.sessionId)).size >= 2;
        return `<article class="${ready ? "ready" : ""}"><strong>${k.name}</strong><small>${attempts.length ? Math.round((passes.length / attempts.length) * 100) + "% pass" : "No graded attempts"}</small><small>${ready ? "3 recent passes / 2 sessions" : passes.length + " / " + attempts.length + " passed"}</small></article>`;
      })
      .join("");
    const recentSessions = [...new Set(filtered.map((a) => a.sessionId))].slice(
      0,
      5,
    );
    $("#study-session-trend").innerHTML = recentSessions.length
      ? "<p>Recent sessions · same mode, instrument, and tempo</p>" +
        recentSessions
          .map((id) => {
            const attempts = filtered.filter((a) => a.sessionId === id),
              graded = attempts.filter((a) =>
                ["pass", "needs-work"].includes(a.result.status),
              );
            return `<p>${escape(new Date(attempts[0].startedAt).toLocaleDateString())} · ${minutes(attempts)} min · ${graded.length ? Math.round((graded.filter((a) => a.result.status === "pass").length / graded.length) * 100) + "% pass" : "Not graded"} · ${attempts.length} attempts</p>`;
          })
          .join("")
      : "";
    $("#study-attempts").innerHTML =
      filtered
        .slice(0, 60)
        .map(
          (a) =>
            `<article class="study-attempt"><div><strong>${escape(a.key)} · ${statusLabel(a.result.status)}${a.result.notes?.length ? " · " + a.result.correct + "/7" : ""}</strong><small>${escape(new Date(a.startedAt).toLocaleString())} · ${a.tempo} BPM · ${a.instrument === "bb-trumpet" ? "Written B♭ trumpet" : "Concert pitch"} · ${a.synced ? "Synced" : a.remote ? "Audio pending on recording device" : "Saved locally"}</small><small>${escape(a.result.feedback || "")}</small>${reportHTML(a.result)}</div><button class="button" data-study-replay="${a.id}">Replay / save</button></article>`,
        )
        .join("") || "<p>No attempts at this mode and tempo yet.</p>";
    const errors = Array.from({ length: 7 }, (_, i) =>
      filtered.reduce(
        (n, a) => n + (a.result.notes?.[i]?.correct === false ? 1 : 0),
        0,
      ),
    );
    if (Math.max(...errors) > 0) {
      const p = document.createElement("p");
      p.textContent = `Most frequent note error: note ${errors.indexOf(Math.max(...errors)) + 1} (${Math.max(...errors)} attempts).`;
      $("#study-attempts").prepend(p);
    }
    $("#study-attempts")
      .querySelectorAll("[data-study-replay]")
      .forEach((b) =>
        b.addEventListener("click", () =>
          replay(list.find((a) => a.id === b.dataset.studyReplay)).catch((e) =>
            msg("#study-cloud", e.message),
          ),
        ),
      );
  }
  async function replay(a) {
    if (running || starting) {
      msg("#study-cloud", "Finish training before replaying a take.");
      return;
    }
    if (playURL) URL.revokeObjectURL(playURL);
    const chunks = await S.chunks(a.id),
      blob = chunks.length
        ? E.wav(chunks, a.sampleRate)
        : await fetch(API + "/" + a.id + "/audio").then((r) => {
            if (!r.ok)
              throw new Error("Audio is not available on this device yet.");
            return r.blob();
          });
    playURL = URL.createObjectURL(blob);
    const player = $("#study-player");
    player.src = playURL;
    player.hidden = false;
    player.play().catch(() => {});
    let link = $("#study-download");
    if (!link) {
      link = document.createElement("a");
      link.id = "study-download";
      link.className = "button";
      player.after(link);
    }
    link.href = playURL;
    link.download = `Study-1-${a.key}-${a.id}.wav`;
    link.textContent = "Download this take";
  }
  function controls(active) {
    $("#study-setup")
      .querySelectorAll("input,select")
      .forEach((e) => (e.disabled = active));
    $("#study-start").hidden = active;
    $("#study-stop").hidden = !active;
    $("#study-reset").hidden = !active;
    if (!active) modeChanged();
  }
  async function acquireLock() {
    if (!navigator.locks)
      throw new Error(
        "This browser does not support safe training-session locking.",
      );
    await new Promise((resolve, reject) => {
      navigator.locks
        .request(
          "jazz-study-one-capture",
          { ifAvailable: true },
          async (lock) => {
            if (!lock) {
              reject(new Error("Training is already open in another tab."));
              return;
            }
            await new Promise((done) => {
              releaseLock = done;
              resolve();
            });
          },
        )
        .catch(reject);
    });
  }
  async function start() {
    if (running || starting || trial) return;
    starting = true;
    $("#study-start").disabled = true;
    try {
      if (
        globalThis.JazzRecording?.isBusy?.() ||
        globalThis.JazzGuideToneTrainer?.isBusy?.()
      )
        throw new Error("Finish the current recording or trainer first.");
      await S.open();
      await acquireLock();
      if (!starting) throw new Error("Training start cancelled.");
      settings = {
        tempo: Number($("#study-tempo").value),
        mode: $("#study-mode").value,
        instrument: $("#study-instrument").value,
        rounds: Number($("#study-rounds").value),
        keys: selected(),
      };
      if (
        !Number.isInteger(settings.tempo) ||
        settings.tempo < 40 ||
        settings.tempo > 160 ||
        !settings.keys.length
      )
        throw new Error(
          "Choose at least one key and a tempo from 40 to 160 BPM.",
        );
      $("#study-player").pause();
      sessionId = crypto.randomUUID();
      const reservation = await globalThis.JazzTrainingSessionTake.prepare(sessionId);
      const device = $("#study-input").value;
      stream = await navigator.mediaDevices.getUserMedia({
        audio: {
          deviceId: device ? { exact: device } : undefined,
          echoCancellation: false,
          noiseSuppression: false,
          autoGainControl: false,
        },
        video: { width: {ideal:1280}, height: {ideal:720}, frameRate: {ideal:30}, ...($("#study-camera").value ? {deviceId:{exact:$("#study-camera").value}} : {}) },
      });
      if (!starting) throw new Error("Training start cancelled.");
      stream.getTracks().forEach((t) => {
        t.addEventListener("ended", () => stop("Microphone disconnected."));
        t.addEventListener("mute", () =>
          stop("Microphone became unavailable."),
        );
      });
      ctx = new AudioContext({ sampleRate: 48000, latencyHint: "interactive" });
      await ctx.audioWorklet.addModule("../assets/jazz/study-one-worklet.js");
      await ctx.resume();
      if (ctx.state !== "running")
        throw new Error("Audio could not start. Try again.");
      source = ctx.createMediaStreamSource(stream);
      node = new AudioWorkletNode(ctx, "jazz-study-capture");
      sink = ctx.createGain();
      sink.gain.value = 0;
      source.connect(node).connect(sink).connect(ctx.destination);
      node.port.onmessage = onAudio;
      ctx.addEventListener("statechange", () => {
        if (running && ctx?.state !== "running") stop("Audio was suspended.");
      });
      await navigator.storage?.persist?.().catch(() => false);
      if (!starting || document.hidden || location.hash !== "#training")
        throw new Error("Return to Training to start.");
      await globalThis.JazzTrainingSessionTake.start(stream, reservation, () => stop("Session recording interrupted."));
      if (!starting || document.hidden) throw new Error("Training start cancelled.");
      running = true;
      round = 1;
      repeatProgress = {};
      position = 0;
      sequence = E.shuffle(settings.keys);
      controls(true);
      $("#study-retry").hidden = true;
      $("#study-emergency").hidden = true;
      $("#study-history-mode").value = settings.mode;
      $("#study-history-instrument").value = settings.instrument;
      $("#study-history-tempo").value = settings.tempo;
      await nextTrial();
    } catch (error) {
      running = false;
      await cleanup();
      msg("#study-feedback", error.message);
      controls(false);
    } finally {
      starting = false;
      $("#study-start").disabled = false;
    }
  }
  async function nextTrial() {
    if (!running) return;
    $("#study-reset").disabled = true;
    pendingRepeat = false;
    reviewPaused = false;
    $("#study-continue").textContent = "Continue practice";
    $("#study-continue").hidden = true;
    $("#study-skip-repeat").hidden = true;
    if (position === sequence.length) {
      if (settings.rounds && round >= settings.rounds) {
        await stop("Round complete.");
        $("#study-retry").hidden = false;
        return;
      }
      round++;
      sequence = E.shuffle(settings.keys, sequence.at(-1));
      position = 0;
    }
    $("#study-report").replaceChildren();
    const key = sequence[position++],
      ex = E.exercise(key, settings.instrument),
      record = {
        id: crypto.randomUUID(),
        sessionId,
        recordingId: sessionId,
        sessionOffsetMs: globalThis.JazzTrainingSessionTake.offsetMs(),
        key,
        instrument: settings.instrument,
        mode: settings.mode,
        tempo: settings.tempo,
        round,
        startedAt: new Date().toISOString(),
        durationMs: 0,
        sampleRate: ctx.sampleRate,
        audioBytes: 44,
        captureDiagnostics: {
          contextSampleRate: ctx.sampleRate,
          ...Object.fromEntries(Object.entries(stream.getAudioTracks()[0]?.getSettings?.() || {})
            .filter(([key]) => ["sampleRate", "channelCount", "echoCancellation", "noiseSuppression", "autoGainControl", "latency"].includes(key))),
          maxDeliveryLagMs: 0,
        },
        result: { status: "interrupted", correct: 0, notes: [], feedback: "" },
        finalized: false,
        synced: false,
      };
    await S.put(record);
    if (!running) return;
    const beat = 60 / settings.tempo,
      start = ctx.currentTime + 0.35;
    trial = {
      record,
      ex,
      start,
      answer: start + 4 * beat,
      end: start + 20 * beat,
      beat,
      frames: [],
      chunks: [],
      writes: Promise.resolve(),
      writeError: null,
      done: false,
      index: 0,
      lastUI: -99,
      interrupted: false,
    };
    $("#study-prompt").innerHTML = `2–5 into <strong>${escape(key)}</strong>`;
    msg("#study-feedback", "");
    $("#study-feedback").dataset.status = "";
    msg(
      "#study-save",
      "Recording · session video uploads when you finish; audio backup saving.",
    );
    renderScore(ex);
    $("#study-score").hidden = settings.mode !== "learn";
    $(".study-stage").scrollIntoView({ block: "start", behavior: "smooth" });
    node.port.postMessage({
      type: "start",
      id: record.id,
      start,
      end: trial.end,
    });
    for (let b = 0; b < 20; b++) click(start + b * beat, b % 4 === 0);
    cancelAnimationFrame(uiTimer);
    updateBeat();
    $("#study-reset").disabled = false;
  }
  function click(time, accent) {
    const o = ctx.createOscillator(),
      g = ctx.createGain();
    o.frequency.value = accent ? 1100 : 760;
    g.gain.setValueAtTime(0, time);
    g.gain.linearRampToValueAtTime(0.065, time + 0.003);
    g.gain.exponentialRampToValueAtTime(0.0001, time + 0.045);
    o.connect(g).connect(ctx.destination);
    o.start(time);
    o.stop(time + 0.05);
    oscillators.add(o);
    o.onended = () => {
      oscillators.delete(o);
      o.disconnect();
      g.disconnect();
    };
  }
  function updateBeat() {
    if (!trial || trial.done || !ctx) return;
    uiTimer = requestAnimationFrame(updateBeat);
    const timestamp = ctx.getOutputTimestamp?.();
    const heardTime = timestamp?.performanceTime > 0
      ? timestamp.contextTime + (performance.now() - timestamp.performanceTime) / 1000
      : ctx.currentTime - (ctx.outputLatency || 0);
    const frame = E.metronomeFrame(heardTime - trial.start, trial.beat);
    const b = frame.beat;
    const dial = $("#study-beats");
    $("#study-pendulum-arm").setAttribute("transform", `rotate(${frame.angle} 120 130)`);
    dial.dataset.hit = frame.hit && b < 20 ? frame.side : "";
    dial.dataset.accent = String(frame.number === 1);
    $("#study-beat-number").textContent = b >= 0 && b < 20 ? frame.number : "—";
    dial.setAttribute("aria-label", b >= 0 && b < 20 ? `Metronome beat ${frame.number} of 4` : "Metronome ready");
    if (b === trial.lastUI) return;
    trial.lastUI = b;
    msg(
      "#study-position",
      `Round ${round} · ${position}/${sequence.length} · ${settings.tempo} BPM`,
    );
    msg(
      "#study-phase",
      b < 0
        ? "Get ready"
        : b < 4
          ? `Count in · ${b + 1}`
          : b < 20
            ? `Play · bar ${Math.floor((b - 4) / 4) + 1} of 4`
            : "Grading",
    );

    $("#study-score")
      .querySelectorAll("[data-note]")
      .forEach((e) => {
        const n = trial.ex.notes[Number(e.dataset.note)];
        e.classList.toggle(
          "active",
          b - 4 >= n.beat && b - 4 < n.beat + n.length,
        );
      });
  }
  function renderScore(ex) {
    $("#study-score").innerHTML =
      E.notation(ex, settings.instrument) +
      ex.notes
        .map(
          (n, i) =>
            `<span data-note="${i}">${n.name}<small>${n.length === 8 ? "4 beats + 4 optional" : n.length + " beat" + (n.length > 1 ? "s" : "")}</small></span>`,
        )
        .join("");
    const p = document.createElement("p");
    p.style.width = "100%";
    p.textContent = ex.chords.join(" → ");
    $("#study-score").prepend(p);
  }
  function onAudio({ data }) {
    const t = trial;
    if (!t || data.id !== t.record.id || t.done) return;
    if (data.type === "samples") {
      const samples = data.samples, level = A.rms(samples);
      t.record.captureDiagnostics.maxDeliveryLagMs = Math.max(
        t.record.captureDiagnostics.maxDeliveryLagMs,
        Math.round(Math.max(0, ctx.currentTime - (data.frame + samples.length) / ctx.sampleRate) * 1000),
      );
      t.chunks.push(samples);
      const index = t.index++;
      t.writes = t.writes
        .then(() => S.chunk(t.record.id, index, samples))
        .catch((error) => {
          t.writeError = error;
          stop("Local saving failed. Download the retained audio.");
        });
      const detected = A.detectPitch(samples, ctx.sampleRate, {
          analysisSize: 2048,
          minimumRMS: 0.008,
        }),
        midi =
          detected && detected.clarity >= 0.8
            ? A.describePitch(detected.frequency)?.midi
            : null;
      const feedback = E.livePitch(
        midi == null ? null : 69 + 12 * Math.log2(detected.frequency / 440),
        t.ex, data.frame / ctx.sampleRate - t.answer + samples.length / ctx.sampleRate / 2,
        t.record.tempo, t.record.instrument,
      );
      const live = $("#study-live-pitch");
      live.hidden = false;
      live.dataset.state = feedback.state;
      msg("#study-played-note", feedback.played);
      msg("#study-target-note", feedback.target);
      msg("#study-pitch-cue", feedback.state === "listening" && level >= 0.008 ? "Sound detected · pitch unclear" : feedback.cue);
      t.frames.push({
        t: data.frame / ctx.sampleRate - t.answer,
        dt: samples.length / ctx.sampleRate,
        midi,
        rms: level,
      });
      if (!t.endingOptional && !t.interrupted && E.optionalHoldReleased(t.frames, t.record.tempo)) {
        t.endingOptional = true;
        t.end = ctx.currentTime;
        oscillators.forEach(o => { try { o.stop(); } catch {} });
        oscillators.clear();
        node.port.postMessage({type: "stop"});
      }
    } else if (data.type === "done")
      finishTrial(t, data.frame).catch((error) => {
        running = false;
        msg("#study-feedback", error.message);
        emergency(t);
        cleanup();
        controls(false);
      });
  }
  function emergency(t) {
    if (emergencyURL) URL.revokeObjectURL(emergencyURL);
    emergencyURL = URL.createObjectURL(E.wav(t.chunks, t.record.sampleRate));
    const a = $("#study-emergency");
    a.href = emergencyURL;
    a.download = "Study-1-recovery-" + t.record.id + ".wav";
    a.hidden = false;
    msg(
      "#study-save",
      "Save failed. Download the retained audio before leaving this tab.",
    );
  }
  async function finishTrial(t, frame) {
    if (t.done) return;
    t.done = true;
    $("#study-reset").disabled = true;
    $("#study-beats").dataset.hit = "";
    $("#study-live-pitch").hidden = true;
    cancelAnimationFrame(uiTimer);
    const result = E.grade(t.frames, t.ex, t.record.tempo, t.interrupted),
      blob = E.wav(t.chunks, t.record.sampleRate);
    t.record.durationMs = Math.max(
      0,
      Math.min(
        Math.round(16 * t.beat * 1000),
        Math.round((frame / t.record.sampleRate - t.answer) * 1000),
      ),
    );
    t.record.audioBytes = blob.size;
    result.gradingLatencyMs = t.interrupted
      ? null
      : Math.max(0, Math.round(((ctx?.currentTime || t.end) - t.end) * 1000));
    t.record.result = result;
    msg("#study-phase", statusLabel(result.status));
    $("#study-feedback").dataset.status = result.status;
    msg(
      "#study-feedback",
      `${statusLabel(result.status)}${result.notes.length ? " · " + result.correct + "/7 notes" : ""} — ${result.feedback}`,
    );
    $("#study-score").hidden = false;
    $("#study-report").innerHTML = reportHTML(result);
    await t.writes;
    if (t.writeError) {
      running = false;
      emergency(t);
      trial = null;
      await cleanup();
      controls(false);
      return;
    }
    try {
      const saved = await S.chunks(t.record.id);
      if (E.wav(saved, t.record.sampleRate).size !== blob.size)
        throw new Error("Local recording verification failed");
      t.record.finalized = true;
      await S.put(t.record);
    } catch (error) {
      running = false;
      emergency(t);
      trial = null;
      await cleanup();
      controls(false);
      return;
    }
    msg("#study-save", "Uploading exercise audio and results…");
    trial = null;
    $("#study-continue").disabled = false;
    await renderHistory();
    sync();
    if (running && t.restartRequested) {
      position--;
      await nextTrial();
      return;
    }
    if (running) {
      pendingRepeat = E.repeatMissed(sequence, position, t.record.key, result, repeatProgress);
      $("#study-skip-repeat").hidden = !pendingRepeat;
      if (pendingRepeat && result.status !== "not-graded") {
        msg("#study-phase", `Repeating ${t.record.key} · ${repeatProgress.streak}/2 consecutive 7/7s · ${repeatProgress.streak === 1 ? "one more to move on" : "two in a row to move on"}`);
        msg("#study-position", `Round ${round} · ${position}/${sequence.length} · ${settings.tempo} BPM`);
      }
      if (result.status === "not-graded") {
        reviewPaused = true;
        $("#study-continue").hidden = false;
        $("#study-continue").textContent = "Retry after audio check";
        msg("#study-phase", "Audio check · paused · streak unchanged");
      } else if (reviewPaused) msg("#study-phase", "Paused to review · continue when ready");
      $("#study-reset").disabled = false;
      feedbackTimer = setTimeout(
        () => {
          if (reviewPaused) return;
          nextTrial().catch(async (e) => {
            msg("#study-feedback", e.message);
            await stop("Local save failed.");
          });
        },
        pendingRepeat ? 4000 : 2000,
      );
    } else {
      await cleanup();
      controls(false);
    }
  }
  function restartRound() {
    if (!running || $("#study-reset").disabled || (trial && trial.done)) return;
    $("#study-reset").disabled = true;
    clearTimeout(feedbackTimer);
    reviewPaused = false;
    oscillators.forEach(o => { try { o.stop(); } catch {} });
    oscillators.clear();
    if (trial) {
      const t = trial;
      t.restartRequested = true;
      t.interrupted = true;
      node.port.postMessage({type: "stop"});
      setTimeout(() => {
        if (trial === t && !t.done) finishTrial(t, Math.round((ctx?.currentTime || t.start) * t.record.sampleRate));
      }, 350);
    } else {
      // Reuse an already queued recovery repetition; otherwise replay this key.
      if (!pendingRepeat) position--;
      nextTrial().catch(error => stop(error.message));
    }
  }
  async function cleanup() {
    $("#study-continue").hidden = true;
    await globalThis.JazzTrainingSessionTake.finish().catch(error => msg("#study-session-save", error.message));
    pendingRepeat = false;
    $("#study-skip-repeat").hidden = true;
    $("#study-live-pitch").hidden = true;
    cancelAnimationFrame(uiTimer);
    clearTimeout(feedbackTimer);
    oscillators.forEach((o) => {
      try {
        o.stop();
      } catch {}
    });
    oscillators.clear();
    node?.disconnect();
    source?.disconnect();
    sink?.disconnect();
    stream?.getTracks().forEach((t) => t.stop());
    stream = null;
    const old = ctx;
    ctx = null;
    node = null;
    source = null;
    sink = null;
    if (old) await old.close().catch(() => {});
    releaseLock?.();
    releaseLock = null;
  }
  async function stop(reason = "Session finished.") {
    starting = false;
    running = false;
    clearTimeout(feedbackTimer);
    oscillators.forEach((o) => {
      try {
        o.stop();
      } catch {}
    });
    oscillators.clear();
    if (trial && !trial.done) {
      trial.interrupted = true;
      node?.port.postMessage({ type: "stop" });
      const t = trial;
      setTimeout(() => {
        if (trial === t && !t.done)
          finishTrial(
            t,
            Math.round((ctx?.currentTime || t.start) * t.record.sampleRate),
          );
      }, 350);
    } else if (!trial) {
      await cleanup();
      controls(false);
      msg("#study-phase", reason);
      await renderHistory();
    }
  }
  async function initialize() {
    if (initialized) return;
    initialized = true;
    try {
      await S.open();
      await navigator.locks.request(
        "jazz-study-one-capture",
        { ifAvailable: true },
        async (lock) => {
          if (lock) { await S.recover(); await globalThis.JazzTrainingSessionTake.initialize(); }
        },
      );
      msg(
        "#study-save",
        "Ready · session video uploads automatically when you finish.",
      );
      await renderHistory();
      sync();
      const devices = await navigator.mediaDevices?.enumerateDevices();
      for (const [i, d] of (devices || []).filter(d => d.kind === "videoinput").entries())
        $("#study-camera").add(new Option(d.label || `Camera ${i+1}`,d.deviceId));
      for (const [i, d] of (devices || [])
        .filter((d) => d.kind === "audioinput")
        .entries())
        $("#study-input").add(
          new Option(d.label || `Microphone ${i + 1}`, d.deviceId),
        );
    } catch (error) {
      $("#study-start").disabled = true;
      msg(
        "#study-save",
        "Local recording storage unavailable: " + error.message,
      );
    }
  }
  function setTempo(value) {
    const tempo = Number(value);
    if (!Number.isInteger(tempo) || tempo < 40 || tempo > 160) return;
    $("#study-tempo").value = tempo;
    if ($("#study-tempo-slider")) $("#study-tempo-slider").value = tempo;
    if (!running && !starting) msg("#study-position", `${tempo} BPM · four-bar answer`);
    try { localStorage.setItem("jazz-study-one-tempo", String(tempo)); } catch (_) {}
  }
  try {
    const savedTempo = localStorage.getItem("jazz-study-one-tempo");
    if (savedTempo) setTempo(savedTempo);
  } catch (_) {}
  $("#study-history-tempo").value = $("#study-tempo").value;
  $("#study-tempo").addEventListener("input", (event) => setTempo(event.target.value));
  $("#study-tempo-slider")?.addEventListener("input", (event) => setTempo(event.target.value));
  $("#study-setup").addEventListener("submit", (event) =>
    event.preventDefault(),
  );
  $("#study-start").addEventListener("click", start);
  $("#study-report").addEventListener("toggle", (event) => {
    if (event.target.open && running) {
      reviewPaused = true;
      clearTimeout(feedbackTimer);
      $("#study-continue").hidden = false;
      $("#study-continue").disabled = Boolean(trial);
      msg("#study-phase", "Paused to review · continue when ready");
    }
  }, true);
  $("#study-continue").addEventListener("click", () => {
    if (!running || trial) return;
    clearTimeout(feedbackTimer);
    nextTrial().catch(error => stop(error.message));
  });
  $("#study-skip-repeat").addEventListener("click", () => {
    if (!running || !pendingRepeat || trial) return;
    sequence.splice(position, 1);
    repeatProgress = {};
    pendingRepeat = false;
    $("#study-skip-repeat").hidden = true;
    clearTimeout(feedbackTimer);
    nextTrial().catch((error) => stop(error.message));
  });
  $("#study-reset").addEventListener("click", restartRound);
  document.addEventListener("keydown", event => {
    if (location.hash !== "#training" || event.repeat || event.ctrlKey || event.metaKey || event.altKey || event.key.toLowerCase() !== "r") return;
    if (event.target.closest?.("input, textarea, select, [contenteditable]")) return;
    if (running) { event.preventDefault(); restartRound(); }
  });
  $("#study-stop").addEventListener("click", () => stop());
  $("#study-sync").addEventListener("click", sync);
  $("#study-history-instrument").addEventListener("change", () =>
    renderHistory(),
  );
  $("#study-history-mode").addEventListener("change", () => renderHistory());
  $("#study-history-tempo").addEventListener("change", () => renderHistory());
  $("#study-retry").addEventListener("click", async () => {
    const attempts = (await history()).filter(
        (a) => a.sessionId === sessionId && a.result.status === "needs-work",
      ),
      missed = new Set(attempts.map((a) => a.key));
    if (!missed.size) {
      msg("#study-feedback", "No missed keys in that session.");
      return;
    }
    $("#study-mode").value = "learn";
    modeChanged();
    $("#study-keys")
      .querySelectorAll("input")
      .forEach((b) => (b.checked = missed.has(b.value)));
    start();
  });
  document.addEventListener("jazz:view-change", (e) => {
    if (e.detail.view === "training") initialize();
    else if (running || starting)
      stop("Training paused after leaving the page.");
  });
  document.addEventListener("visibilitychange", () => {
    if (document.hidden && (running || starting))
      stop("Training interrupted when the tab was hidden.");
  });
  addEventListener("pagehide", () => {
    if (running || starting) stop("Page closed.");
  });
  addEventListener("online", () => {
    if (initialized) sync();
  });
  addEventListener("beforeunload", (e) => {
    if (running || trial || starting || globalThis.JazzTrainingSessionTake.isBusy?.()) {
      e.preventDefault();
      e.returnValue = "";
    }
  });
  globalThis.JazzStudyTraining = {
    isBusy: () => running || starting || !!trial || !!globalThis.JazzTrainingSessionTake.isBusy?.(),
  };
  modeChanged();
  if (location.hash === "#training") initialize();
})();
