import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('caddy_upgrade', ROOT / 'deploy' / 'upgrade-caddy-runtime.py')
upgrade = importlib.util.module_from_spec(spec); spec.loader.exec_module(upgrade)
SHA = 'a' * 40
MODULES = 'http.handlers.reverse_proxy\nhttp.handlers.file_server\ntls.issuance.acme\n'


class CaddyUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name); self.bundle = self.root / 'bundle'; self.bundle.mkdir()
        self.binary = self.root / 'caddy'; self.binary.write_bytes(b'old'); self.binary.chmod(0o755)
        (self.bundle / 'caddy').write_bytes(b'patched')
        self.config = self.root / 'Caddyfile'; self.config.write_text('existing private proxy config')
        self.auth = self.root / 'auth.json'; self.auth.write_text('existing auth secrets')
        self.calls = []
        p = patch.object(upgrade.auth.inventory, 'auth_build_info', return_value={
            'status': 'read', 'go_version': 'go1.27.3', 'source_modified': False, 'source_commit': SHA})
        p.start(); self.addCleanup(p.stop)

    def command(self, args, label):
        self.calls.append((label, args))
        if label == 'Installed version': return 'v2.11.4 old'
        if label == 'Candidate version': return 'v2.11.7 patched'
        if 'module inventory' in label: return MODULES
        if label == 'Environment-file metadata': return '/etc/caddy/jazz.env (ignore_errors=no)'
        return ''

    def install(self, **kwargs):
        return upgrade.upgrade(self.bundle, SHA, binary=self.binary, preserved=(self.config, self.auth),
                               backups=self.root / 'backup', receipt=self.root / 'state/caddy.sha256',
                               runner=kwargs.pop('runner', self.command), is_idle=kwargs.pop('is_idle', lambda: True),
                               active=kwargs.pop('active', lambda *_: True), home='/var/lib/caddy', **kwargs)

    def test_updates_binary_only_and_records_verified_hash(self):
        before = upgrade.auth.fingerprint((self.config, self.auth)); backup = self.install()
        self.assertEqual(self.binary.read_bytes(), b'patched'); self.assertEqual(backup.read_bytes(), b'old')
        self.assertEqual(upgrade.auth.fingerprint((self.config, self.auth)), before)
        self.assertEqual((self.root / 'state/caddy.sha256').read_text().strip(), hashlib.sha256(b'patched').hexdigest())

    def test_missing_current_module_is_refused(self):
        def command(args, label):
            if label == 'Installed module inventory': return MODULES + 'http.handlers.custom\n'
            return self.command(args, label)
        with self.assertRaisesRegex(upgrade.UpgradeError, 'omits installed'): self.install(runner=command)
        self.assertEqual(self.binary.read_bytes(), b'old')

    def test_invalid_configuration_does_not_restart(self):
        def command(args, label):
            if label == 'Current proxy configuration validation': raise upgrade.UpgradeError('invalid config')
            return self.command(args, label)
        with self.assertRaisesRegex(upgrade.UpgradeError, 'invalid config'): self.install(runner=command)
        self.assertEqual(self.binary.read_bytes(), b'old')
        self.assertNotIn('Patched proxy restart', [label for label, _ in self.calls])

    def test_busy_film_service_is_not_interrupted(self):
        with self.assertRaisesRegex(upgrade.UpgradeError, 'Film job active'): self.install(is_idle=lambda: False)
        self.assertEqual(self.binary.read_bytes(), b'old')

    def test_failed_restart_rolls_back(self):
        def command(args, label):
            if label == 'Patched proxy restart': raise upgrade.UpgradeError('restart failed')
            return self.command(args, label)
        with self.assertRaisesRegex(upgrade.UpgradeError, 'previous binary restored'): self.install(runner=command)
        self.assertEqual(self.binary.read_bytes(), b'old')

    def test_failed_private_access_rolls_back(self):
        def command(args, label):
            if label == 'Patched private-route verification': raise upgrade.UpgradeError('access failed')
            return self.command(args, label)
        with self.assertRaisesRegex(upgrade.UpgradeError, 'previous binary restored'): self.install(runner=command)
        self.assertEqual(self.binary.read_bytes(), b'old')

    def test_failed_rollback_does_not_claim_health(self):
        with self.assertRaisesRegex(upgrade.UpgradeError, 'operator intervention'): self.install(active=lambda *_: False)

    def test_concurrent_binary_replacement_is_preserved(self):
        def command(args, label):
            if label == 'Current proxy configuration validation': self.binary.write_bytes(b'other deploy')
            return self.command(args, label)
        with self.assertRaisesRegex(upgrade.UpgradeError, 'concurrent changes'): self.install(runner=command)
        self.assertEqual(self.binary.read_bytes(), b'other deploy')

    def test_downgrade_is_refused(self):
        def command(args, label):
            if label == 'Installed version': return 'v2.12.0 newer'
            return self.command(args, label)
        with self.assertRaisesRegex(upgrade.UpgradeError, 'downgrade'): self.install(runner=command)

    def test_validation_consumes_env_files_without_putting_secrets_in_argv(self):
        args = upgrade.validation_command(Path('/tmp/caddy'), Path('/etc/caddy/Caddyfile'), '/etc/caddy/jazz.env (ignore_errors=no)', '/var/lib/caddy')
        self.assertIn('--uid=caddy', args)
        self.assertIn('--property=EnvironmentFile=/etc/caddy/jazz.env', args)
        self.assertNotIn('existing auth secrets', str(args))


if __name__ == '__main__': unittest.main()
