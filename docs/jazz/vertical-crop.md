# Clip Studio vertical export

Add clips to Output, enable **Also render vertical (9:16)**, and select an output
clip. Drag the red dashed frame to move it; drag an edge or corner to resize it
while keeping 9:16. The area outside the frame is excluded from the vertical
version. Arrow keys move the frame and + / − change its size; the size slider and
Reset offer the same controls. **Apply to clips from this take** copies framing
to that recording's other output clips. Each copy can subsequently be adjusted
independently. Framing stays with the local project when clips are reordered or
the page reloads; source recordings and candidate time boundaries are unchanged.

Render produces the existing Full 1920×1080 file plus a Vertical 1080×1920 file.
Both have the same cuts, AAC playback audio and a lossless ALAC master track,
with original levels. Separate download links avoid browser restrictions on
multiple automatic downloads. Links expire after one hour; outputs keep the
existing temporary-object retention settings. Leave the page open while rendering.
Turning vertical off preserves the existing single-output/automatic-download flow.

## Implementation

This extends the existing synchronous Jazz API renderer, not Magic Film or its
private VM. No database migration, new service or new IAM grant is needed.
Deploy the API and static assets together through the normal release workflow.

`POST /v1/studio/renders` accepts optional `includeVertical: true` and per-clip
`verticalCrop: {centerX, centerY, scale}`. Omitted crops are centered at full size.
Centers are normalized to the upright source image, and scale is in [0.1,1]
relative to the largest fitting 9:16 rectangle. Server-side finite/bounds checks
and actual oriented, square-pixel source dimensions determine the crop. Client
URLs, pixel dimensions and FFmpeg expressions are never accepted.

The response retains the original top-level Full `url`, `filename`, expiry and
quality fields for existing clients, including individual clip downloads. An
additive `outputs` array names the Full/Vertical files, sizes, URLs and expiry.

Dual rendering processes one source clip at a time. Each clip yields a lossless
NUT stream with normalized Full video, cropped Vertical video and PCM audio. A
concat demuxer consumes separate inherited pipes in order; the final encoders run
once per movie and stream both MP4s directly to private GCS uploads. No complete
intermediate movie is held in memory or `/tmp`. Independent NUT files must not
be byte-appended into one input, and separate raw video/audio input pipes must
not be used: those approaches can corrupt packets or deadlock during probing.

Each cut is aligned to a whole 30 fps video frame; its PCM audio is padded/trimmed
to the corresponding 1,600 samples per frame at 48 kHz. Both versions use that
same timeline and report the aligned output duration. MP4 header finalization
waits for codec priming information so video B-frames, AAC and ALAC stay aligned.
Any source, encoder, upload or request-cancellation failure stops the pipeline,
aborts uploads and attempts bounded cleanup of both objects. No success links
are returned for a partial pair. Encoding/decoding threads and upload buffers
are bounded for the existing 2 CPU / 2 GiB service.

## Verification

- `node --test assets/jazz/clip-studio-model.test.js`: defaults, backward
  compatibility, geometry, independent crops, persistence and request snapshots.
- `go test ./...` with disposable Postgres variables: actual FFmpeg crop pixels,
  rotation/SAR, mixed orientations, fractional cuts, audio/frame counts, sparse
  keyframe seeking, upload/source failure and cancellation.
- `node tools/jazz/vertical-crop-check.cjs`: actual player and synthetic video,
  mouse/touch framing, edge resize, keyboard, cancellation, reload, viewport
  changes, rotated playback, frozen render request and both download links.
  CI uploads the screenshots as `jazz-vertical-crop-verification`.

The local 24-clip stress fixture uses 720p sources, 1080p outputs and a 12-second
timeline. It completed the paired render in about nine seconds, with two active
FFmpeg processes peaking around 667 MiB combined RSS. This establishes bounded
memory for the tested fixture; it is not a production timing promise for long
or higher-resolution recordings. The UI labels progress as an estimate.
