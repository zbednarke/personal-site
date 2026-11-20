import datetime as dt
import importlib.util
import unittest
from pathlib import Path

HERE = Path(__file__).parent

def load(name):
    spec = importlib.util.spec_from_file_location(name, HERE / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

reuse = load('reuse_verification')
record = load('record_verification')
NOW = dt.datetime(2026, 11, 20, 20, tzinfo=dt.timezone.utc)
REPO, TARGET, HEAD, TESTED, TREE = 'owner/site', 'a'*40, 'b'*40, 'c'*40, 'd'*40


class FakeGitHub:
    def __init__(self):
        self.pull = {'merged_at': NOW.isoformat(), 'merge_commit_sha': TARGET, 'head': {'sha': HEAD, 'repo': {'full_name': REPO}}}
        self.run = {'id': 42, 'status': 'completed', 'conclusion': 'success', 'event': 'pull_request', 'path': reuse.WORKFLOW, 'head_sha': HEAD, 'head_repository': {'full_name': REPO}, 'run_started_at': (NOW - dt.timedelta(minutes=10)).isoformat(), 'run_attempt': 2}
        self.artifacts = [{'id': 123, 'name': 'release-verification-2', 'expired': False}]
        self.proof = {'version': 1, 'tree': TREE, 'commit': TESTED, 'run_id': '42', 'run_attempt': '2'}
        self.trees = {HEAD: TREE, TESTED: TREE}

    def json(self, path):
        if path.endswith('/pulls'): return [self.pull]
        if '/git/commits/' in path: return {'tree': {'sha': self.trees[path.rsplit('/', 1)[1]]}}
        if '/runs?' in path: return {'workflow_runs': [self.run]}
        if path.endswith('/artifacts'): return {'artifacts': self.artifacts}
        raise AssertionError('Unexpected API call')

    def receipt(self, path):
        if not path.endswith('/123/zip'): raise AssertionError('Unexpected artifact')
        return self.proof


class ReuseTests(unittest.TestCase):
    def check(self, client):
        return reuse.find_verified_run(client, REPO, TARGET, TREE, NOW)

    def test_exact_recent_same_repository_merge_reuses(self):
        self.assertEqual(self.check(FakeGitHub()), 42)

    def test_changed_head_or_actual_tested_merge_tree_reruns(self):
        for commit in (HEAD, TESTED):
            client = FakeGitHub(); client.trees[commit] = 'e'*40
            self.assertIsNone(self.check(client))

    def test_changed_receipt_tree_version_run_or_attempt_reruns(self):
        for key, value in [('tree', 'e'*40), ('version', 0), ('run_id', '43'), ('run_attempt', '1')]:
            client = FakeGitHub(); client.proof[key] = value
            self.assertIsNone(self.check(client))

    def test_stale_future_failed_cancelled_wrong_workflow_or_event_reruns(self):
        mutations = [('run_started_at', (NOW-dt.timedelta(hours=2)).isoformat()),
                     ('run_started_at', (NOW+dt.timedelta(minutes=1)).isoformat()),
                     ('status', 'in_progress'), ('conclusion', 'failure'), ('conclusion', 'cancelled'),
                     ('path', '.github/workflows/unrelated.yml'), ('event', 'push'), ('head_sha', 'e'*40)]
        for key, value in mutations:
            client = FakeGitHub(); client.run[key] = value
            self.assertIsNone(self.check(client), (key, value))

    def test_forks_or_unmerged_or_wrong_merge_commit_rerun(self):
        for change in ('fork', 'unmerged', 'wrong_merge', 'run_fork'):
            client = FakeGitHub()
            if change == 'fork': client.pull['head']['repo']['full_name'] = 'someone/else'
            if change == 'unmerged': client.pull['merged_at'] = None
            if change == 'wrong_merge': client.pull['merge_commit_sha'] = 'e'*40
            if change == 'run_fork': client.run['head_repository']['full_name'] = 'someone/else'
            self.assertIsNone(self.check(client))

    def test_missing_expired_ambiguous_or_old_attempt_receipt_reruns(self):
        for change in ('missing', 'expired', 'duplicate', 'old_attempt'):
            client = FakeGitHub()
            if change == 'missing': client.artifacts = []
            if change == 'expired': client.artifacts[0]['expired'] = True
            if change == 'duplicate': client.artifacts *= 2
            if change == 'old_attempt': client.artifacts[0]['name'] = 'release-verification-1'
            self.assertIsNone(self.check(client))


class AggregateTests(unittest.TestCase):
    def needs(self, reused=False):
        return {'preflight': {'result': 'success', 'outputs': {'reused': str(reused).lower()}},
                **{job: {'result': 'skipped' if reused else 'success'} for job in ('unit','browser','runtime')}}

    def test_full_success_and_verified_reuse_pass(self):
        record.validate(self.needs())
        record.validate(self.needs(True))

    def test_failed_cancelled_or_unexpected_skipped_jobs_cannot_turn_green(self):
        for reused in (False, True):
            for job in ('preflight','unit','browser','runtime'):
                for result in ('failure','cancelled','skipped'):
                    if reused and result == 'skipped' and job != 'preflight': continue
                    needs = self.needs(reused); needs[job]['result'] = result
                    with self.assertRaises(ValueError): record.validate(needs)

    def test_missing_reuse_proof_cannot_skip_checks(self):
        needs = self.needs(True); needs['preflight']['outputs'] = {}
        with self.assertRaises(ValueError): record.validate(needs)


if __name__ == '__main__': unittest.main()
