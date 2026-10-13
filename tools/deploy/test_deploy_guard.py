"""The deploy guard: nothing is deployed over production that this commit does
not contain. deploy/check-deployed-commit.sh runs in a temporary git repository;
deploy/read-live-release.sh reads a temporary release through a fake
`gcloud compute ssh`; deploy/check-cloud-run-commit.sh reads revisions from a
fake `gcloud run`; package-static-release.py lists live files the repository
does not publish."""
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CHECK = ROOT / 'deploy' / 'check-deployed-commit.sh'
CLOUD_RUN = ROOT / 'deploy' / 'check-cloud-run-commit.sh'
READ = ROOT / 'deploy' / 'read-live-release.sh'
PACKAGE = ROOT / 'deploy' / 'package-static-release.py'
DEPLOYER = 'github-deploy@parabolio-prod.iam.gserviceaccount.com'


def git(repo, *args):
    return subprocess.run(['git', *args], cwd=repo, capture_output=True, text=True, check=True).stdout.strip()


class GitRepoTest(unittest.TestCase):
    """main: base -> live -> target; a side branch off base that main lacks."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp()); self.addCleanup(shutil.rmtree, self.tmp)
        self.repo = self.tmp / 'repo'; self.repo.mkdir()
        git(self.repo, 'init', '-q', '-b', 'main')
        git(self.repo, 'config', 'user.email', 't@example.com'); git(self.repo, 'config', 'user.name', 't')
        self.base = self.commit('base')
        self.live = self.commit('live')
        self.target = self.commit('target')
        git(self.repo, 'checkout', '-q', '-b', 'side', self.base)
        self.side = self.commit('out of band')
        git(self.repo, 'checkout', '-q', 'main')

    def commit(self, message):
        (self.repo / 'f.txt').write_text(message)
        git(self.repo, 'add', 'f.txt'); git(self.repo, 'commit', '-q', '-m', message)
        return git(self.repo, 'rev-parse', 'HEAD')

    def env(self, **extra):
        env = {k: v for k, v in os.environ.items() if k not in ('ALLOW_OVERWRITE', 'MISSING')}
        return {**env, **extra}


class CheckDeployedCommitTests(GitRepoTest):
    def check(self, live, target=None, **env):
        return subprocess.run(['bash', str(CHECK), 'static site', live, target or self.target],
                              cwd=self.repo, env=self.env(**env), capture_output=True, text=True)

    def assertRefused(self, result, reason):
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('::error', result.stdout)
        self.assertIn(reason, result.stdout)
        self.assertIn('not in main', result.stderr)
        self.assertIn('allow_overwrite_out_of_band', result.stderr)

    def test_an_ancestor_passes(self):
        result = self.check(self.live)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('is an ancestor of', result.stdout)

    def test_the_same_commit_passes(self):
        self.assertEqual(self.check(self.target).returncode, 0)

    def test_a_short_target_ref_resolves(self):
        self.assertEqual(self.check(self.live, target='main').returncode, 0)

    def test_a_commit_that_is_not_an_ancestor_fails(self):
        self.assertRefused(self.check(self.side), 'not an ancestor')

    def test_a_newer_live_commit_fails(self):
        # Deploying an older commit over a newer one would erase it too.
        self.assertRefused(self.check(self.target, target=self.live), 'not an ancestor')

    def test_an_unknown_sha_fails(self):
        self.assertRefused(self.check('0123456789abcdef0123456789abcdef01234567'), 'not a commit in this repository')

    def test_garbage_in_the_release_commit_fails(self):
        self.assertRefused(self.check('hand-deployed'), 'not a commit in this repository')

    def test_a_missing_release_commit_fails(self):
        self.assertRefused(self.check('missing'), 'no .release-commit')
        self.assertRefused(self.check(''), 'no .release-commit')

    def test_missing_only_warns_when_asked(self):
        result = self.check('', MISSING='warn')
        self.assertEqual(result.returncode, 0)
        self.assertIn('::warning', result.stdout)

    def test_the_override_passes_every_failure_with_a_warning(self):
        for live in (self.side, '0123456789abcdef0123456789abcdef01234567', 'missing'):
            result = self.check(live, ALLOW_OVERWRITE='true')
            self.assertEqual(result.returncode, 0, live)
            self.assertIn('::warning', result.stdout)
            self.assertIn('Overwriting anyway', result.stdout)

    def test_override_false_still_fails(self):
        self.assertEqual(self.check(self.side, ALLOW_OVERWRITE='false').returncode, 1)


# Like gcloud: `compute ssh ... --command CMD` runs CMD; `run services|revisions
# describe NAME` and `run revisions list` print $FAKE_RUN/<name>.json.
FAKE_GCLOUD = r'''#!/usr/bin/env bash
if [[ $1 == compute ]]; then
  cmd=
  while [[ $# -gt 0 ]]; do case $1 in --command) cmd=$2; shift 2 ;; *) shift ;; esac; done
  [[ ${FAKE_SSH:-ok} == fail ]] && exit 255
  # A first run's ssh-keygen prints its fingerprint and randomart to stdout.
  [[ ${FAKE_SSH:-ok} == banner ]] && printf 'Your identification has been saved\n+---[ED25519 256]--+\n|  .o.            |\n'
  exec bash -c "$cmd"
fi
if [[ $1 == run && $3 == list ]]; then
  while [[ $# -gt 0 ]]; do case $1 in --service) cat "$FAKE_RUN/list-$2.json"; exit ;; *) shift ;; esac; done
fi
[[ $1 == run && $3 == describe ]] || { echo "unexpected gcloud $*" >&2; exit 9; }
[[ -f $FAKE_RUN/$4.json ]] || { echo "not found: $4" >&2; exit 1; }
cat "$FAKE_RUN/$4.json"
'''


class FakeGcloudTest(GitRepoTest):
    def setUp(self):
        super().setUp()
        self.bin = self.tmp / 'bin'; self.bin.mkdir()
        (self.bin / 'gcloud').write_text(FAKE_GCLOUD); (self.bin / 'gcloud').chmod(0o755)
        self.run_dir = self.tmp / 'run'; self.run_dir.mkdir()

    def env(self, **extra):
        return super().env(PATH=f'{self.bin}:{os.environ["PATH"]}', FAKE_RUN=str(self.run_dir), **extra)


class CloudRunTests(FakeGcloudTest):
    def serve(self, *revisions, creator=DEPLOYER, history=()):
        """revisions: (name, commit-sha label or None, percent[, creator, image])."""
        traffic, listed = [], list(history)
        for name, sha, percent, *rest in revisions:
            who = rest[0] if rest else creator
            image = rest[1] if len(rest) > 1 else f'img@{name}'
            rev = {'metadata': {'name': name, 'labels': {'commit-sha': sha} if sha else {},
                                'annotations': {'serving.knative.dev/creator': who}},
                   'status': {'imageDigest': image}}
            (self.run_dir / f'{name}.json').write_text(json.dumps(rev))
            listed.append(rev)
            traffic.append({'revisionName': name, 'percent': percent})
        (self.run_dir / 'jazz-api.json').write_text(json.dumps({'status': {'traffic': traffic}}))
        (self.run_dir / 'list-jazz-api.json').write_text(json.dumps(listed))

    def check(self, **env):
        return subprocess.run(['bash', str(CLOUD_RUN), 'jazz-api', self.target, DEPLOYER, '--',
                               '--project', 'p', '--region', 'r'],
                              cwd=self.repo, env=self.env(**env), capture_output=True, text=True)

    def test_an_ancestor_label_passes(self):
        self.serve(('jazz-api-1', self.live, 100))
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('is an ancestor of', result.stdout)

    def test_a_label_that_is_not_an_ancestor_fails(self):
        self.serve(('jazz-api-1', self.side, 100))
        result = self.check()
        self.assertEqual(result.returncode, 1)
        self.assertIn('not an ancestor', result.stdout)

    def test_an_unknown_label_fails(self):
        self.serve(('jazz-api-1', 'f' * 40, 100))
        self.assertEqual(self.check().returncode, 1)

    def test_an_unlabelled_legacy_revision_only_warns(self):
        self.serve(('jazz-api-1', None, 100))
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('::warning', result.stdout)

    def test_every_serving_revision_is_checked_and_idle_ones_are_not(self):
        self.serve(('jazz-api-1', self.live, 90), ('jazz-api-2', self.side, 10), ('jazz-api-0', self.side, 0))
        result = self.check()
        self.assertEqual(result.returncode, 1)
        self.assertIn('jazz-api-2', result.stdout)
        self.assertNotIn('jazz-api-0', result.stdout)

    def test_a_revision_deployed_by_someone_else_fails_even_with_a_carried_over_label(self):
        self.serve(('jazz-api-2', self.live, 100, 'zach@example.com', 'img@new'))
        result = self.check()
        self.assertEqual(result.returncode, 1)
        self.assertIn('deployed by zach@example.com', result.stdout)
        self.assertIn('allow_overwrite_out_of_band', result.stderr)

    def test_a_settings_change_by_hand_on_the_workflows_image_is_checked_by_label(self):
        earlier = {'metadata': {'name': 'jazz-api-1', 'annotations': {'serving.knative.dev/creator': DEPLOYER}},
                   'status': {'imageDigest': 'img@ours'}}
        self.serve(('jazz-api-2', self.live, 100, 'Zach@Example.com', 'img@ours'), history=[earlier])
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('only its settings changed', result.stdout)

    def test_the_override_passes(self):
        self.serve(('jazz-api-1', self.side, 50), ('jazz-api-2', self.live, 50, 'zach@example.com', 'img@new'))
        result = self.check(ALLOW_OVERWRITE='true')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout.count('Overwriting anyway'), 2)

    def test_an_unreadable_service_fails(self):
        self.assertEqual(self.check().returncode, 1)


class ReadLiveReleaseTests(FakeGcloudTest):
    PATHS = ['index.html', 'assets']

    def setUp(self):
        super().setUp()
        release = self.tmp / 'site' / 'releases' / 'r1'
        (release / 'assets').mkdir(parents=True)
        (release / 'index.html').write_text('home'); (release / 'assets' / 'a.js').write_text('a')
        (release / 'hand-made.html').write_text('out of band')
        self.release = release
        self.current = self.tmp / 'site' / 'current'; self.current.symlink_to(release)
        self.out = self.tmp / 'out'; self.out.mkdir()

    def read(self, **env):
        return subprocess.run(['bash', str(READ), str(self.current), *self.PATHS, '--', 'gcloud', 'compute', 'ssh', 'vm'],
                              cwd=self.out, env=self.env(READ_TIMEOUT='10', **env), capture_output=True, text=True)

    def text(self, name):
        return (self.out / name).read_text()

    def test_reads_commit_files_and_hashes_in_one_call(self):
        (self.release / '.release-commit').write_text(self.live + '\n')
        result = self.read()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.text('live-commit.txt').strip(), self.live)
        self.assertEqual(sorted(self.text('live-files.txt').split()),
                         ['.release-commit', 'assets/a.js', 'hand-made.html', 'index.html'])
        self.assertEqual(sorted(line.split()[1] for line in self.text('live.sha256').splitlines()),
                         ['assets/a.js', 'index.html'])

    def test_output_before_the_read_does_not_hide_the_commit(self):
        # The first deploy with this guard read "missing": gcloud generated a
        # key and ssh-keygen's output came before the release-commit line.
        (self.release / '.release-commit').write_text(self.live + '\n')
        result = self.read(FAKE_SSH='banner')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.text('live-commit.txt').strip(), self.live)
        self.assertEqual(sorted(self.text('live-files.txt').split()),
                         ['.release-commit', 'assets/a.js', 'hand-made.html', 'index.html'])

    def test_a_missing_release_commit_reads_as_missing_and_fails_the_check(self):
        self.assertEqual(self.read().returncode, 0)
        self.assertEqual(self.text('live-commit.txt').strip(), 'missing')
        result = subprocess.run(['bash', str(CHECK), 'static site', self.text('live-commit.txt').strip(), self.target],
                                cwd=self.repo, env=self.env(), capture_output=True, text=True)
        self.assertEqual(result.returncode, 1)

    def test_a_failed_read_leaves_nothing_and_fails(self):
        result = self.read(FAKE_SSH='fail')
        self.assertNotEqual(result.returncode, 0)
        for name in ('live-commit.txt', 'live-files.txt', 'live.sha256'):
            self.assertEqual(self.text(name), '')


class OrphanFilesTests(unittest.TestCase):
    def test_live_files_the_repository_does_not_publish_are_listed_not_deleted(self):
        tmp = Path(tempfile.mkdtemp()); self.addCleanup(shutil.rmtree, tmp)
        (tmp / 'assets').mkdir(); (tmp / 'index.html').write_text('home'); (tmp / 'assets' / 'a.js').write_text('a')
        live_files = tmp / 'live-files.txt'
        live_files.write_text('.release-commit\nindex.html\nassets/a.js\nassets/oob.js\njazz/new/index.html\n')
        result = subprocess.run(['python3', str(PACKAGE), '--live', str(tmp / 'none'), '--live-files', str(live_files),
                                 '--out', str(tmp / 'out.tar.gz'), 'index.html', 'assets'],
                                cwd=tmp, capture_output=True, text=True, check=True)
        self.assertIn('::warning', result.stdout)
        self.assertIn('2 file(s)', result.stdout)
        self.assertIn('not in repo: assets/oob.js', result.stdout)
        self.assertIn('not in repo: jazz/new/index.html', result.stdout)
        self.assertNotIn('not in repo: index.html', result.stdout)
        self.assertNotIn('.release-commit', result.stdout)

    def test_no_warning_when_every_live_file_is_published(self):
        tmp = Path(tempfile.mkdtemp()); self.addCleanup(shutil.rmtree, tmp)
        (tmp / 'index.html').write_text('home')
        (tmp / 'live-files.txt').write_text('.release-commit\nindex.html\n')
        result = subprocess.run(['python3', str(PACKAGE), '--live', 'none', '--live-files', 'live-files.txt',
                                 '--out', 'out.tar.gz', 'index.html'], cwd=tmp, capture_output=True, text=True, check=True)
        self.assertNotIn('::warning', result.stdout)


if __name__ == '__main__':
    unittest.main()
