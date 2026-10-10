(() => {
  "use strict";

  const NATURAL_PITCH_CLASSES = { C: 0, D: 2, E: 4, F: 5, G: 7, A: 9, B: 11 };
  const LETTERS = ["C", "D", "E", "F", "G", "A", "B"];
  const FLAT_ROOTS = ["C", "Db", "D", "Eb", "E", "F", "Gb", "G", "Ab", "A", "Bb", "B"];
  const QUALITY = {
    m7: { suffix: "m7", third: 3, seventh: 10 },
    m7b5: { suffix: "m7♭5", third: 3, seventh: 10 },
    7: { suffix: "7", third: 4, seventh: 10 },
    "7b9": { suffix: "7(♭9)", third: 4, seventh: 10 },
    maj7: { suffix: "maj7", third: 4, seventh: 11 },
  };

  function modulo(value, divisor = 12) {
    return ((value % divisor) + divisor) % divisor;
  }

  function normalizeRoot(root) {
    return String(root || "").replaceAll("♭", "b").replaceAll("♯", "#");
  }

  function pitchClassForRoot(root) {
    const match = /^([A-G])([b#]?)$/.exec(normalizeRoot(root));
    if (!match) return null;
    return modulo(NATURAL_PITCH_CLASSES[match[1]] + (match[2] === "b" ? -1 : match[2] === "#" ? 1 : 0));
  }

  function transposeRoot(root, semitones) {
    const pitchClass = pitchClassForRoot(root);
    return pitchClass == null ? String(root || "") : FLAT_ROOTS[modulo(pitchClass + semitones)];
  }

  function accidentalForDifference(difference) {
    if (difference === -2) return "𝄫";
    if (difference === -1) return "♭";
    if (difference === 1) return "♯";
    if (difference === 2) return "𝄪";
    return "";
  }

  function spellPitchClass(pitchClass, letter) {
    const natural = NATURAL_PITCH_CLASSES[letter];
    let difference = modulo(pitchClass - natural);
    if (difference > 6) difference -= 12;
    return `${letter}${accidentalForDifference(difference)}`;
  }

  function guideToneName(chord, degree, transpose = 0) {
    const quality = QUALITY[chord?.quality];
    const root = transposeRoot(chord?.root, transpose);
    const match = /^([A-G])/.exec(root);
    if (!quality || !match || (degree !== 3 && degree !== 7)) return "—";
    const rootLetterIndex = LETTERS.indexOf(match[1]);
    const targetLetter = LETTERS[modulo(rootLetterIndex + (degree === 3 ? 2 : 6), 7)];
    const rootPitchClass = pitchClassForRoot(root);
    const targetPitchClass = modulo(rootPitchClass + (degree === 3 ? quality.third : quality.seventh));
    return spellPitchClass(targetPitchClass, targetLetter);
  }

  function chordSymbol(chord, instrument = "concert") {
    const transpose = instrument === "bb-trumpet" ? 2 : 0;
    const quality = QUALITY[chord?.quality];
    return `${transposeRoot(chord?.root, transpose)}${quality?.suffix || chord?.quality || ""}`;
  }

  function targetForChord(chord, degree, instrument = "concert") {
    const quality = QUALITY[chord?.quality];
    const rootPitchClass = pitchClassForRoot(chord?.root);
    if (!quality || rootPitchClass == null || (degree !== 3 && degree !== 7)) return null;
    const concertPitchClass = modulo(rootPitchClass + (degree === 3 ? quality.third : quality.seventh));
    const writtenTranspose = instrument === "bb-trumpet" ? 2 : 0;
    return {
      degree,
      concertPitchClass,
      writtenPitchClass: modulo(concertPitchClass + writtenTranspose),
      concertName: guideToneName(chord, degree, 0),
      displayName: guideToneName(chord, degree, writtenTranspose),
    };
  }

  function flattenTune(tune) {
    const events = [];
    (tune?.bars || []).forEach((bar, barIndex) => {
      (bar || []).forEach((chord, chordIndex) => events.push({
        ...chord,
        barIndex,
        measureNumber: barIndex + 1,
        chordIndex,
      }));
    });
    return events;
  }

  function gradePitch(nearestMidi, expectedPitchClass) {
    if (!Number.isFinite(nearestMidi) || !Number.isFinite(expectedPitchClass)) return null;
    const playedPitchClass = modulo(Math.round(nearestMidi));
    return { playedPitchClass, correct: playedPitchClass === modulo(expectedPitchClass) };
  }

  function eventDurationMS(event, tempo) {
    const bpm = Math.max(1, Number(tempo) || 60);
    return (60000 / bpm) * Math.max(1, Number(event?.beats) || 4);
  }

  const api = {
    QUALITY,
    chordSymbol,
    eventDurationMS,
    flattenTune,
    gradePitch,
    guideToneName,
    modulo,
    pitchClassForRoot,
    targetForChord,
    transposeRoot,
  };
  globalThis.JazzGuideToneEngine = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})();
