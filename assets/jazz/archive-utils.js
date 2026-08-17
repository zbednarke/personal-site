(() => {
  "use strict";

  function dateKey(date) {
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, "0");
    const day = String(date.getDate()).padStart(2, "0");
    return `${year}-${month}-${day}`;
  }

  function parseDateKey(value) {
    const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(value || ""));
    if (!match) return null;
    const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
    return dateKey(date) === value ? date : null;
  }

  function monthGrid(month) {
    const first = new Date(month.getFullYear(), month.getMonth(), 1);
    const mondayOffset = (first.getDay() + 6) % 7;
    const start = new Date(first);
    start.setDate(first.getDate() - mondayOffset);
    return Array.from({ length: 42 }, (_, index) => {
      const date = new Date(start);
      date.setDate(start.getDate() + index);
      return {
        date,
        key: dateKey(date),
        inMonth: date.getMonth() === first.getMonth() && date.getFullYear() === first.getFullYear(),
      };
    });
  }

  function durationSeconds(milliseconds) {
    const value = Number(milliseconds || 0) / 1000;
    return Number.isFinite(value) && value > 0 ? value : 0;
  }

  function formatPlaybackTime(seconds) {
    const total = Math.max(0, Math.floor(Number(seconds) || 0));
    const hours = Math.floor(total / 3600);
    const minutes = Math.floor((total % 3600) / 60);
    const remainder = total % 60;
    if (hours) return `${hours}:${String(minutes).padStart(2, "0")}:${String(remainder).padStart(2, "0")}`;
    return `${String(minutes).padStart(2, "0")}:${String(remainder).padStart(2, "0")}`;
  }

  function formatPracticeDuration(milliseconds, precise = false) {
    const minutes = Math.max(0, Number(milliseconds || 0) / 60000);
    if (precise && minutes > 0 && minutes < 10) return `${minutes.toFixed(1)} min`;
    return `${Math.round(minutes)} min`;
  }

  function recordingTitle(recording) {
    return String(recording?.practiceBlockTitle || recording?.practiceSessionTitle || "Uncategorized practice");
  }

  function takeLabel(recording) {
    const number = Number(recording?.takeNumber || 0);
    return number > 0 ? `Take ${number}` : "Take";
  }

  const api = {
    dateKey,
    durationSeconds,
    formatPlaybackTime,
    formatPracticeDuration,
    monthGrid,
    parseDateKey,
    recordingTitle,
    takeLabel,
  };
  globalThis.JazzArchiveUtils = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})();
