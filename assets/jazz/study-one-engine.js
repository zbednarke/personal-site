(() => {
  "use strict";
  const keys = [
    ["C", 60, "C D E F G A B"],
    ["B♭", 58, "B♭ C D E♭ F G A"],
    ["A♭", 56, "A♭ B♭ C D♭ E♭ F G"],
    ["G♭", 54, "G♭ A♭ B♭ C♭ D♭ E♭ F"],
    ["E", 64, "E F♯ G♯ A B C♯ D♯"],
    ["D", 62, "D E F♯ G A B C♯"],
    ["B", 59, "B C♯ D♯ E F♯ G♯ A♯"],
    ["A", 57, "A B C♯ D E F♯ G♯"],
    ["G", 55, "G A B C D E F♯"],
    ["F", 65, "F G A B♭ C D E"],
    ["E♭", 63, "E♭ F G A♭ B♭ C D"],
    ["D♭", 61, "D♭ E♭ F G♭ A♭ B♭ C"],
  ].map(([name, midi, scale]) => ({ name, midi, scale: scale.split(" ") }));
  const mod = (n) => ((n % 12) + 12) % 12;
  function exercise(key, instrument = "bb-trumpet") {
    const k = keys.find((k) => k.name === key);
    if (!k) throw new Error("Unknown key");
    return {
      key,
      instrument,
      chords: [`${k.scale[1]}m7`, `${k.scale[4]}7`, `${key}maj7`],
      notes: [2, 5, 9, 12, 11, 7, 0].map((offset, i) => ({
        midi: k.midi + offset - (instrument === "bb-trumpet" ? 2 : 0),
        name: k.scale[[1, 3, 5, 0, 6, 4, 0][i]],
        beat: [0, 1, 2, 3, 4, 6, 8][i],
        length: [1, 1, 1, 1, 2, 2, 8][i],
      })),
    };
  }
  function livePitch(midi, ex, seconds, tempo, instrument = "bb-trumpet") {
    const beat = seconds * tempo / 60;
    if (beat >= 12 && beat < 16) return { state: "waiting", played: "—", target: "—", cue: "Optional hold · release to finish" };
    const target = ex.notes.find((n) => beat >= n.beat && beat < n.beat + n.length);
    if (!target) return { state: "waiting", played: "—", target: "—", cue: seconds < 0 ? "Count in" : "Answer complete" };
    if (!Number.isFinite(midi)) return { state: "listening", played: "—", target: target.name, cue: "Listening…" };
    const nearest = target.midi + 12 * Math.round((midi - target.midi) / 12);
    const delta = midi - nearest;
    const written = Math.round(midi) + (instrument === "bb-trumpet" ? 2 : 0);
    const names = ["C", "D♭", "D", "E♭", "E", "F", "G♭", "G", "A♭", "A", "B♭", "B"];
    const correct = Math.round(midi) === nearest;
    const played = (correct ? target.name : names[mod(written)]);
    return { state: correct ? "correct" : "wrong", played, target: target.name,
      cue: correct ? "● On the note" : delta < 0 ? "↑ Play higher" : "↓ Play lower" };
  }
  function metronomeFrame(elapsed, beatSeconds) {
    const phase = elapsed / beatSeconds;
    const beat = Math.floor(phase + 1e-9);
    return {
      angle: -40 * Math.cos(Math.PI * phase),
      beat,
      number: ((beat % 4) + 4) % 4 + 1,
      side: ((beat % 2) + 2) % 2 === 0 ? "left" : "right",
      hit: elapsed >= 0 && (phase - beat) * beatSeconds < Math.min(0.12, beatSeconds * 0.25),
    };
  }
  function optionalHoldReleased(frames, tempo) {
    const start = 12 * 60 / tempo;
    let quiet = 0, next = null;
    for (let i = frames.length - 1; i >= 0; i--) {
      const f = frames[i];
      if (f.t + f.dt <= start) break;
      if (!Number.isFinite(f.rms) || f.rms >= 0.008 ||
          (next !== null && Math.abs(f.t + f.dt - next) > 0.02)) break;
      quiet += Math.max(0, f.t + f.dt - Math.max(start, f.t));
      if (quiet >= 0.3) return true;
      next = f.t;
    }
    return false;
  }
  function repeatMissed(sequence, position, key, result, progress = {}) {
    if (result.status === "interrupted") return false;
    // An unreadable recording is neither a miss nor a successful recovery rep.
    if (result.status === "not-graded") {
      sequence.splice(position, 0, key);
      return true;
    }
    if (progress.key !== key) Object.assign(progress, { key, recovery: false, streak: 0 });
    if (result.correct !== 7) {
      progress.recovery = true;
      progress.streak = 0;
    } else if (progress.recovery) {
      progress.streak++;
      if (progress.streak >= 2) { progress.key = null; return false; }
    } else {
      progress.key = null;
      return false;
    }
    sequence.splice(position, 0, key);
    return true;
  }
  function shuffle(selection, previous, random = Math.random) {
    const result = [...selection];
    for (let i = result.length - 1; i > 0; i--) {
      const j = Math.floor(random() * (i + 1));
      [result[i], result[j]] = [result[j], result[i]];
    }
    if (result.length > 1 && result[0] === previous)
      [result[0], result[1]] = [result[1], result[0]];
    return result;
  }
  function grade(frames, ex, tempo, interrupted = false) {
    const beat = 60 / tempo,
      duration = 12 * beat;
    const all = frames.filter((f) => f.t >= 0 && f.t < duration);
    if (interrupted)
      return {
        status: "interrupted",
        correct: 0,
        notes: [],
        feedback: "Trial interrupted; excluded from accuracy.",
      };
    // Missing analysis coverage is not evidence of a musical mistake.
    const coverage = all.reduce((s, f) => s + f.dt, 0) / duration;
    const audible = all.filter((f) => f.rms >= 0.008);
    const audibleTime = audible.reduce((sum, f) => sum + f.dt, 0);
    const trackedTime = audible.filter(f => Number.isFinite(f.midi)).reduce((sum, f) => sum + f.dt, 0);
    const signalQuality = {capturedFraction: coverage, audibleSeconds: audibleTime,
      trackedFraction: audibleTime ? trackedTime / audibleTime : 0};
    if (
      coverage < 0.85 ||
      (audibleTime >= beat &&
        signalQuality.trackedFraction < 0.6)
    )
      return {
        status: "not-graded",
        signalQuality,
        correct: 0,
        notes: [],
        feedback:
          coverage < 0.85
            ? `Only ${Math.min(100,Math.round(coverage*100))}% of the required grading window was captured. No grade assigned; check the microphone connection and retry.`
            : `Audio check needed: pitch was readable in only ${Math.round(signalQuality.trackedFraction * 100)}% of the audible signal. This attempt is not scored and your streak is unchanged. Retry when ready; if recognition stays unreliable, finish this take and restart training to reconnect the microphone.`,
      };
    const matches = (f, n) =>
      Number.isFinite(f.midi) && mod(Math.round(f.midi)) === mod(n.midi);
    const notes = ex.notes.map((n) => {
      const start = n.beat * beat,
        end = (n.beat + (n.beat === 8 ? 4 : n.length)) * beat,
        margin = Math.min(0.12, beat * 0.18);
      const region = all.filter(
        (f) => f.t >= start + margin && f.t < end - margin,
      );
      const correctTime = region
        .filter((f) => matches(f, n))
        .reduce((s, f) => s + f.dt, 0);
      const ratio = correctTime / Math.max(0.01, end - start - 2 * margin);
      const onset = all.find(
        (f, i) =>
          f.t >= start - beat * 0.4 &&
          f.t < end &&
          matches(f, n) &&
          matches(all[i + 1] || {}, n),
      );
      const pitches = new Map();
      for (const frame of region)
        if (Number.isFinite(frame.midi)) {
          const pitch = Math.round(frame.midi);
          pitches.set(pitch, (pitches.get(pitch) || 0) + frame.dt);
        }
      const observed = [...pitches].sort((a, b) => b[1] - a[1])[0]?.[0] ?? null;
      const voicedTime = region.filter(f => Number.isFinite(f.midi)).reduce((sum,f)=>sum+f.dt,0);
      const regionTime = region.reduce((sum,f)=>sum+f.dt,0);
      const classes = new Map();
      for (const [pitch,time] of pitches) classes.set(mod(pitch),(classes.get(mod(pitch)) || 0)+time);
      const dominant = [...classes].sort((a,b)=>b[1]-a[1])[0];
      return {
        capturedFraction: Math.min(1, regionTime / Math.max(0.01, end-start-2*margin)),
        voicedFraction: regionTime ? voicedTime / regionTime : 0,
        audibleFraction: regionTime ? region.filter(f=>f.rms >= 0.008).reduce((sum,f)=>sum+f.dt,0)/regionTime : 0,
        dominantPitchClass: dominant?.[0] ?? null,
        pitchDominance: voicedTime ? (dominant?.[1] || 0)/voicedTime : 0,
        detectedMidi: observed,
        meanCents: region.filter((f) => matches(f, n)).length
          ? Math.round(
              region
                .filter((f) => matches(f, n))
                .reduce(
                  (sum, f) => sum + (f.midi - Math.round(f.midi)) * 100,
                  0,
                ) / region.filter((f) => matches(f, n)).length,
            )
          : null,
        name: n.name,
        beat: n.beat,
        correct: ratio >= 0.72,
        coverage: Math.min(1, ratio),
        offsetMs: onset ? Math.round((onset.t - start) * 1000) : null,
      };
    });
    const tonic = ex.notes[6];
    let gap = 0,
      maxGap = 0,
      held = 0;
    all
      .filter((f) => f.t >= 8 * beat + 0.15 && f.t < duration - 0.12)
      .forEach((f) => {
        if (matches(f, tonic)) {
          held += f.dt;
          gap = 0;
        } else {
          gap += f.dt;
          maxGap = Math.max(maxGap, gap);
        }
      });
    const holdOK =
      notes[6].correct && maxGap <= 0.22 && held >= 4 * beat - 0.55;
    const tolerance = Math.max(120, beat * 220);
    const timingOK = notes.every(
      (n) => n.offsetMs !== null && Math.abs(n.offsetMs) <= tolerance,
    );
    const early =
      frames
        .filter((f) => f.t < -0.15 && f.t >= -beat && Number.isFinite(f.midi))
        .reduce((s, f) => s + f.dt, 0) > 0.15;
    const correct = notes.filter((n) => n.correct).length;
    const status =
      correct === 7 && timingOK && holdOK && !early ? "pass" : "needs-work";
    const wrong = notes.find((n) => !n.correct);
    let feedback = wrong
      ? `Beat ${wrong.beat + 1}: expected ${wrong.name}.`
      : !holdOK
        ? "Hold the final tonic for its first four beats without a break."
        : early
          ? "Wait for the count-in to finish."
          : !timingOK
            ? "Notes correct; bring the entrances closer to the beat."
            : "All seven notes, steady timing, and a complete tonic hold.";
    const issues = [];
    const names = keys.find(k=>k.name===ex.key)?.scale.some(n=>n.includes("♯"))
      ? ["C","C♯","D","D♯","E","F","F♯","G","G♯","A","A♯","B"]
      : ["C","D♭","D","E♭","E","F","G♭","G","A♭","A","B♭","B"];
    const nameFor = pc => ex.notes.find(n=>mod(n.midi)===pc)?.name || names[mod(pc+(ex.instrument === "concert" ? 0 : 2))];
    notes.forEach((n,i)=>{
      n.location = `Bar ${Math.floor(n.beat/4)+1}, beat ${n.beat%4+1}`;
      n.heard = n.dominantPitchClass === null || n.pitchDominance < 0.6 ? null : nameFor(n.dominantPitchClass);
      const add = (type,message,advice)=>issues.push({noteIndex:i,location:n.location,type,message,advice});
      if (!n.correct) {
        if (n.capturedFraction < 0.85) add("missing-audio", `Not enough audio was captured to explain the ${n.name} result reliably.`, "Check the microphone connection and try again; this may be a capture problem.");
        else if (n.audibleFraction < 0.2) add("missing-note", `Expected ${n.name}; little or no sound was captured.`, "Check the input meter, then enter on the click.");
        else if (n.voicedFraction < 0.35 || n.pitchDominance < 0.6) add("unclear-pitch", `Expected ${n.name}; the pitch was not stable enough to identify confidently.`, "Replay this note; check the microphone and aim for a steady tone.");
        else if (n.dominantPitchClass !== mod(ex.notes[i].midi)) {
          const delta = mod(ex.notes[i].midi-n.dominantPitchClass+6)-6;
          add("wrong-note", `Expected ${n.name}; mostly heard ${n.heard}.`, `Aim ${Math.abs(delta)} semitone${Math.abs(delta)===1?"":"s"} ${delta>0?"higher":"lower"} toward ${n.name} (octave flexible).`);
        } else add("short-note", `${n.name} was found, but matched only ${Math.round(n.coverage*100)}% of its note window.`, "Keep the target sounding until the next change; avoid clipping its ending.");
      }
      if (n.correct && n.offsetMs !== null && Math.abs(n.offsetMs)>tolerance)
        add(n.offsetMs<0?"early-entry":"late-entry", `${n.name} arrived about ${Math.round(Math.abs(n.offsetMs)/10)*10} ms ${n.offsetMs<0?"early":"late"}.`, "Line up the attack with the metronome endpoint.");
    });
    if (!holdOK && notes[6].correct) issues.push({noteIndex:6,location:"Bar 3 · required tonic hold",type:"hold-gap",message:`${tonic.name} matched about ${Math.min(4,held/beat).toFixed(1)} of 4 required beats; longest break in matching pitch was ${Math.round(maxGap*1000)} ms.`,advice:"Keep the tonic steady for the first four beats. The following optional bar does not count."});
    if (early) issues.unshift({noteIndex:0,location:"Count-in",type:"count-in",message:"A pitched sound was detected before the answer began.",advice:"Wait through all four count-in clicks, then enter on the next click."});
    const primary = issues[0];
    const report = {version:1, pitchScore:`${correct}/7`, timing:timingOK && !early?"On time":"Needs attention", hold:holdOK?"First four beats complete":"Needs attention", issues,
      nextStep: primary ? `${primary.location}: ${primary.advice}` : "Keep the same centered sound and timing on the next key."};
    if (primary) feedback = `${primary.location}: ${primary.message}${issues.length>1 ? ` ${issues.length-1} more issue${issues.length===2?"":"s"} in the report.` : ""}`;
    return {
      status,
      signalQuality,
      correct,
      notes,
      report,
      timingOK,
      holdOK,
      holdBeats: Math.min(4, held / beat),
      longestHoldGapMs: Math.round(maxGap * 1000),
      responseMs: notes[0].offsetMs,
      early,
      feedback,
    };
  }
  function wav(chunks, rate) {
    const total = chunks.reduce((n, c) => n + c.length, 0),
      buffer = new ArrayBuffer(44 + total * 2),
      v = new DataView(buffer);
    const str = (at, s) =>
      [...s].forEach((c, i) => v.setUint8(at + i, c.charCodeAt(0)));
    str(0, "RIFF");
    v.setUint32(4, 36 + total * 2, true);
    str(8, "WAVEfmt ");
    v.setUint32(16, 16, true);
    v.setUint16(20, 1, true);
    v.setUint16(22, 1, true);
    v.setUint32(24, rate, true);
    v.setUint32(28, rate * 2, true);
    v.setUint16(32, 2, true);
    v.setUint16(34, 16, true);
    str(36, "data");
    v.setUint32(40, total * 2, true);
    let at = 44;
    for (const chunk of chunks)
      for (const sample of chunk) {
        v.setInt16(
          at,
          Math.max(-32768, Math.min(32767, Math.round(sample * 32767))),
          true,
        );
        at += 2;
      }
    return new Blob([buffer], { type: "audio/wav" });
  }
  function notation(ex, instrument = "bb-trumpet") {
    const natural = { C: 0, D: 2, E: 4, F: 5, G: 7, A: 9, B: 11 },
      letters = "CDEFGAB";
    const positions = [85, 160, 235, 310, 400, 490, 600, 690];
    const notes = [...ex.notes, ex.notes[6]];
    const staff = [56, 66, 76, 86, 96]
      .map((y) => `<line x1="24" x2="738" y1="${y}" y2="${y}"/>`)
      .join("");
    const symbols = notes
      .map((n, i) => {
        const letter = n.name[0],
          acc = n.name.includes("♭") ? -1 : n.name.includes("♯") ? 1 : 0;
        const written = n.midi + (instrument === "bb-trumpet" ? 2 : 0),
          octave = Math.round((written - natural[letter] - acc) / 12) - 1;
        const y = 96 - (octave * 7 + letters.indexOf(letter) - 30) * 5,
          x = positions[i];
        let ledger = "";
        for (let ly = 106; ly <= y; ly += 10)
          ledger += `<line x1="${x - 12}" x2="${x + 12}" y1="${ly}" y2="${ly}"/>`;
        for (let ly = 46; ly >= y; ly -= 10)
          ledger += `<line x1="${x - 12}" x2="${x + 12}" y1="${ly}" y2="${ly}"/>`;
        return `<g data-note="${Math.min(i, 6)}">${ledger}${acc ? `<text x="${x - 24}" y="${y + 5}" font-size="19">${acc < 0 ? "♭" : "♯"}</text>` : ""}<ellipse cx="${x}" cy="${y}" rx="8" ry="5" fill="${i < 4 ? "currentColor" : "#22261e"}" stroke="currentColor"/>${i < 6 ? `<line x1="${x + 7}" x2="${x + 7}" y1="${y}" y2="${y - 28}"/>` : ""}${i === 6 ? `<path d="M ${x + 6} ${y + 12} Q ${x + 45} ${y + 30} ${x + 84} ${y + 12}" fill="none" stroke="currentColor"/>` : ""}</g>`;
      })
      .join("");
    return `<svg viewBox="0 0 760 150" role="img" aria-label="Study 1 written notation, four quarter notes, two half notes, and two tied whole notes">${staff}<text x="26" y="93" font-size="48">𝄞</text>${[355, 550, 650, 738].map((x) => `<line x1="${x}" x2="${x}" y1="56" y2="96"/>`).join("")}${symbols}</svg>`;
  }
  globalThis.JazzStudyOne = { keys, exercise, shuffle, grade, wav, notation, livePitch, repeatMissed, optionalHoldReleased, metronomeFrame };
})();
