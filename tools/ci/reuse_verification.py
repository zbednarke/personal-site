"""Reuse a recent successful PR run only for the exact merged source tree.

All API/artifact failures cause fresh tests. No artifact content is executed.
Forks, changed trees, stale runs, failed runs, and missing receipts cannot skip CI.
"""
import datetime as dt
import io
import json
import os
from pathlib import Path
import subprocess
import urllib.error
import urllib.parse
import urllib.request
import zipfile

WORKFLOW = '.github/workflows/trumpets-tests.yml'
MAX_AGE = dt.timedelta(hours=1)
MAX_BYTES = 1024 * 1024


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class GitHub:
    def __init__(self, token):
        self.token = token

    def read(self, path, redirect=False):
        request = urllib.request.Request('https://api.github.com/' + path, headers={
            'Authorization': 'Bearer ' + self.token,
            'Accept': 'application/vnd.github+json',
            'X-GitHub-Api-Version': '2022-11-28',
        })
        try:
            response = urllib.request.build_opener(NoRedirect()).open(request, timeout=20)
        except urllib.error.HTTPError as error:
            if not redirect or error.code != 302:
                raise
            location = error.headers['Location']
            if urllib.parse.urlparse(location).scheme != 'https':
                raise ValueError('Artifact redirect must use HTTPS')
            # Never forward the GitHub token to signed blob-storage URLs.
            response = urllib.request.urlopen(location, timeout=20)
        with response:
            data = response.read(MAX_BYTES + 1)
        if len(data) > MAX_BYTES:
            raise ValueError('Oversized verification metadata')
        return data

    def json(self, path):
        return json.loads(self.read(path))

    def receipt(self, path):
        with zipfile.ZipFile(io.BytesIO(self.read(path, redirect=True))) as archive:
            if archive.namelist() != ['release-verification.json']:
                raise ValueError('Unexpected verification archive')
            if archive.getinfo('release-verification.json').file_size > MAX_BYTES:
                raise ValueError('Oversized verification receipt')
            return json.loads(archive.read('release-verification.json'))


def find_verified_run(client, repository, target, tree, now):
    prefix = 'repos/' + repository
    pulls = client.json(f'{prefix}/commits/{target}/pulls')
    for pull in pulls:
        if not pull.get('merged_at') or pull.get('merge_commit_sha') != target:
            continue
        if (pull['head'].get('repo') or {}).get('full_name') != repository:
            continue
        head = pull['head']['sha']
        # Head and tested merge trees must BOTH match the deployed tree. A
        # changed base, merge conflict resolution, or post-test edit reruns CI.
        if client.json(f'{prefix}/git/commits/{head}')['tree']['sha'] != tree:
            continue
        query = urllib.parse.urlencode({'event': 'pull_request', 'head_sha': head, 'status': 'success', 'per_page': 20})
        runs = client.json(f'{prefix}/actions/workflows/trumpets-tests.yml/runs?{query}')['workflow_runs']
        for run in runs:
            if (run.get('status'), run.get('conclusion'), run.get('event'), run.get('path'), run.get('head_sha')) != ('completed', 'success', 'pull_request', WORKFLOW, head):
                continue
            if (run.get('head_repository') or {}).get('full_name') != repository:
                continue
            started = dt.datetime.fromisoformat(run['run_started_at'].replace('Z', '+00:00'))
            if not dt.timedelta(0) <= now - started <= MAX_AGE:
                continue
            artifacts = client.json(f"{prefix}/actions/runs/{run['id']}/artifacts")['artifacts']
            name = f"release-verification-{run['run_attempt']}"
            matching = [a for a in artifacts if a['name'] == name and not a['expired']]
            if len(matching) != 1:
                continue
            receipt = client.receipt(f"{prefix}/actions/artifacts/{matching[0]['id']}/zip")
            if receipt.get('version') != 1 or receipt.get('tree') != tree or receipt.get('run_id') != str(run['id']) or receipt.get('run_attempt') != str(run['run_attempt']):
                continue
            if client.json(f"{prefix}/git/commits/{receipt['commit']}")['tree']['sha'] != tree:
                continue
            return run['id']
    return None


def main():
    run_id = None
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    force = event.get('inputs', {}).get('force_full_verification') in (True, 'true')
    if os.environ['GITHUB_EVENT_NAME'] == 'push' and not force:
        try:
            tree = subprocess.check_output(['git', 'rev-parse', 'HEAD^{tree}'], text=True).strip()
            run_id = find_verified_run(GitHub(os.environ['GH_TOKEN']), os.environ['GITHUB_REPOSITORY'], os.environ['GITHUB_SHA'], tree, dt.datetime.now(dt.timezone.utc))
        except Exception as error:
            print('Fresh verification required: ' + type(error).__name__)
    with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
        output.write(f"reused={'true' if run_id else 'false'}\n")
    message = f'Reusing exact-tree verification from run {run_id} (less than one hour old).' if run_id else 'Running fresh verification.'
    print(message)
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as summary:
        summary.write(message + '\n')


if __name__ == '__main__':
    main()
