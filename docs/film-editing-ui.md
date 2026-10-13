# Film editing and regeneration UI

Status: implemented 2026-10-01; deployment and live-media verification recorded
in the workflow guide. This document retains the original design intent. See [workflow guide](jazz-workflows.md) for the current rendering system.

## Outcome

A user opens a finished practice film, removes or trims passages, chooses the
whole-day intro and pitch trail, previews the result with sound, and renders a
new downloadable version from any authenticated browser. The server uses the
exact saved edit. No local server, terminal, or new automatic selection is needed.

Reference acceptance scenario: open the September 30 reference film, select passage 9 at original film time
1:14.88, choose “Start film here,” retain passages 9–13, enable a four-second day
overview and the pitch trail, and render approximately 43 seconds. The overview
replaces opening picture while opening audio continues; it adds no time.

## Screen and interaction design

Extend Clip Studio rather than create another editor. Every finished-film card
gets **Edit this film**, beside Watch and Download. Opening it loads its saved
edit and identifies the version explicitly: “September 30 · Daily film · v1.”
If another draft has unsaved changes, offer Resume draft or Open a separate edit;
never silently replace it. A new edit starts as a draft derived from that film.

Desktop layout:

- Main area: large player with synchronized sound and pitch overlay, play/pause,
  scrubber, elapsed/total time and full-screen playback.
- Below it: horizontal film timeline with section-colored clip thumbnails.
  The selected clip has a strong outline, handles and a duration label. Removed
  clips disappear; Undo remains available.
- Side panel: selected passage controls above a compact “Film appearance” panel.
- Sticky bottom bar: “5 passages · 0:43,” cloud save state, **Preview intro** and
  primary **Render this edit**. Keep “Make a new automatic selection” in a separate
  secondary menu so editing cannot accidentally rerun the selector.

On narrow screens, stack player, horizontally scrollable timeline, selected
passage controls and collapsible appearance controls. Keep the render bar visible
without covering trim handles. No interaction depends on hover or dragging.

### Passage editing

Click a thumbnail or its matching player region to select a passage. Show:
“Passage 9 · Study 1 · source take 10,” its original film range and current draft
range, plus the source range. Original passage labels remain stable after edits;
current sequence positions are separate. This avoids renumbering confusion when
the user refers to passage 9 after removing passages 1–8.

Primary controls:

- **Remove passage** (Delete/Backspace when the timeline has focus).
- **Start film here**: keeps the selected passage and all subsequent passages;
  removes preceding passages as one undoable action. It keeps the whole selected
  passage even when the playhead is in its middle.
- **End film here**: equivalent operation retaining through the selected passage.
- **Trim**: draggable in/out handles plus precise time fields and keyboard nudges.
  Show source context so a note onset or ending is not cut accidentally.
- **Move earlier / later**, with drag reordering as an additional affordance.
- **Undo / Redo** for removal, reorder, trim and appearance changes.

No deletion here deletes source recordings. MVP trims stay within a source take;
minimum passage length 0.5 seconds. Reject inverted or out-of-source ranges.
Allow edits across existing approved boundaries, but do not silently include a
rejected source region: explain the conflict and require an explicit separate
unreject action. Empty drafts are savable but cannot render.

The player maps a click on the shortened timeline to the correct source time.
Seeking across a cut must update video, audio and pitch together. Show a loading
state instead of playing the wrong source audio during a switch.

### Film appearance

**Day overview intro** toggle, default on when deriving from an intro-enabled
version. Duration choices: 3, 4 (default), or 5 seconds. Supporting text:
“Shows your whole practice day over the opening audio. Film length stays the same.”

Use the existing date, take count, section colors and animated pitch-map grid.
Highlight the newly selected opening passage, not the original film's first
passage. Keep all takes in the overview, including takes omitted from the edit;
label this “Whole practice day.” For large days, use a readable grouped layout
instead of shrinking dozens of labels. Keep honest relative note lengths within
each take; disclose that each take has its own time scale in detailed help.

**Pitch trail** toggle. Default written B♭ trumpet; offer Concert pitch as a small
secondary setting. Reuse the tested bottom treatment with a fixed playhead.
Estimated notes are never presented as performance grades. Missing analysis
shows a neutral “Pitch unavailable” state; do not invent traces.

When enabled together, the day overview dissolves into video with the pitch trail
already synchronized. Intro length cannot exceed the film length; for films under
three seconds, disable intro and explain why. If a cut falls during the intro,
audio follows the saved edit normally and the reveal shows the matching current
clip, not necessarily the opening clip.

**Preview intro** renders a low-resolution preview of the opening six seconds
(or the whole edit if shorter), with audio and the chosen composition. Identify
it as a preview; it does not create a finished-film shelf entry. Reuse it only
when the edit revision, analysis and appearance settings match exactly.

### Rendering and versions

**Render this edit** immediately submits the saved revision. Show stages:
“Preparing passages → Rendering video → Checking playback → Saving film.”
Use stage progress or an indeterminate indicator rather than a fabricated ETA.
Allow navigation and reloading; the server continues. Cancel leaves sources,
previous films and the editable draft intact.

The draft remains editable during rendering. Clearly say “Rendering revision 4”
and, after further edits, “New changes are not in this render.” Never mutate a
running job's snapshot. Completion adds a new version with Watch, Download and
Edit this version. Do not replace a prior version or auto-publish to YouTube.
Offer an H.264/AAC MP4 download with a human-readable date/version filename.

If another job occupies the current single-render worker, show that job and a
clear retry action; MVP does not pretend to support a queue. Duplicate submission
of the same idempotency key returns the existing job. Failure retains the draft,
shows a useful stage-specific explanation, and offers Retry.

## Persistence and API contract

Current gaps: `highlight-local.js` imports a completed job's project into a local
browser timeline, while hosted POST `/jobs` accepts only date/target duration and
runs automatic selection. `finished-films.js` has no edit entry point. Prototype
compositors are manual scripts with macOS font dependencies. These must be joined
through a server-owned edit contract, not browser-supplied media URLs or commands.

Proposed authenticated routes under `/jazz/highlight-api`:

| Route | Purpose |
| --- | --- |
| GET `/films/:id/edit` | Original saved edit, immutable media/analysis references and versions |
| POST `/edits` | Create draft from a film ID |
| GET `/edits/:id` | Fetch current cloud draft and revision |
| PATCH `/edits/:id` | Save clips and appearance with expected revision |
| POST `/edits/:id/previews` | Render short preview of a specified revision |
| POST `/edits/:id/renders` | Render exact revision with idempotency key |
| GET `/jobs/:id` | Persisted progress, errors and resulting version |

Draft schema: schemaVersion, id, owner, practiceDate, sourceFilmId, revision,
title, ordered clips, appearance, createdAt, updatedAt. Each clip retains a stable
id, original passage label, recordingId, sourceStartMs and sourceEndMs. A clip may
also retain validated presentation metadata: pitch-trail visibility, chapter and
subtitle text, and source-clock caption ranges. Appearance
stores intro enabled/duration, pitch trail enabled and pitch convention. The server
resolves source assets and analysis by trusted IDs. Never accept executable filter
strings, arbitrary filesystem paths or browser-supplied signed URLs.

Autosave after a short debounce, on blur and before render; show Saving / Saved /
Offline / Conflict. Retain an offline browser copy. Revision conflicts return 409
and preserve both local changes and the server version; offer reload or Save as
separate edit. Do not merge reorder operations silently.

An immutable render manifest records draft revision, ordered source ranges,
source media hashes, analysis version/hash, appearance, renderer version, output
checksum and exact segment presentation timestamps. Publish it with the film's
private assets and link it from index metadata. Source-clock mapping must use the
embedded video audio for video recordings. Persist exact boundaries during render;
new versions must never depend on keyframe guessing.

Validate ownership and source availability again at submission. Missing/deleted
sources produce a specific error naming affected passages; do not drop passages
or run selection as a fallback. Retain generated manifests and analysis even when
intermediate cuts are cleaned up. Backfill existing films from saved job projects;
films without recoverable projects remain watch/download only with an explanation.

## Implementation sequence

1. Add shared, versioned edit validation and timeline mapping; cloud draft storage,
   ownership checks, optimistic concurrency and legacy-project import.
2. Add Edit this film, stable passage selection, bulk keep-before/after, trims,
   reorder, undo/redo and cloud save feedback to the existing Clip Studio.
3. Add exact-edit server rendering that bypasses selection and persists segment
   timestamps. Preserve current automatic-generation behavior separately.
4. Extract the two prototype compositors into a shared portable rendering module,
   package licensed fonts, pin dependencies, add appearance settings and preview.
5. Wire version publication/download and deploy after end-to-end acceptance.

## Required tests and acceptance

- Contract unit tests: valid saved edit round-trip; bad schema, nonfinite times,
  inverted/out-of-range/too-short ranges, empty render, invalid intro duration;
  foreign recording IDs and rejected-region conflicts are refused.
- Editor model tests: Start film here on passage 9 retains exactly original 9–13;
  stable IDs survive renumbering; one undo restores all removed passages; reorder
  and trim recalculate duration and source-time mapping; settings undo correctly.
- API tests: revision conflict cannot overwrite; duplicate submissions create one
  job; exact edit never invokes selection; forged ownership fails; render snapshot
  survives later draft changes; busy, cancel, restart and missing-source states.
- Composition integration: synthetic tones/timecoded frames prove intro replaces
  picture without adding/repeating audio, trim starts correctly, and pitch aligns
  on both sides of cuts, including a cut within the intro and irregular timestamps.
  Test audio-only takes, missing notes, portrait footage and short films.
- Persistence tests: reload/on another browser restores the saved draft; failed
  publication does not expose an incomplete version; original version survives;
  source cleanup leaves edit manifests recoverable.
- UI acceptance: keyboard-only edits, mobile controls, focused-input shortcuts,
  revision labels and screen-reader progress; preview never appears as a final film.
- Real-media release check: reproduce the September 30 five-passage reference,
  decode the output, inspect opening/reveal/cut frames and listen across cuts.
  Verify duration to frame/sample tolerance and audio continuity; AAC re-encoding
  requires signal comparison, not a compressed-payload hash equality assertion.

Done means this acceptance scenario can be completed in an ordinary authenticated
browser and the resulting MP4 downloaded without an agent or local process.

## Implementation notes

The editor is a focused dialog within Clip Studio. Timeline cards use pitch
sparklines instead of thumbnail photographs. Precise numeric fields and source
range sliders provide trimming; move buttons and drag provide reordering. The
live source preview shows estimated pitch separately; the server-rendered intro
preview shows the final composition. Offline backups and explicit revision-conflict
recovery are included. These are intentional presentation choices; the saved edit
and server renderer share the specified exact-selection behavior.
