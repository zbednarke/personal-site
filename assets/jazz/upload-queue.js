(() => {
  "use strict";

  class UploadQueue {
    constructor(worker, onState = () => {}) {
      if (typeof worker !== "function") throw new TypeError("UploadQueue requires a worker");
      this.worker = worker;
      this.onState = onState;
      this.jobs = [];
      this.running = false;
      this.nextID = 1;
    }

    enqueue(payload) {
      const job = {
        id: `upload-${this.nextID++}`,
        payload,
        status: "queued",
        progress: 0,
        error: "",
      };
      this.jobs.push(job);
      this.emit(job);
      this.schedule();
      return job.id;
    }

    retry(id) {
      const job = this.jobs.find((candidate) => candidate.id === id && candidate.status === "failed");
      if (!job) return false;
      job.status = "queued";
      job.progress = 0;
      job.error = "";
      this.emit(job);
      this.schedule();
      return true;
    }

    hasPending() {
      return this.jobs.length > 0;
    }

    schedule() {
      queueMicrotask(() => this.drain());
    }

    emit(job) {
      this.onState({
        id: job.id,
        payload: job.payload,
        status: job.status,
        progress: job.progress,
        error: job.error,
      });
    }

    async drain() {
      if (this.running) return;
      this.running = true;
      try {
        let job = this.jobs.find((candidate) => candidate.status === "queued");
        while (job) {
          job.status = "uploading";
          this.emit(job);
          try {
            await this.worker(job.payload, (progress) => {
              if (job.status !== "uploading") return;
              job.progress = Math.max(0, Math.min(100, Math.round(progress)));
              this.emit(job);
            });
            job.status = "complete";
            job.progress = 100;
            this.emit(job);
            this.jobs = this.jobs.filter((candidate) => candidate !== job);
          } catch (error) {
            job.status = "failed";
            job.error = error instanceof Error ? error.message : String(error);
            this.emit(job);
          }
          job = this.jobs.find((candidate) => candidate.status === "queued");
        }
      } finally {
        this.running = false;
      }
    }
  }

  globalThis.JazzUploadQueue = UploadQueue;
})();
