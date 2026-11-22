#!/usr/bin/python3 -I
"""Narrow root-only guest shutdown command; never reads credentials or media.

The Go worker calls this only between jobs, after checking its persisted queue.
An authenticated periodic reconciler repairs a job arriving during shutdown.
Any active operator session, Codex process or VM bootstrap defers poweroff.
"""
import os
import pwd as unix_accounts
import re
import subprocess
import sys


def run(args):
    return subprocess.run(args, capture_output=True, text=True, timeout=5)


def busy():
    uid = unix_accounts.getpwnam('magic-codex').pw_uid
    processes = run(['/usr/bin/ps', '-u', str(uid), '-o', 'pid='])
    if processes.returncode not in (0, 1) or processes.stderr.strip():
        raise RuntimeError('Could not inspect Codex processes')
    if processes.stdout.strip():
        return True
    sessions = run(['/usr/bin/loginctl', 'list-sessions', '--no-legend', '--no-pager'])
    if sessions.returncode:
        raise RuntimeError('Could not inspect operator sessions')
    for line in sessions.stdout.splitlines():
        fields = line.split()
        if len(fields) < 3 or not fields[1].isdigit() or not re.fullmatch(r'[A-Za-z0-9_-]+', fields[0]):
            raise RuntimeError('Unrecognized operator session')
        remote = run(['/usr/bin/loginctl', 'show-session', fields[0], '--property=Remote', '--value'])
        if remote.returncode:
            raise RuntimeError('Could not inspect operator session')
        if remote.stdout.strip() == 'yes':
            return True
    startup = run(['/usr/bin/systemctl', 'show', 'google-startup-scripts.service', '--property=SubState', '--value'])
    if startup.returncode:
        raise RuntimeError('Could not inspect VM bootstrap')
    # A oneshot bootstrap can remain ActiveState=active, SubState=exited.
    return startup.stdout.strip() not in ('dead', 'exited')


def main():
    if os.geteuid() != 0 or len(sys.argv) != 1:
        return 1
    try:
        if busy():
            return 75
        result = run(['/usr/bin/systemctl', 'poweroff', '--no-block'])
        return 0 if result.returncode == 0 else 75
    except (OSError, RuntimeError, KeyError, subprocess.TimeoutExpired):
        return 75  # Fail closed; never dump process/session details.


if __name__ == '__main__':
    raise SystemExit(main())
