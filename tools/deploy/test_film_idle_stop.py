import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('film_idle_stop', Path(__file__).resolve().parents[2] / 'deploy/magic-film-idle-stop.py')
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


class IdleStopTest(unittest.TestCase):
    def invoke(self, *, codex='', sessions='', remote='no', startup='exited', failed=None):
        calls = []
        def run(args):
            calls.append(args)
            if failed and failed in args:
                raise subprocess.TimeoutExpired(args, 5)
            if args[0].endswith('/ps'): value = codex
            elif 'list-sessions' in args: value = sessions
            elif 'show-session' in args: value = remote
            elif 'show' in args: value = startup
            else: value = ''
            return subprocess.CompletedProcess(args, 0, value, '')
        with patch.object(helper, 'run', side_effect=run), patch.object(helper.os, 'geteuid', return_value=0), patch.object(helper.sys, 'argv', ['helper']), patch.object(helper.unix_accounts, 'getpwnam') as user:
            user.return_value.pw_uid = 999
            result = helper.main()
        return result, calls

    def test_only_idle_guest_can_poweroff(self):
        result, calls = self.invoke()
        self.assertEqual(result, 0)
        self.assertEqual(calls[-1], ['/usr/bin/systemctl', 'poweroff', '--no-block'])
        self.assertIn('--property=SubState', calls[-2])

    def test_sessions_processes_bootstrap_and_inspection_failures_defer(self):
        for kwargs in [dict(codex='123'), dict(sessions='1 1000 operator -', remote='yes'),
                       dict(startup='running'), dict(startup='unknown'),
                       dict(sessions='unrecognized output'), dict(failed='list-sessions'),
                       dict(failed='/usr/bin/ps'), dict(failed='show')]:
            with self.subTest(kwargs=kwargs):
                result, calls = self.invoke(**kwargs)
                self.assertEqual(result, 75)
                self.assertFalse(any('poweroff' in command for command in calls))

    def test_no_arguments_and_root_only(self):
        for uid, argv in [(1000, ['helper']), (0, ['helper', '--anything'])]:
            with patch.object(helper.os, 'geteuid', return_value=uid), patch.object(helper.sys, 'argv', argv), patch.object(helper, 'run') as run:
                self.assertEqual(helper.main(), 1)
                run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
