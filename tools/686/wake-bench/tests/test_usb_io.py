import time
import unittest

from usb_io import read_exact, read_line


class FragmentedPort:
    def __init__(self, chunks):
        self.chunks = iter(chunks)

    def read(self, size):
        chunk = next(self.chunks, b"")
        assert len(chunk) <= size
        return chunk

    readline = read


class USBProtocolTests(unittest.TestCase):
    def test_header_survives_partial_readline(self):
        port = FragmentedPort([b"LIVE ", b"", b"PCM 1280\r", b"\n"])
        self.assertEqual(read_line(port, time.monotonic() + 1), b"LIVE PCM 1280")

    def test_pcm_survives_partial_reads(self):
        port = FragmentedPort([b"x" * 1024, b"", b"y" * 256])
        self.assertEqual(read_exact(port, 1280), b"x" * 1024 + b"y" * 256)

    def test_incomplete_header_is_not_returned(self):
        port = FragmentedPort([b"LIVE PCM "])
        with self.assertRaises(TimeoutError):
            read_line(port, time.monotonic() + 0.01)
