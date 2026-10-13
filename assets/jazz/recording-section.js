(() => {
  "use strict";

  const API_BASE = "./api/v1";
  const Model = globalThis.JazzRecordingSectionModel;
  const dialog = document.querySelector("#recording-section-dialog");
  const form = document.querySelector("#recording-section-form");
  const choices = document.querySelector("#recording-section-choices");
  const status = document.querySelector("#recording-section-status");
  const save = document.querySelector("#save-recording-section");
  const toast = document.querySelector("#section-change-toast");
  let active = null;
  let saving = false;
  let toastTimer = null;
  let openToken = 0;

  async function api(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
      ...options,
      headers: { ...(options.body ? { "Content-Type": "application/json" } : {}), ...(options.headers || {}) },
    });
    const body = response.status === 204 ? null : await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(body?.error || `Request failed (${response.status})`);
      error.status = response.status;
      throw error;
    }
    return body;
  }

  function setStatus(message, tone = "") {
    status.textContent = message;
    status.dataset.tone = tone;
  }

  function selectedID() {
    return form.querySelector('input[name="recording-section"]:checked')?.value || "";
  }

  function syncSave() {
    const creating = form.querySelector('input[name="recording-section"]:checked')?.dataset.create === "true";
    const newTitle = form.querySelector("[data-new-section-title]")?.value.trim() || "";
    save.disabled = saving || !active || !selectedID() || selectedID() === active.practiceBlockId || (creating && !newTitle);
  }

  function sectionOption(block, current, ordinal) {
    const label = document.createElement("label");
    label.className = "recording-section-option";
    const input = document.createElement("input");
    input.type = "radio";
    input.name = "recording-section";
    input.value = block.id;
    input.checked = block.id === current;
    const copy = document.createElement("span");
    const title = document.createElement("strong");
    title.textContent = block.title;
    const detail = document.createElement("small");
    detail.textContent = `Section ${ordinal} · ${block.category || "practice"} · ${block.targetMinutes || 0} min`;
    copy.append(title, detail);
    label.append(input, copy);
    return label;
  }

  async function open(recording) {
    if (!dialog || !recording?.id || !recording.practiceSessionId || !recording.practiceDate || !recording.practiceBlockId) return;
    const token = ++openToken;
    active = recording;
    saving = false;
    choices.replaceChildren();
    choices.disabled = true;
    save.disabled = true;
    setStatus("Loading sections…");
    dialog.showModal();
    try {
      const [result, fresh] = await Promise.all([
        api(`/practice-sessions/${encodeURIComponent(recording.practiceSessionId)}/blocks?date=${encodeURIComponent(recording.practiceDate)}`),
        api(`/recordings/${encodeURIComponent(recording.id)}`),
      ]);
      if (token !== openToken || !dialog.open) return;
      if (!Model.applyFreshRecording(recording, [fresh])) throw new Error("This recording is no longer available");
      active = recording;
      const blocks = result.blocks || [];
      const current = blocks.find((block) => block.id === recording.practiceBlockId);
      const available = blocks.filter((block) => block.id !== recording.practiceBlockId);
      const currentGroup = document.createElement("div");
      currentGroup.className = "recording-section-group";
      currentGroup.innerHTML = "<legend>Current section</legend>";
      if (current) currentGroup.append(sectionOption(current, recording.practiceBlockId, current.position + 1));
      const availableGroup = document.createElement("div");
      availableGroup.className = "recording-section-group";
      availableGroup.innerHTML = "<legend>Available sections</legend>";
      available.forEach((block) => availableGroup.append(sectionOption(block, recording.practiceBlockId, block.position + 1)));
      const createGroup = document.createElement("div");
      createGroup.className = "recording-section-group";
      createGroup.innerHTML = `<legend>Create for this day</legend><label class="recording-section-option"><input type="radio" name="recording-section" value="${crypto.randomUUID()}" data-create="true"><span><strong>New section</strong><small>Saved only in this practice day</small></span></label><label class="recording-section-new"><span>Section title</span><input data-new-section-title maxlength="160" placeholder="Long tones"></label>`;
      choices.replaceChildren(currentGroup, availableGroup, createGroup);
      active.currentBlock = current;
      choices.disabled = false;
      setStatus(available.length ? "Choose a section." : "No other sections are available for this practice day.");
      syncSave();
    } catch (error) {
      setStatus(`Could not load sections: ${error.message}`, "error");
    }
  }

  function dispatchChange(recording, result) {
    recording.practiceBlockId = result.practiceBlockId;
    recording.practiceBlockTitle = result.practiceBlockTitle || recording.practiceBlockTitle;
    recording.practiceBlockCategory = result.practiceBlockCategory || recording.practiceBlockCategory;
    recording.practiceBlockTrack = result.practiceBlockTrack || recording.practiceBlockTrack;
    recording.sectionAssignmentRevision = result.sectionAssignmentRevision;
    dispatchEvent(new CustomEvent("jazz:recording-section-changed", { detail: { recording, result } }));
  }

  function showUndo(recording, result, previousTitle) {
    clearTimeout(toastTimer);
    const copy = toast.querySelector("span");
    const button = toast.querySelector("button");
    copy.textContent = `Moved to ${result.practiceBlockTitle}.`;
    button.hidden = false;
    button.disabled = false;
    toast.hidden = false;
    button.onclick = async () => {
      button.disabled = true;
      try {
        const undone = await api(`/recordings/${recording.id}/section`, {
          method: "PATCH",
          body: JSON.stringify({
            practiceBlockId: result.previousPracticeBlockId,
            expectedPracticeBlockId: result.practiceBlockId,
            expectedRevision: result.sectionAssignmentRevision,
          }),
        });
        undone.practiceBlockTitle = previousTitle;
        dispatchChange(recording, undone);
        copy.textContent = `Moved back to ${previousTitle}.`;
        button.hidden = true;
      } catch (error) {
        copy.textContent = error.status === 409 ? "Undo unavailable because the section changed again." : `Undo failed: ${error.message}`;
        button.hidden = error.status === 409;
        button.disabled = false;
      }
    };
    toastTimer = setTimeout(() => { toast.hidden = true; }, 9000);
  }

  form?.addEventListener("change", syncSave);
  form?.addEventListener("input", syncSave);
  form?.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!active || saving || save.disabled) return;
    const submitting = active;
    const destinationID = selectedID();
    const creating = form.querySelector('input[name="recording-section"]:checked')?.dataset.create === "true";
    const newTitle = form.querySelector("[data-new-section-title]")?.value.trim() || "";
    saving = true;
    syncSave();
    choices.disabled = true;
    setStatus("Saving…", "saving");
    const previousTitle = submitting.practiceBlockTitle || "previous section";
    try {
      const result = await api(`/recordings/${submitting.id}/section`, {
        method: "PATCH",
        body: JSON.stringify({
          practiceBlockId: destinationID,
          expectedPracticeBlockId: submitting.practiceBlockId,
          expectedRevision: Number(submitting.sectionAssignmentRevision || 0),
          ...(creating ? { createTarget: {
            blockKey: `custom-${destinationID}`,
            title: newTitle,
            instructions: "",
            category: submitting.currentBlock?.category || submitting.practiceBlockCategory || "fundamentals",
            track: submitting.currentBlock?.track || submitting.practiceBlockTrack || "trumpet",
            targetMinutes: Number(submitting.currentBlock?.targetMinutes || 10),
          } } : {}),
        }),
      });
      dispatchChange(submitting, result);
      dialog.close();
      showUndo(submitting, result, previousTitle);
    } catch (error) {
      if (error.status === 409) {
        const latest = await api(`/recordings/${encodeURIComponent(submitting.id)}`).catch(() => null);
        if (latest) Model.applyFreshRecording(submitting, [latest]);
      }
      setStatus(error.status === 409 ? "Not saved: this recording changed elsewhere. Close and reopen to review its latest section." : `Not saved: ${error.message}`, "error");
      choices.disabled = false;
    } finally {
      saving = false;
      syncSave();
    }
  });
  dialog?.querySelectorAll("[data-recording-section-cancel]").forEach((button) => button.addEventListener("click", () => {
    if (!saving) dialog.close();
  }));
  dialog?.addEventListener("cancel", (event) => {
    if (saving) event.preventDefault();
    else openToken += 1;
  });

  globalThis.JazzRecordingSections = { open };
})();
