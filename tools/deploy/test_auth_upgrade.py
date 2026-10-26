import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('auth_upgrade', ROOT / 'deploy' / 'upgrade-jazz-auth.py')
upgrade = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)
SHA, OLD = 'a' * 40, 'b' * 40


class AuthUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name); self.bundle = self.root / 'bundle'; self.bundle.mkdir()
        self.binary = self.root / 'jazz-auth'; self.binary.write_bytes(b'old'); self.binary.chmod(0o755)
        (self.bundle / 'jazz-auth').write_bytes(b'patched')
        (self.bundle / 'accepted-auth-sources.txt').write_text(OLD + '\n' + SHA)
        self.config, self.caddy = self.root / 'config', self.root / 'Caddyfile'
        self.config.write_text('secret configuration'); self.config.chmod(0o640)
        self.caddy.write_text('existing routing')
        self.calls = []
        self.metadata = patch.object(upgrade.inventory, 'auth_build_info', side_effect=lambda path: {
            'status': 'read', 'source_commit': OLD if path == self.binary else SHA,
            'source_modified': False, 'go_version': 'go1.24.7' if path == self.binary else 'go1.27.3'})
        self.metadata.start(); self.addCleanup(self.metadata.stop)

    def run_command(self, args, label, **kwargs):
        self.calls.append(label)

    def install(self, **kwargs):
        return upgrade.upgrade(self.bundle, SHA, binary=self.binary, preserved=(self.config, self.caddy),
                               backups=self.root / 'backups', runner=kwargs.pop('runner', self.run_command),
                               health=kwargs.pop('health', lambda: True), **kwargs)

    def test_upgrade_preserves_configuration_and_saves_old_binary(self):
        before = upgrade.fingerprint((self.config, self.caddy))
        backup = self.install()
        self.assertEqual(self.binary.read_bytes(), b'patched'); self.assertEqual(backup.read_bytes(), b'old')
        self.assertEqual(upgrade.fingerprint((self.config, self.caddy)), before)
        self.assertIn('Patched private-route verification', self.calls)

    def test_configuration_rejection_does_not_install_or_restart(self):
        def fail(args, label, **kwargs):
            self.run_command(args, label)
            if label == 'Existing configuration validation': raise upgrade.UpgradeError('rejected')
        with self.assertRaises(upgrade.UpgradeError): self.install(runner=fail)
        self.assertEqual(self.binary.read_bytes(), b'old'); self.assertNotIn('Patched auth restart', self.calls)

    def test_failed_baseline_verification_does_not_install(self):
        def fail(args, label, **kwargs):
            if label == 'Baseline private-route verification': raise upgrade.UpgradeError('rejected')
        with self.assertRaises(upgrade.UpgradeError): self.install(runner=fail)
        self.assertEqual(self.binary.read_bytes(), b'old')

    def test_failed_restart_restores_previous_binary(self):
        def fail(args, label, **kwargs):
            self.run_command(args, label)
            if label == 'Patched auth restart': raise upgrade.UpgradeError('failed')
        with self.assertRaisesRegex(upgrade.UpgradeError, 'previous binary restored'): self.install(runner=fail)
        self.assertEqual(self.binary.read_bytes(), b'old'); self.assertIn('Previous auth restart', self.calls)

    def test_failed_verification_restores_previous_binary(self):
        def fail(args, label, **kwargs):
            if label == 'Patched private-route verification': raise upgrade.UpgradeError('failed')
        with self.assertRaisesRegex(upgrade.UpgradeError, 'previous binary restored'): self.install(runner=fail)
        self.assertEqual(self.binary.read_bytes(), b'old')

    def test_failed_rollback_is_not_reported_as_recovered(self):
        with self.assertRaisesRegex(upgrade.UpgradeError, 'operator intervention'): self.install(health=lambda: False)

    def test_unknown_or_dirty_old_source_is_refused(self):
        old = {'status': 'read', 'source_commit': OLD, 'source_modified': False}
        new = {'status': 'read', 'source_commit': SHA, 'source_modified': False, 'go_version': 'go1.27.3'}
        for changed in ({**old, 'source_commit': 'c' * 40}, {**old, 'source_modified': True}, {'status': 'unavailable'}):
            with self.assertRaises(upgrade.UpgradeError): upgrade.validate_builds(changed, new, SHA, {OLD})

    def test_old_or_dirty_candidate_is_refused(self):
        old = {'status': 'read', 'source_commit': OLD, 'source_modified': False}
        for version in ['go1.24.7', 'go1.27.2', 'go1.27rc1']:
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.validate_builds(old, {'status': 'read', 'source_commit': SHA, 'source_modified': False, 'go_version': version}, SHA, {OLD})

    def test_compiler_minimum(self):
        for version in ['go1.27.3', 'go1.27.4', 'go1.28.0']: self.assertTrue(upgrade.compiler_is_patched(version))

    def test_command_error_withholds_private_output(self):
        with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, b'private response', b'secret')):
            with self.assertRaisesRegex(upgrade.UpgradeError, 'output withheld') as error:
                upgrade.checked(['verify'], 'Verification')
        self.assertNotIn('secret', str(error.exception)); self.assertNotIn('private response', str(error.exception))


if __name__ == '__main__': unittest.main()
