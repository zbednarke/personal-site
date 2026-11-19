import { createEmberModel } from "./model.js";

const canvas = document.querySelector("#ember-canvas");
const talk = document.querySelector("#talk");
const talkLabel = document.querySelector("#talk-label");
const talkHint = document.querySelector("#talk-hint");
const stateLabel = document.querySelector("#state");
const statusDot = document.querySelector("#status-dot");
const conversation = document.querySelector("#conversation");
const typedForm = document.querySelector("#typed-form");
const typedInput = document.querySelector("#typed-input");
const latency = document.querySelector("#latency");
const model = createEmberModel(canvas);

const history = [];
let stream = null;
let recorder = null;
let chunks = [];
let recordingStartedAt = 0;
let busy = false;
let activeAudio = null;
let audioContext = null;
let micAnalyser = null;
let micFrame = 0;
let outputFrame = 0;

function setState(kind, message) {
  model.setState(kind);
  stateLabel.textContent = message;
  statusDot.classList.toggle("live", ["listening", "thinking", "speaking"].includes(kind));
  statusDot.classList.toggle("error", kind === "error");
  talk.classList.toggle("recording", kind === "listening");
}

function message(role, text) {
  const article = document.createElement("article");
  article.className = `message ${role === "user" ? "user-message" : "ember-message"}`;
  const label = document.createElement("span");
  label.textContent = role === "user" ? "RECLAIMER" : "686";
  const copy = document.createElement("p");
  copy.textContent = text;
  article.append(label, copy);
  conversation.append(article);
  conversation.scrollTop = conversation.scrollHeight;
}

async function api(path, options) {
  const response = await fetch(`./api/v1/686/${path}`, options);
  if (response.ok) return response;
  let detail = "The private communications relay failed.";
  try {
    const body = await response.json();
    if (response.status === 401 && body.error === "signin_required") {
      const login = new URL("/auth/login", location.origin);
      login.searchParams.set("next", location.pathname + location.search);
      location.assign(login);
      detail = "Your session expired. Sign in again to reconnect.";
    } else if (body.error) {
      detail = body.error;
    }
  } catch (_) {}
  throw new Error(detail);
}

function preferredAudioType() {
  return ["audio/webm;codecs=opus", "audio/mp4", "audio/webm", "audio/ogg;codecs=opus"]
    .find((type) => globalThis.MediaRecorder?.isTypeSupported(type)) || "";
}

async function enableMicrophone() {
  if (stream) return true;
  if (!navigator.mediaDevices?.getUserMedia || !globalThis.MediaRecorder) {
    throw new Error("This browser does not provide microphone recording.");
  }
  stream = await navigator.mediaDevices.getUserMedia({
    audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
  });
  const type = preferredAudioType();
  recorder = new MediaRecorder(stream, type ? { mimeType: type } : undefined);
  recorder.addEventListener("dataavailable", (event) => {
    if (event.data.size) chunks.push(event.data);
  });
  recorder.addEventListener("stop", transmitRecording);
  talkLabel.textContent = "Hold to talk";
  talkHint.textContent = "release to transmit";
  talk.setAttribute("aria-label", "Hold to talk, release to transmit");
  setState("idle", "Microphone ready. Hold the control and speak.");
  beginMicMeter();
  return true;
}

async function ensureAudioContext() {
  audioContext ||= new (globalThis.AudioContext || globalThis.webkitAudioContext)();
  if (audioContext.state === "suspended") await audioContext.resume();
  return audioContext;
}

async function beginMicMeter() {
  const context = await ensureAudioContext();
  micAnalyser = context.createAnalyser();
  micAnalyser.fftSize = 256;
  context.createMediaStreamSource(stream).connect(micAnalyser);
  const data = new Uint8Array(micAnalyser.frequencyBinCount);
  const tick = () => {
    micAnalyser.getByteFrequencyData(data);
    const average = data.reduce((sum, value) => sum + value, 0) / data.length / 255;
    if (recorder?.state === "recording") model.setAudioLevel(Math.min(1, average * 3.4));
    micFrame = requestAnimationFrame(tick);
  };
  cancelAnimationFrame(micFrame);
  tick();
}

async function startRecording(event) {
  event?.preventDefault();
  if (busy || recorder?.state === "recording") return;
  try {
    stopSpeech();
    if (!stream) {
      await enableMicrophone();
      return;
    }
    chunks = [];
    recorder.start(250);
    recordingStartedAt = performance.now();
    setState("listening", "Listening… release when you are finished.");
  } catch (error) {
    setState("error", error.message || "Microphone permission was not granted.");
  }
}

function stopRecording(event) {
  event?.preventDefault();
  if (recorder?.state !== "recording") return;
  if (performance.now() - recordingStartedAt < 260) {
    recorder.stop();
    setState("idle", "That press was too brief. Hold while speaking, then release.");
    return;
  }
  recorder.stop();
  setState("thinking", "Transcribing your transmission…");
}

async function transmitRecording() {
  if (performance.now() - recordingStartedAt < 260 || chunks.length === 0) return;
  busy = true;
  talk.disabled = true;
  const type = recorder.mimeType || chunks[0].type || "audio/webm";
  const extension = type.includes("mp4") ? "m4a" : type.includes("ogg") ? "ogg" : "webm";
  const audio = new Blob(chunks, { type });
  const form = new FormData();
  form.append("audio", audio, `utterance.${extension}`);
  const started = performance.now();
  try {
    const response = await api("transcribe", { method: "POST", body: form });
    const { transcript } = await response.json();
    await converse(transcript, started);
  } catch (error) {
    fail(error);
  } finally {
    busy = false;
    talk.disabled = false;
  }
}

async function converse(text, started = performance.now()) {
  const utterance = String(text || "").trim();
  if (!utterance) return;
  message("user", utterance);
  history.push({ role: "user", content: utterance });
  while (history.length > 15) history.shift();
  setState("thinking", "Consulting containment protocols…");

  const response = await api("respond", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ messages: history }),
  });
  const body = await response.json();
  const line = String(body.response || "").trim();
  if (!line) throw new Error("The construct returned an empty response.");
  history.push({ role: "assistant", content: line });
  while (history.length > 15) history.shift();
  message("assistant", line);
  latency.textContent = `${Math.round(performance.now() - started)} MS`;
  await speak(line);
}

async function speak(text) {
  setState("thinking", "Synthesizing vocal matrix…");
  const response = await api("speak", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ text }),
  });
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const audio = new Audio(url);
  activeAudio = audio;
  audio.addEventListener("playing", () => setState("speaking", "686 Culpable Ember is speaking."));
  audio.addEventListener("ended", () => {
    URL.revokeObjectURL(url);
    if (activeAudio === audio) activeAudio = null;
    model.setAudioLevel(0);
    setState("idle", "Channel open. Hold to speak again.");
  }, { once: true });
  audio.addEventListener("error", () => {
    URL.revokeObjectURL(url);
    fail(new Error("The browser could not play the synthesized voice."));
  }, { once: true });

  const context = await ensureAudioContext();
  const source = context.createMediaElementSource(audio);
  const analyser = context.createAnalyser();
  analyser.fftSize = 256;
  source.connect(analyser);
  analyser.connect(context.destination);
  const data = new Uint8Array(analyser.frequencyBinCount);
  const meter = () => {
    if (audio.paused || audio.ended) return;
    analyser.getByteFrequencyData(data);
    const average = data.reduce((sum, value) => sum + value, 0) / data.length / 255;
    model.setAudioLevel(Math.min(1, average * 2.8));
    outputFrame = requestAnimationFrame(meter);
  };
  cancelAnimationFrame(outputFrame);
  await audio.play();
  meter();
}

function stopSpeech() {
  if (!activeAudio) return;
  activeAudio.pause();
  activeAudio.currentTime = 0;
  activeAudio = null;
  cancelAnimationFrame(outputFrame);
  model.setAudioLevel(0);
}

function fail(error) {
  console.error(error);
  setState("error", error?.message || "The communications relay failed.");
}

let heldPointer = null;
let heldKey = null;

talk.addEventListener("pointerdown", (event) => {
  if (event.button !== 0 || heldPointer !== null || heldKey !== null) return;
  heldPointer = event.pointerId;
  talk.setPointerCapture?.(event.pointerId);
  startRecording(event);
});
function releasePointer(event) {
  if (heldPointer !== event.pointerId) return;
  heldPointer = null;
  stopRecording(event);
}
talk.addEventListener("pointerup", releasePointer);
talk.addEventListener("pointercancel", releasePointer);
talk.addEventListener("lostpointercapture", releasePointer);
talk.addEventListener("contextmenu", (event) => event.preventDefault());

document.addEventListener("keydown", (event) => {
  const isTalkKey = event.code === "KeyV" || event.code === "Space";
  if (!isTalkKey || event.altKey || event.ctrlKey || event.metaKey) return;
  if (event.target.closest?.("input, textarea, select, [contenteditable]")) return;
  if (event.code === "Space" && event.target.closest?.("button, a") && event.target !== talk) return;
  event.preventDefault();
  if (event.repeat || heldKey !== null || heldPointer !== null) return;
  heldKey = event.code;
  startRecording(event);
});
document.addEventListener("keyup", (event) => {
  if (heldKey !== event.code) return;
  heldKey = null;
  stopRecording(event);
});
window.addEventListener("blur", () => {
  if (heldKey === null) return;
  heldKey = null;
  stopRecording();
});

typedForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  const text = typedInput.value.trim();
  if (!text || busy) return;
  typedInput.value = "";
  busy = true;
  talk.disabled = true;
  stopSpeech();
  try {
    await converse(text);
  } catch (error) {
    fail(error);
  } finally {
    busy = false;
    talk.disabled = false;
  }
});

window.addEventListener("pagehide", () => {
  stopSpeech();
  stream?.getTracks().forEach((track) => track.stop());
  cancelAnimationFrame(micFrame);
  model.dispose();
});
