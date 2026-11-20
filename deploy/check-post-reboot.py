"""Validate an actual new boot, not just a successful reboot request command."""
import json
from pathlib import Path
import sys


def validate(before, after, action):
    if action not in ('request_reboot', 'already_completed', 'not_required'):
        raise ValueError('Unknown reboot decision')
    if not before.get('boot_id') or not after.get('boot_id'):
        raise ValueError('Missing boot identity')
    if action == 'request_reboot' and after['boot_id'] == before['boot_id']:
        raise ValueError('New boot not observed yet')
    if not all(after.get('services_active', {}).get(name) for name in ('caddy', 'jazz-auth', 'jazz-highlights')):
        raise ValueError('Services not ready')
    if action == 'request_reboot' and after.get('reboot_required') is not False:
        raise ValueError('Host still reports reboot required')
    if after.get('caddy_build', {}).get('go_version') != 'go1.27.3' or after.get('jazz_auth', {}).get('go_version') != 'go1.27.3':
        raise ValueError('Patched server compilers not observed')
    if (after.get('highlights_runtime') or {}).get('pillow') != '13.0.0':
        raise ValueError('Patched film interpreter not observed')
    if (after.get('caddy_integrity') or {}).get('matches_managed_build') is not True:
        raise ValueError('Proxy does not match the verified installed build')
    return after


if __name__ == '__main__':
    try:
        before = json.loads(Path(sys.argv[1]).read_text())
        records = [s.split('=', 1)[1] for s in Path(sys.argv[2]).read_text().splitlines() if s.startswith('SITE_RUNTIME_INVENTORY=')]
        if len(records) != 1: raise ValueError('No unique runtime receipt')
        print(json.dumps(validate(before, json.loads(records[0]), Path(sys.argv[3]).read_text().strip())))
    except (ValueError, OSError, KeyError):
        print('Post-reboot verification pending or incomplete', file=sys.stderr)
        raise SystemExit(1)
