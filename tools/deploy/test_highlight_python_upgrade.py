import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('highlight_upgrade', ROOT / 'deploy' / 'upgrade-highlight-python.py')
upgrade = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)
SHA = 'a' * 40


class HighlightPythonUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name); self.bundle = self.root / 'bundle'; self.bundle.mkdir()
        wheels = self.bundle / 'wheels'; wheels.mkdir()
        lines = []
        for name, version in [('Pillow', '13.0.0'), ('pip', '26.2.2')]:
            value = (name + ' test fixture').encode()
            (wheels / f'{name.lower()}-{version}-py3-none-any.whl').write_bytes(value)
            lines.append(f'{name}=={version} --hash=sha256:' + hashlib.sha256(value).hexdigest())
        (self.bundle / 'highlight-runtime-wheels.txt').write_text('\n'.join(lines))
        self.venvs = self.root / 'venvs'; self.override = self.root / 'unit.d/security.conf'
        self.live = self.root / 'hosted.py'; self.live.write_text('existing app code')
        self.calls = []; self.new_pillow = '13.0.0'; self.new_numpy = '1.24.2'

    def command(self, args, label):
        self.calls.append((label, args))
        if label == 'Service command check':
            return f'{{ path=/usr/bin/python3 ; argv[]=/usr/bin/python3 {self.live} ; ignore_errors=no ; }}'
        if label == 'Runtime version check':
            old = args[0] == '/usr/bin/python3'
            return json.dumps({'python': [3, 11], 'pillow': '9.4.0' if old else self.new_pillow,
                               'numpy': '1.24.2' if old else self.new_numpy})
        return ''

    def install(self, **kwargs):
        return upgrade.upgrade(self.bundle, SHA, root=self.venvs, override=self.override, live=self.live,
                               runner=kwargs.pop('runner', self.command), is_idle=kwargs.pop('is_idle', lambda: True),
                               health=kwargs.pop('health', lambda: True), **kwargs)

    def labels(self):
        return [label for label, _ in self.calls]

    def test_success_preserves_application_and_installs_only_service_override(self):
        python = self.install()
        self.assertEqual(self.live.read_text(), 'existing app code')
        self.assertEqual(self.override.read_bytes(), upgrade.unit(python, self.live))
        self.assertIn('Patched private-route verification', self.labels())
        install = next(args for label, args in self.calls if label == 'Offline pinned Pillow installation')
        self.assertIn('--no-index', install[3]); self.assertIn('--no-deps', install[3])

    def test_tampered_wheel_stops_before_any_host_operation(self):
        next((self.bundle / 'wheels').glob('pillow-*.whl')).write_bytes(b'tampered')
        with self.assertRaisesRegex(upgrade.UpgradeError, 'checksum mismatch'): self.install()
        self.assertEqual(self.calls, []); self.assertFalse(self.override.exists())

    def test_missing_wheel_is_refused(self):
        next((self.bundle / 'wheels').glob('pip-*.whl')).unlink()
        with self.assertRaisesRegex(upgrade.UpgradeError, 'Missing'): self.install()

    def test_active_job_does_not_stage_or_restart(self):
        with self.assertRaisesRegex(upgrade.UpgradeError, 'active'): self.install(is_idle=lambda: False)
        self.assertFalse(self.venvs.exists()); self.assertNotIn('Patched film service restart', self.labels())

    def test_job_started_during_staging_does_not_restart(self):
        states = iter([True, False])
        with self.assertRaisesRegex(upgrade.UpgradeError, 'during staging'): self.install(is_idle=lambda: next(states))
        self.assertFalse(self.override.exists()); self.assertNotIn('Patched film service restart', self.labels())

    def test_numpy_change_is_refused_before_override(self):
        self.new_numpy = '2.5.4'
        with self.assertRaisesRegex(upgrade.UpgradeError, 'preserved NumPy'): self.install()
        self.assertFalse(self.override.exists())

    def test_unpatched_candidate_is_refused(self):
        self.new_pillow = '9.4.0'
        with self.assertRaisesRegex(upgrade.UpgradeError, 'Patched Pillow'): self.install()
        self.assertFalse(self.override.exists())

    def test_failed_compatibility_tests_do_not_restart(self):
        def fail(args, label):
            if label == 'Patched media compatibility tests': raise upgrade.UpgradeError('tests failed')
            return self.command(args, label)
        with self.assertRaisesRegex(upgrade.UpgradeError, 'tests failed'): self.install(runner=fail)
        self.assertFalse(self.override.exists()); self.assertNotIn('Patched film service restart', self.labels())

    def test_restart_failure_restores_previous_service(self):
        def fail(args, label):
            if label == 'Patched film service restart': raise upgrade.UpgradeError('restart failed')
            return self.command(args, label)
        with self.assertRaisesRegex(upgrade.UpgradeError, 'previous service restored'): self.install(runner=fail)
        self.assertFalse(self.override.exists()); self.assertIn('Previous film service restart', self.labels())

    def test_access_verification_failure_rolls_back(self):
        def fail(args, label):
            if label == 'Patched private-route verification': raise upgrade.UpgradeError('access failed')
            return self.command(args, label)
        with self.assertRaisesRegex(upgrade.UpgradeError, 'previous service restored'): self.install(runner=fail)
        self.assertFalse(self.override.exists())

    def test_rollback_failure_requires_operator(self):
        with self.assertRaisesRegex(upgrade.UpgradeError, 'operator intervention'): self.install(health=lambda: False)

    def test_unknown_service_command_is_not_overwritten(self):
        for start in ['path=/opt/custom/python ; argv[]=/opt/custom/python app.py ;',
                      f'path=/usr/bin/python3 ; argv[]=/usr/bin/python3 -u {self.live} ;']:
            with self.assertRaisesRegex(upgrade.UpgradeError, 'Custom'):
                upgrade.current_interpreter(start, self.venvs, self.live)

    def test_command_errors_do_not_expose_private_output(self):
        with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, 'private data', 'secret value')):
            with self.assertRaisesRegex(upgrade.UpgradeError, 'output withheld') as error: upgrade.checked(['verify'], 'Check')
        self.assertNotIn('secret value', str(error.exception)); self.assertNotIn('private data', str(error.exception))


if __name__ == '__main__': unittest.main()
