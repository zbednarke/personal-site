"""Run Ember's three-detector OR test over USB. Idle audio stays in memory."""

import argparse
from collections import deque
from datetime import datetime, timezone
import ctypes as C
from pathlib import Path
import signal
import time

import numpy as np
import serial

from bench import make_spotters
from usb_io import read_exact, read_line

RATE = 16000
BLOCK_BYTES = 640
BLOCK_MS = 20
LEAD_BLOCKS = 400 // BLOCK_MS
WINDOW_BLOCKS = 5000 // BLOCK_MS
OVERLAP_BLOCKS = 2000 // BLOCK_MS
WINDOW_STRIDE = WINDOW_BLOCKS - OVERLAP_BLOCKS
NAMES = ("Ember", "Sherpa English", "Sherpa Phonetic")


def log(message):
    print(f"{datetime.now(timezone.utc).isoformat(timespec='milliseconds')} {message}", flush=True)


class Native:
    def __init__(self, path, enrollment, threshold):
        lib = C.CDLL(str(path))
        lib.ember_live_create.argtypes = (C.c_char_p, C.c_size_t, C.c_uint)
        lib.ember_live_create.restype = C.c_void_p
        lib.ember_live_destroy.argtypes = (C.c_void_p,)
        lib.ember_live_destroy.restype = None
        lib.ember_live_reset.argtypes = (C.c_void_p,)
        lib.ember_live_reset.restype = None
        for fn, result in ((lib.ember_live_gate, C.c_uint), (lib.ember_live_match, C.c_bool)):
            fn.argtypes = (C.c_void_p, C.c_char_p)
            fn.restype = result
        self.lib = lib
        self.enrollment = enrollment
        self.threshold = threshold
        self.reset()

    def reset(self):
        if getattr(self, "state", None):
            self.close()
        self.state = self.lib.ember_live_create(self.enrollment, len(self.enrollment), self.threshold)
        if not self.state:
            raise RuntimeError("Invalid Ember enrollment.")

    def close(self):
        self.lib.ember_live_destroy(self.state)
        self.state = None

    def candidate(self, frame):
        return self.lib.ember_live_gate(self.state, frame)

    def fork(self):
        other = object.__new__(type(self))
        other.lib = self.lib
        other.enrollment = self.enrollment
        other.threshold = self.threshold
        other.reset()
        return other

    def match(self, frame):
        return self.lib.ember_live_match(self.state, frame)


class WakeWindow:
    def __init__(self, native, models):
        self.native = native.fork()
        self.models = models
        self.streams = [model.create_stream() for model in models]
        self.blocks = 0

    def advance(self, frame, samples):
        self.blocks += 1
        hits = [NAMES[0]] if self.native.match(frame) else []
        return hits + self.advance_sherpa(samples)

    def advance_sherpa(self, samples):
        hits = []
        for name, model, stream in zip(NAMES[1:], self.models, self.streams):
            stream.accept_waveform(RATE, samples)
            while model.is_ready(stream):
                model.decode_stream(stream)
                if model.get_result(stream) == "HEY_343":
                    hits.append(name)
                    model.reset_stream(stream)
                    break
        return hits

    def close(self):
        self.native.close()


class Ensemble:
    def __init__(self, native, models, diagnostics=False):
        self.native = native
        self.models = models
        self.diagnostics = diagnostics
        self.recent = deque(maxlen=LEAD_BLOCKS)
        self.windows = []

    def restart(self):
        if self.diagnostics and self.windows:
            log(f"WAKE GATE interrupted candidate_ms={self.windows[0].blocks * BLOCK_MS}")
        self.clear_windows()
        self.native.reset()
        self.recent.clear()

    def clear_windows(self):
        for window in self.windows:
            window.close()
        self.windows.clear()

    @staticmethod
    def samples(frame):
        return np.frombuffer(frame, dtype="<i2").astype(np.float32) / 32768

    def open_window(self):
        window = WakeWindow(self.native, self.models)
        self.windows.append(window)
        return window

    def add(self, frame):
        self.recent.append(frame)
        event = self.native.candidate(frame)
        if event == 1:
            window = self.open_window()
            if self.diagnostics:
                log("WAKE GATE open")
            for block in self.recent:
                hits = window.advance(block, self.samples(block))
                if hits:
                    self.clear_windows()
                    return hits
            return []
        if not self.windows:
            return []

        samples = self.samples(frame)
        hits = []
        for window in self.windows:
            for detector in window.advance(frame, samples):
                if detector not in hits:
                    hits.append(detector)
        if hits:
            self.clear_windows()
            return hits
        if event == 2:
            return self.finish()
        # Start the next window before the old one ends. Each receives the
        # overlap as it arrives, so rolling never replays two seconds at once.
        if self.windows[-1].blocks >= WINDOW_STRIDE:
            self.open_window()
            if self.diagnostics:
                log(f"WAKE GATE overlap overlap_ms={OVERLAP_BLOCKS * BLOCK_MS}")
        if event == 3 or self.windows[0].blocks >= WINDOW_BLOCKS:
            hits = self.finish_window(self.windows.pop(0), "limit")
            if hits:
                self.clear_windows()
                return hits
        return []

    def finish_window(self, window, reason):
        try:
            hits = (window.advance_sherpa(np.zeros(RATE // 2, dtype=np.float32))
                    if reason == "silence" else [])
            if self.diagnostics and not hits:
                log(f"WAKE REJECT candidate_ms={window.blocks * BLOCK_MS} ended={reason} votes=0")
            return hits
        finally:
            window.close()

    def finish(self):
        while self.windows:
            hits = self.finish_window(self.windows.pop(0), "silence")
            if hits:
                self.clear_windows()
                return hits
        return []


def exchange(port, command, expected, timeout=3):
    port.write(command + b"\n")
    until = time.monotonic() + timeout
    while time.monotonic() < until:
        try:
            line = read_line(port, until)
        except TimeoutError as error:
            raise TimeoutError(command.decode()) from error
        if line.startswith(expected):
            return line
        if line.startswith((b"WIFI TURN", b"FOLLOWUP ", b"BUTTON ", b"WAKE MATCH")):
            log(line.decode())
            if line in (b"BUTTON PRESS", b"WAKE MATCH") or line.startswith(b"FOLLOWUP SPEECH"):
                until = max(until, time.monotonic() + 45)
    raise TimeoutError(command.decode())


def prepare(port):
    for _ in range(7):
        try:
            exchange(port, b"HELLO", b"EMBER PCM12", 1)
            break
        except TimeoutError:
            pass
    else:
        raise TimeoutError("Ember did not respond to HELLO.")
    header = exchange(port, b"WAKE EXPORT", (b"WAKE MODEL ", b"WAKE EXPORT EMPTY"))
    if header == b"WAKE EXPORT EMPTY":
        raise RuntimeError("Ember has no enrolled templates.")
    _, _, threshold, count = header.decode().split()
    payload = bytearray()
    for _ in range(int(count)):
        length = read_exact(port, 1)
        payload.extend(length)
        payload.extend(read_exact(port, length[0] * 12))
    return bytes(payload), int(threshold)


def serve_port(port, args, models):
    enrollment, threshold = prepare(port)
    native = Native(args.engine, enrollment, threshold)
    ensemble = Ensemble(native, models, diagnostics=True)
    try:
        armed = False
        announced = False
        failed_exchanges = 0
        while True:
            try:
                line = exchange(port, b"WAKE LIVE READ" if armed else b"WAKE LIVE BEGIN", b"LIVE ", 4)
                pcm = read_exact(port, 2 * BLOCK_BYTES) if line == b"LIVE PCM 1280" else None
                failed_exchanges = 0
            except TimeoutError as error:
                failed_exchanges += 1
                if failed_exchanges >= 3:
                    raise
                log(f"USB resync: {error}")
                port.reset_input_buffer()
                ensemble.restart()
                armed = announced = False
                continue
            if line == b"LIVE READY":
                armed = True
                ensemble.restart()
                if not announced:
                    log("READY: three-way OR online (5s windows, 2s overlap)")
                announced = True
                continue
            if pcm is None:
                if line != b"LIVE BUSY":
                    log(line.decode())
                    announced = False
                armed = line == b"LIVE BUSY" and armed
                ensemble.restart()
                time.sleep(.2)
                continue
            hits = ensemble.add(pcm[:BLOCK_BYTES]) or ensemble.add(pcm[BLOCK_BYTES:])
            if not hits:
                continue
            log("WAKE OR " + ", ".join(hits))
            result = exchange(port, b"WAKE LIVE TRIGGER", b"LIVE ")
            log(result.decode())
            if result == b"LIVE ACCEPTED":
                until = time.monotonic() + 45
                while time.monotonic() < until:
                    try:
                        reply = read_line(port, until)
                    except TimeoutError:
                        break
                    if reply.startswith((b"WIFI TURN", b"FOLLOWUP ", b"WAKE MATCH")):
                        log(reply.decode())
                    if reply in (b"WIFI TURN DONE", b"WIFI TURN FAILED"):
                        break
                armed = False
            ensemble.restart()
    finally:
        ensemble.clear_windows()
        native.close()
        try:
            if port.is_open:
                port.write(b"WAKE LIVE END\n")
        except (OSError, serial.SerialException):
            pass


def live(args, models):
    pause = .5
    while True:
        port = serial.Serial(port=None, baudrate=921600, timeout=.2, write_timeout=2, exclusive=True)
        port.port = args.port
        port.dtr = port.rts = False
        started = time.monotonic()
        try:
            with port:
                serve_port(port, args, models)
        except (OSError, serial.SerialException) as error:
            if time.monotonic() - started >= 10:
                pause = .5
            log(f"USB unavailable ({error}); retrying in {pause:g}s")
            time.sleep(pause)
            pause = min(8, pause * 2)


def replay(args, models):
    native = Native(args.engine, (args.replay / "enrollment.bin").read_bytes(), args.threshold)
    ensemble = Ensemble(native, models)
    try:
        for path in sorted(args.replay.glob("*.pcm")):
            ensemble.restart()
            samples = path.read_bytes()
            hits = []
            for offset in range(0, len(samples) - BLOCK_BYTES + 1, BLOCK_BYTES):
                hits = ensemble.add(samples[offset:offset + BLOCK_BYTES])
                if hits:
                    break
            print(path.name, hits or ensemble.finish(), flush=True)
    finally:
        ensemble.clear_windows()
        native.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--engine", type=Path, required=True)
    parser.add_argument("--port", default="/dev/cu.usbserial-0001")
    parser.add_argument("--replay", type=Path)
    parser.add_argument("--threshold", type=int, default=70)
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, lambda *_: signal.raise_signal(signal.SIGINT))
    backend = make_spotters()
    try:
        if args.replay:
            replay(args, backend)
        else:
            live(args, backend)
    except KeyboardInterrupt:
        pass
