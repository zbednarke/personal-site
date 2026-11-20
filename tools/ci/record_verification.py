"""Final required check: fail closed on failed/cancelled/unexpected skipped jobs."""
import json
import os
from pathlib import Path
import subprocess


def validate(needs):
    if needs['preflight']['result'] != 'success':
        raise ValueError('Verification preflight did not succeed')
    reused = needs['preflight']['outputs'].get('reused') == 'true'
    expected = 'skipped' if reused else 'success'
    for job in ('unit', 'browser', 'runtime'):
        if needs[job]['result'] != expected:
            raise ValueError(f'{job}: expected {expected}, got {needs[job]["result"]}')


if __name__ == '__main__':
    validate(json.loads(os.environ['VERIFICATION_NEEDS']))
    receipt = {
        'version': 1,
        'tree': subprocess.check_output(['git', 'rev-parse', 'HEAD^{tree}'], text=True).strip(),
        'commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip(),
        'run_id': os.environ['GITHUB_RUN_ID'],
        'run_attempt': os.environ['GITHUB_RUN_ATTEMPT'],
    }
    Path('release-verification.json').write_text(json.dumps(receipt) + '\n')
