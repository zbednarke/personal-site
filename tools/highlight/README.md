# Local Clip Studio editor

From Clip Studio, **Make today's film** opens a loopback worker on this PC. It snapshots the selected day's existing non-rejected candidates, prioritizes manual/liked moments, launches `gpt-6-astra` with `model_reasoning_effort="medium"`, validates the returned cut list, and renders a local 1080p MP4. No YouTube upload is performed.

## Start

Requires Python 3.10+, Node, FFmpeg/ffprobe and a current Codex CLI signed in with `codex login`. A dedicated current CLI can be installed without changing your global CLI:

```powershell
npm install --prefix .local/highlight-tools @openai/codex@latest
./tools/highlight/start.ps1
```

Leave the worker running, open the Jazz page's Clip Studio, choose the practice date and press **Make today's film**. Permit the editor window if the browser blocks popups. In the local editor, watch progress, cancel a job, replay/download completed drafts, or open a draft in Clip Studio. Timeline import saves a previous-timeline backup. If the current day's candidate bounds or rejections changed since generation, import refuses stale cuts.

The worker binds only to `127.0.0.1:8765`. It does not expose a public API or require a website API key. The website obtains its normal short-lived signed media URLs and passes them to the worker window through an origin-checked handoff. Worker mutations require a per-process local token; arbitrary websites cannot start an agent through a cross-origin request. Signed URLs are not stored in the agent manifest or passed to shell commands. Only Google Storage HTTPS URLs are accepted; redirects are refused.

## Editing behavior

- No new highlight scan: only supplied suggested/manual candidates are eligible.
- Review every manual or liked candidate; include at least one when available.
- Whole candidates or trims inside their bounds; no repeated overlapping source time.
- Choose a 1, 2, 3, 5, or 10 minute target in Clip Studio. The editor aims within 15 seconds of that target, allowing a shorter film when eligible material is insufficient; maximum 60 clips. Each generation gets a separate job and film, so several versions can coexist on the same practice day.
- Astra receives notes, labels, activity bins and representative frames. It does not directly hear the music; energy is not a musical-quality score. User curation is the strongest taste signal.
- Video uses its embedded audio to preserve the shared recorder clock, especially for paused takes. Audio-only clips use WAV audio with a simple title slate. MP4 audio is AAC; this is a shareable export, not a new lossless master.
- No generated music, speech, pitch correction, speed changes or automatic publication.

Jobs live under `.local/highlights/<job-id>/`: source media, candidate evidence, Codex events, edit plan, timeline JSON and rendered MP4. These files are ignored by Git. Reasoning is cloud-hosted; downloads, signal analysis and rendering run on this PC. Candidate evidence is sent to Codex/OpenAI. Closing the local editor window does not cancel a running job. Cancel stops child processes; restarting the worker marks interrupted jobs as failed rather than silently restarting a paid run. Completed drafts survive restarts. Source media currently remains per job; manage disk use by removing completed job folders when no longer needed.

The website tab must remain available to return a draft directly. Downloaded `project.json` is also retained locally. Multiple simultaneous editing jobs are refused. CLI/model/auth failures are shown without switching models.

## Validation

```powershell
python -m unittest discover -s tools/highlight -p 'test_*.py' -v
node --test assets/jazz/*.test.js
```

Manual release checks: real Astra-medium invocation, source boundary validation, actual FFmpeg render/decode, audio-only slate, popup handoff, saved draft retrieval, cancellation and stale-candidate rejection.

## Automatic site publication

`start.ps1` also starts a separate publisher on this PC. It watches completed jobs, uploads verified video/poster files through the existing Google Cloud CLI login and IAP connection, then atomically updates `/srv/zachbednarke.com/films/index.json`. The private Jazz site serves this persistent index independently of static deployments. Each job has a unique film name and retains its original practice date; previous films are preserved. Clip Studio refreshes the shelf every 20 seconds while visible, without interrupting playback. Local development receives a matching index and local media links.

The publisher requires an authenticated `gcloud` installation with access to the site VM. It does not upload to YouTube. Upload failures leave the completed local draft intact and retry after five minutes. Per-job `publication.json` records upload status; publisher logs live in `.local/highlights`. Run `python tools/highlight/publish.py` to attach publishing to an already-running worker without restarting it. A loopback lock prevents duplicate publishers. The PC and publisher must remain running until upload completes.
