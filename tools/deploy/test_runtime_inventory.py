import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[2] / 'deploy' / 'inventory-site-runtime.py'
spec = importlib.util.spec_from_file_location('runtime_inventory', SOURCE)
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)


def encoded(value):
    value = value.encode()
    length, prefix = len(value), bytearray()
    while length >= 128:
        prefix.append((length & 127) | 128)
        length >>= 7
    return bytes(prefix + bytes([length])) + value


class RuntimeInventoryTests(unittest.TestCase):
    def binary(self, version='go1.24.4', modules='build\tvcs.revision=' + 'a' * 40 + '\nbuild\tvcs.modified=false\n'):
        header = b'\xff Go buildinf:' + bytes([8, 2]) + bytes(16)
        return bytes(16) + header + encoded(version) + encoded(modules)

    def test_reads_go_version_and_revision_without_dumping_module_data(self):
        result = inventory.go_build_info(self.binary(modules='private-not-to-be-printed\nbuild\tvcs.revision=' + 'a' * 40))
        self.assertEqual(result['go_version'], 'go1.24.4')
        self.assertEqual(result['source_commit'], 'a' * 40)
        self.assertNotIn('private-not-to-be-printed', str(result))

    def test_missing_and_truncated_headers_are_not_invented(self):
        for data in (b'', b'not a Go binary', self.binary()[:35], self.binary()[:-20]):
            self.assertEqual(inventory.go_build_info(data), {'status': 'unavailable'})

    def test_dirty_build_is_reported(self):
        self.assertTrue(inventory.go_build_info(self.binary(modules='build\tvcs.modified=true\n'))['source_modified'])

    def test_long_inline_module_info(self):
        result = inventory.go_build_info(self.binary(modules='x' * 300 + '\nbuild\tvcs.revision=' + 'b' * 40))
        self.assertEqual(result['source_commit'], 'b' * 40)

    def test_invalid_version_does_not_escape_into_report(self):
        self.assertEqual(inventory.go_build_info(self.binary(version='secret\nvalue')), {'status': 'unavailable'})

    def test_command_failure_does_not_print_stderr(self):
        with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, 'sensitive stdout', 'sensitive stderr')):
            self.assertIsNone(inventory.output(['missing']))

    def test_command_error_is_unavailable(self):
        with patch.object(subprocess, 'run', side_effect=FileNotFoundError):
            self.assertIsNone(inventory.output(['missing']))


if __name__ == '__main__':
    unittest.main()
