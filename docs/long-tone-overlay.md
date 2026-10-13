# Long-tone playback overlay

Status: deployed September 18, 2026 from `e23c69f` on `codex/long-tone-overlay`. The design below records the intended behavior; the implementation notes identify the current contract and remaining calibration work.

## Implementation notes

- Saved video and audio players in recording cards, Archive, and Clip Studio share an explicit **Long tones** toggle. Video uses its embedded audio; audio playback uses its own asset. The compact tuner and left rail follow source time, including seeks and playback-rate changes. Concert and B-flat written note labels are selectable.
- The Go worker streams 16 kHz mono PCM through a YIN detector using 64 ms windows, 20 ms hops, and a separate 10 ms energy envelope. The envelope prevents a pitch window from donating time before an actual onset. AAC decoding is capped at the audio stream's presentation end so codec padding cannot complete a short hold. Live Opus/WebM without duration metadata uses decoded EOF under the same duration cap; other assets without verifiable timing report analysis unavailable.
- PostgreSQL migration 040 (022 on the out-of-band line) adds durable leased jobs and one-minute trace chunks. Authenticated POST/GET `/v1/recordings/{id}/long-tone-analysis` creates/reuses analysis and serves source-time ranges of at most two minutes. Identity includes object name, immutable generation, checksum, asset, and analysis version. The browser retains at most three chunks.
- Each frame carries frequency, clarity, RMS, state, locked note, original hold start, and verified duration. These provide seek context without a separate persisted segment table. Detector and classifier share the version `long-tone-yin-v1`; changes to either currently require reanalysis.
- Unknown or conflicting pitch freezes progress and conservatively restarts verified time on recovery. This deliberately favors avoiding false completion and may reset a real sustained note after a detector glitch. Recovering a transient octave error without a reset remains future calibration work.
- A continuing tone needs 120 ms of stable evidence after crossing the goal before showing completion (approximately 120–140 ms of display delay). This prevents a trailing pitch window from awarding 30 seconds to a note that changed at 29.98 seconds. An exact 30-second take can instead complete at its verified EOF or measured silence boundary. Energy breaks shorter than the 10 ms envelope resolution are not guaranteed detectable.
- Worker retries and decode duration/output are bounded. Manual retry resets the bounded attempt budget atomically. Missing audio or analysis failure leaves ordinary playback usable. Deployment requirements, including CPU allocation for background workers, are in `jazz-api/README.md`.

Verification includes deterministic PCM and AAC/Opus fixtures around 29.98/30.00/30.20 seconds, short breaths, legato note changes, delayed audio, a video longer than its audio track, PostgreSQL lifecycle/authorization tests, frontend model tests, and Chromium desktop/mobile playback checks. A local Chromium test also exercised the complete HTTP request, PostgreSQL worker, actual MP4 decoding, persisted trace, completion, and backward seek. Synthetic codec pitch medians stayed within two cents of the input/PCM reference. Real trumpet recordings have not yet been hand-labeled or calibrated; soft notes, room noise, tongued reattacks without silence, and harmonic-rich attacks remain important real-audio checks.

To use it, open a saved recording and select **Long-tone overlay** below the player. The first request prepares the trace; later playback reuses it. Choose **Concert** or **B♭ written** in the adjacent pitch selector. The rail measures duration, while the tuner independently shows pitch centering.

Deployment: Cloud Run revision `jazz-api-long-tones-e23c69f` serves the API with one background worker per instance and CPU allocated outside requests. The frontend release is `20260918-long-tones-e23c69f`. Production Opus/WebM analysis and synchronized playback have been verified; Jazz authentication remains enabled.

## Outcome and scope

On a saved practice recording, show a quiet pitch tuner near the bottom of the video and a thin vertical progress bar along the left edge. The bar reaches its goal when one note has been sustained continuously for 30 seconds. The tuner shows how sharp or flat the sound is relative to that note. User confirmed saved-clip playback first.

The first version covers saved video/audio playback in Clip Studio, Archive, and recording cards. It does not change recorded audio, modify the previous section-reassignment feature, listen to a new microphone stream, or burn graphics into exports. Live capture feedback and exported overlays are later work.

## Product rules

- A hold is one sustained sounding note, including its octave. Separate breaths cannot accumulate toward a single 30-second goal.
- Pitch centering and duration are independent. A slightly sharp 30-second note completes the duration goal; the tuner continues showing the error. Avoid describing completion as perfect tone.
- A confident change to another note resets the bar even without silence between notes. Ideally a transient detector error should not reset it automatically; v1 conservatively restarts after unresolved uncertainty as described above.
- Loudness fluctuations alone do not reset a hold. Relative level and pitch variability are useful later stability measures, but neither proves unchanging timbre or overall tone quality.
- At 30 seconds the rail stays full while the note continues. Completion is a static label, with no sound, confetti, or flashing.
- Instrument preference changes note labels only. Reuse B-flat written/concert pitch preference and include octave; cents and continuity stay in sounding-pitch coordinates. Start with A4 = 440 Hz, with the reference captured in classifier configuration.
- Overlay is an explicit player toggle. Automatic enablement must use a stable exercise identifier, not section-name substring matching. Existing arbitrary custom section titles do not provide that identifier yet; a user can enable the overlay regardless of title in v1.

## Minimal visual design

Bottom: a translucent, compact tuner strip with note name, a horizontal -50 to +50 cents scale, fixed center tick, moving marker, and signed cents value. A small center band represents the provisional +/-10-cent centered range. A soft brass marker becomes muted green in the band; position and text also communicate the result.

Left: a 4–5 px bar filling bottom to top, capped around 180 px high, inset 12–16 px. A small `18.4 / 30s` label sits next to it. At completion show `30s held`; actual continuous duration remains available as accessible text.

Keep overlay noninteractive so click-to-play still works. Put its on/off control outside the image. Measure the custom player's controls and position the tuner above them. Archive has external controls. Clip Studio currently has native controls: adopt the shared custom player for reliable over-video placement, or temporarily use a strip below the video. Small rendered players and audio-only playback use the below-player strip. Fullscreen should target the wrapper; native-video fullscreen may not support DOM overlays.

Display `Analyzing…`, `Finding note…`, waiting, uncertainty, and unavailable states without guessed readings. Pause/buffering freeze the display. Expose the bar as a progressbar, announce completion/errors only, and respect reduced motion.

The inline design preview uses sample readings and a video placeholder. It is not a recording analysis or a production UI.

## Existing foundation

- `assets/jazz/audio-analysis.js` has a YIN-style monophonic detector, RMS, note/cents conversion, and a short-window stability helper.
- `recording.js` already has a preflight tuner; it uses animation-driven snapshots, unsuitable as a continuity clock.
- `pitch-worklet.js` is an effects processor with a separate detector. Do not use its pitch-corrected output or its cruder estimator to judge long tones.
- `media-playback.js`, `archive.js`, and `clip-studio.js` have distinct player lifecycles that need one shared overlay controller.
- Existing waveform peaks are coarse amplitude summaries. They cannot recover pitch or establish uninterrupted 30-second notes.
- The Go backend's private recording assets and FFmpeg support now also serve the durable pitch-analysis worker and trace API described above.

Synthetic baseline investigation found the current detector within 0.4 cents on 45 clean generated tone combinations. This is a useful starting point, not evidence of accuracy on real trumpet recordings.

## Source audio and synchronization

Analyze the audio from the exact asset being played:

- Video playback: embedded audio from that video, using its presentation timeline.
- WAV playback: the WAV itself.

Video and lossless WAV are captured separately and currently have no persisted time mapping. Do not apply a WAV trace directly to video.currentTime. Video currently records the same raw microphone stream, without pitch correction. AAC/Opus may affect confidence and low-level details; compare decoded video audio with PCM fixtures before trusting continuity results.

The decoder must preserve/map presentation timestamps, stream starts, and codec delay. Missing video audio must yield an explicit unavailable state, not an unsynchronized fallback.

For a trimmed view into an original recording, retain original source coordinates. Seeking 20 seconds into an established hold shows 20 seconds held, with surrounding-source context available. A separately uploaded file has no evidence before its own start. Concatenated clips require an explicit edit-to-source mapping and do not count across edits.

## Analysis architecture and contract

Use bounded offline processing, then animate the result during playback. Avoid downloading/decoding an entire multi-hour recording in the browser or introducing a second audible player. Browser decodeAudioData requires complete file data; four-hour mono 48 kHz PCM is already multiple gigabytes when decoded.

1. Authenticated request creates or reuses a durable analysis job for a specific asset version. Persist pending/running/ready/failed states, bounded retries, and worker ownership/lease. Select the worker runtime and deployment configuration as part of the first backend implementation task; an untracked goroutine in a request handler is insufficient.
2. A bounded worker streams/decodes the asset in windows, keeping only overlap and classifier state in memory. Analysis failures never break normal playback.
3. Store chunked timestamped observations and hold segments. Separate detector version from hold-policy version, so policy tuning can reuse observations where sufficient.
4. An authenticated manifest/range API returns the requested source-time window and segment context. Cache with a size limit. A range in the middle of a long hold must retain the true segment start, not restart at the chunk boundary.

Cache identity: recording ID, asset kind, immutable storage object generation/checksum, detector version, and classifier configuration/version. Signed URL, title, section assignment, and playback speed are not source identities. Reclassification affects overlay visibility, not the audio-derived trace.

Observation fields: source start/end time (derived from sample indices and presentation timestamps), frequency, periodicity/confidence, RMS dBFS, voiced state, clipping/uncertainty flags, and discontinuities. Pitch frames should use overlapping windows but must not double-count their duration.

Segment fields: start/end source time, locked sounding note including octave, confirmed duration, completion timestamp if any, end reason, and uncertainty spans. Persist enough context to answer arbitrary seeks deterministically.

Audio upload completion gates analysis availability. An uploading recording may show `Available after upload`; never analyze an incomplete object as a finished take. Authorization applies to jobs, manifests, and chunks; deletion/source replacement invalidates availability.

## Hold detector

Pure state machine: listening -> acquiring -> holding -> uncertain -> completed, with a confirmed stop/change returning to acquiring or listening. Compute durations from contiguous source intervals, never wall-clock or render callbacks.

Provisional starting parameters, to be calibrated against annotated recordings:

| Parameter | Starting value |
| --- | --- |
| Pitch analysis window | Approximately 43–64 ms |
| Pitch hop | 20 ms |
| Energy/envelope interval | 10 ms |
| Onset acquisition | Approximately 200 ms |
| Start/retain clarity | Approximately 0.90 / 0.85 |
| Same-note retention | Hysteresis around locked note, initially +/-65–70 cents |
| Confirm different note | Approximately 100–120 ms outside retained-note band |
| Confirm energy break | Approximately 30–40 ms below release threshold |
| Maximum pending pitch ambiguity | Approximately 100 ms |

These are engineering hypotheses, not musical standards. Tune them based on false completions and false resets, especially near adjacent notes, quiet releases, vibrato, and weak fundamentals.

Backdate a confirmed onset to its first accepted voiced interval. Backdate a confirmed break/change to its detected boundary rather than the end of confirmation delay. Keep the target reference fixed during the hold; repeatedly recentering it on the player's drifting pitch would hide drift.

Distinguish missing pitch confidence while audible periodic sound remains from confirmed silence. Pending uncertainty does not earn time. Recover it only if reanalysis establishes continuity; unresolved gaps terminate or invalidate that candidate for a verified completion. Do not sum two verified spans around an unknown gap. A provisional progress display is not a completed 30-second claim.

Use a noise floor estimated from genuine silent intervals with onset/release hysteresis. Do not raise the floor continually while a note is sounding. Confirmed energy breaks reset same-note reattacks. Do not promise detection of every tongued reattack without silence; validate a dedicated reattack rule on real examples before making it reset holds.

An isolated octave error is uncertain, not automatically a new note or automatically folded into the original octave. A sustained octave change starts a new hold.

## Playback controller

Drive the overlay from source time, preferably a video frame callback when available, with a normal media-time fallback. Seeking, pausing, buffering, hidden tabs, and playback rate cannot change the analysis result.

- A 30-second source hold passes in 15 wall-clock seconds at 2x playback, but remains a 30-second hold.
- Seeking backward restores the earlier note/bar, rather than retaining future completion.
- Source-window data not loaded: display preparing/unknown, never interpolate continuity across unknown spans.
- Switching clips or assets cancels/ignores stale requests and disposes listeners/render loops.
- Do not create another microphone stream or route media through Web Audio solely for this overlay.

## Dependency-driven implementation workflow

### Wave 1: parallel prototypes

Audio agent: synthetic and encoded fixtures, detector audit, pure hold classifier and tests.

Playback/backend agent: asset timeline contract, streaming decoder experiment, trace/storage/API schema, worker deployment choice and memory budget.

UI agent: overlay component against deterministic sample trace; compact layout, source-time seeks, and player attachment adapter.

Integrator owns the shared trace/state contract before dependent code begins. Agents own separate files and do not independently rewrite shared players or endpoints.

### Gate 1: detector and timeline evidence

Pass exact-threshold tests, breath/note-change tests, codec comparisons, and known timestamp alignment fixtures. Inspect a small hand-labeled sample of real trumpet recordings using the user's actual microphone. Report uncertainty rather than lowering thresholds to make the demo pass.

### Wave 2: integrated vertical slice

Implement one saved video end to end: authenticated analysis request, bounded processing, persisted trace, and Clip Studio overlay. Verify seek/replay/pause/rate and failure recovery. Normal media playback must work if analysis fails.

### Wave 3: broaden and review

Attach the same controller to Archive and recording cards; test small players, fullscreen, audio-only assets, section changes, concurrent requests, deletion, and long assets. Have a fresh reviewer test the acceptance cases independently. Deploy only after integrated review and the normal deployment decision.

Live-recording feedback can follow with the same classifier observing sample-indexed raw PCM, without changing the saved-playback contract. Export burn-in is a separate feature.

## Acceptance checks

- Verified 29.98 seconds never completes; 30 seconds completes once, within the defined analysis resolution.
- 15 seconds plus a breath plus 15 seconds never completes.
- Same-note restart, legato semitone change, sustained octave change, and source discontinuity begin new attempts.
- Normal pitch wobble moves the tuner without spuriously resetting the bar; slow drift remains visible against a fixed target.
- No false completion across uncertain, missing, duplicated, or out-of-order data.
- Analysis chunk boundaries do not break a true sustained tone.
- Seek, pause, buffering, rate changes, replay, and clip trimming reproduce the source-time result.
- Video and WAV use their respective source timelines, including codec/container offsets.
- Encoded AAC/Opus and PCM fixtures agree within documented pitch/boundary tolerances.
- Real-audio validation reports false resets, false completions, pitch error, and timing error rather than a subjective claim of accuracy.
- Private/deleted asset protections hold; concurrent analysis deduplicates; retries do not duplicate work; processing is memory bounded.
- Overlay never obstructs player controls, alters audio, or prevents playback when analysis is unavailable.

## References

- YIN paper: https://iro.umontreal.ca/~pift6080/H09/documents/papers/yin_pitch_tracker.pdf
- Web Audio specification: https://www.w3.org/TR/webaudio-1.0/
- Whole-file browser decoding constraint: https://developer.mozilla.org/en-US/docs/Web/API/BaseAudioContext/decodeAudioData
