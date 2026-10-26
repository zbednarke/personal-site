import contextlib
import datetime
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('reboot_once', ROOT / 'deploy' / 'reboot-site-once.py')
reboot = importlib.util.module_from_spec(spec); spec.loader.exec_module(reboot)
NOW = datetime.datetime(2026, 10, 26, 6, tzinfo=datetime.timezone.utc)


class RebootOnceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name); self.boot = root / 'boot'; self.boot.write_text('old-boot')
        self.required = root / 'required'; self.required.touch(); self.state = root / 'state/reboot.json'; self.calls = []

    def run_request(self, **kwargs):
        with contextlib.redirect_stdout(io.StringIO()):
            return reboot.request(state=self.state, boot=self.boot, required=self.required, now=kwargs.pop('now', NOW),
                                  runner=kwargs.pop('runner', lambda args, **_: self.calls.append(args)),
                                  is_idle=kwargs.pop('is_idle', lambda: True), **kwargs)

    def test_one_reboot_then_never_an_automatic_second_reboot(self):
        self.assertEqual(self.run_request()['action'], 'request_reboot')
        self.assertIn(['systemctl', 'reboot'], self.calls)
        self.boot.write_text('new-boot'); self.calls.clear()
        self.assertEqual(self.run_request()['action'], 'already_completed')
        self.assertEqual(self.calls, [])

    def test_expired_approval_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, 'expired'): self.run_request(now=reboot.EXPIRY)
        self.assertEqual(self.calls, []); self.assertFalse(self.state.exists())

    def test_no_reboot_needed_consumes_the_one_time_approval(self):
        self.required.unlink()
        self.assertEqual(self.run_request()['action'], 'not_required')
        self.required.touch()
        self.assertEqual(self.run_request()['action'], 'already_completed')
        self.assertEqual(self.calls, [])

    def test_busy_film_job_does_not_request_reboot(self):
        with self.assertRaisesRegex(RuntimeError, 'active'): self.run_request(is_idle=lambda: False)
        self.assertEqual(self.calls, []); self.assertFalse(self.state.exists())

    def test_inactive_service_does_not_request_reboot(self):
        def fail(args, **kwargs): raise RuntimeError('inactive')
        with self.assertRaisesRegex(RuntimeError, 'inactive'): self.run_request(runner=fail)
        self.assertFalse(self.state.exists())

    def test_pending_same_boot_can_retry_an_unfulfilled_request(self):
        self.state.parent.mkdir(); self.state.write_text(json.dumps({'boot_id': 'old-boot', 'completed': False}))
        self.assertEqual(self.run_request()['action'], 'request_reboot')


if __name__ == '__main__': unittest.main()
