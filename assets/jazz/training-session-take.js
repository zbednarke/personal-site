(() => {
  "use strict";
  const store = globalThis.JazzTrainingTakeStore;
  const $ = (selector) => document.querySelector(selector);
  let active,
    finishing,
    syncing = false,
    syncRequested = false,
    retryTimer,
    retryDelay = 5000;
  const urls = [];
  function status(message) {
    $("#study-session-save").textContent = message;
  }
  async function prepare(id) {
    const now = new Date();
    const date = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
    const result = await globalThis.JazzPracticeSession.ensureGuidedBlocks(
      date,
      [
        {
          blockKey: "training-study-one-ii-v-i",
          position: 99,
          title: "Study 1 · ii–V–I training",
          instructions:
            "Five training sessions per day. Each session is one camera-and-microphone take.",
          category: "training",
          track: "training",
          targetMinutes: 20,
          dayOnly: true,
        },
      ],
      "add",
    );
    const block = result.blocks.find(
      (b) => b.blockKey === "training-study-one-ii-v-i",
    );
    if (!block) throw new Error("Could not connect the training section.");
    if (block.recordings?.length >= 20)
      throw new Error("This section has reached its 20-take limit.");
    return {
      id,
      practiceSessionID: result.session.id,
      blockContext: { id: block.id },
      recordedAt: now.toISOString(),
      takeNumber: (block.recordings?.length || 0) + 1,
      state: "capturing",
    };
  }
  async function start(stream, reservation, onFailure) {
    if (active || finishing)
      throw new Error("A training take is still being saved.");
    const mimeType = globalThis.JazzRecordingPolicy.preferredVideoType(
      MediaRecorder.isTypeSupported.bind(MediaRecorder),
    );
    if (
      !mimeType ||
      !stream.getVideoTracks().length ||
      !stream.getAudioTracks().length
    )
      throw new Error(
        "Training takes require a supported camera and microphone.",
      );
    document
      .querySelectorAll("#study-session-takes video")
      .forEach((v) => v.pause());
    await store.put(reservation);
    const t = (active = {
      ...reservation,
      stream,
      started: performance.now(),
      writes: Promise.resolve(),
      video: [],
      ai: 0,
      vi: 0,
      error: null,
    });
    t.ready = new Promise((resolve) => {
      t.readyResolve = resolve;
    });
    const save = (kind, index, data) => {
      t.writes = t.writes
        .then(() => store.chunk(t.id, kind, index, data))
        .catch((error) => {
          if (!t.error) {
            t.error = error;
            status(
              "Local session saving failed. Finishing the take for download.",
            );
            onFailure();
          }
        });
    };
    t.audio = new globalThis.JazzLosslessRecorder((samples, rate) => {
      t.sampleRate = rate;
      save("audio", t.ai++, samples);
    });
    try {
      await t.audio.start(stream);
      if (t.cancelled) throw new Error("Training start cancelled.");
      t.recorder = new MediaRecorder(stream, {
        mimeType,
        videoBitsPerSecond: 2500000,
        audioBitsPerSecond: 192000,
      });
      t.recorder.addEventListener("dataavailable", (e) => {
        if (!e.data?.size) return;
        t.video.push(e.data);
        save("video", t.vi++, e.data);
      });
      t.videoStopped = new Promise((resolve) =>
        t.recorder.addEventListener("stop", resolve, { once: true }),
      );
      t.recorder.addEventListener("error", () => {
        status("Camera recording failed; finishing retained media.");
        onFailure();
      });
      t.recorder.start();
      if (globalThis.JazzRecordingPolicy.supportsChunkFlush(mimeType))
        t.flush = setInterval(() => {
          if (t.recorder.state === "recording") t.recorder.requestData();
        }, 5000);
      t.type = t.recorder.mimeType || mimeType;
      await store.put({
        ...reservation,
        sampleRate: t.audio.context.sampleRate,
        videoContentType: t.type,
      });
      t.started = performance.now();
      const preview = $("#study-camera-preview");
      preview.srcObject = stream;
      preview.hidden = false;
      preview.play().catch(() => {});
      status(
        "Recording session video + microphone · one take for this entire run.",
      );
    } catch (error) {
      clearInterval(t.flush);
      await t.audio.cancel().catch(() => {});
      if (t.recorder?.state === "recording") t.recorder.stop();
      active = null;
      await store.put({ ...reservation, state: "cancelled" });
      throw error;
    } finally {
      t.readyResolve();
    }
  }
  function offsetMs() {
    return active ? Math.round(performance.now() - active.started) : 0;
  }
  async function finish() {
    if (finishing) return finishing;
    if (!active) return;
    const t = active;
    t.cancelled = true;
    active = null;
    finishing = (async () => {
      await t.ready;
      if (!t.recorder) return;
      clearInterval(t.flush);
      if (t.recorder.state !== "inactive") t.recorder.stop();
      const audio = await t.audio.stop();
      await t.videoStopped;
      await t.writes;
      const videoBlob = new Blob(t.video, { type: t.type.split(";")[0] });
      const camera = t.stream.getVideoTracks()[0].getSettings();
      const capture = {
        clientCaptureId: t.id,
        blob: audio.blob,
        durationMS: audio.durationMS,
        sampleRate: audio.sampleRate,
        waveformPeaks: audio.waveformPeaks,
        contentType: "audio/wav",
        mediaKind: "video",
        videoBlob,
        videoContentType: t.type,
        videoCodec: /codecs?=([^;]+)/i.exec(t.type)?.[1] || "",
        videoWidth: camera.width || 1280,
        videoHeight: camera.height || 720,
        videoFrameRate: camera.frameRate || 30,
        recordedAt: t.recordedAt,
        practiceSessionID: t.practiceSessionID,
        blockContext: t.blockContext,
        takeNumber: t.takeNumber,
        skillIds: [],
        tuneId: "",
        notes: `Study 1 training session ${t.id}`,
      };
      const saved = {
        id: t.id,
        state: "saved",
        recordedAt: t.recordedAt,
        capture,
      };
      if (!videoBlob.size)
        throw new Error(
          "Camera produced no video. Exercise audio is retained separately.",
        );
      try {
        await store.put(saved);
        status("Uploading session take to your private cloud…");
      } catch (error) {
        emergency(capture);
        throw new Error(
          "Session storage failed. Download the retained video and audio now.",
        );
      }
      await render();
      sync();
    })()
      .catch((error) => {
        status(error.message);
        throw error;
      })
      .finally(() => {
        const preview = $("#study-camera-preview");
        preview.pause();
        preview.srcObject = null;
        preview.hidden = true;
        finishing = null;
      });
    return finishing;
  }
  function link(blob, label, name) {
    const a = document.createElement("a");
    const url = URL.createObjectURL(blob);
    a.href = url;
    a.download = name;
    a.textContent = label;
    a.className = "button";
    return a;
  }
  function emergency(capture) {
    const host = $("#study-session-recovery");
    host.append(
      link(
        capture.videoBlob,
        "Download retained video",
        "training-recovery.mp4",
      ),
      link(capture.blob, "Download retained audio", "training-recovery.wav"),
    );
  }
  async function render() {
    const host = $("#study-session-takes");
    urls.splice(0).forEach((url) => URL.revokeObjectURL(url));
    host.replaceChildren();
    const attempts = await globalThis.JazzStudyStore.all();
    for (const t of (await store.all())
      .filter((t) => t.capture)
      .sort((a, b) => b.recordedAt.localeCompare(a.recordedAt))
      .slice(0, 10)) {
      const card = document.createElement("article");
      card.className = "study-session-take";
      const title = document.createElement("p");
      title.textContent = `${new Date(t.recordedAt).toLocaleString()} · ${t.state === "synced" ? "Uploaded to cloud · in Archive" : "Waiting for cloud upload · backup saved"}`;
      card.append(title);
      const video = document.createElement("video");
      video.controls = true;
      video.preload = "metadata";
      video.playsInline = true;
      video.addEventListener("play", () => {
        if (globalThis.JazzStudyTraining?.isBusy()) video.pause();
      });
      video.src = URL.createObjectURL(t.capture.videoBlob);
      urls.push(video.src);
      card.append(video);
      const extension = t.capture.videoContentType.startsWith("video/mp4")
        ? "mp4"
        : "webm";
      card.append(
        link(
          t.capture.videoBlob,
          "Download session video",
          `training-${t.id}.${extension}`,
        ),
        link(t.capture.blob, "Download session audio", `training-${t.id}.wav`),
      );
      for (const a of attempts.filter(
        (a) => a.sessionId === t.id && a.sessionOffsetMs != null,
      )) {
        const button = document.createElement("button");
        button.className = "button";
        button.textContent = `${a.key} · ${a.result.correct}/7`;
        button.onclick = () => {
          if (globalThis.JazzStudyTraining?.isBusy()) return;
          video.currentTime = a.sessionOffsetMs / 1000;
          video.play().catch(() => {});
        };
        card.append(button);
      }
      host.append(card);
    }
  }
  async function sync() {
    if (syncing) {
      syncRequested = true;
      return;
    }
    clearTimeout(retryTimer);
    syncing = true;
    let failed = false;
    try {
      for (const t of await store.all()) {
        if (t.state !== "saved") continue;
        status("Uploading session take to your private cloud…");
        t.recordingId = await globalThis.JazzRecording.uploadTrainingTake(
          t.capture,
          (percent) =>
            status(`Uploading session take · ${Math.round(percent)}%`),
        );
        t.state = "synced";
        await store.put(t);
      }
      retryDelay = 5000;
      if (!active) {
        const takes = await store.all();
        status(
          takes.some((t) => t.state === "saved")
            ? "Session upload queued…"
            : takes.some((t) => t.state === "synced")
              ? "Session takes uploaded to cloud · available in Archive."
              : "Ready · session video uploads automatically when you finish.",
        );
      }
      globalThis.JazzPracticeSession.refresh();
      dispatchEvent(new CustomEvent("jazz:recordings-changed"));
    } catch (error) {
      failed = true;
      status(
        `Cloud upload failed: ${error.message}. Backup saved; retrying automatically.`,
      );
      retryTimer = setTimeout(() => sync(), retryDelay);
      retryDelay = Math.min(60000, retryDelay * 2);
    } finally {
      await render().catch(() => {});
      syncing = false;
      if (syncRequested) {
        syncRequested = false;
        if (!failed) sync();
      }
    }
  }
  async function initialize() {
    // A crashed session remains visible as retained chunks, never as a complete take.
    for (const t of await store.all())
      if (t.state === "capturing") {
        const chunks = await store.chunks(t.id);
        const audio = chunks
          .filter((c) => c.kind === "audio")
          .sort((a, b) => a.index - b.index)
          .map((c) => c.data);
        const video = chunks
          .filter((c) => c.kind === "video")
          .sort((a, b) => a.index - b.index)
          .map((c) => c.data);
        if (audio.length || video.length) {
          status(
            "An interrupted session has retained media. Download it before clearing browser data.",
          );
          const host = $("#study-session-recovery");
          if (video.length)
            host.append(
              link(
                new Blob(video, { type: t.videoContentType || "video/mp4" }),
                "Download interrupted video (may need repair)",
                `interrupted-${t.id}.${t.videoContentType?.startsWith("video/webm") ? "webm" : "mp4"}`,
              ),
            );
          if (audio.length)
            host.append(
              link(
                globalThis.JazzLosslessRecorder.encodeWAV24(
                  audio,
                  audio.reduce((s, c) => s + c.length, 0),
                  t.sampleRate || 48000,
                ),
                "Download interrupted audio",
                `interrupted-${t.id}.wav`,
              ),
            );
        }
      }
    await render();
    sync();
  }
  globalThis.JazzTrainingSessionTake = {
    isBusy: () => Boolean(active || finishing),
    prepare,
    start,
    finish,
    offsetMs,
    initialize,
    sync,
  };
  addEventListener("online", sync);
  $("#study-session-sync").addEventListener("click", sync);
})();
