import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('release_plan', ROOT / 'deploy/plan-release.py')
plan = importlib.util.module_from_spec(spec); spec.loader.exec_module(plan)


class ReleasePlanTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.old_cwd = Path.cwd()
        os.chdir(self.temporary.name)
        self.addCleanup(os.chdir, self.old_cwd)
        self.git('init', '-q')
        self.git('config', 'user.name', 'Release test')
        self.git('config', 'user.email', 'test@example.invalid')
        self.base = self.commit('jazz-api/main.go', 'backend v1')

    def git(self, *args):
        return subprocess.check_output(['git', *args], stderr=subprocess.DEVNULL, text=True).strip()

    def commit(self, path, text):
        p = Path(path); p.parent.mkdir(parents=True, exist_ok=True); p.write_text(text)
        self.git('add', path); self.git('commit', '-qm', 'test')
        return self.git('rev-parse', 'HEAD')

    def state(self, base):
        return ({'status': {'traffic': [{'percent': 100, 'revisionName': 'serving'}]}},
                [{'metadata': {'name': 'serving', 'labels': {'commit-sha': base}}}])

    def test_homepage_and_documentation_changes_do_not_rebuild_backend(self):
        head = self.commit('index.html', 'new homepage')
        head = self.commit('docs/notes.md', 'new notes')
        result = plan.plan_service(*self.state(self.base), head)
        self.assertFalse(result['deploy'])
        self.assertEqual(result['revision'], 'serving')

    def test_api_migrations_and_deployment_policy_changes_rebuild(self):
        for path in ('jazz-api/main.go', 'jazz-api/migrations/new.sql', '.github/workflows/deploy.yml', 'deploy/plan-release.py'):
            with self.subTest(path=path):
                head = self.commit(path, 'changed')
                self.assertTrue(plan.plan_service(*self.state(self.base), head)['deploy'])
                self.base = head

    def test_partial_deploy_checks_each_services_real_baseline(self):
        backend = self.commit('jazz-api/main.go', 'backend v2')
        head = self.commit('index.html', 'homepage')
        self.assertFalse(plan.plan_service(*self.state(backend), head)['deploy'])
        self.assertTrue(plan.plan_service(*self.state(self.base), head)['deploy'])

    def test_split_missing_unknown_nonancestor_or_force_rebuilds(self):
        head = self.commit('index.html', 'homepage')
        for base in ('', 'unknown', 'f'*40):
            self.assertTrue(plan.plan_service(*self.state(base), head)['deploy'])
        service, revisions = self.state(self.base)
        service['status']['traffic'] = [{'percent': 50, 'revisionName': 'serving'}, {'percent': 50, 'revisionName': 'other'}]
        self.assertTrue(plan.plan_service(service, revisions, head)['deploy'])
        self.assertTrue(plan.plan_service({}, [], head)['deploy'])
        self.assertTrue(plan.plan_service(self.state(self.base)[0], [], head)['deploy'])
        self.assertTrue(plan.plan_service(*self.state(self.base), head, force=True)['deploy'])
        self.assertTrue(plan.plan_service(*self.state(head), self.base)['deploy'])

    def test_missing_snapshot_fails_closed(self):
        self.assertTrue(plan.read_plan(Path('.'), 'jazz-api', self.base, False)['deploy'])


if __name__ == '__main__': unittest.main()
