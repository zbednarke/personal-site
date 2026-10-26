(() => {
  "use strict";
  const API = "./api/v1/studio/magic-films";
  const terminal = new Set(["complete", "failed", "cancelled"]);
  let active = null;
  let timer = null;
  const anchor = document.querySelector("#studio-render");
  if (!anchor) return;

  const button = document.createElement("button");
  button.type = "button"; button.id = "studio-make-film"; button.textContent = "Make today's film";
  button.title = "Start an on-demand cloud editor; you may close this page while it works";
  const lengthLabel = document.createElement("label");
  lengthLabel.className = "studio-magic-length";
  const lengthText = document.createElement("span"); lengthText.textContent = "Film length";
  const length = document.createElement("select"); length.id = "studio-magic-length";
  for (const minutes of [1, 2, 3, 5, 10]) length.append(new Option(`${minutes} min`, String(minutes * 60), false, minutes === 2));
  lengthLabel.append(lengthText, length);
  // Optional creative direction for the editor; empty keeps today's behaviour.
  const directionLabel = document.createElement("label");
  directionLabel.className = "studio-magic-direction";
  const directionText = document.createElement("span"); directionText.textContent = "How should this come out? (optional)";
  const direction = document.createElement("textarea"); direction.id = "studio-magic-direction";
  direction.maxLength = 1000; direction.rows = 2;
  direction.placeholder = "e.g. quiet and reflective, focus on the second half, slow pacing";
  directionLabel.append(directionText, direction);
  const cancel = document.createElement("button");
  cancel.type = "button"; cancel.id = "studio-cancel-film"; cancel.textContent = "Cancel Magic Film"; cancel.hidden = true;
  const open = document.createElement("button");
  open.type = "button"; open.id = "studio-open-film-draft"; open.textContent = "Open film in timeline"; open.hidden = true;
  const restore = document.createElement("button");
  restore.id = "studio-restore-local-backup"; restore.type = "button"; restore.textContent = "Restore previous timeline";
  restore.title = "Restore the timeline saved before opening a Magic Film draft";
  const statusElement = document.createElement("div");
  statusElement.id = "studio-magic-status"; statusElement.setAttribute("role", "status"); statusElement.hidden = true;
  for (const element of [lengthLabel, directionLabel, button, cancel, open, restore]) anchor.before(element);
  anchor.parentElement.append(statusElement);
  // Phones hide the output dock, so the film controls move to a panel under
  // the Clip Studio header there (the timeline's open/restore stay behind).
  const phone = matchMedia("(max-width: 760px)");
  const phonePanel = document.createElement("section");
  phonePanel.className = "studio-magic-phone"; phonePanel.setAttribute("aria-label", "Magic Film");
  document.querySelector("#studio .clip-studio-header")?.after(phonePanel);
  function place() {
    if (phone.matches && phonePanel.isConnected) {
      for (const element of [lengthLabel, directionLabel, button, cancel, statusElement]) if (element.parentElement !== phonePanel) phonePanel.append(element);
      return;
    }
    // Back on the dock, in the original order (before open/restore) for tabbing.
    if (lengthLabel.parentElement !== anchor.parentElement) open.before(lengthLabel, directionLabel, button, cancel);
    if (statusElement.parentElement !== anchor.parentElement) anchor.parentElement.append(statusElement);
  }
  phone.addEventListener?.("change", place);
  place();

  function status(message) {
    statusElement.removeAttribute("data-error");
    statusElement.setAttribute("role", "status");
    statusElement.textContent = message ? `Magic Film · ${message}` : "";
    statusElement.hidden = !message;
  }
  function failure(problem, jobId) {
    statusElement.replaceChildren();
    statusElement.dataset.error = "true";
    statusElement.setAttribute("role", "alert");
    statusElement.hidden = false;
    for (const [tag, text] of [["strong", problem.title], ["p", problem.message], ["p", problem.action]]) {
      const node = document.createElement(tag);
      node.textContent = text;
      statusElement.append(node);
    }
    if (jobId) {
      const reference = document.createElement("small");
      reference.textContent = `Job reference: ${jobId}`;
      statusElement.append(reference);
    }
    const check = document.createElement("button");
    check.type = "button";
    check.textContent = "Refresh film status";
    check.addEventListener("click", refresh);
    statusElement.append(check);
  }
  function requestFailure(code) {
    if (code === 401 || code === 403) return { title: "Site sign-in required", message: "The site could not authorize this request.", action: "Sign in to the site again, then check the film status. This is separate from the film worker's AI access." };
    if (code === 429) return { title: "Too many requests", message: "The site temporarily limited this request.", action: "Wait a moment, then check the film status before submitting again." };
    if (code === 409) return { title: "A film is already running", message: "Another film is being made for this day.", action: "Check its status, or wait for it to finish before making another." };
    if (code === 400 || code === 422) return { title: "Check your film selection", message: "The film request could not be accepted.", action: "Choose a practice day with eligible moments, scan it or add manual moments, and check the film length and direction." };
    return { title: "Could not reach the film service", message: "The latest film status could not be confirmed. An existing job may still be running.", action: "Check your connection and refresh the film status before submitting again." };
  }
  function showRequestFailure(error) { failure(error.filmFailure || requestFailure()); }
  async function request(path = "", options = {}) {
    const response = await fetch(API + path, {
      ...options,
      headers: { ...(options.body ? { "Content-Type": "application/json" } : {}), ...(options.headers || {}) },
    });
    const body = await response.json().catch(() => null);
    if (!response.ok || response.redirected) {
      const error = new Error("Film request failed");
      error.filmFailure = response.redirected ? requestFailure(401) : body?.failure || requestFailure(response.status);
      throw error;
    }
    if (!body) throw new Error("Film service returned an invalid response");
    return body;
  }
  function selectedDate() { return document.querySelector("#studio-date")?.value || ""; }
  function render(job) {
    active = job || null;
    const running = job && !terminal.has(job.status);
    button.disabled = Boolean(running); length.disabled = Boolean(running); direction.disabled = Boolean(running);
    button.textContent = job?.status === "failed" ? (job.failure?.retryable === false ? "Try again after fixing access" : "Make film again") : "Make today's film";
    cancel.hidden = !running; open.hidden = job?.status !== "complete" || !job.project;
    if (!job) status("");
    else if (job.status === "failed") failure(job.failure || { title: "Film could not finish", message: "Something went wrong while making this film.", action: "Try again once. If it fails again, use the job reference to investigate." }, job.id);
    else status(job.message);
    document.dispatchEvent(new CustomEvent("jazz:magic-films-updated", { detail: { job } }));
  }
  async function refresh() {
    clearTimeout(timer);
    const date = selectedDate();
    if (!date) return;
    try {
      if (active && !terminal.has(active.status)) render(await request(`/${active.id}`));
      else {
        const result = await request(`?date=${encodeURIComponent(date)}`);
        render((result.jobs || []).find(job => !terminal.has(job.status)) || result.jobs?.[0] || null);
      }
    } catch (error) { showRequestFailure(error); }
    if (active && !terminal.has(active.status)) timer = setTimeout(refresh, 3000);
  }
  button.addEventListener("click", async () => {
    button.disabled = true; status("Submitting to the on-demand cloud worker…");
    try {
      const clientId = crypto.randomUUID();
      render(await request("", { method: "POST", body: JSON.stringify({ clientId, date: selectedDate(), targetSeconds: Number(length.value), direction: direction.value.trim() }) }));
      timer = setTimeout(refresh, 1500);
    } catch (error) { showRequestFailure(error); button.disabled = false; }
  });
  cancel.addEventListener("click", async () => {
    if (!active || !confirm("Cancel this Magic Film job?")) return;
    cancel.disabled = true;
    try { await request(`/${active.id}/cancel`, { method: "POST", body: "{}" }); status("Cancellation requested"); timer = setTimeout(refresh, 1000); }
    catch (error) { showRequestFailure(error); }
    finally { cancel.disabled = false; }
  });
  open.addEventListener("click", () => {
    try { globalThis.JazzClipStudioMagic.importDraft(active); status("Film draft opened. Your previous timeline was backed up on this device."); }
    catch (error) { status(error.message); }
  });
  restore.addEventListener("click", () => {
    try { globalThis.JazzClipStudioMagic.restorePrevious(); status("Previous timeline restored."); }
    catch (error) { status(error.message); }
  });
  // "Re-make with new direction" on a finished film: load its direction and
  // length here to edit. Making the film again starts a new job, so the
  // earlier film stays on the shelf.
  globalThis.JazzMagicFilmComposer = {
    remake(job) {
      if (active && !terminal.has(active.status)) { status("Wait for the running film to finish, or cancel it first."); return; }
      direction.value = String(job?.direction || "");
      if ([...length.options].some(option => option.value === String(job?.targetSeconds))) length.value = String(job.targetSeconds);
      direction.scrollIntoView({ block: "center", behavior: "smooth" });
      direction.focus();
      status("Edit the direction, then make the film again. The earlier film stays.");
    },
  };
  document.addEventListener("jazz:studio-date-change", () => { active = null; refresh(); });
  document.addEventListener("jazz:view-change", event => { if (event.detail?.view === "studio") refresh(); });
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
  addEventListener("online", refresh);
  if (location.hash === "#studio") refresh();
})();
