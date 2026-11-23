# Ember wake phrase bench

Run `.venv/bin/python bench.py` in this directory, then open
`http://127.0.0.1:4199/`. Connect the CP2102 USB cable to Ember and close
other serial monitors first. The page asks for ten short clips and stores WAV
files, the match results, and a copy of the enrolled acoustic features under
`recordings/`. The server only listens on localhost.

Ember sends raw 16 kHz PCM over USB. The comparison runs the existing C++
firmware matcher with the device's saved templates and sensitivity, and runs
the English and phonetic Sherpa ONNX recognizers on the same capture. The
firmware replay starts with a fresh noise estimate and matcher state on each
clip. The test does not measure false wakes per hour or the latency of lighting
the eye on hardware.

Rebuild the native matcher after editing the firmware wake-word code:

```sh
firmware=../firmware/src
c++ -O2 -std=c++17 -I "$firmware" compare.cpp "$firmware/wake_word.cpp" -o compare
```

Set up the Python environment and unpack both Zipformer archives:

```sh
python3 -m venv .venv
.venv/bin/python -m pip install -r requirements.txt
mkdir -p models
for model in sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01 sherpa-onnx-kws-zipformer-zh-en-3M-2025-12-20; do
  curl --fail --location "https://github.com/k2-fsa/sherpa-onnx/releases/download/kws-models/$model.tar.bz2" |
    tar -xj -C models
done
```

Recordings and exported enrollment are ignored by Git.

## Two-stage replay and field test

For a saved session with corrected `labels.json`, run
`python two_stage.py recordings/SESSION --threshold 70`. It compares voting
rules within each fixed candidate segment and writes `two-stage.json`.
The live bridge uses overlapping windows during sustained sound. Pass
`--review recordings/SESSION` to `bench.py` to view saved results
without holding the USB port.

The standalone firmware supports `WAKE FIELD START`, `WAKE FIELD STATUS`,
and `WAKE FIELD STOP` over USB serial at 921600 baud. Start clears the
aggregate counters and enables shadow mode across power cycles. It counts
candidate speech bursts while the wake detector would otherwise be idle.
The board needs its configured Wi-Fi and synchronized clock. Candidates
from the idle gate stay on the device. Button turns and any follow-ups
after a button turn can still use the hosted service. The device stores aggregate time and
counts, with a checkpoint after about 15 minutes of measured idle time
when it next reaches a gap in speech. Stop saves final counts and
resumes the regular wake matcher.

## Live OR over USB

With firmware containing the `WAKE LIVE` serial commands, build the native
matcher library on the Mac and start the USB bridge:

```sh
firmware=../firmware/src
c++ -O2 -std=c++17 -dynamiclib -I "$firmware" live_engine.cpp "$firmware/wake_word.cpp" -o /tmp/ember-live-engine.dylib
.venv/bin/python live_or.py --engine /tmp/ember-live-engine.dylib
```

When the bridge reports ready, say the wake phrase into Ember and wait for
the solid-blue eye before asking a question. The host applies the speech gate
and accepts a match from any of the three detectors. During sustained
sound, the host runs five-second windows with two seconds of overlap until
the gate closes. The ESP32 does the recording, hosted voice request, playback,
and follow-ups. The live bridge
does not keep idle recordings. Stopping the bridge releases the serial port
and lets the standalone wake matcher resume after the lease expires.

## Protocol and capture checks

From this directory:

```sh
python3 -m unittest discover -s tests
c++ -std=c++17 -Wall -Wextra -Werror -I ../firmware/src tests/wake_question.cpp -o /tmp/ember-wake-question-test
/tmp/ember-wake-question-test
```
