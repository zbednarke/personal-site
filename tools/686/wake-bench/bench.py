import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import re
import subprocess
from threading import Thread, Lock
import time
import wave

import numpy as np
import serial
import sherpa_onnx

from usb_io import read_exact, read_line

ROOT = Path(__file__).resolve().parent
MODEL = ROOT / "models/sherpa-onnx-kws-zipformer-gigaspeech-3.3M-2024-01-01"
PHONE_MODEL = ROOT / "models/sherpa-onnx-kws-zipformer-zh-en-3M-2025-12-20"
RATE = 16000
LOCAL_HOSTS = {"localhost:4199", "127.0.0.1:4199"}
PROMPTS = [
    ("“Hey, three four three”", "Normal voice, about 1–2 feet from Ember.", "wake"),
    ("“Hey, three four three”", "Normal voice again, same distance.", "wake"),
    ("“Hey, three four three”", "Say it softly, without whispering.", "wake"),
    ("“Hey, three four three”", "Say it a little higher pitched than usual.", "wake"),
    ("“Hey, three four three”", "Use a slower, more relaxed delivery.", "wake"),
    ("“Hey, three four three”", "Step back to roughly six feet.", "wake"),
    ("“Hey, three four two”", "Come back to 1–2 feet. Say it naturally.", "negative"),
    ("“Hey, three for free”", "Same distance and normal voice.", "negative"),
    ("“What do you think about cheesecake?”", "Ordinary speech, with no wake phrase.", "negative"),
    ("“Hello world”", "Normal voice. This checks Sherpa on a stock keyword.", "control"),
]
guard = Lock()
state = {"phase": "connecting", "error": None, "results": [], "prompts": PROMPTS}
port = None
threshold = None
run = ROOT / "recordings" / time.strftime("%Y%m%d-%H%M%S")
spotter = None
phone_spotter = None


def sherpa_result(pcm, detector):
    stream = detector.create_stream()
    normalized = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768
    # Let the transducer emit its trailing blank without trimming the input.
    normalized = np.concatenate((normalized, np.zeros(RATE // 2, dtype=np.float32)))
    detections = {}
    for offset in range(0, len(normalized), RATE // 25):
        stream.accept_waveform(RATE, normalized[offset:offset + RATE // 25])
        while detector.is_ready(stream):
            detector.decode_stream(stream)
            keyword = detector.get_result(stream)
            if keyword:
                detections.setdefault(keyword, round((offset + RATE // 25) * 1000 / RATE))
                detector.reset_stream(stream)
    return detections


def line_matching(prefix, timeout):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        line = read_line(port, deadline)
        if line.startswith(prefix) or line == b"ERROR":
            return line
    raise TimeoutError(f"Ember did not return the expected response {prefix!r}.")


def exact(count):
    return read_exact(port, count)


def make_spotters():
    spotter = sherpa_onnx.KeywordSpotter(
        tokens=str(MODEL / "tokens.txt"),
        encoder=str(MODEL / "encoder-epoch-12-avg-2-chunk-16-left-64.int8.onnx"),
        decoder=str(MODEL / "decoder-epoch-12-avg-2-chunk-16-left-64.onnx"),
        joiner=str(MODEL / "joiner-epoch-12-avg-2-chunk-16-left-64.int8.onnx"),
        keywords_file=str(ROOT / "keywords.txt"),
    )
    phone_spotter = sherpa_onnx.KeywordSpotter(
        tokens=str(PHONE_MODEL / "tokens.txt"),
        encoder=str(PHONE_MODEL / "encoder-epoch-13-avg-2-chunk-8-left-64.int8.onnx"),
        decoder=str(PHONE_MODEL / "decoder-epoch-13-avg-2-chunk-8-left-64.onnx"),
        joiner=str(PHONE_MODEL / "joiner-epoch-13-avg-2-chunk-8-left-64.int8.onnx"),
        keywords_file=str(ROOT / "keywords-phones.txt"),
    )
    return spotter, phone_spotter


def setup(device):
    global port, threshold, spotter, phone_spotter
    run.mkdir(parents=True, mode=0o700)
    spotter, phone_spotter = make_spotters()
    port = serial.Serial(port=None, baudrate=921600, timeout=0.5, write_timeout=2, exclusive=True)
    port.port = device
    port.dtr = port.rts = False
    port.open()
    try:
        for _ in range(12):
            port.write(b"HELLO\n")
            try:
                if line_matching(b"EMBER PCM12", 1) == b"EMBER PCM12":
                    break
            except TimeoutError:
                continue
        else:
            raise RuntimeError("Ember did not respond over USB.")
        port.write(b"WAKE EXPORT\n")
        header = line_matching((b"WAKE MODEL ", b"WAKE EXPORT EMPTY"), 2)
        if header == b"WAKE EXPORT EMPTY":
            raise RuntimeError("Ember has no saved wake enrollment.")
        _, _, threshold, count = header.decode().split()
        if int(count) != 6 or not 42 <= int(threshold) <= 74:
            raise RuntimeError("Unexpected matcher configuration from Ember.")
        data = bytearray()
        lengths = []
        for _ in range(int(count)):
            length = exact(1)[0]
            if length > 120:
                raise RuntimeError("Invalid acoustic template length received.")
            data.append(length)
            data.extend(exact(length * 12))
            lengths.append(length)
        (run / "enrollment.bin").write_bytes(data)
        print(f"Ember ready. Matcher threshold {threshold}; template lengths {lengths}.", flush=True)
        state.update(phase="ready", threshold=threshold, lengths=lengths)
    except Exception:
        port.close()
        raise


def capture(index):
    pcm = bytearray()
    try:
        port.write(b"MIC START\n")
        if line_matching(b"OK", 2) != b"OK":
            raise RuntimeError("Ember could not start the microphone.")
        try:
            for block in range(110):  # About 4.4 seconds, including a 400 ms lead-in.
                port.write(b"MIC READ\n")
                if line_matching(b"MIC DATA ", 2) != b"MIC DATA 1280":
                    raise RuntimeError("Ember could not read the microphone.")
                pcm.extend(exact(1280))
                if block == 9:
                    with guard:
                        state["phase"] = "speak"
        finally:
            port.write(b"MIC STOP\n")
            line_matching(b"OK", 2)
        with guard:
            state["phase"] = "scoring"
        stem = run / f"{index + 1:02d}"
        raw = stem.with_suffix(".pcm")
        raw.write_bytes(pcm)
        with wave.open(str(stem.with_suffix(".wav")), "wb") as out:
            out.setparams((1, 2, RATE, 0, "NONE", "not compressed"))
            out.writeframes(pcm)
        native = subprocess.check_output(
            [str(ROOT / "compare"), str(run / "enrollment.bin"), str(raw), threshold], text=True
        )
        match_ms, mean, section = map(int, native.split())
        detected = sherpa_result(pcm, spotter)
        detected_phones = sherpa_result(pcm, phone_spotter)
        result = {
            "index": index,
            "ember": match_ms or None,
            "mean": mean,
            "section": section,
            "sherpa": detected.get("HEY_343"),
            "control": detected.get("CONTROL"),
            "sherpa_phones": detected_phones.get("HEY_343"),
            "control_phones": detected_phones.get("CONTROL"),
            "peak": int(np.max(np.abs(np.frombuffer(pcm, dtype="<i2").astype(np.int32)))),
        }
        with guard:
            state["results"].append(result)
            (run / "results.json").write_text(json.dumps(state["results"], indent=2) + "\n")
            state["phase"] = "done" if len(state["results"]) == len(PROMPTS) else "ready"
        print(json.dumps(result), flush=True)
    except Exception as error:
        with guard:
            state.update(phase="ready", error=str(error))
        print(str(error), flush=True)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, payload, content_type="application/json", code=200):
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(payload)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Security-Policy", "frame-ancestors 'none'")
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self):
        if self.headers.get("Host") not in LOCAL_HOSTS:
            self.reply(b'{}', code=403)
            return
        if self.path == "/":
            self.reply((ROOT / "index.html").read_bytes(), "text/html; charset=utf-8")
        elif self.path == "/state":
            with guard:
                status = json.dumps(state).encode()
            self.reply(status)
        elif re.fullmatch(r"/clip/[0-9]{2}", self.path):
            clip = run / (self.path[-2:] + ".wav")
            if clip.exists():
                self.reply(clip.read_bytes(), "audio/wav")
            else:
                self.reply(b'{}', code=404)
        else:
            self.reply(b'{}', code=404)

    def do_POST(self):
        if (self.path != "/record"
                or self.headers.get("Origin") != f"http://{self.headers.get('Host')}"
                or self.headers.get("Host") not in LOCAL_HOSTS):
            self.reply(b'{}', code=403)
            return
        with guard:
            if state["phase"] != "ready":
                self.reply(b'{}', code=409)
                return
            index = len(state["results"])
            state.update(phase="arming", error=None)
        Thread(target=capture, args=(index,), daemon=True).start()
        self.reply(b'{}', code=202)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", default="/dev/cu.usbserial-0001")
    parser.add_argument("--review", type=Path, help="open an existing recording session without USB")
    args = parser.parse_args()
    if args.review:
        run = args.review.resolve()
        corrected_prompts = list(PROMPTS)
        labels = run / "labels.json"
        if labels.exists():
            for i, label in enumerate(json.loads(labels.read_text())["labels"]):
                if label != corrected_prompts[i][2]:
                    corrected_prompts[i] = ("“Three four three”", "Label corrected after recording.", label)
        state.update(phase="done", prompts=corrected_prompts, results=json.loads((run / "results.json").read_text()))
        replay = run / "two-stage.json"
        if replay.exists():
            state["two_stage"] = json.loads(replay.read_text())
    else:
        setup(args.port)
    server = ThreadingHTTPServer(("127.0.0.1", 4199), Handler)
    print("http://127.0.0.1:4199/", flush=True)
    try:
        server.serve_forever()
    finally:
        server.server_close()
        if port is not None:
            port.close()
