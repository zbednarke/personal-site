const { test } = require("node:test");
const assert = require("node:assert/strict");
require("./study-one-engine.js");
const E = globalThis.JazzStudyOne;
function performance(ex, tempo = 80, change = (f) => f) {
  const beat = 60 / tempo,
    frames = [];
  for (let t = -4 * beat; t < 16 * beat - 0.001; t += 0.04) {
    const n = ex.notes.find(
      (n) => t >= n.beat * beat && t < (n.beat + n.length) * beat,
    );
    frames.push(
      change({ t, dt: 0.04, midi: n?.midi ?? null, rms: n ? 0.15 : 0 }, n),
    );
  }
  return frames;
}
test("PDF note spellings and all twelve destination keys", () => {
  const expected = {
    C: "D F A C B G C",
    "B♭": "C E♭ G B♭ A F B♭",
    "A♭": "B♭ D♭ F A♭ G E♭ A♭",
    "G♭": "A♭ C♭ E♭ G♭ F D♭ G♭",
    E: "F♯ A C♯ E D♯ B E",
    D: "E G B D C♯ A D",
    B: "C♯ E G♯ B A♯ F♯ B",
    A: "B D F♯ A G♯ E A",
    G: "A C E G F♯ D G",
    F: "G B♭ D F E C F",
    "E♭": "F A♭ C E♭ D B♭ E♭",
    "D♭": "E♭ G♭ B♭ D♭ C A♭ D♭",
  };
  for (const k of E.keys) {
    const ex = E.exercise(k.name, "concert");
    assert.equal(ex.notes.map((n) => n.name).join(" "), expected[k.name]);
    assert.deepEqual(
      ex.notes.map((n) => n.beat),
      [0, 1, 2, 3, 4, 6, 8],
    );
    assert.equal(
      ex.notes.reduce((s, n) => s + n.length, 0),
      16,
    );
    assert.deepEqual(
      E.exercise(k.name).notes.map((n) => n.midi),
      ex.notes.map((n) => n.midi - 2),
    );
  }
});
test("correct performances pass at all keys and boundary tempos", () => {
  for (const key of E.keys)
    for (const tempo of [40, 80, 160]) {
      const ex = E.exercise(key.name);
      assert.equal(
        E.grade(performance(ex, tempo), ex, tempo).status,
        "pass",
        `${key.name} ${tempo}`,
      );
    }
});
test("octave displacement passes, semitone errors do not", () => {
  const ex = E.exercise("C");
  assert.equal(
    E.grade(
      performance(ex, 80, (f) => ({
        ...f,
        midi: f.midi === null ? null : f.midi + 12,
      })),
      ex,
      80,
    ).status,
    "pass",
  );
  assert.equal(
    E.grade(
      performance(ex, 80, (f, n) => ({
        ...f,
        midi: n && n.beat === 2 ? f.midi + 1 : f.midi,
      })),
      ex,
      80,
    ).correct,
    6,
  );
});
test("briefly touching right notes is insufficient; early release and breaths fail", () => {
  const ex = E.exercise("A♭");
  assert.notEqual(
    E.grade(
      performance(ex, 80, (f, n) => ({
        ...f,
        midi: n && f.t - n.beat * 0.75 > 0.15 ? null : f.midi,
      })),
      ex,
      80,
    ).status,
    "pass",
  );
  for (const cut of [(f) => f.t > 8.5, (f) => f.t > 7 && f.t < 7.4]) {
    const g = E.grade(
      performance(ex, 80, (f) => (cut(f) ? { ...f, midi: null, rms: 0 } : f)),
      ex,
      80,
    );
    assert.equal(g.holdOK, false);
  }
});
test("silence is missed response, noise and missing coverage are not graded", () => {
  const ex = E.exercise("D");
  assert.equal(
    E.grade(
      performance(ex, 80, (f) => ({ ...f, midi: null, rms: 0 })),
      ex,
      80,
    ).status,
    "needs-work",
  );
  assert.equal(
    E.grade(
      performance(ex, 80, (f) => ({ ...f, midi: null, rms: 0.2 })),
      ex,
      80,
    ).status,
    "not-graded",
  );
  assert.equal(E.grade([], ex, 80).status, "not-graded");
  assert.equal(E.grade([], ex, 80, true).status, "interrupted");
});
test("late and early entrances cannot pass", () => {
  const ex = E.exercise("D");
  const late = performance(ex).map((f) => ({ ...f, t: f.t + 0.25 }));
  assert.notEqual(E.grade(late, ex, 80).status, "pass");
  const early = performance(ex, 80, (f) =>
    f.t < 0 && f.t > -0.5 ? { ...f, midi: ex.notes[0].midi, rms: 0.1 } : f,
  );
  assert.equal(E.grade(early, ex, 80).early, true);
});
test("shuffled rounds have exact membership and no boundary repeat", () => {
  for (let i = 0; i < 100; i++) {
    const previous = "C",
      round = E.shuffle(
        E.keys.map((k) => k.name),
        previous,
      );
    assert.equal(new Set(round).size, 12);
    assert.notEqual(round[0], previous);
  }
  assert.deepEqual(E.shuffle(["C"], "C"), ["C"]);
});
test("WAV combines chunks with a valid length and clipping", async () => {
  const b = E.wav([new Float32Array([0, 1]), new Float32Array([-1, 2])], 48000),
    v = new DataView(await b.arrayBuffer());
  assert.equal(b.size, 52);
  assert.equal(v.getUint32(40, true), 8);
  assert.equal(v.getInt16(50, true), 32767);
});
require("./audio-analysis.js");
test("real pitch detector grades synthesized trumpet-range phrases across all keys", () => {
  const A = globalThis.JazzAudioAnalysis,
    rate = 48000,
    chunk = 4096,
    tempo = 80,
    beat = 60 / tempo;
  for (const k of E.keys) {
    const ex = E.exercise(k.name),
      frames = [];
    for (let at = 0; at < 20 * beat * rate; at += chunk) {
      const samples = new Float32Array(Math.min(chunk, 20 * beat * rate - at));
      for (let i = 0; i < samples.length; i++) {
        const t = (at + i) / rate - 4 * beat,
          n = ex.notes.find(
            (n) => t >= n.beat * beat && t < (n.beat + n.length) * beat,
          );
        if (n) {
          const f = 440 * 2 ** ((n.midi - 69) / 12);
          samples[i] =
            0.15 * Math.sin((2 * Math.PI * f * (at + i)) / rate) +
            0.04 * Math.sin((4 * Math.PI * f * (at + i)) / rate);
        }
      }
      const pitch = A.detectPitch(samples, rate, { minimumRMS: 0.008 });
      frames.push({
        t: at / rate - 4 * beat,
        dt: samples.length / rate,
        rms: A.rms(samples),
        midi: pitch ? A.describePitch(pitch.frequency).midi : null,
      });
    }
    assert.equal(E.grade(frames, ex, tempo).status, "pass", k.name);
  }
});

test("live feedback transposes, directs toward nearest octave and follows the beat", () => {
  for (const instrument of ["bb-trumpet", "concert"]) {
    for (const key of E.keys) {
      const ex = E.exercise(key.name, instrument);
      for (const n of ex.notes) {
        const time = (n.beat + 0.2) * 60 / 50;
        for (const octave of [-12, 0, 12]) {
          const correct = E.livePitch(n.midi + octave, ex, time, 50, instrument);
          assert.equal(correct.state, "correct");
          assert.equal(correct.played, n.name);
          assert.equal(E.livePitch(n.midi + octave - 1, ex, time, 50, instrument).cue, "↑ Play higher");
          assert.equal(E.livePitch(n.midi + octave + 1, ex, time, 50, instrument).cue, "↓ Play lower");
        }
      }
      assert.equal(E.livePitch(null, ex, 0, 50, instrument).state, "listening");
      assert.equal(E.livePitch(60, ex, -1, 50, instrument).target, "—");
      assert.equal(E.livePitch(60, ex, 20, 50, instrument).state, "waiting");
    }
  }
});

test("missed keys repeat immediately and extend the round until 7/7", () => {
  const sequence = ["C", "D", "E"];
  assert.equal(E.repeatMissed(sequence, 1, "C", {correct: 3, status: "needs-work"}), true);
  assert.deepEqual(sequence, ["C", "C", "D", "E"]);
  E.repeatMissed(sequence, 2, "C", {correct: 6, status: "needs-work"});
  assert.deepEqual(sequence, ["C", "C", "C", "D", "E"]);
  assert.equal(E.repeatMissed(sequence, 3, "C", {correct: 7, status: "pass"}), false);
  assert.equal(sequence[3], "D");
});
test("last-key miss extends the round; skip removes only its pending repeat", () => {
  const sequence = ["C", "D"];
  E.repeatMissed(sequence, 2, "D", {correct: 0, status: "not-graded"});
  assert.deepEqual(sequence, ["C", "D", "D"]);
  sequence.splice(2, 1);
  assert.deepEqual(sequence, ["C", "D"]);
  assert.equal(E.repeatMissed(sequence, 2, "D", {correct: 0, status: "interrupted"}), false);
  assert.equal(E.repeatMissed(sequence, 2, "D", {correct: 7, status: "needs-work"}), false);
});

 test("tonic second bar never penalizes silence, wrong notes, noise or absent analysis", () => {
   for (const tempo of [40, 50, 80, 160]) {
     const ex = E.exercise("A♭");
     const full = performance(ex, tempo);
     for (const tail of [null, 61, NaN]) {
       const frames = full.map(f => f.t >= 12 * 60 / tempo ? {...f, midi: tail, rms: tail === null ? 0 : 0.5} : f);
       const result = E.grade(frames, ex, tempo);
       assert.equal(result.status, "pass");
       assert.equal(result.correct, 7);
       assert.equal(result.holdOK, true);
     }
     assert.equal(E.grade(full.filter(f => f.t < 12 * 60 / tempo), ex, tempo).status, "pass");
     assert.match(E.livePitch(null, ex, 13 * 60 / tempo, tempo).cue, /Optional hold/);
   }
 });

test("optional hold release requires sustained quiet after the required first bar", () => {
  for (const tempo of [40,50,80,160]) {
    const start=12*60/tempo;
    const quiet=(from,count,rms=0)=>Array.from({length:count},(_,i)=>({t:from+i*0.05,dt:0.05,rms}));
    assert.equal(E.optionalHoldReleased(quiet(start-1,20),tempo),false);
    assert.equal(E.optionalHoldReleased(quiet(start,4),tempo),false);
    assert.equal(E.optionalHoldReleased(quiet(start,7),tempo),true);
    assert.equal(E.optionalHoldReleased(quiet(start,10,0.1),tempo),false);
    assert.equal(E.optionalHoldReleased([...quiet(start,4),...quiet(start+0.2,1,0.1),...quiet(start+0.25,4)],tempo),false);
    assert.equal(E.optionalHoldReleased([...quiet(start,4),...quiet(start+1,4)],tempo),false);
  }
});

test("first-try 7/7 advances; after a miss two consecutive 7/7s are required", () => {
  const sequence=["C","D","E"], progress={};
  const result=correct=>({correct,status:correct===7?"pass":"needs-work"});
  assert.equal(E.repeatMissed(sequence,1,"C",result(7),progress),false);
  assert.equal(E.repeatMissed(sequence,2,"D",result(5),progress),true);
  assert.equal(progress.streak,0);
  assert.equal(E.repeatMissed(sequence,3,"D",result(7),progress),true);
  assert.equal(progress.streak,1);
  assert.equal(E.repeatMissed(sequence,4,"D",result(6),progress),true);
  assert.equal(progress.streak,0);
  assert.equal(E.repeatMissed(sequence,5,"D",result(7),progress),true);
  assert.equal(E.repeatMissed(sequence,6,"D",result(7),progress),false);
  assert.equal(sequence[6],"E");
  assert.equal(E.repeatMissed(sequence,7,"E",result(7),progress),false);
});
test("interruption does not advance recovery; skip resets it for the next encounter", () => {
  const sequence=["C"], progress={};
  E.repeatMissed(sequence,1,"C",{correct:0,status:"needs-work"},progress);
  E.repeatMissed(sequence,2,"C",{correct:7,status:"needs-work"},progress);
  assert.equal(progress.streak,1);
  assert.equal(E.repeatMissed(sequence,3,"C",{correct:0,status:"interrupted"},progress),false);
  assert.equal(progress.streak,1);
  assert.equal(E.repeatMissed(sequence,3,"C",{correct:7,status:"pass"},{}),false);
});

test("pendulum reaches alternating endpoints exactly on each scheduled beat", () => {
 for(const bpm of [40,50,80,160]) {
  const beat=60/bpm;
  for(let n=0;n<20;n++) {
   const f=E.metronomeFrame(n*beat,beat);
   assert.ok(Math.abs(f.angle-(n%2?40:-40))<1e-9);
   assert.equal(f.number,n%4+1);assert.equal(f.hit,true);
   assert.equal(f.side,n%2?'right':'left');
   const halfway=E.metronomeFrame((n+0.5)*beat,beat);
   assert.ok(Math.abs(halfway.angle)<1e-9);assert.equal(halfway.hit,false);
  }
  assert.equal(E.metronomeFrame(-0.1,beat).hit,false);
 }
});

test("reports name the wrong written note and provide a targeted correction", () => {
 for(const instrument of ['bb-trumpet','concert']) {
  const ex=E.exercise('C',instrument);
  const result=E.grade(performance(ex,80,(f,n)=>n===ex.notes[1]?{...f,midi:n.midi+1}:f),ex,80);
  const issue=result.report.issues.find(i=>i.type==='wrong-note');
  assert.equal(issue.location,'Bar 1, beat 2');
  assert.match(issue.message,/Expected F; mostly heard G♭/);
  assert.match(issue.advice,/1 semitone lower/);
 }
});
test("reports distinguish silent, unstable, short notes and late entrances", () => {
 const ex=E.exercise('C');
 const missing=E.grade(performance(ex,80,(f,n)=>n===ex.notes[2]?{...f,midi:null,rms:0}:f),ex,80);
 assert.ok(missing.report.issues.some(i=>i.type==='missing-note'));
 const unclear=E.grade(performance(ex,80,(f,n)=>n===ex.notes[2]?{...f,midi:null,rms:0.2}:f),ex,80);
 assert.ok(unclear.report.issues.some(i=>i.type==='unclear-pitch'));
 const short=E.grade(performance(ex,80,(f,n)=>n===ex.notes[2]&&f.t>1.95?{...f,midi:null,rms:0}:f),ex,80);
 assert.ok(short.report.issues.some(i=>i.type==='short-note'));
 const late=E.grade(performance(ex,80,(f,n)=>n===ex.notes[4]&&f.t<3.24?{...f,midi:null,rms:0}:f),ex,80);
 assert.ok(late.report.issues.some(i=>i.type==='late-entry'));
 assert.match(late.report.nextStep,/metronome endpoint/);
});
test("7/7 hold failure is explained separately; optional silence adds no issue", () => {
 const ex=E.exercise('C');
 const gap=E.grade(performance(ex,80,(f)=>f.t>7&&f.t<7.3?{...f,midi:null,rms:0}:f),ex,80);
 assert.equal(gap.correct,7);
 assert.ok(gap.report.issues.some(i=>i.type==='hold-gap'));
 const released=E.grade(performance(ex,80,(f)=>f.t>=9?{...f,midi:null,rms:0}:f),ex,80);
 assert.equal(released.report.issues.length,0);
});


test("intermittent pitch recognition is unscorable, not seven wrong notes", () => {
  const ex = E.exercise("E♭");
  let index = 0;
  const frames = performance(ex, 50, f => ({...f, rms: 0.1, midi: index++ % 3 === 0 ? f.midi : null}));
  const result = E.grade(frames, ex, 50);
  assert.equal(result.status, "not-graded");
  assert.equal(result.notes.length, 0);
  assert.ok(result.signalQuality.trackedFraction > 0.2);
  assert.match(result.feedback, /streak is unchanged/);
});

test("unscorable retries preserve recovery streak and do not create a new penalty", () => {
  const sequence = ["F", "E♭"], progress = {};
  const unknown = {status: "not-graded", correct: 0};
  E.repeatMissed(sequence, 1, "F", unknown, progress);
  assert.deepEqual(progress, {});
  assert.equal(E.repeatMissed(sequence, 2, "F", {status: "pass", correct: 7}, progress), false);
  E.repeatMissed(sequence, 3, "E♭", {status: "needs-work", correct: 5}, progress);
  E.repeatMissed(sequence, 4, "E♭", {status: "pass", correct: 7}, progress);
  const before = {...progress};
  E.repeatMissed(sequence, 5, "E♭", unknown, progress);
  assert.deepEqual(progress, before);
  assert.equal(E.repeatMissed(sequence, 6, "E♭", {status: "pass", correct: 7}, progress), false);
});
