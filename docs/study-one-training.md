# Study 1 training

Training is available at `/jazz/#training`; the Blue Bossa trainer remains at its existing link and retains its history. The new exercise follows the user's two-page “2-5-1 First Study” PDF: ii root/third/fifth/seventh as quarters, V third/root as halves, and I tonic tied across two whole-note bars. The PDF register and all twelve spellings are encoded explicitly. B-flat trumpet mode displays written keys and grades sounding pitch two semitones below.

## Recording and grading

An AudioWorklet captures mono PCM against the AudioContext clock. Every 4096-sample chunk is committed to IndexedDB using strict durability. Each trial header is committed before capture starts. Count-in and answer share the audio clock with scheduled metronome clicks. Recording contains microphone input, not internally mixed clicks. Learn mode shows notation and note names; Lesson mode reveals them only after grading.

Pitch-class grading accepts octave displacement and ignores brief attack regions. A note requires sustained matching evidence. A completed tonic hold requires sufficient coverage without gaps longer than 220 ms. Low coverage or predominantly unpitched audible input is not graded. Silence with otherwise complete capture is a missed response. Note offsets, dominant detected MIDI pitch, tuning deviations, tonic coverage, hold gaps, early entrance, and grading latency are stored with results. Tolerances are provisional pending real trumpet calibration; seamless same-pitch rearticulation is not reliably distinguished.

Feedback is rendered before waiting for persistence or network work. Progression waits for verified local audio and final-result commits. Local save failure ends the session and offers an in-memory WAV download. Reloaded unfinished trials recover as interrupted, never as successful performances. Hiding the tab, leaving Training, microphone interruption, or stopping mid-trial interrupts the trial. A Web Lock prevents two Study 1 tabs from recording/recovering the same local store concurrently; same-page recording and Blue Bossa starts are mutually excluded. Other browser tabs running the older practice recorder do not participate in this lock.

IndexedDB is browser-local, not an OS file backup. Persistent storage is requested, but browser data deletion still removes local copies. Replay/download is available for each attempt. Audio is synced as bounded 16-bit PCM WAV to private Cloud Storage. Training attempt UUIDs make metadata and audio retries idempotent; mismatched metadata for an existing ID returns 409. The API checks ownership and validates the WAV header, size, format, and exercise duration. Training history and recordings are separate from ordinary section takes, and are counted on the Training screen; they do not inflate Today section timers.

## Metrics

Exercise time excludes count-in and feedback. History includes daily/week totals, completed twelve-key Lesson rounds, per-key pass rates, recent-session results, and common note-error positions. Accuracy and readiness exclude interrupted/not-graded trials and are filtered by mode, tempo, and instrument. The readiness cue requires the latest three graded attempts to pass across at least two sessions. Local and cloud results merge by UUID. Up to 60 recent attempts are shown for the selected settings; cumulative metrics use all fetched history.

## Verification

- `npm ci --prefix assets/jazz`
- `npm test --prefix assets/jazz`
- In `jazz-api`, `JAZZ_LAYOUT_TEST_DATABASE_URL=<disposable-postgres> go test ./...`

Tests cover all twelve PDF spellings, transposition, grading at 40/80/160 BPM, generated audio through the pitch detector, wrong/late/early notes, octave shifts, noise, silence, missing frames, broken/short tonic holds, shuffle boundaries, local recovery, quota/write failure, offline capture and idempotent reconnection, and database ownership/retries/restart compatibility. A local browser fixture also exercises the actual AudioWorklet on an AudioContext clock without a microphone. Real instrument latency/room noise and mobile microphone behavior require field testing.


## Session camera takes (September 28)

Each Study 1 Start-to-Finish run now captures one 720p camera + microphone video and a full 24-bit WAV, including automatic repeats and between-exercise feedback. Camera selection and a muted preview are on Training. Each exercise remains its own graded audio attempt and carries the session recording UUID and an approximate video offset. Session cards provide video replay/download and result buttons that seek to exercises. The same take appears in Archive and the day's dedicated `training-study-one-ii-v-i` section. Five runs produce five takes, subject to the existing 20-take section limit.

PCM chunks and periodic MP4 chunks are saved to a separate IndexedDB database during capture; complete video/WAV assets are committed before upload. Interrupted video is explicitly labeled as potentially needing container repair; WebM fallback may not deliver bytes until stopped. Exercise audio recovery remains independent. Browser storage is not an OS backup. Download controls are available for completed local takes and recovered chunks. Initialization requires a connected practice day. The current generic day accounting counts the complete session take duration (including count-ins/feedback); Training's exercise totals still exclude those intervals. The more precise day-credit ledger in the integration spec is not implemented by this change.

Session uploads use a stable `clientCaptureId` and server ownership/payload checks. Init retries renew uploads on the same row; completed retries return the existing take. Normal recorder behavior is unchanged. Existing historical exercises remain audio-only. Blue Bossa session video is not connected by this Study 1 change.
