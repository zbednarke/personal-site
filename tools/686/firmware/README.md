# Culpable Ember bench test

One blue eye on GPIO 18. Ember can run from wall power using its own mic and
speaker. The local 686 demo also supports audio over USB.

| From | To |
| --- | --- |
| ESP32 GND | Breadboard ground rail |
| ESP32 GPIO 18 | D5. The LED's long leg shares column 5 at E5. |
| LED short leg at E4 | One resistor lead at D4, in the same column |
| Other resistor lead | Directly into the blue ground rail |

Power off while wiring. Use the terminal labeled **18** and leave the toy board
and passive speaker disconnected.

## Simulate

Open this folder in Cursor or VS Code with the Wokwi extension installed.
Run **Wokwi: Start Simulator** from the Command Palette. The simulated LED
breathes as soon as the firmware starts.
Enter `PULSE` in the Wokwi serial monitor to see the response animation.
`STATUS`, `EYES ON`, `EYES OFF`, and `IDLE` are also available.

To rebuild this project on this Mac:

```sh
pio run
```

The bench firmware also accepts `VOICE 0` through `VOICE 255` over USB
serial at 921600 baud. Send `IDLE` to restore the idle animation. If level updates
stop for 800 ms, the eye automatically returns to its idle animation.

## Test the speaker on a physical bench

Unplug USB while wiring the MAX98357A. Leave the eye on GPIO 18.

| Amp pin | Connection |
| --- | --- |
| BCLK | ESP32 GPIO 26 |
| LRC / LRCLK | ESP32 GPIO 25 |
| DIN | ESP32 GPIO 27 |
| VIN | Verified 5 V supply |
| GND | ESP32 common ground |
| SPK+ and SPK− | One lead of an 8 Ω speaker to each terminal |

After flashing, send `TONE` over USB serial at 921600 baud to play a quiet
480 Hz tone. In Chrome, click **Connect ESP32 audio** at
`http://127.0.0.1:4187/686/` and choose the CP2102 port. Speak into the Mac
microphone. The ESP32 plays the streamed audio and drives the eye from the same
samples as they leave I²S.

## Test the round microphone

| INMP441 pin | Connection |
| --- | --- |
| VDD | ESP32 3V3 |
| GND and L/R | ESP32 common ground |
| SCK | ESP32 GPIO 32 |
| WS | ESP32 GPIO 33 |
| SD | ESP32 GPIO 34 |

Over USB serial at 921600 baud, `MIC TEST` captures three seconds. A
`MIC PCM16 16000 48000` line precedes 96,000 bytes of 16 kHz, signed
16-bit little-endian mono PCM. To use the same input in the web demo, connect
ESP32 audio, leave **Ember mic** selected, and hold **V** until the page shows
that it's listening. Release to send the recording. **Mac mic** is available
from the same input selector.

## Wi-Fi push-to-talk

With USB unplugged, connect a normally open momentary button between ESP32
GPIO 23 and common ground. On a four-leg switch use the two terminals that
show continuity only when pressed. The firmware provides the pull-up.

Provision Wi-Fi and the device token over USB using the personal-site
`tools/686/ember_wifi.py` tool. This uses a local password prompt and stores
settings in the ESP32's nonvolatile storage. The tool's `create` and
`serve-lan` commands can prepare a TLS endpoint on a Mac for bench testing.
The Mac must remain awake for that LAN endpoint.

After the ESP32 reports `WIFI READY`, it can run on a 5 V wall supply. Hold
the button, speak when the eye is solid blue, and release to send. Ember keeps up to 12 seconds of compressed speech in RAM while HTTPS connects
and uploads. A failed connection before the upload is committed can retry
using that same recording. The audio buffer is released after the turn. USB
`WIFI STATUS` reports readiness.

## Wake phrase

Connect Ember over USB and send `WAKE TRAIN` in a serial monitor at 921600
baud. For each of three samples, wait for the eye to turn solid blue and say
**“Hey three four three”** once at a natural pace. Leave a short silence after
each phrase. `WAKE TRAIN SAVED` confirms enrollment. `WAKE STATUS` checks it
and `WAKE CANCEL` discards a training session in progress. Use `WAKE ADD` to
keep the original samples and enroll three variations, such as a quieter,
higher, or slower delivery. The ESP32 stores
only acoustic features in its flash. Idle audio stays on the device.

In normal use say the wake phrase, then speak when the eye is solid blue.
Ember gives you about six seconds to start your question. After it hears
speech, about 1.2 seconds of silence sends the turn. Serial playback logs
include timing and allowlisted hosted errors. After the reply, a medium steady blue eye
marks an eight-second window for a follow-up question. Speak normally without
the wake phrase. Ember detects follow-up speech locally and keeps a short audio
lead-in to preserve the first word. If no one speaks, it returns to listening
for the wake phrase. Wake detection is tuned to the enrolled speaker.

| Blue eye | Meaning |
| --- | --- |
| Brief bright light on startup | Power on |
| Single blink repeating | Joining Wi-Fi |
| Double blink repeating | Wi-Fi joined, synchronizing the clock |
| Dim, steady | Ready for the button, waiting for wake enrollment |
| Slow, dim breathing | Ready for wake phrase or button |
| Steady, full brightness | Recording |
| Faster, brighter breathing | Waiting for the reply |
| Flickers in time with speech | Playing the reply |
| Medium, steady blue | Follow-up window. Speak to continue |
| One short flash, then slow breathing | Follow-up window closed |
| Three quick flashes | Something failed |
