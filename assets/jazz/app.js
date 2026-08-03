(() => {
  "use strict";

  const DATA = globalThis.JAZZ_DATA;
  const STORAGE_KEY = "zach-jazz-project-v1";
  const SYNC_META_KEY = "zach-jazz-project-sync-v1";
  const OUTBOX_KEY = "zach-jazz-project-outbox-v1";
  const CLOUD_BOUND_KEY = "zach-jazz-project-cloud-bound-v1";
  const API_BASE = "./api/v1";
  const MAX_SKILL_LEVEL = 4;
  const stateDefaults = {
    version: DATA.version,
    skillLevels: {},
    objectives: {},
    repertoire: {},
    bosses: {},
    scene: {},
    practice: [],
    peopleCanCall: 0,
  };

  let state = loadState();
  let activeTrack = "all";
  let activeSkillId = null;
  let toastTimer = null;
  let syncRevision = loadSyncRevision();
  let syncOutbox = loadOutbox();
  let syncInFlight = false;
  let syncPaused = false;

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

  function normalizeState(saved) {
    if (!saved || typeof saved !== "object") return structuredClone(stateDefaults);
    const people = Number(saved.peopleCanCall || 0);
    const practice = Array.isArray(saved.practice)
      ? saved.practice.flatMap((entry) => {
          if (!entry || typeof entry !== "object" || !/^\d{4}-\d{2}-\d{2}$/.test(String(entry.date || ""))) return [];
          const minutes = Number(entry.minutes);
          if (!Number.isFinite(minutes) || minutes <= 0) return [];
          return [{
            id: String(entry.id || `imported-${crypto.randomUUID()}`),
            date: String(entry.date),
            minutes: Math.min(360, Math.round(minutes)),
            track: String(entry.track || "trumpet").slice(0, 30),
            note: String(entry.note || "").slice(0, 100),
            preset: Boolean(entry.preset),
          }];
        })
      : [];
    return {
      ...structuredClone(stateDefaults),
      ...saved,
      skillLevels: { ...(saved.skillLevels || {}) },
      objectives: { ...(saved.objectives || {}) },
      repertoire: { ...(saved.repertoire || {}) },
      bosses: { ...(saved.bosses || {}) },
      scene: { ...(saved.scene || {}) },
      practice,
      peopleCanCall: Number.isFinite(people) ? Math.max(0, Math.min(999, Math.round(people))) : 0,
    };
  }

  function loadState() {
    try {
      const saved = JSON.parse(localStorage.getItem(STORAGE_KEY) || "null");
      return normalizeState(saved);
    } catch {
      return structuredClone(stateDefaults);
    }
  }

  function persistState() {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  }

  function saveState(eventType = "campaign.changed") {
    persistState();
    syncOutbox.push({
      clientMutationId: crypto.randomUUID(),
      eventType,
      state: structuredClone(state),
    });
    if (syncOutbox.length > 200) syncOutbox = syncOutbox.slice(-200);
    saveOutbox();
    flushOutbox();
  }

  function loadSyncRevision() {
    try {
      return Math.max(0, Number(JSON.parse(localStorage.getItem(SYNC_META_KEY) || "{}").revision || 0));
    } catch {
      return 0;
    }
  }

  function saveSyncRevision() {
    localStorage.setItem(SYNC_META_KEY, JSON.stringify({ revision: syncRevision, updatedAt: new Date().toISOString() }));
  }

  function loadOutbox() {
    try {
      const saved = JSON.parse(localStorage.getItem(OUTBOX_KEY) || "[]");
      return Array.isArray(saved) ? saved : [];
    } catch {
      return [];
    }
  }

  function saveOutbox() {
    localStorage.setItem(OUTBOX_KEY, JSON.stringify(syncOutbox));
  }

  function hasMeaningfulProgress(candidate) {
    return candidate.practice.length > 0
      || Number(candidate.peopleCanCall) > 0
      || [candidate.skillLevels, candidate.objectives, candidate.repertoire, candidate.bosses, candidate.scene]
        .some((group) => Object.values(group).some(Boolean));
  }

  function setSyncStatus(message, tone = "") {
    const element = document.getElementById("sync-status");
    if (!element) return;
    element.textContent = message;
    element.dataset.tone = tone;
  }

  async function apiRequest(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
      ...options,
      headers: {
        ...(options.body ? { "Content-Type": "application/json" } : {}),
        ...(options.headers || {}),
      },
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(body.error || `Cloud request failed (${response.status})`);
      error.status = response.status;
      error.body = body;
      throw error;
    }
    return body;
  }

  async function initializeCloudSync() {
    setSyncStatus("Connecting private cloud progress...");
    try {
      const remote = await apiRequest("/state");
      syncRevision = Number(remote.revision || 0);
      saveSyncRevision();
      const cloudBound = localStorage.getItem(CLOUD_BOUND_KEY) === "1";

      if (syncOutbox.length) {
        localStorage.setItem(CLOUD_BOUND_KEY, "1");
        await flushOutbox();
        return;
      }

      if (remote.hasState) {
        if (hasMeaningfulProgress(state) && !cloudBound) {
          const useCloud = confirm("Cloud progress already exists. Load it here? Cancel keeps this browser copy unchanged so you can export it first.");
          if (!useCloud) {
            syncPaused = true;
            setSyncStatus("Browser copy preserved; cloud sync paused.", "offline");
            return;
          }
        }
        state = normalizeState(remote.state);
        persistState();
        localStorage.setItem(CLOUD_BOUND_KEY, "1");
        renderAll();
        setSyncStatus("Progress saved privately in the cloud.", "online");
        return;
      }

      if (hasMeaningfulProgress(state) && !cloudBound) {
        const migrate = confirm("Move this browser's Jazz Project progress into your private cloud account now?");
        if (!migrate) {
          syncPaused = true;
          setSyncStatus("Progress remains in this browser; cloud sync paused.", "offline");
          return;
        }
        localStorage.setItem(CLOUD_BOUND_KEY, "1");
        saveState("campaign.browser_imported");
        return;
      }

      localStorage.setItem(CLOUD_BOUND_KEY, "1");
      setSyncStatus("Private cloud progress is ready.", "online");
    } catch {
      setSyncStatus("Cloud unavailable; changes are queued safely on this device.", "offline");
    }
  }

  async function flushOutbox() {
    if (syncPaused || syncInFlight || !syncOutbox.length) return;
    syncInFlight = true;
    setSyncStatus(`Syncing ${syncOutbox.length} queued change${syncOutbox.length === 1 ? "" : "s"}...`);
    try {
      while (syncOutbox.length) {
        const mutation = syncOutbox[0];
        try {
          const response = await apiRequest("/sync", {
            method: "POST",
            body: JSON.stringify({ ...mutation, baseRevision: syncRevision }),
          });
          syncRevision = Number(response.revision || syncRevision + 1);
          saveSyncRevision();
          syncOutbox.shift();
          saveOutbox();
        } catch (error) {
          if (error.status === 409 && Number.isFinite(Number(error.body?.revision))) {
            syncRevision = Number(error.body.revision);
            saveSyncRevision();
            continue;
          }
          throw error;
        }
      }
      localStorage.setItem(CLOUD_BOUND_KEY, "1");
      setSyncStatus("Progress saved privately in the cloud.", "online");
    } catch {
      setSyncStatus("Cloud unavailable; changes are queued safely on this device.", "offline");
    } finally {
      syncInFlight = false;
    }
  }

  function localDateKey(date = new Date()) {
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, "0");
    const day = String(date.getDate()).padStart(2, "0");
    return `${year}-${month}-${day}`;
  }

  function dateFromKey(key) {
    const [year, month, day] = key.split("-").map(Number);
    return new Date(year, month - 1, day);
  }

  function addDays(date, amount) {
    const next = new Date(date);
    next.setDate(next.getDate() + amount);
    return next;
  }

  function startOfWeek(date = new Date()) {
    const day = date.getDay();
    const mondayOffset = day === 0 ? -6 : 1 - day;
    const monday = addDays(new Date(date.getFullYear(), date.getMonth(), date.getDate()), mondayOffset);
    monday.setHours(0, 0, 0, 0);
    return monday;
  }

  function practiceMinutesBetween(start, end) {
    return state.practice.reduce((sum, entry) => {
      const date = dateFromKey(entry.date);
      return date >= start && date < end ? sum + Number(entry.minutes || 0) : sum;
    }, 0);
  }

  function setText(id, value) {
    const element = document.getElementById(id);
    if (element) element.textContent = value;
  }

  function showToast(message) {
    const toast = $("#toast");
    toast.textContent = message;
    toast.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toast.classList.remove("show"), 2400);
  }

  function renderStats() {
    const totalMinutes = state.practice.reduce((sum, item) => sum + Number(item.minutes || 0), 0);
    const startedTunes = Object.values(state.repertoire).filter((stage) => Number(stage) > 0).length;
    const bosses = Object.values(state.bosses).filter(Boolean).length;
    const skillLevels = Object.values(state.skillLevels).reduce((sum, level) => sum + Number(level || 0), 0);
    const objectives = Object.values(state.objectives).filter(Boolean).length;
    const repertoireStages = Object.values(state.repertoire).reduce((sum, stage) => sum + Number(stage || 0), 0);
    const sceneSteps = Object.values(state.scene).filter(Boolean).length;
    const xp = totalMinutes + skillLevels * 80 + objectives * 35 + repertoireStages * 45 + bosses * 250 + sceneSteps * 90;
    const level = Math.floor(xp / 1000) + 1;

    const skillPart = skillLevels / (DATA.skills.length * MAX_SKILL_LEVEL);
    const missionPart = objectives / DATA.mission.objectives.length;
    const tunePart = repertoireStages / (DATA.repertoire.length * 6);
    const bossPart = bosses / DATA.bosses.length;
    const scenePart = sceneSteps / DATA.sceneSteps.length;
    const progress = Math.round((skillPart * 0.42 + missionPart * 0.16 + tunePart * 0.16 + bossPart * 0.18 + scenePart * 0.08) * 100);

    setText("stat-hours", (totalMinutes / 60).toFixed(1));
    setText("stat-streak", calculateStreak());
    setText("stat-tunes", startedTunes);
    setText("stat-bosses", bosses);
    setText("stat-people", Number(state.peopleCanCall || 0));
    setText("campaign-xp", xp.toLocaleString());
    setText("campaign-level", `LV ${String(level).padStart(2, "0")}`);
    setText("campaign-progress", `${progress}%`);
    $("#campaign-meter").style.width = `${progress}%`;
    setText("skills-earned", skillLevels);
  }

  function calculateStreak() {
    const activeDates = new Set(state.practice.filter((entry) => Number(entry.minutes) > 0).map((entry) => entry.date));
    if (!activeDates.size) return 0;
    let cursor = new Date();
    if (!activeDates.has(localDateKey(cursor))) cursor = addDays(cursor, -1);
    let streak = 0;
    while (activeDates.has(localDateKey(cursor))) {
      streak += 1;
      cursor = addDays(cursor, -1);
    }
    return streak;
  }

  function renderSessions() {
    const list = $("#session-list");
    const today = localDateKey();
    list.replaceChildren();
    DATA.sessions.forEach((session) => {
      const logId = `daily-${today}-${session.id}`;
      const complete = state.practice.some((entry) => entry.id === logId);
      const card = document.createElement("article");
      card.className = `session-card${complete ? " complete" : ""}`;
      card.innerHTML = `
        <span class="session-time">${session.time}</span>
        <div class="session-copy"><h3>${session.title}</h3><p>${session.detail}</p></div>
        <button class="check-button" type="button" aria-label="${complete ? "Undo" : "Complete"} ${session.title}" aria-pressed="${complete}">✓</button>`;
      $("button", card).addEventListener("click", () => {
        const existing = state.practice.findIndex((entry) => entry.id === logId);
        if (existing >= 0) {
          state.practice.splice(existing, 1);
          showToast("Session reopened");
        } else {
          state.practice.push({ id: logId, date: today, minutes: session.minutes, track: session.track, note: session.title, preset: true });
          showToast(`+${session.minutes} minutes logged`);
        }
        saveState(existing >= 0 ? "practice.session_reopened" : "practice.session_completed");
        renderAll();
      });
      list.appendChild(card);
    });
  }

  function renderWeek() {
    const monday = startOfWeek();
    const nextMonday = addDays(monday, 7);
    const minutes = practiceMinutesBetween(monday, nextMonday);
    const percent = Math.min(100, Math.round((minutes / DATA.weeklyTargetMinutes) * 100));
    setText("week-hours", (minutes / 60).toFixed(1));
    setText("week-percent", `${percent}%`);
    $("#week-meter").style.width = `${percent}%`;
    setText("week-note", minutes >= DATA.weeklyTargetMinutes
      ? "Weekly target cleared. Protect the streak; do not manufacture fatigue."
      : `${Math.max(0, DATA.weeklyTargetMinutes - minutes)} focused minutes remain in this week's campaign.`);

    const chart = $("#week-chart");
    chart.replaceChildren();
    const dailyMinutes = [];
    for (let index = 0; index < 7; index += 1) {
      const date = addDays(monday, index);
      const key = localDateKey(date);
      const value = state.practice.filter((entry) => entry.date === key).reduce((sum, entry) => sum + Number(entry.minutes || 0), 0);
      dailyMinutes.push({ date, key, value });
    }
    const max = Math.max(100, ...dailyMinutes.map((day) => day.value));
    dailyMinutes.forEach((day) => {
      const column = document.createElement("div");
      column.className = `day-column${day.key === localDateKey() ? " today" : ""}`;
      column.title = `${day.value} minutes`;
      column.innerHTML = `<div class="day-bar-track"><span class="day-bar" style="height:${Math.max(2, (day.value / max) * 100)}%"></span></div><span class="day-label">${day.date.toLocaleDateString(undefined, { weekday: "short" }).slice(0, 2)}</span>`;
      chart.appendChild(column);
    });
  }

  function renderMission() {
    const list = $("#objective-list");
    list.replaceChildren();
    DATA.mission.objectives.forEach((objective, index) => {
      const complete = Boolean(state.objectives[index]);
      const button = document.createElement("button");
      button.type = "button";
      button.className = `objective${complete ? " complete" : ""}`;
      button.setAttribute("aria-pressed", complete);
      button.textContent = objective;
      button.addEventListener("click", () => {
        state.objectives[index] = !complete;
        saveState("mission.objective_toggled");
        renderAll();
      });
      list.appendChild(button);
    });
    const completed = Object.values(state.objectives).filter(Boolean).length;
    setText("mission-count", `${completed} / ${DATA.mission.objectives.length}`);
    setText("mission-unlock", completed === DATA.mission.objectives.length
      ? "Mission clear: Blue Bossa is ready for the real room."
      : "Complete the mission to unlock Boss 3: play Blue Bossa from memory.");
  }

  function skillLevel(id) {
    return Math.max(0, Math.min(MAX_SKILL_LEVEL, Number(state.skillLevels[id] || 0)));
  }

  function skillUnlocked(skill) {
    return skill.prereqs.every((id) => skillLevel(id) >= 1);
  }

  function renderTrackTabs() {
    const tabs = $("#track-tabs");
    tabs.replaceChildren();
    DATA.tracks.forEach((track) => {
      const button = document.createElement("button");
      button.type = "button";
      button.role = "tab";
      button.className = "track-tab";
      button.setAttribute("aria-selected", track.id === activeTrack);
      button.textContent = track.name;
      button.addEventListener("click", () => {
        activeTrack = track.id;
        renderTrackTabs();
        renderSkillTree();
      });
      tabs.appendChild(button);
    });
  }

  function renderSkillTree() {
    const tree = $("#skill-tree-grid");
    tree.replaceChildren();
    const visible = DATA.skills.filter((skill) => activeTrack === "all" || skill.track === activeTrack);
    const tiers = [...new Set(visible.map((skill) => skill.tier))].sort((a, b) => a - b);
    tiers.forEach((tierNumber) => {
      const tier = document.createElement("section");
      tier.className = "skill-tier";
      const label = document.createElement("div");
      label.className = "tier-label";
      label.textContent = `Tier ${tierNumber} · ${["Foundation", "Connection", "Fluency", "Bandstand", "First call"][tierNumber - 1]}`;
      const grid = document.createElement("div");
      grid.className = "skill-tier-grid";
      visible.filter((skill) => skill.tier === tierNumber).forEach((skill) => {
        const level = skillLevel(skill.id);
        const unlocked = skillUnlocked(skill);
        const node = document.createElement("button");
        node.type = "button";
        node.className = `skill-node${!unlocked ? " locked" : ""}${level === MAX_SKILL_LEVEL ? " mastered" : ""}${unlocked && level === 0 ? " active" : ""}`;
        node.disabled = !unlocked;
        node.innerHTML = `
          <div class="node-top"><span class="node-track">${trackName(skill.track)}</span><span class="node-level">${unlocked ? `LV ${level}/${MAX_SKILL_LEVEL}` : "LOCKED"}</span></div>
          <h3>${skill.name}</h3>
          <p>${skill.summary}</p>
          ${unlocked ? `<div class="node-pips">${[1,2,3,4].map((n) => `<span class="node-pip${n <= level ? " on" : ""}"></span>`).join("")}</div>` : `<span class="node-lock">Requires ${skill.prereqs.map(skillName).join(" + ")}</span>`}`;
        if (unlocked) node.addEventListener("click", () => openSkill(skill.id));
        grid.appendChild(node);
      });
      tier.append(label, grid);
      tree.appendChild(tier);
    });
  }

  function trackName(id) {
    return DATA.tracks.find((track) => track.id === id)?.name || id;
  }

  function skillName(id) {
    return DATA.skills.find((skill) => skill.id === id)?.name || id;
  }

  function openSkill(id) {
    activeSkillId = id;
    renderSkillDialog();
    $("#skill-dialog").showModal();
  }

  function renderSkillDialog() {
    const skill = DATA.skills.find((item) => item.id === activeSkillId);
    if (!skill) return;
    const level = skillLevel(skill.id);
    setText("skill-dialog-track", `${trackName(skill.track)} · Tier ${skill.tier}`);
    setText("skill-dialog-title", skill.name);
    setText("skill-dialog-copy", skill.summary);
    setText("skill-dialog-unlock", skill.unlock);
    const road = $("#skill-level-road");
    road.replaceChildren();
    skill.levels.forEach((description, index) => {
      const stepLevel = index + 1;
      const step = document.createElement("div");
      step.className = `level-step${stepLevel <= level ? " complete" : ""}${stepLevel === level + 1 ? " current" : ""}`;
      step.innerHTML = `<span class="level-index">LV ${stepLevel}</span><span>${description}</span>`;
      road.appendChild(step);
    });
    $("#level-down").disabled = level <= 0;
    $("#level-up").disabled = level >= MAX_SKILL_LEVEL;
    $("#level-up").textContent = level >= MAX_SKILL_LEVEL ? "Skill mastered" : "Complete next level";
  }

  function renderRepertoire() {
    const grid = $("#repertoire-grid");
    grid.replaceChildren();
    DATA.repertoire.forEach((tune, index) => {
      const stage = Math.max(0, Math.min(6, Number(state.repertoire[tune.id] || 0)));
      const card = document.createElement("article");
      card.className = `tune-card${tune.current ? " current" : ""}`;
      card.innerHTML = `
        <span class="tune-index">${String(index + 1).padStart(2, "0")}${tune.current ? " · CURRENT" : ""}</span>
        <h3>${tune.title}</h3>
        <p class="tune-lesson">${tune.lesson}</p>
        <div class="stars" aria-label="${stage} of 6 stages">${[1,2,3,4,5,6].map((n) => `<span class="${n <= stage ? "on" : ""}">★</span>`).join("")}</div>
        <div class="tune-stage">${DATA.repertoireStages[stage]}</div>
        <div class="tune-actions">
          <button type="button" data-direction="down" aria-label="Move ${tune.title} back one stage" ${stage === 0 ? "disabled" : ""}>−</button>
          <button type="button" data-direction="up" aria-label="Advance ${tune.title} one stage" ${stage === 6 ? "disabled" : ""}>${stage === 6 ? "Gig ready" : "+ Advance"}</button>
        </div>`;
      $$("button", card).forEach((button) => button.addEventListener("click", () => {
        const direction = button.dataset.direction === "up" ? 1 : -1;
        state.repertoire[tune.id] = Math.max(0, Math.min(6, stage + direction));
        saveState("repertoire.stage_changed");
        renderAll();
      }));
      grid.appendChild(card);
    });
  }

  function renderRoadmap() {
    const grid = $("#roadmap-grid");
    grid.replaceChildren();
    DATA.roadmap.forEach((phase, index) => {
      const section = document.createElement("article");
      section.className = `roadmap-phase${index === 0 ? " current" : ""}`;
      section.innerHTML = `<span class="phase-number">${phase.number}</span><h3>${phase.title}</h3><p>${phase.copy}</p><ul>${phase.items.map((item) => `<li>${item}</li>`).join("")}</ul>`;
      grid.appendChild(section);
    });
  }

  function renderScene() {
    const route = $("#scene-route");
    route.replaceChildren();
    DATA.sceneSteps.forEach((step, index) => {
      const complete = Boolean(state.scene[step.id]);
      const unlocked = index === 0 || Boolean(state.scene[DATA.sceneSteps[index - 1].id]);
      const stop = document.createElement("article");
      stop.className = `scene-stop${complete ? " complete" : ""}${unlocked && !complete ? " current" : ""}`;
      stop.innerHTML = `<button type="button" class="stop-dot" ${!unlocked ? "disabled" : ""} aria-label="${complete ? "Reopen" : "Complete"} ${step.title}" aria-pressed="${complete}">${complete ? "✓" : index + 1}</button><h3>${step.title}</h3><p>${step.detail}</p>`;
      $("button", stop).addEventListener("click", () => {
        if (!unlocked) return;
        const newValue = !complete;
        state.scene[step.id] = newValue;
        if (!newValue) DATA.sceneSteps.slice(index + 1).forEach((future) => { state.scene[future.id] = false; });
        saveState("scene.step_toggled");
        renderAll();
      });
      route.appendChild(stop);
    });
    setText("scene-count", Object.values(state.scene).filter(Boolean).length);
  }

  function renderBosses() {
    const list = $("#boss-list");
    list.replaceChildren();
    DATA.bosses.forEach((boss, index) => {
      const complete = Boolean(state.bosses[index]);
      const unlocked = index === 0 || Boolean(state.bosses[index - 1]);
      const card = document.createElement("article");
      card.className = `boss-card${complete ? " complete" : ""}${unlocked && !complete ? " current" : ""}${!unlocked ? " locked" : ""}`;
      card.innerHTML = `<span class="boss-number">B${String(index + 1).padStart(2, "0")}</span><div class="boss-copy"><h3>${boss.title}</h3><p>${boss.detail}</p></div><button type="button" class="boss-toggle" ${!unlocked ? "disabled" : ""} aria-label="${complete ? "Reopen" : "Clear"} ${boss.title}" aria-pressed="${complete}">✓</button>`;
      $("button", card).addEventListener("click", () => {
        if (!unlocked) return;
        const newValue = !complete;
        state.bosses[index] = newValue;
        if (!newValue) DATA.bosses.slice(index + 1).forEach((_, futureIndex) => { state.bosses[index + futureIndex + 1] = false; });
        saveState("boss.status_changed");
        renderAll();
        showToast(newValue ? `Boss ${index + 1} cleared · +250 XP` : `Boss ${index + 1} reopened`);
      });
      list.appendChild(card);
    });
  }

  function renderAll() {
    renderStats();
    renderSessions();
    renderWeek();
    renderMission();
    renderTrackTabs();
    renderSkillTree();
    renderRepertoire();
    renderScene();
    renderBosses();
    if ($("#skill-dialog").open) renderSkillDialog();
  }

  function setupDialogs() {
    $$('[data-close-dialog]').forEach((button) => button.addEventListener("click", () => button.closest("dialog").close()));
    $$('dialog').forEach((dialog) => dialog.addEventListener("click", (event) => {
      const rect = dialog.getBoundingClientRect();
      const outside = event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom;
      if (outside) dialog.close();
    }));
    $("#open-log").addEventListener("click", () => $("#log-dialog").showModal());
    $("#level-up").addEventListener("click", () => changeSkillLevel(1));
    $("#level-down").addEventListener("click", () => changeSkillLevel(-1));
  }

  function changeSkillLevel(direction) {
    const skill = DATA.skills.find((item) => item.id === activeSkillId);
    if (!skill || !skillUnlocked(skill)) return;
    const current = skillLevel(skill.id);
    const next = Math.max(0, Math.min(MAX_SKILL_LEVEL, current + direction));
    state.skillLevels[skill.id] = next;
    saveState("skill.level_changed");
    renderAll();
    showToast(direction > 0 ? `${skill.name} · level ${next}` : `${skill.name} adjusted`);
  }

  function setupPracticeLog() {
    $("#log-form").addEventListener("submit", (event) => {
      event.preventDefault();
      const form = new FormData(event.currentTarget);
      const minutes = Math.max(1, Math.min(360, Number(form.get("minutes"))));
      state.practice.push({
        id: `manual-${Date.now()}`,
        date: localDateKey(),
        minutes,
        track: String(form.get("track")),
        note: String(form.get("note") || "").slice(0, 100),
      });
      saveState("practice.session_logged");
      event.currentTarget.reset();
      $("#log-dialog").close();
      renderAll();
      showToast(`+${minutes} minutes logged`);
    });
  }

  function setupDataActions() {
    $("#edit-people").addEventListener("click", () => {
      const answer = prompt("How many musicians could realistically call you for a rehearsal or gig today?", String(state.peopleCanCall || 0));
      if (answer === null) return;
      const value = Number(answer);
      if (!Number.isInteger(value) || value < 0 || value > 999) {
        showToast("Enter a whole number from 0 to 999");
        return;
      }
      state.peopleCanCall = value;
      saveState("network.people_count_changed");
      renderStats();
    });

    $("#export-data").addEventListener("click", () => {
      const blob = new Blob([JSON.stringify(state, null, 2)], { type: "application/json" });
      const link = document.createElement("a");
      link.href = URL.createObjectURL(blob);
      link.download = `jazz-project-${localDateKey()}.json`;
      link.click();
      setTimeout(() => URL.revokeObjectURL(link.href), 1000);
      showToast("Progress exported");
    });

    $("#import-data").addEventListener("change", async (event) => {
      const file = event.target.files?.[0];
      if (!file) return;
      try {
        const imported = JSON.parse(await file.text());
        if (!imported || typeof imported !== "object" || !Array.isArray(imported.practice)) throw new Error("Invalid file");
        state = normalizeState(imported);
        saveState("campaign.file_imported");
        renderAll();
        showToast("Progress imported");
      } catch {
        showToast("That progress file could not be read");
      } finally {
        event.target.value = "";
      }
    });

    $("#reset-data").addEventListener("click", () => {
      if (!confirm("Reset every skill, practice log, tune, and boss fight? Export first if you may want it back.")) return;
      state = structuredClone(stateDefaults);
      saveState("campaign.reset");
      renderAll();
      showToast("Campaign reset");
    });
  }

  renderRoadmap();
  setupDialogs();
  setupPracticeLog();
  setupDataActions();
  addEventListener("jazz:activity-logged", (event) => {
    const activity = event.detail;
    if (!activity?.id || state.practice.some((entry) => entry.id === `activity-${activity.id}`)) return;
    const occurredAt = new Date(activity.occurredAt || Date.now());
    const languageCategories = new Set(["scales", "ear-training", "improvisation"]);
    state.practice.push({
      id: `activity-${activity.id}`,
      date: localDateKey(occurredAt),
      minutes: Number(activity.durationMinutes),
      track: languageCategories.has(activity.category) ? "language" : "trumpet",
      note: String(activity.title || "Practice activity").slice(0, 100),
    });
    saveState("practice.activity_logged");
    renderAll();
    showToast(`+${activity.durationMinutes} minutes logged`);
  });
  renderAll();
  addEventListener("online", flushOutbox);
  initializeCloudSync();
})();
