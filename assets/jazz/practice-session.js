(() => {
  "use strict";

  const API_BASE = "./api/v1";
  const $ = (selector, root = document) => root.querySelector(selector);
  let sessions = [];
  let activeSession = null;

  async function api(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
      ...options,
      headers: {
        ...(options.body ? { "Content-Type": "application/json" } : {}),
        ...(options.headers || {}),
      },
    });
    const body = response.status === 204 ? null : await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body?.error || `Request failed (${response.status})`);
    return body;
  }

  function localDateTimeValue(date = new Date()) {
    const shifted = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
    return shifted.toISOString().slice(0, 16);
  }

  function defaultTitle() {
    return `Practice - ${new Date().toLocaleDateString(undefined, { month: "short", day: "numeric" })}`;
  }

  function resetDraft() {
    $("#practice-session-title").value = defaultTitle();
    $("#practice-session-started").value = localDateTimeValue();
    $("#practice-session-notes").value = "";
    $("#practice-session-started").disabled = false;
  }

  async function loadSessions() {
    try {
      const result = await api("/practice-sessions");
      sessions = result.sessions || [];
      const activeSummary = sessions.find((session) => session.status === "active");
      activeSession = activeSummary ? await api(`/practice-sessions/${activeSummary.id}`) : null;
      renderActiveSession();
      renderHistory();
      return activeSession;
    } catch (error) {
      $("#practice-session-summary").textContent = `Practice sessions are temporarily unavailable: ${error.message}`;
      return null;
    }
  }

  async function ensureActive() {
    if (activeSession) return activeSession;
    const title = $("#practice-session-title").value.trim() || defaultTitle();
    const startedValue = $("#practice-session-started").value;
    const summary = $("#practice-session-notes").value.trim();
    const created = await api("/practice-sessions", {
      method: "POST",
      body: JSON.stringify({
        title,
        summary,
        startedAt: startedValue ? new Date(startedValue).toISOString() : new Date().toISOString(),
      }),
    });
    activeSession = await api(`/practice-sessions/${created.id}`);
    await refreshSessionListOnly();
    renderActiveSession();
    renderHistory();
    return activeSession;
  }

  async function refreshSessionListOnly() {
    const result = await api("/practice-sessions");
    sessions = result.sessions || [];
  }

  function renderActiveSession() {
    const begin = $("#begin-practice-session");
    const finish = $("#finish-practice-session");
    const save = $("#save-practice-session");
    const timeline = $("#practice-activity-timeline");
    if (!activeSession) {
      $("#practice-session-heading").textContent = "No active session";
      $("#practice-session-summary").textContent = "Start a session to group work, notes, and recordings into one reviewable timeline.";
      begin.hidden = false;
      finish.hidden = true;
      save.hidden = true;
      timeline.innerHTML = '<p class="empty-activities">No work logged in this session yet.</p>';
      if (!$("#practice-session-title").value) resetDraft();
      return;
    }

    $("#practice-session-heading").textContent = activeSession.title;
    $("#practice-session-summary").textContent = `${activeSession.totalMinutes} minutes logged - ${activeSession.activityCount} off-mic activit${activeSession.activityCount === 1 ? "y" : "ies"} - ${activeSession.recordingCount} recording${activeSession.recordingCount === 1 ? "" : "s"}`;
    $("#practice-session-title").value = activeSession.title;
    $("#practice-session-started").value = localDateTimeValue(new Date(activeSession.startedAt));
    $("#practice-session-started").disabled = true;
    $("#practice-session-notes").value = activeSession.summary || "";
    begin.hidden = true;
    finish.hidden = false;
    save.hidden = false;
    timeline.replaceChildren();
    if (!activeSession.activities?.length) {
      timeline.innerHTML = '<p class="empty-activities">No work logged in this session yet.</p>';
      return;
    }
    activeSession.activities.forEach((activity) => {
      const entry = document.createElement("article");
      entry.className = "activity-entry";
      entry.innerHTML = `
        <div class="activity-entry-top"><span>${escapeHTML(activity.category)}</span><span>${activity.durationMinutes} min</span></div>
        <h4>${escapeHTML(activity.title)}</h4>
        <p>${escapeHTML(activity.notes || "No additional note.")}</p>`;
      timeline.appendChild(entry);
    });
  }

  function renderHistory() {
    const history = $("#practice-session-history");
    history.replaceChildren();
    if (!sessions.length) {
      history.innerHTML = '<p class="empty-recordings">Your completed and active sessions will appear here.</p>';
      return;
    }
    sessions.forEach((session) => {
      const card = document.createElement("article");
      card.className = `session-history-card${session.status === "active" ? " active" : ""}`;
      card.innerHTML = `
        <div class="session-history-card-top"><span>${new Date(session.startedAt).toLocaleDateString()}</span><span>${session.status}</span></div>
        <h4>${escapeHTML(session.title)}</h4>
        <p>${escapeHTML(session.summary || "No session note yet.")}</p>
        <div class="session-history-card-meta"><span>${session.totalMinutes} min</span><span>${session.activityCount} activities</span><span>${session.recordingCount} takes</span></div>`;
      history.appendChild(card);
    });
  }

  async function saveSession() {
    const session = await ensureActive();
    activeSession = await api(`/practice-sessions/${session.id}`, {
      method: "PATCH",
      body: JSON.stringify({
        title: $("#practice-session-title").value.trim() || session.title,
        summary: $("#practice-session-notes").value.trim(),
      }),
    });
    await refreshSessionListOnly();
    renderActiveSession();
    renderHistory();
  }

  async function finishSession() {
    if (!activeSession || !confirm("Finish this practice session? You can still view all of its notes and recordings afterward.")) return;
    activeSession = await api(`/practice-sessions/${activeSession.id}`, {
      method: "PATCH",
      body: JSON.stringify({
        title: $("#practice-session-title").value.trim() || activeSession.title,
        summary: $("#practice-session-notes").value.trim(),
        status: "completed",
        endedAt: new Date().toISOString(),
      }),
    });
    activeSession = null;
    await refreshSessionListOnly();
    resetDraft();
    renderActiveSession();
    renderHistory();
  }

  async function addActivity(event) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const button = $('button[type="submit"]', event.currentTarget);
    button.disabled = true;
    try {
      const session = await ensureActive();
      const activity = await api(`/practice-sessions/${session.id}/activities`, {
        method: "POST",
        body: JSON.stringify({
          category: String(form.get("category")),
          title: String(form.get("title") || "").trim(),
          durationMinutes: Number(form.get("minutes")),
          notes: String(form.get("notes") || "").trim(),
          occurredAt: new Date().toISOString(),
        }),
      });
      dispatchEvent(new CustomEvent("jazz:activity-logged", { detail: activity }));
      event.currentTarget.elements.title.value = "";
      event.currentTarget.elements.notes.value = "";
      activeSession = await api(`/practice-sessions/${session.id}`);
      await refreshSessionListOnly();
      renderActiveSession();
      renderHistory();
    } catch (error) {
      $("#practice-session-summary").textContent = `Could not save that work: ${error.message}`;
    } finally {
      button.disabled = false;
    }
  }

  function escapeHTML(value) {
    const element = document.createElement("span");
    element.textContent = value;
    return element.innerHTML;
  }

  globalThis.JazzPracticeSession = {
    ensureActive,
    currentID: () => activeSession?.id || "",
    refresh: loadSessions,
  };

  resetDraft();
  $("#begin-practice-session").addEventListener("click", () => ensureActive().catch((error) => {
    $("#practice-session-summary").textContent = `Could not begin the session: ${error.message}`;
  }));
  $("#save-practice-session").addEventListener("click", () => saveSession().catch((error) => {
    $("#practice-session-summary").textContent = `Could not save session notes: ${error.message}`;
  }));
  $("#finish-practice-session").addEventListener("click", () => finishSession().catch((error) => {
    $("#practice-session-summary").textContent = `Could not finish the session: ${error.message}`;
  }));
  $("#practice-activity-form").addEventListener("submit", addActivity);
  loadSessions();
})();
