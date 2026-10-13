# Film tools: site-VM film service and local editor

Clip Studio has two film buttons:

- **Make today's film** runs the cloud Magic Film as an on-demand Cloud Run
  Job (`assets/jazz/highlight-local.js`, `jazz-api/magic_film_worker.go`). It
  does not use this directory. Deployment is documented in
  [`jazz-api/README.md`](../../jazz-api/README.md#magic-film-cloud-run-job).
- **Make note-map film** (`assets/jazz/highlight-hosted.js`) calls the site
  VM's film service at `/jazz/highlight-api`, which is `hosted.py` from this
  directory, installed at `/opt/jazz-highlights`. It analyzes every take,
  publishes a whole-day note map beside the film, and keeps the exact edit
  that the film editor (`assets/jazz/film-editor.js`) reopens. The same service
  serves the Library tab's PDFs (`library.py`) and day note analysis.

The film service was built and deployed outside this repository between
September 11 and October 5, 2026 and restored here from its last release.
The release archive was truncated: the release's `worker.py`,
`test_highlight.py`, `test_publish.py` and the end of `test_edit_api.py` were
lost, so this directory still has the September 11 versions of the first
three. A read-only comparison with `/opt/jazz-highlights` on the VM (Oct 13)
found every other file here identical to what runs there. The VM also has
`worker.py`, the full `test_edit_api.py`, `test_highlight.py`,
`test_publish.py`, and `test_exact_render.py`, `test_film_edits.py`,
`test_hosted.py`, `test_library.py` and `test_note_map.py`. Copy those back
(with fixture IDs and personal strings replaced) before changing or
redeploying the service from this repository.

See the [Jazz workflow guide](../../docs/jazz-workflows.md) for current status, exact-edit pitch overlays, storage and rendering decisions.

# Hosted daily films

The **Make note-map film** button calls `/jazz/highlight-api/jobs` on the authenticated site. No localhost server, popup, browser media download, or CLI is required. Submit a practice date and a 1/2/3/5/10-minute target; the service retrieves owned recordings and rejections from the existing API, analyzes, selects, renders and atomically publishes the film and note map. Reloading the page reconnects to persisted job status. A server restart marks interrupted jobs failed so they can be retried explicitly.

One job runs at a time on the site VM. Source media and render intermediates are removed after each job; the content-addressed analysis cache is capped at 500 MB. The service has CPU and memory limits, a 2 GB disk reserve, and runs without root access. Caddy's existing Jazz authentication verifies and overwrites the user header before forwarding to the loopback-only service. POSTs require the site's Origin; browser-supplied URLs/source lists are rejected. This deployment serves the existing single Jazz owner and denies other identities.

Install `deploy/jazz-highlights.service`, deploy this directory to `/opt/jazz-highlights`, install Python 3 + NumPy + FFmpeg, and run `deploy/install-jazz-highlights.py` as root. Credentials reuse the server-only Caddy environment; the browser never receives the gateway key. Deployment must verify anonymous denial, authenticated status/start/cancel, and a real server render.

## Optional local editor

The following legacy local workflow remains available for development and offline preparation. The production button no longer invokes it.

# Local editing tools

The legacy local worker runs on this PC. It analyzes every uploaded recording for the selected day, prioritizes manual/liked moments, discovers reliable pitched phrases, balances sections and takes, validates the cut list, and renders a local 1080p MP4. No YouTube upload is performed.

## Start

Requires Python 3.10+, NumPy (`python -m pip install -r tools/highlight/requirements.txt`) and FFmpeg/ffprobe. The default audio-aware editor is deterministic and does not require a model call. Legacy candidate-only jobs additionally require Node and a signed-in Codex CLI. A dedicated current CLI can be installed without changing your global CLI:

```powershell
npm install --prefix .local/highlight-tools @openai/codex@latest
./tools/highlight/start.ps1
```

The production button does not launch this worker. Local development integrations can submit a manifest through the worker’s origin-checked handoff. In the local editor, watch progress, cancel a job, replay/download completed drafts, or open a draft in Clip Studio. Timeline import saves a previous-timeline backup. If the current day's candidate bounds or rejections changed since generation, import refuses stale cuts.

The worker binds only to `127.0.0.1:8765`. It does not expose a public API or require a website API key. The website obtains its normal short-lived signed media URLs and passes them to the worker window through an origin-checked handoff. Worker mutations require a per-process local token; arbitrary websites cannot start an agent through a cross-origin request. Signed URLs are not stored in the agent manifest or passed to shell commands. Only Google Storage HTTPS URLs are accepted; redirects are refused.

## Editing behavior

- Full source scan: analyze embedded video audio on the video clock (or the WAV for audio-only takes). Cache versioned, SHA-256-keyed analysis across jobs. Reject user-excluded intervals when discovering passages. Generated passages import as editable local timeline clips without creating artificial manual/liked source candidates.
- Review every manual or liked candidate; include at least one when available.
- Whole candidates or trims inside their bounds; no repeated overlapping source time.
- Choose a 1, 2, 3, 5, or 10 minute target in Clip Studio. The editor aims within 15 seconds of that target, allowing a shorter film when eligible material is insufficient; maximum 24 clips. Each generation gets a separate job and film, so several versions can coexist on the same practice day.
- The default planner balances sections and recordings, preferring stable detected phrases and breath/note boundaries. Automatic selections require readable pitch and low clipping; this is signal reliability, not musical correctness. Manual/liked selections remain eligible even when the detector cannot interpret them.
- `analysis.json` retains 40 ms pitch/confidence/level observations and consolidated note events for every recording. `note-map.html` provides a complete searchable-by-time piano roll, concert/written B♭ labels, timing, cents and pitch spread. Speech, polyphony, octave ambiguity and distorted recordings are not reliably transcribed. No notes are invented in unreadable regions.
- Analysis uses bounded PCM batches; memory for DSP does not grow with source duration. Compact analysis output does grow with duration. The raw microphone recordings and training grades are never changed.
- Video uses its embedded audio to preserve the shared recorder clock, especially for paused takes. Audio-only clips use WAV audio with a simple title slate. MP4 audio is AAC; this is a shareable export, not a new lossless master.
- No generated music, speech, pitch correction, speed changes or automatic social-platform publication.

Jobs live under `.local/highlights/<job-id>/`: source media, candidate evidence, Codex events, edit plan, timeline JSON and rendered MP4. These files are ignored by Git. Downloads, signal analysis, default selection and rendering run on this computer. Legacy candidate-only jobs send candidate evidence to Codex/OpenAI for editing. Closing the local editor window does not cancel a running job. Cancel stops child processes; restarting the worker marks interrupted jobs as failed rather than silently restarting a paid run. Completed drafts survive restarts. Source media currently remains per job; manage disk use by removing completed job folders when no longer needed.

The website tab must remain available to return a draft directly. Downloaded `project.json` is also retained locally. Multiple simultaneous editing jobs are refused. CLI/model/auth failures are shown without switching models.

## Validation

```powershell
python -m unittest discover -s tools/highlight -p 'test_*.py' -v
node --test assets/jazz/*.test.js
```

Legacy local editor release checks: real Astra-medium invocation, source boundary validation, actual FFmpeg render/decode, audio-only slate, popup handoff, saved draft retrieval, cancellation and stale-candidate rejection.

## Automatic site publication

`start.ps1` also starts a separate publisher on this PC. It watches completed jobs, uploads verified video/poster files through the existing Google Cloud CLI login and IAP connection, then atomically updates `/srv/zachbednarke.com/films/index.json`. The private Jazz site serves this persistent index independently of static deployments. Each job has a unique film name and retains its original practice date; previous films are preserved. Clip Studio refreshes the shelf every 20 seconds while visible, without interrupting playback. Local development receives a matching index and local media links.

The publisher requires an authenticated `gcloud` installation with access to the site VM. It does not upload to YouTube. Upload failures leave the completed local draft intact and retry after five minutes. Per-job `publication.json` records upload status; publisher logs live in `.local/highlights`. Run `python tools/highlight/publish.py` to attach publishing to an already-running worker without restarting it. A loopback lock prevents duplicate publishers. The PC and publisher must remain running until upload completes.

On macOS/Linux run `sh tools/highlight/start.sh`. Completed jobs expose MP4, timeline, audio-analysis JSON and a standalone interactive note map. The note map can also be opened directly from its job folder.

## Private music library

The standalone Library tab uses the same authenticated service at `/jazz/highlight-api/library`. PDFs and metadata live in `/srv/jazz-highlights/library`, outside static releases and film job cleanup. This directory should be included in server data backups. Uploads stream to temporary files, require a PDF signature, enforce a 100 MB limit and a 2 GB disk reserve, and only publish metadata after the complete file is committed. SHA-256 deduplication keeps repeated uploads from creating duplicate books. File delivery supports byte ranges for PDF readers. All reads require the verified Jazz owner; writes also require same-origin authentication. Reading and downloading do not create practice time or attach documents to days/trainers.

Library metadata may include a `resources` list for recordings, performances, or other study references. Each resource requires an HTTPS `url` and `title`; `artist` and `note` are optional. Update this list with `PATCH /library/:id` and a body containing only `{"resources": [...]}`. The Library card renders the listening panel only when resources are present, so ordinary books retain the compact card layout.
