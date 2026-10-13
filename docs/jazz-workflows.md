# Jazz workflow guide

Last reconciled: 2026-09-30. Start here when continuing Jazz development.

> **History (October 13, 2026).** This line of work (film service, note maps,
> film editor, Library, Training, long tones, captions, moving takes between
> sections) was built and deployed between September 11 and October 5 outside
> this repository. From October 11 the GitHub deploy of `main` replaced the
> page and API and hid it. It was restored by merging its last release
> (`archive-note-map-v1`, Oct 5) into `main`. Its database migrations 021-025
> are now 039-043; production already had their tables. The release archive
> was truncated, so `tools/highlight` was then synced from the VM's
> `/opt/jazz-highlights` (October 13); see
> [tools/highlight/README.md](../tools/highlight/README.md). Clip Studio's
> **Make today's film** is now the cloud Magic Film; this guide's film service
> is the **Make note-map film** button.
Code is versioned in this repository; generated media and local job directories
are not. A Git commit alone does not back up media or mean changes are deployed.

## What exists

| Workflow | State | Entry point |
| --- | --- | --- |
| Daily film generation | Live, authenticated, server hosted | `tools/highlight/hosted.py`, `worker.py`, `audio_map.py` |
| Whole-day note atlas | Live; all takes, section colors, zoom and take details | `tools/highlight/note-map.html`, `note_map.py` |
| Scrolling pitch overlay | Working manual prototype; two full films published | `tools/highlight/pitch_overlay_demo.py` |
| Whole-day overview intro | Four-second manual prototype | `tools/highlight/preroll_demo.py`; recipe below |
| Standalone PDF library | Live; private upload, search, read and download | `tools/highlight/library.py`, `assets/jazz/library.js` |
| Browser-compatible sign-in | Superseded by Sign in with Google (main, October 10) | `deploy/jazz-auth.md`, `jazz-api/cmd/jazz-auth/` |
| Study 1 trainer | Live; session recording and quick round restart | `assets/jazz/study-one-trainer.js`, [trainer spec](study-one-training.md) |
| Long-tone overlay | See feature spec and implementation | [Long-tone spec](long-tone-overlay.md) |

The production film button uses same-origin `/jazz/highlight-api/jobs`.
`assets/jazz/highlight-local.js` retains its historical name but no longer
requires a localhost worker. Local worker/editor instructions in the
[worker README](../tools/highlight/README.md) describe a separate legacy path.

## Film pipeline and its limits

1. The browser submits an explicit practice date and target duration.
2. The authenticated worker fetches owned recordings and rejected regions from
   the API, then downloads source media. For video takes, analysis uses the
   embedded video audio so pitch and video share a clock.
3. `audio_map.py` analyzes every source, not just selected passages. Mono 16 kHz
   audio is processed in bounded batches with a 40 ms hop. Full observations and
   consolidated note events are stored in `analysis.json`; content/version
   hashes support reuse of analysis.
4. The deterministic planner selects readable phrases, respecting rejections,
   preferring manual/liked selections and balancing recordings and sections.
   Automatic candidates use voiced/readable fractions and clipping thresholds.
   Selection is capped at 24 passages and may be shorter than the target.
5. `worker.py` renders and verifies the film. `publish_remote.py` installs the
   film, full analysis and portable note atlas, then updates the locked index.

This is estimated monophonic pitch, not MIDI transcription or a musical grade.
Speech, overlapping notes, distortion and octave ambiguity can mislead it.
Confidence means detector certainty, not correctness. Current selection does
not directly use trainer grades, expected notes, rhythmic accuracy or visual
interest. The whole-day atlas uses cumulative recorded time, excluding gaps
between takes. Its pitch histogram measures detected duration.

## Reproduce the exact edit with a pitch overlay

Always identify **practice date + job UUID** first. An open browser tab or the
word “today” is not sufficient. Different jobs on the same day have different
edits. To preserve a film the user already likes, composite its existing movie;
do not rerun selection.

Job IDs for specific films are recorded in the private film index and the
published `.edit.json` manifests on the site VM, not in this public repository.

Published originals use `/jazz/films/DATE-JOB.mp4`. Manual overlay variants use
`/jazz/films/DATE-magic-day-pitch-roll-vN.mp4`. These variants are private
direct links and are not added to the film shelf index. Originals remain
unchanged.

From the repository root, install NumPy for analysis (worker requirements),
Pillow for this prototype, and FFmpeg/ffprobe. The prototype currently uses
macOS Arial font paths; make fonts configurable before moving rendering to Linux.

```sh
# Existing local job with cut files: 15-second design sample, cut index 3.
python3 tools/highlight/pitch_overlay_demo.py \
  .local/highlights/<job-id> \
  .local/pitch-overlay-demo/pitch-trail-v1.mp4 --clip 3

# Preserve the entire September 30 edit and its original audio.
python3 tools/highlight/pitch_overlay_demo.py \
  .local/highlights/<job-id> \
  .local/pitch-overlay-demo/2026-09-30-magic-day-pitch-roll.mp4 --full
```

Full-film mode needs `highlight.mp4`, `project.json` and `analysis.json` in the
job directory. Hosted jobs clean up cuts and source downloads: recover the
published movie and analysis from the persistent films directory, and the
project from server job state. Do not assume `.local/` survives another machine.

The renderer uses source-relative notes and written B♭ pitch (+2 semitones).
It saves an adjacent `.edit.json` containing source job and resolved segments.
If cut files are absent, it checks keyframes near planned boundaries and fails
on ambiguity. This is a fallback: persist exact segment presentation timestamps
in future production manifests instead of relying on recovery.

**Timing decision:** retain `-fps_mode passthrough -enc_time_base -1` for irregular
concatenated timestamps. Omitting the encoder time base lost seven frames in the
September 30 experiment. Audio is stream-copied, never re-recorded or synthesized.

Before publishing a variant, compare original and output:

```sh
ffprobe -v error -show_entries \
  format=duration:stream=codec_type,nb_frames,width,height,start_time \
  -of json INPUT.mp4
ffmpeg -v error -i INPUT.mp4 -map 0:a:0 -c copy -f hash -
ffmpeg -v error -i OUTPUT.mp4 -f null -
```

Run the probe and audio hash on both files. Require equal audio hashes, duration,
frame counts and start timestamps; inspect frames near cuts and in representative
sections. September 30 passed these checks: 3545 video frames, video start
0.021029 seconds, audio start 0. Publish under a new name using temporary upload,
checksum verification and atomic rename, retaining the original.

## Storage, hosting and recovery

| Data/service | Location |
| --- | --- |
| Recording bytes | Private GCS, brokered by `jazz-api` |
| Practice and recording metadata | PostgreSQL through `jazz-api` |
| Static frontend | `/srv/zachbednarke.com/current` symlink to a release |
| Published films, analysis, note HTML and index | `/srv/zachbednarke.com/films` |
| Hosted job state and saved projects | `/srv/jazz-highlights/<job-id>/` |
| PDF library files and metadata | `/srv/jazz-highlights/library` on GCP VM disk, **not GCS** |
| Hosted worker code/service | `/opt/jazz-highlights`, `jazz-highlights`, loopback port 8769 |
| Auth service | `jazz-auth`, loopback port 8768 |

Site VM: `actual-server`, project `actual-budget-zb`, zone `us-west1-a`.
Connect with `gcloud compute ssh` and `--tunnel-through-iap`. Deployment helpers:
`deploy/install-jazz-highlights.py`, `deploy/verify-jazz-highlights.py`, and
`deploy/jazz-highlights.service`. Follow their checks; avoid restarting during a
render. Interrupted jobs are marked failed on restart. Only one render runs at
a time. Source downloads/intermediate cuts are disposable; projects, published
analysis, originals and library metadata are not.

Last known frontend release (2026-10-05): see the VM's current symlink. Verify the current
symlink before a future deployment. Copy the current release, replace explicit
files, verify, then switch the symlink atomically; retain the old release for
rollback. For transfers, streaming a tar over IAP SSH has been more reliable
than SCP. Finish and verify transfer before extraction or service restart.

Keep gateway credentials, auth keys, passwords and signed playback URLs out of
Git, manifests and logs. Sign into each browser normally through `/jazz/sign-in`;
do not transplant session cookies. Publication here means authenticated site
storage, not public social or YouTube sharing.

**Backup boundary:** VM library/film/job directories need durable backups;
automated backups have not been verified. Git does not protect these files.
Never delete local copies of library or media files without explicit approval.

## Next experiment: four-second whole-day intro

Original design; first prototype recorded below. Use September 30's actual analysis, not the
September 28 reference screenshot.

- 0–1 s: date and a clean whole-day grid; no browser chrome or interactive controls.
- 1–3 s: reveal pitch traces left to right, colored by section, then hold briefly.
- 3–4 s: emphasize the opening selected passage and transition into video with
  the bottom pitch roll.
- Start the opening passage's audio immediately under the overview. Reveal its
  video four seconds later at the matching timestamp, keeping the original edit
  and total duration; do not duplicate four seconds of music.

Long-tone plateaus and ii–V–I patterns should be readable. Full-take compression
can make even long notes tiny, so use an honest overview plus a brief zoom rather
than distorting note lengths. First render a ten-second prototype (four seconds
of overview and six of the transition/video) before another complete movie.

## Self-service editing roadmap

See the [film editing UI specification](film-editing-ui.md) for exact-edit cloud
drafts, passage removal, intro/pitch-trail controls, previews, versioned renders
and required contract/integration tests. The self-service implementation and deployment notes are below.

Exact edits can also carry optional, server-validated presentation data on each
passage: `graphics.pitchTrail` selects the passages that receive the live pitch
roll, `chapter` and `subtitle` create a three-second title card, and `captions`
contains source-clock caption ranges. These values are declarative text and
booleans; the API does not accept filter expressions. Source-clock captions stay
aligned when passages are reordered, and validation rejects captions outside the
saved passage. This is intended for authored day stories such as an unboxing,
where speech should remain visually clean and note graphics should enter with the
playing.

## Maintaining this guide

Update this file when a prototype becomes production, storage changes, or a
rendering decision changes. Keep commands and tests beside their implementation;
record date, job ID, source edit, output name and verification for each approved
film variant. Preserve full analysis and exact edit manifests alongside media.
Useful regressions live in `tools/highlight/test_*.py` and
`assets/jazz/note-map.test.js`; renderer changes also need real decode, timing,
audio identity and visual checks. A unit test alone cannot establish sync.

### Four-second intro prototype (2026-09-30)

Implemented manually in `tools/highlight/preroll_demo.py`; not integrated into
hosted generation. Uses all 14 analyzed takes (102 recorded minutes), concurrent
trace reveal, opening-passage emphasis and a dissolve at 3.45 seconds into the
matching video timestamp. Audio is the source film's first four seconds, encoded
as AAC for the preview. No additional music or repeated audio is introduced.

```sh
python3 tools/highlight/preroll_demo.py \
  .local/highlights/<job-id> \
  .local/pitch-overlay-demo/2026-09-30-preroll-v1.mp4 \
  --date 2026-09-30 \
  --video .local/pitch-overlay-demo/2026-09-30-magic-day-pitch-roll.mp4
```

Output verified: four seconds, 120 video frames, audio stream present, full decode
successful, overview frame visually inspected. Adjacent JSON records source and
recipe. Cards normalize time and pitch per take; compare shapes, not absolute
note lengths between different cards. This first preview uses a dissolve rather
than the proposed passage zoom.

### User's final shorter edit (2026-09-30)

Keep original clips 9–13, starting at saved boundary 74.880013 seconds. The
four-second overview replaces opening video, with continuous audio from that
same point; it adds no duration. Existing pitch trail stays on the remaining
video. Reproduction:

```sh
python3 tools/highlight/preroll_demo.py \
  .local/highlights/<job-id> \
  .local/pitch-overlay-demo/2026-09-30-final-cut-v1.mp4 \
  --date 2026-09-30 \
  --video .local/pitch-overlay-demo/2026-09-30-magic-day-pitch-roll.mp4 \
  --start 74.880013 --first-clip 9 --full
```

This composition re-encodes audio to AAC and video to H.264. The adjacent JSON
records the retained clips and trim offset; original films remain unchanged.

### Highest-note passage second (v2)

User requested swapping the highest-note passage with the second passage of the
shorter edit. Saved note events identify original passage 11 as the highest
(retained passage 3, concert MIDI 85), so the new original-passage order is
**9, 11, 10, 12, 13**. The opening audio/intro is unchanged.

### Authored day story (2026-10-01)

The first production use of passage graphics was an authored day story with
eight source-clock passages mixing speech and playing. Pitch graphics appear on
the musical passages; source-timed captions and chapter cards carry the spoken
story. Its 46-test install gate, full output decode and nine-frame visual
contact-sheet inspection passed. The published edit manifest, analysis and note
map are adjacent to the film on the VM and are the reproduction record.

`tools/highlight/reorder_film.py VIDEO EDIT_JSON OUTPUT --order 9,11,10,12,13`
reorders the existing pitch-roll film using its segment manifest and writes a
sidecar recipe. Apply `preroll_demo.py --full` to that output with a job project
containing the reordered clips and the original whole-day analysis. Do not use
the already composited intro as the base, which would repeat the intro treatment.
Local output: `.local/pitch-overlay-demo/2026-09-30-final-cut-v2.mp4`.

## Self-service editor implementation (2026-10-01)

`finished-films.js` now opens a dedicated editor within Clip Studio. The editor
uses `film-edit-model.js` for pure undoable operations and `film-editor.js` for
cloud persistence, source playback, settings and jobs. Source preview shows the
passage and estimated pitch; Preview intro produces the actual composition.

Server responsibilities are separated:

- `film_edits.py`: strict edit validation, owner-scoped storage, revision conflicts
  and recovery of old projects; drafts persist under `/srv/jazz-highlights/edits`.
- `edit_api.py`: authenticated edit/preview/render routes and fresh source ownership
  and rejected-region checks. New automatic selection remains a separate endpoint.
- `exact_render.py`: source-clock trims, integer-frame segment manifests, PCM cut
  audio before final AAC encoding, checksums and decode/frame verification.
- `composition.py`: portable atlas and pitch graphics, configured by immutable
  render settings. Production does not invoke the historical demo scripts.
- `media_response.py`: range-aware private preview delivery.

Previews stay in their job directories and do not enter the finished-film index.
Finished exact edits publish a private `.edit.json` manifest beside each MP4;
source files and intermediate cuts are cleaned, manifests and analysis remain.
The single-render limit applies to previews too. Cloud drafts are not source
recordings: removing passages never deletes recordings or earlier film versions.

Deploy using `deploy/install-film-editor.py`; it verifies the expected current
frontend, refuses an active render, runs the Python suite on the VM, keeps code
and frontend rollback copies, and checks service health before switching the
frontend. Dependencies are system FFmpeg, NumPy, Pillow >=9.4 and DejaVu fonts.

Regression commands:

```sh
node --test assets/jazz/film-edit-model.test.js assets/jazz/film-editor.test.js
python3 -m unittest discover -s tools/highlight -p 'test_*.py'
```

JavaScript DOM tests need jsdom (as do existing note-map tests). FFmpeg integration
tests exercise a cut inside the intro, reordered source tones, exact frame counts,
unchanged audio through composition, audio-only/missing-pitch output, cancellation
and restart recovery. Browser QA also checks desktop and phone layouts.

Validation note: the initial Matroska/PCM cut format rounded timestamps enough
to fail the source-audio correlation test on the VM's FFmpeg 5.1, despite passing
on local FFmpeg. Exact rendering now uses MOV/PCM intermediates; both platforms
pass the same test. Do not replace that format without rerunning the real-media
continuity checks. Composition repeats its final graphics frame rather than
shortening video at the overlay input boundary, preserving the final video frame.

Production follow-ups from real-source verification: duration limits apply to
inputs as well as outputs, so short cuts do not scan the remainder of long takes.
When graphics require a final encode, intermediate cuts use the faster ultrafast
preset; the final graphic composition still uses the normal quality settings.
Manifests include the renderer source fingerprint and FFmpeg version. Preview
source preparation fetches only recordings needed for the opening six seconds,
and a preview may end partway through a passage without failing the saved-edit
minimum-length rule.

Use `python3 deploy/package-film-editor.py` to prepare the deployment tar. It
reads the installer's explicit frontend list and includes only worker code/tests,
never recordings, library PDFs or local credentials. Transfer that tar over IAP,
extract to staging, then invoke `install-film-editor.py` with the verified current
release path and a fresh release name. Graphics receive a compact note-event JSON
file; full per-frame analysis remains available in the published analysis asset.

Release verification (2026-10-01): `20261001-film-editor-v3` passed all 42
Python tests on the server and locally; 126 JavaScript tests passed locally.
The live editor reproduced the September 30 reference order [9, 11, 10, 12, 13]
and published a separate 43-second film, preserving its parent. Draft reload,
render cancellation/retry, source playback, desktop and mobile were exercised.

Concat manifests explicitly declare each cut's integer-frame duration. MOV's
reported container duration can round fractional-frame cuts to milliseconds;
using it implicitly introduces audio offsets on FFmpeg 5.1. The 61-frame plus
59-frame integration fixture guards against this. Retain both PCM intermediates
and explicit concat durations.

The deployed v3 intro-preview path was exercised through the authenticated live
UI: a preview job completed, and its private ranged MP4 played in the browser
at 854×480 for exactly six seconds with no media error.

## Recording captions and combined day analysis (2026-10-01)

Clip Studio's analysis menu can queue both analysis tracks for an entire day.
The film worker downloads each owned lossless master once to populate its bounded
pitch cache and save the usual note-event map. In parallel, `jazz-api` queues
durable Speech-to-Text V2 batch jobs against private, generation-specific FLAC
sidecars streamed from the 24-bit WAV masters. The masters remain unchanged and
the sidecars are reused on retries. Caption
rows are tied to object generation and checksum, include source-relative times,
and remain separate from pitch observations so speech cannot be interpreted as a
bad musical passage. Selecting a take or clip loads its transcript beneath the
player; clicking a caption seeks to it, and the active line follows playback.

The relevant implementation is `jazz-api/captions.go`, migration 043 (025 on the out-of-band line),
`tools/highlight/worker.py` / `hosted.py`, and `assets/jazz/clip-studio.js`.
Production permissions and worker settings are documented in `jazz-api/README.md`.

The first production run covered a full practice day of about two hours. Every
take produced a timed transcript, and the independent pitch pass stored note
events for the same recordings.

Standalone day edits can be created before a general-purpose film exists. The
edit API snapshots server-owned recording IDs, durations and section titles for
the requested practice day, then applies the same revision, rejection, source
boundary and exact-render checks used by descendant film edits. A published day
edit has an immutable edit manifest but no invented parent-film relationship.

The first use was a seven-passage, two-minute day cut combining speech and
playing. It renders the four-second day overview and the written-B-flat pitch
trail and is recoverable from its edit manifest on the VM.
