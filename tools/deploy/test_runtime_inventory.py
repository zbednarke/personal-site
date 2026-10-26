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

    def test_cloud_collector_rejects_mutations_before_execution(self):
        with patch.object(subprocess, 'run') as run:
            for command in (['projects', 'add-iam-policy-binding'], ['run', 'services', 'update'], ['storage', 'buckets', 'delete']):
                with self.assertRaises(ValueError):
                    inventory.command_json(command, 'name')
            run.assert_not_called()

    def test_access_denied_never_becomes_an_empty_safe_inventory(self):
        with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, 'secret', '403 forbidden secret')):
            state, data = inventory.command_json(['compute', 'instances', 'list'], 'name')
        self.assertEqual(state, {'status': 'permission_denied'})
        self.assertIsNone(data)
        self.assertNotIn('secret', str(state))

    def test_empty_observed_inventory_differs_from_missing_access(self):
        with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '[]', '')):
            state, data = inventory.command_json(['compute', 'instances', 'list'], 'name')
        self.assertEqual(state['status'], 'observed')
        self.assertEqual(data, [])

    def test_iam_public_grants_survive_but_identity_details_do_not(self):
        result = inventory.iam_summary({'bindings': [{'role': 'roles/run.invoker', 'members': ['allUsers', 'user:private@example.org'], 'condition': {'expression': 'secret-expression'}}]})
        self.assertEqual(result['bindings'][0]['public'], ['allUsers'])
        self.assertTrue(result['bindings'][0]['conditional'])
        self.assertNotIn('private@example', str(result))
        self.assertNotIn('secret-expression', str(result))

    def test_sql_does_not_report_absent_ssl_or_backup_settings_as_enabled(self):
        result = inventory.sql_summary({'name': 'private-db', 'settings': {}})
        self.assertIsNone(result['ssl_mode'])
        self.assertIsNone(result['backups_enabled'])
        self.assertNotIn('private-db', str(result))

    def test_run_redaction_does_not_leak_env_or_annotation_credentials(self):
        result = inventory.run_summary({'metadata': {'name': 'jazz-api', 'annotations': {'private-secret': 'secret-value', 'run.googleapis.com/ingress': 'all'}}, 'spec': {'template': {'spec': {'containers': [{'image': 'image@sha256:abc', 'env': [{'name': 'TOKEN', 'value': 'secret-value'}]}]}}}})
        self.assertEqual(result['ingress'], 'all')
        self.assertNotIn('secret-value', str(result))

    def test_listener_loopback_and_wildcard_are_distinguished_without_host_addresses(self):
        result = inventory.listener_summary('tcp LISTEN 0 128 127.0.0.1:8768 0.0.0.0:* users:(("jazz-auth",pid=1,fd=3))\ntcp LISTEN 0 128 [::]:8080 [::]:* users:(("python",pid=2,fd=4))')
        self.assertEqual([s['binding'] for s in result], ['loopback', 'wildcard'])
        self.assertEqual(result[1]['port'], 8080)
        self.assertNotIn('127.0.0.1', str(result))

    def test_firewall_disabled_rule_stays_explicit_and_ssh_iap_is_not_worldwide(self):
        result = inventory.firewall_summary({'name': 'ssh', 'disabled': True, 'sourceRanges': ['35.235.240.0/20'], 'allowed': [{'IPProtocol': 'tcp', 'ports': ['22']}]})
        self.assertTrue(result['disabled'])
        self.assertFalse(result['internet_wide'])
        self.assertTrue(result['public_source'])


if __name__ == '__main__':
    unittest.main()
