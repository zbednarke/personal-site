(() => {
  "use strict";
  const Model = globalThis.JazzClipStudioModel;
  if (!Model) return;

  function mount(media, value, onChange, onCommit) {
    const stage = media.closest(".video-player-stage") || media.parentElement;
    const layer = document.createElement("div");
    layer.className = "studio-crop-layer";
    const frame = document.createElement("div");
    frame.className = "studio-crop-frame";
    frame.tabIndex = 0;
    frame.setAttribute("role", "group");
    frame.setAttribute("aria-label", "Vertical crop. Arrow keys move; plus and minus resize.");
    for (const direction of ["nw", "n", "ne", "e", "se", "s", "sw", "w"]) {
      const handle = document.createElement("button");
      handle.type = "button";
      handle.className = `studio-crop-handle ${direction}`;
      handle.dataset.resize = direction;
      handle.setAttribute("aria-label", `Resize vertical crop ${direction}`);
      frame.append(handle);
    }
    layer.append(frame); stage.append(layer);
    let crop = Model.normalizeCrop(value), bounds = null, drag = null;

    function draw() {
      const geometry = Model.cropGeometry(crop, media.videoWidth, media.videoHeight);
      const video = media.getBoundingClientRect(), container = stage.getBoundingClientRect();
      layer.hidden = !geometry || !video.width || !video.height;
      if (layer.hidden) return;
      const factor = Math.min(video.width / media.videoWidth, video.height / media.videoHeight);
      bounds = { width: media.videoWidth * factor, height: media.videoHeight * factor };
      Object.assign(layer.style, { left: `${video.left - container.left + (video.width - bounds.width) / 2}px`,
        top: `${video.top - container.top + (video.height - bounds.height) / 2}px`, width: `${bounds.width}px`, height: `${bounds.height}px` });
      Object.assign(frame.style, { left: `${geometry.x * 100}%`, top: `${geometry.y * 100}%`, width: `${geometry.width * 100}%`, height: `${geometry.height * 100}%` });
      frame.setAttribute("aria-description", `9:16 crop, ${Math.round(geometry.scale * 100)} percent size`);
    }
    function change(next) {
      const geometry = Model.cropGeometry(next, media.videoWidth, media.videoHeight);
      if (!geometry) return;
      crop = { centerX: geometry.centerX, centerY: geometry.centerY, scale: geometry.scale };
      draw(); onChange({ ...crop });
    }
    function move(event) {
      if (!drag || event.pointerId !== drag.pointerId) return;
      const dx = event.clientX - drag.x, dy = event.clientY - drag.y;
      if (!drag.direction) {
        change({ ...drag.crop, centerX: drag.crop.centerX + dx / drag.bounds.width, centerY: drag.crop.centerY + dy / drag.bounds.height });
      } else {
        const base = Model.cropGeometry({ scale: 1 }, media.videoWidth, media.videoHeight);
        const sx = drag.direction.includes("e") ? 1 : drag.direction.includes("w") ? -1 : 0;
        const sy = drag.direction.includes("s") ? 1 : drag.direction.includes("n") ? -1 : 0;
        const width = base.width * drag.bounds.width * Math.abs(sx), height = base.height * drag.bounds.height * Math.abs(sy);
        change({ ...drag.crop, scale: drag.crop.scale + 2 * (sx * dx * width + sy * dy * height) / (width * width + height * height) });
      }
    }
    function finish(event) {
      if (!drag || (event.pointerId != null && event.pointerId !== drag.pointerId)) return;
      const previous = drag; drag = null;
      if (event.type === "pointercancel" || event.key === "Escape") change(previous.crop);
      if (frame.hasPointerCapture?.(previous.pointerId)) frame.releasePointerCapture(previous.pointerId);
      onCommit();
    }
    function start(event) {
      if (event.button !== 0 || !bounds || drag) return;
      event.preventDefault(); event.stopPropagation();
      const geometry = Model.cropGeometry(crop, media.videoWidth, media.videoHeight);
      drag = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, bounds: { ...bounds },
        crop: { centerX: geometry.centerX, centerY: geometry.centerY, scale: geometry.scale }, direction: event.target.dataset.resize || "" };
      frame.setPointerCapture(event.pointerId); frame.focus({ preventScroll: true });
    }
    function key(event) {
      if (event.key === "Escape" && drag) { event.preventDefault(); finish(event); return; }
      const step = event.shiftKey ? .05 : .01;
      const next = { ...crop };
      if (event.key === "ArrowLeft") next.centerX -= step;
      else if (event.key === "ArrowRight") next.centerX += step;
      else if (event.key === "ArrowUp") next.centerY -= step;
      else if (event.key === "ArrowDown") next.centerY += step;
      else if (event.key === "+" || event.key === "=") next.scale += .05;
      else if (event.key === "-" || event.key === "_") next.scale -= .05;
      else return;
      event.preventDefault(); event.stopPropagation(); change(next); onCommit();
    }
    frame.addEventListener("pointerdown", start);
    frame.addEventListener("pointermove", move);
    frame.addEventListener("pointerup", finish);
    frame.addEventListener("pointercancel", finish);
    frame.addEventListener("lostpointercapture", finish);
    frame.addEventListener("keydown", key);
    frame.addEventListener("click", event => { event.preventDefault(); event.stopPropagation(); });
    const observer = new ResizeObserver(draw);
    observer.observe(media); observer.observe(stage);
    media.addEventListener("loadedmetadata", draw);
    media.addEventListener("resize", draw);
    document.addEventListener("fullscreenchange", draw);
    draw();
    return {
      update(next) { crop = Model.normalizeCrop(next); draw(); },
      dispose() {
        if (drag) finish({ type: "pointercancel" });
        observer.disconnect(); media.removeEventListener("loadedmetadata", draw); media.removeEventListener("resize", draw);
        document.removeEventListener("fullscreenchange", draw); layer.remove();
      },
    };
  }
  globalThis.JazzClipStudioCrop = { mount };
})();
