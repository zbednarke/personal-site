"""Read complete USB protocol messages despite partial serial timeouts."""

import time


def read_exact(port, size, timeout=1.0):
    payload = bytearray()
    deadline = time.monotonic() + timeout
    while len(payload) < size and time.monotonic() < deadline:
        payload.extend(port.read(size - len(payload)))
    if len(payload) != size:
        raise TimeoutError(f"Short USB read ({len(payload)}/{size}).")
    return bytes(payload)


def read_line(port, deadline, limit=1024):
    line = bytearray()
    while len(line) < limit and time.monotonic() < deadline:
        part = port.readline(limit - len(line))
        line.extend(part)
        if part.endswith(b"\n"):
            return bytes(line).strip()
    raise TimeoutError("Incomplete USB response line.")
