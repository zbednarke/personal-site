"""Tests for deploy/create-workbench-service.py (command building only; no gcloud)."""
import argparse
import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('create_workbench_service', ROOT / 'deploy' / 'create-workbench-service.py')
create = importlib.util.module_from_spec(spec)
spec.loader.exec_module(create)

# A fictional jazz-api description: plain values and secret references.
API = {
    'spec': {'template': {
        'metadata': {'annotations': {'run.googleapis.com/cloudsql-instances': 'fixture:us-central1:db'}},
        'spec': {'serviceAccountName': 'api@fixture.iam.gserviceaccount.com', 'containers': [{
            'image': 'us-central1-docker.pkg.dev/fixture/cloud-run-source-deploy/jazz-api@sha256:abc',
            'env': [
                {'name': 'GCS_BUCKET', 'value': 'fixture-bucket'},
                {'name': 'GCP_SERVICE_ACCOUNT', 'value': 'signer@fixture.iam.gserviceaccount.com'},
                {'name': 'DATABASE_URL', 'valueFrom': {'secretKeyRef': {'name': 'fixture-db-url', 'key': 'latest'}}},
                {'name': 'GATEWAY_KEY', 'valueFrom': {'secretKeyRef': {'name': 'fixture-gateway', 'key': '3'}}},
                {'name': 'PORT', 'value': '8080'},
            ],
        }]},
    }},
    'status': {'latestReadyRevisionName': 'jazz-api-00042'},
}
IAM = {'bindings': [{'role': 'roles/run.invoker', 'members': ['allUsers']}]}


def args(**kw):
    base = dict(owner='owner-fixture', vapid_public='BFixturePublicKey', cap='25', anthropic_secret='prism-anthropic-api-key',
                github_secret='workbench-github-token', vapid_secret='workbench-vapid-private-key', project='fixture', region='us-central1')
    base.update(kw)
    return argparse.Namespace(**base)


class CreateServiceTest(unittest.TestCase):
    def test_copies_the_api_and_adds_workbench_settings(self):
        env, commands = create.build_commands(API, IAM, args(), {'workbench-github-token': True, 'workbench-vapid-private-key': True}, '/tmp/env.yaml')
        deploy = commands[-1]
        self.assertEqual(env['WORKBENCH_MODE'], '1')
        self.assertEqual(env['WORKBENCH_OWNER_SUBJECT'], 'owner-fixture')
        self.assertEqual(env['GCP_SERVICE_ACCOUNT'], 'signer@fixture.iam.gserviceaccount.com')
        self.assertNotIn('PORT', env)
        self.assertIn('us-central1-docker.pkg.dev/fixture/cloud-run-source-deploy/jazz-api@sha256:abc', deploy)
        secrets = deploy[deploy.index('--set-secrets') + 1].split(',')
        self.assertIn('DATABASE_URL=fixture-db-url:latest', secrets)
        self.assertIn('GATEWAY_KEY=fixture-gateway:3', secrets)
        self.assertIn('ANTHROPIC_API_KEY=prism-anthropic-api-key:latest', secrets)
        self.assertIn('WORKBENCH_GITHUB_TOKEN=workbench-github-token:latest', secrets)
        self.assertIn('WORKBENCH_VAPID_PRIVATE_KEY=workbench-vapid-private-key:latest', secrets)
        for flag in ['--no-cpu-throttling', '--allow-unauthenticated']:
            self.assertIn(flag, deploy)
        self.assertEqual(deploy[deploy.index('--concurrency') + 1], '40')
        self.assertEqual(deploy[deploy.index('--set-cloudsql-instances') + 1], 'fixture:us-central1:db')
        self.assertEqual(deploy[deploy.index('--env-vars-file') + 1], '/tmp/env.yaml')
        bindings = [c for c in commands if c[:3] == ['gcloud', 'secrets', 'add-iam-policy-binding']]
        self.assertEqual(len(bindings), 3)
        self.assertNotIn('fixture-bucket', ' '.join(create.show(c) for c in commands), 'values stay out of the printed commands')

    def test_optional_secrets_and_private_ingress(self):
        env, commands = create.build_commands(API, {'bindings': []}, args(vapid_public=''), {}, 'f')
        deploy = commands[-1]
        secrets = deploy[deploy.index('--set-secrets') + 1]
        self.assertNotIn('WORKBENCH_GITHUB_TOKEN', secrets)
        self.assertNotIn('WORKBENCH_VAPID', secrets)
        self.assertNotIn('WORKBENCH_VAPID_PUBLIC_KEY', env)
        self.assertIn('--no-allow-unauthenticated', deploy)


if __name__ == '__main__':
    unittest.main()
