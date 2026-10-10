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
  const cancel = document.createElement("button");
  cancel.type = "button"; cancel.id = "studio-cancel-film"; cancel.textContent = "Cancel Magic Film"; cancel.hidden = true;
  const open = document.createElement("button");
  open.type = "button"; open.id = "studio-open-film-draft"; open.textContent = "Open film in timeline"; open.hidden = true;
  const restore = document.createElement("button");
  restore.id = "studio-restore-local-backup"; restore.type = "button"; restore.textContent = "Restore previous timeline";
  restore.title = "Restore the timeline saved before opening a Magic Film draft";
  const statusElement = document.createElement("span");
  statusElement.id = "studio-magic-status"; statusElement.setAttribute("role", "status"); statusElement.hidden = true;
  for (const element of [lengthLabel, button, cancel, open, restore]) anchor.before(element);
  anchor.parentElement.append(statusElement);

  function status(message) {
    statusElement.textContent = message ? `Magic Film · ${message}` : "";
    statusElement.hidden = !message;
  }
  async function request(path = "", options = {}) {
    const response = await fetch(API + path, {
      ...options,
      headers: { ...(options.body ? { "Content-Type": "application/json" } : {}), ...(options.headers || {}) },
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`);
    return body;
  }
  function selectedDate() { return document.querySelector("#studio-date")?.value || ""; }
  function render(job) {
    active = job || null;
    const running = job && !terminal.has(job.status);
    button.disabled = Boolean(running); length.disabled = Boolean(running);
    cancel.hidden = !running; open.hidden = job?.status !== "complete" || !job.project;
    if (!job) status("");
    else if (job.status === "failed") status(`${job.message} · ${job.error || "Try again."}`);
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
    } catch (error) { status(error.message); }
    if (active && !terminal.has(active.status)) timer = setTimeout(refresh, 3000);
  }
  button.addEventListener("click", async () => {
    button.disabled = true; status("Submitting to the on-demand cloud worker…");
    try {
      const clientId = crypto.randomUUID();
      render(await request("", { method: "POST", body: JSON.stringify({ clientId, date: selectedDate(), targetSeconds: Number(length.value) }) }));
      timer = setTimeout(refresh, 1500);
    } catch (error) { status(error.message); button.disabled = false; }
  });
  cancel.addEventListener("click", async () => {
    if (!active || !confirm("Cancel this Magic Film job?")) return;
    cancel.disabled = true;
    try { await request(`/${active.id}/cancel`, { method: "POST", body: "{}" }); status("Cancellation requested"); timer = setTimeout(refresh, 1000); }
    catch (error) { status(error.message); }
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
  document.addEventListener("jazz:studio-date-change", () => { active = null; refresh(); });
  document.addEventListener("jazz:view-change", event => { if (event.detail?.view === "studio") refresh(); });
  document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
  addEventListener("online", refresh);
  if (location.hash === "#studio") refresh();
})();
