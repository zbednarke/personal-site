"""Tests for deploy/install-workbench-route.py (the Caddyfile rewrite only)."""
import importlib.util
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('install_workbench_route', ROOT / 'deploy' / 'install-workbench-route.py')
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

LIVE_LIKE = """zachbednarke.com {
    @not_jazz {
        not path /jazz /jazz/* /assets/jazz/*
    }
    header @not_jazz Permissions-Policy "camera=(), microphone=(), geolocation=()"
    @jazz_private {
        path /jazz /jazz/* /assets/jazz/* /trumpets /trumpets/* /assets/trumpets/* /commonplace /commonplace/* /assets/commonplace/*
        not path /jazz/share/*
    }
    route {
        route @jazz_private {
            reverse_proxy 127.0.0.1:8768 {
                method GET
            }
        }
        # Caddy strips /jazz/api and adds the gateway secret.
        handle_path /jazz/api/* {
            reverse_proxy {$JAZZ_API_URL}
        }
        handle_path /trumpets/api/* {
            reverse_proxy {$JAZZ_API_URL}
        }
        file_server
    }
}
"""


class InstallRouteTest(unittest.TestCase):
    def test_adds_route_and_paths_once(self):
        once = installer.updated_caddyfile(LIVE_LIKE)
        self.assertIn('        handle_path /workbench/api/* {', once)
        self.assertIn('reverse_proxy {$WORKBENCH_API_URL} {', once)
        self.assertIn('header_up X-Jazz-User {http.request.header.X-Jazz-User}', once)
        self.assertIn('header_up X-Jazz-Gateway-Key {$JAZZ_GATEWAY_KEY}', once)
        self.assertIn('flush_interval -1', once, 'event streams are not buffered')
        self.assertIn('/assets/commonplace/* /workbench /workbench/*', once)
        self.assertIn('not path /jazz /jazz/* /assets/jazz/* /trumpets /trumpets/* /commonplace /commonplace/*', once)
        self.assertNotIn('/assets/workbench', once.split('route {')[0], 'the sheet code stays public for service workers')
        # Placed before the /jazz/api block and its comment, inside the route.
        self.assertLess(once.index('handle_path /workbench/api/*'), once.index('# Caddy strips /jazz/api'))
        self.assertLess(once.index('route @jazz_private'), once.index('handle_path /workbench/api/*'))
        self.assertEqual(installer.updated_caddyfile(once), once, 'idempotent')
        self.assertEqual(once.count('{'), once.count('}'))

    def test_example_caddyfile_is_already_complete(self):
        example = (ROOT / 'deploy' / 'Caddyfile.jazz.example').read_text()
        self.assertEqual(installer.updated_caddyfile(example), example)

    def test_refuses_without_sign_in(self):
        with self.assertRaises(installer.InstallError):
            installer.updated_caddyfile('zachbednarke.com {\n    file_server\n}\n')

    def test_without_a_site_wide_microphone_policy(self):
        text = re.sub(r'(?ms)^ +@not_jazz \{.*?\n +\}\n +header @not_jazz[^\n]*\n', '', LIVE_LIKE)
        self.assertNotIn('@not_jazz', text)
        out = installer.updated_caddyfile(text)
        self.assertIn('handle_path /workbench/api/*', out)

    def test_falls_back_to_the_trumpets_api_block(self):
        text = re.sub(r'(?ms)^ +# Caddy strips.*?\n +handle_path /jazz/api/\* \{.*?\n +\}\n', '', LIVE_LIKE)
        self.assertNotIn('/jazz/api', text)
        out = installer.updated_caddyfile(text)
        self.assertLess(out.index('handle_path /workbench/api/*'), out.index('handle_path /trumpets/api/*'))

    def test_reads_the_caddy_environment(self):
        env = installer.read_env('# comment\nJAZZ_API_URL="https://api.example"\nWORKBENCH_API_URL=https://wb.example\n')
        self.assertEqual(env['WORKBENCH_API_URL'], 'https://wb.example')
        self.assertEqual(env['JAZZ_API_URL'], 'https://api.example')


if __name__ == '__main__':
    unittest.main()
