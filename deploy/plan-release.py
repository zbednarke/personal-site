"""Decide backend rebuilds from actual serving revisions, never the last commit.

Run only after the production provenance guard. Missing, split, unknown or
non-ancestor baselines conservatively rebuild. Each service is independent so a
partially failed deployment cannot leave Workbench behind on the next release.
"""
import argparse
import json
from pathlib import Path
import re
import subprocess

BACKEND_INPUTS = ('jazz-api', '.github/workflows/deploy.yml', 'deploy/plan-release.py', 'deploy/check-cloud-run-commit.sh')
SHA = re.compile(r'[0-9a-f]{40}')


def source_changed(base, target, git=subprocess.run):
    if not SHA.fullmatch(base or '') or not SHA.fullmatch(target or ''):
        return True
    if git(['git', 'merge-base', '--is-ancestor', base, target], capture_output=True).returncode:
        return True
    return git(['git', 'diff', '--quiet', base, target, '--', *BACKEND_INPUTS], capture_output=True).returncode != 0


def plan_service(service, revisions, target, force=False):
    traffic = [t for t in service.get('status', {}).get('traffic', []) if t.get('percent', 0) > 0]
    if force:
        return {'deploy': True, 'reason': 'Explicit rebuild/overwrite requested', 'revision': ''}
    if len(traffic) != 1 or traffic[0].get('percent') != 100 or not traffic[0].get('revisionName'):
        return {'deploy': True, 'reason': 'No single fully serving revision', 'revision': ''}
    name = traffic[0]['revisionName']
    candidates = [r for r in revisions if r.get('metadata', {}).get('name') == name]
    if len(candidates) != 1:
        return {'deploy': True, 'reason': 'Serving revision metadata unavailable', 'revision': ''}
    base = candidates[0].get('metadata', {}).get('labels', {}).get('commit-sha', '')
    changed = source_changed(base, target)
    return {'deploy': changed, 'reason': 'Backend/deployment inputs changed or baseline unknown' if changed else 'Serving backend/deployment inputs already match', 'revision': name}


def read_plan(folder, name, target, force):
    try:
        service = json.loads((folder / f'{name}-service.json').read_text())
        revisions = [json.loads(line) for line in (folder / f'{name}-revisions.jsonl').read_text().splitlines()]
        return plan_service(service, revisions, target, force)
    except (OSError, ValueError, TypeError, KeyError, AttributeError):
        return {'deploy': True, 'reason': 'Serving baseline could not be read', 'revision': ''}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--snapshots', type=Path, required=True)
    parser.add_argument('--target', required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--summary', type=Path, required=True)
    parser.add_argument('--force', action='store_true')
    parser.add_argument('--workbench', action='store_true')
    args = parser.parse_args()
    plans = {'api': read_plan(args.snapshots, 'jazz-api', args.target, args.force)}
    plans['workbench'] = read_plan(args.snapshots, 'workbench', args.target, args.force) if args.workbench else {'deploy': False, 'reason': 'Workbench disabled', 'revision': ''}
    with args.output.open('a') as output, args.summary.open('a') as summary:
        summary.write('### Backend deployment plan\n\n')
        for name, plan in plans.items():
            output.write(f"{name}={str(plan['deploy']).lower()}\n")
            if name == 'api':
                output.write(f"api_revision={plan['revision']}\n")
            summary.write(f"- {name}: {'deploy' if plan['deploy'] else 'reuse'} — {plan['reason']}.\n")
            print(f"{name}: {'deploy' if plan['deploy'] else 'reuse'} — {plan['reason']}")
