(() => {
  "use strict";

  const NOTE_NAMES = ["C", "C♯", "D", "E♭", "E", "F", "F♯", "G", "A♭", "A", "B♭", "B"];

  function rms(samples) {
    if (!samples?.length) return 0;
    let energy = 0;
    for (let index = 0; index < samples.length; index += 1) energy += samples[index] * samples[index];
    return Math.sqrt(energy / samples.length);
  }

  function detectPitch(samples, sampleRate, options = {}) {
    if (!samples?.length || !Number.isFinite(sampleRate) || sampleRate <= 0) return null;
    const minimumFrequency = Number(options.minimumFrequency || 65);
    const maximumFrequency = Number(options.maximumFrequency || 1400);
    const threshold = Number(options.threshold || 0.12);
    const minimumRMS = Number(options.minimumRMS || 0.012);
    const size = Math.min(samples.length, Number(options.analysisSize || 2048));
    const level = rms(samples.subarray ? samples.subarray(0, size) : samples.slice(0, size));
    if (level < minimumRMS) return null;

    const minimumLag = Math.max(2, Math.floor(sampleRate / maximumFrequency));
    const maximumLag = Math.min(size - 2, Math.ceil(sampleRate / minimumFrequency));
    if (maximumLag <= minimumLag) return null;

    const difference = new Float32Array(maximumLag + 1);
    const compareLength = size - maximumLag;
    for (let lag = 1; lag <= maximumLag; lag += 1) {
      let sum = 0;
      for (let index = 0; index < compareLength; index += 1) {
        const delta = samples[index] - samples[index + lag];
        sum += delta * delta;
      }
      difference[lag] = sum;
    }

    let cumulative = 0;
    difference[0] = 1;
    for (let lag = 1; lag <= maximumLag; lag += 1) {
      cumulative += difference[lag];
      difference[lag] = cumulative ? (difference[lag] * lag) / cumulative : 1;
    }

    let bestLag = -1;
    for (let lag = minimumLag; lag <= maximumLag; lag += 1) {
      if (difference[lag] >= threshold) continue;
      while (lag + 1 <= maximumLag && difference[lag + 1] < difference[lag]) lag += 1;
      bestLag = lag;
      break;
    }
    if (bestLag < 0) return null;

    const left = difference[bestLag - 1] ?? difference[bestLag];
    const center = difference[bestLag];
    const right = difference[bestLag + 1] ?? difference[bestLag];
    const denominator = 2 * (2 * center - right - left);
    const refinedLag = denominator ? bestLag + ((right - left) / denominator) : bestLag;
    const frequency = sampleRate / refinedLag;
    if (!Number.isFinite(frequency) || frequency < minimumFrequency || frequency > maximumFrequency) return null;
    return { frequency, clarity: Math.max(0, Math.min(1, 1 - center)), rms: level };
  }

  function describePitch(frequency, reference = 440) {
    if (!Number.isFinite(frequency) || frequency <= 0 || !Number.isFinite(reference) || reference <= 0) return null;
    const midi = 69 + (12 * Math.log2(frequency / reference));
    const nearestMidi = Math.round(midi);
    const cents = (midi - nearestMidi) * 100;
    const noteIndex = ((nearestMidi % 12) + 12) % 12;
    return {
      frequency,
      midi,
      nearestMidi,
      note: NOTE_NAMES[noteIndex],
      octave: Math.floor(nearestMidi / 12) - 1,
      cents,
    };
  }

  function stablePitch(history, maximumSpreadCents = 18) {
    if (!Array.isArray(history) || history.length < 3) return null;
    const sorted = history.map((entry) => entry.frequency).filter(Number.isFinite).sort((a, b) => a - b);
    if (sorted.length < 3) return null;
    const median = sorted[Math.floor(sorted.length / 2)];
    const lowCents = 1200 * Math.log2(sorted[0] / median);
    const highCents = 1200 * Math.log2(sorted[sorted.length - 1] / median);
    if (highCents - lowCents > maximumSpreadCents) return null;
    return describePitch(median);
  }

  globalThis.JazzAudioAnalysis = { NOTE_NAMES, rms, detectPitch, describePitch, stablePitch };
})();
