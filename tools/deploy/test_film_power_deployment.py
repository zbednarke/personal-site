import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), Path(__file__).resolve().parents[2] / ('deploy/'+name+'.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


configure = load('configure-private-film-power')
installer = load('install-private-film-worker')
API_URL = 'https://jazz-api-abc-uc.a.run.app'


def api(power='1'):
    env = [{'name':k, 'value':v} for k,v in {
        'MAGIC_FILM_VM_POWER_CONTROL':power, 'DATABASE_URL':'test-db', 'GCS_BUCKET':'test-bucket',
        'MAGIC_FILM_PROVIDER':'codex', 'MAGIC_FILM_VM_PROJECT':'parabolio-prod',
        'MAGIC_FILM_VM_ZONE':'us-central1-a', 'MAGIC_FILM_VM_NAME':'jazz-film-private',
        'MAGIC_FILM_WAKE_CALLER':'jazz-film-wake@parabolio-prod.iam.gserviceaccount.com',
        'MAGIC_FILM_WAKE_AUDIENCE':API_URL,
    }.items()]
    return {'status':{'url':API_URL}, 'spec':{'template':{
        'metadata':{'annotations':{'run.googleapis.com/cloudsql-instances':'project:region:sql'}},
        'spec':{'serviceAccountName':'api@parabolio-prod.iam.gserviceaccount.com', 'containers':[{'env':env}]},
    }}}


class FilmDeploymentTest(unittest.TestCase):
    def test_configuration_is_read_only_without_apply_and_orders_wake_before_idle(self):
        for apply in (False, True):
            calls = []
            def run(args, **kwargs):
                calls.append(args)
                if '--format=json' in args:
                    if args[1:4] == ['run','services','describe']:
                        return subprocess.CompletedProcess(args, 0, json.dumps(api('0')), '')
                    return subprocess.CompletedProcess(args, 1, '', 'NOT_FOUND')
                return subprocess.CompletedProcess(args, 0, '', '')
            with patch.object(configure.subprocess, 'run', side_effect=run), patch.object(configure.urllib.request, 'urlopen', return_value=io.BytesIO(b'{"capability":"private-film-vm-power-v1","enabled":false}')), patch('sys.argv', ['configure']+(['--apply'] if apply else [])), contextlib.redirect_stdout(io.StringIO()):
                configure.main()
            mutations = [args for args in calls if '--format=json' not in args]
            self.assertEqual(bool(mutations), apply)
            if apply:
                role = next(c for c in mutations if c[1:4] == ['iam','roles','create'])
                self.assertIn('--permissions=compute.instances.get,compute.instances.start', role)
                binding = next(c for c in mutations if c[1:4] == ['compute','instances','add-iam-policy-binding'])
                self.assertEqual(binding[4], 'jazz-film-private')
                timer = next(c for c in mutations if c[1:4] == ['scheduler','jobs','create'])
                self.assertIn('--oidc-token-audience='+API_URL, timer)
                update = next(c for c in mutations if c[1:4] == ['run','services','update'])
                self.assertLess(mutations.index(timer), mutations.index(update))
                self.assertFalse(any('stop' in c or 'delete' in c for c in mutations))

    def test_installer_refuses_idle_stop_before_wake_capability_is_ready(self):
        calls = []
        def run(args, **kwargs):
            calls.append(args)
            return subprocess.CompletedProcess(args, 0, json.dumps(api()).encode(), b'')
        with patch.object(installer.subprocess, 'run', side_effect=run), patch.object(installer.urllib.request, 'urlopen', return_value=io.BytesIO(b'{"capability":"old-api"}')), patch('sys.argv', ['install','--binary','unused']):
            with self.assertRaisesRegex(SystemExit, 'not ready'):
                installer.main()
        self.assertEqual(len(calls), 1)  # No secret read, SSH, config write or stop.

    def test_installer_mirrors_power_flag_and_limits_root_shutdown_command(self):
        calls = []
        def run(args, **kwargs):
            calls.append((args, kwargs))
            data = api() if args[1:4] == ['run','services','describe'] else {
                'state':'ENABLED','schedule':'* * * * *', 'httpTarget':{
                    'uri':API_URL+'/internal/magic-film/reconcile', 'httpMethod':'POST',
                    'oidcToken':{'serviceAccountEmail':'jazz-film-wake@parabolio-prod.iam.gserviceaccount.com','audience':API_URL}}}
            return subprocess.CompletedProcess(args, 0, json.dumps(data).encode(), b'')
        with tempfile.TemporaryDirectory() as temp:
            binary = Path(temp)/'worker'; binary.write_bytes(b'test fixture')
            with patch.object(installer.subprocess, 'run', side_effect=run), patch.object(installer.urllib.request, 'urlopen', return_value=io.BytesIO(b'{"capability":"private-film-vm-power-v1","enabled":true}')), patch('sys.argv', ['install','--binary',str(binary)]), contextlib.redirect_stdout(io.StringIO()):
                installer.main()
        inputs = [kwargs['input'] for _, kwargs in calls if 'input' in kwargs]
        self.assertIn(b'magic-film ALL=(root) NOPASSWD: /usr/local/libexec/magic-film-idle-stop ""\n', inputs)
        config = next(value for value in inputs if b'DATABASE_URL=' in value)
        self.assertIn(b'MAGIC_FILM_VM_POWER_CONTROL="1"\n', config)
        self.assertNotIn(b'API_KEY', config)
        self.assertFalse(any('start' in arg for args, _ in calls for arg in args if arg.startswith('--command=')))


if __name__ == '__main__':
    unittest.main()
