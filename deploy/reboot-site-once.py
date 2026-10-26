"""The owner's one-time October 26 approval; never a recurring reboot policy."""
import datetime
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

STATE = Path('/var/lib/site-security-maintenance/reboot-approved-2026-10-26.json')
EXPIRY = datetime.datetime(2026, 10, 27, tzinfo=datetime.timezone.utc)


def decision(previous, boot_id, required, now):
    if previous:
        if previous.get('completed') or previous.get('boot_id') != boot_id:
            return 'already_completed'
    if now >= EXPIRY: return 'approval_expired'
    return 'request_reboot' if required else 'not_required'


def request(*, state=STATE, boot=Path('/proc/sys/kernel/random/boot_id'),
            required=Path('/var/run/reboot-required'), now=None, runner=subprocess.run, is_idle=None):
    boot_id = boot.read_text().strip()
    previous = json.loads(state.read_text()) if state.exists() else None
    action = decision(previous, boot_id, required.exists(), now or datetime.datetime.now(datetime.timezone.utc))
    result = {'action': action, 'boot_id': boot_id, 'approval': 'owner-2026-10-26'}
    if action == 'approval_expired': raise RuntimeError('One-time reboot approval expired; no reboot requested')
    if action == 'request_reboot':
        if is_idle is None:
            spec = importlib.util.spec_from_file_location('film_safety', Path(__file__).with_name('upgrade-highlight-python.py'))
            safety = importlib.util.module_from_spec(spec); spec.loader.exec_module(safety)
            is_idle = safety.idle
        if not is_idle(): raise RuntimeError('A film job is active; reboot not requested')
        for name in ('caddy', 'jazz-auth', 'jazz-highlights'):
            runner(['systemctl', 'is-active', '--quiet', name], check=True, capture_output=True, timeout=30)
    state.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    if state.is_symlink(): raise RuntimeError('Unexpected reboot state symlink')
    state.write_text(json.dumps({'boot_id': previous.get('boot_id', boot_id) if previous else boot_id,
                                 'completed': action in ('already_completed', 'not_required')}))
    state.chmod(0o600)
    print('SITE_REBOOT_RECEIPT=' + json.dumps(result), flush=True)
    if action == 'request_reboot':
        runner(['systemctl', 'reboot'], check=True, capture_output=True, timeout=30)
    return result


if __name__ == '__main__':
    try:
        if os.geteuid() != 0: raise RuntimeError('Run as root')
        request()
    except Exception:
        print('Reboot request failed or refused; no private command output emitted', file=sys.stderr)
        raise SystemExit(1)
