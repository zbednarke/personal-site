"""Tests for deploy/install-commonplace-route.py (the Caddyfile rewrite only)."""
import importlib.util
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('install_commonplace_route', ROOT / 'deploy' / 'install-commonplace-route.py')
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

LIVE_LIKE = """zachbednarke.com {
    @jazz_private {
        path /jazz /jazz/* /assets/jazz/* /trumpets /trumpets/* /assets/trumpets/*
        not path /jazz/share/*
    }
    route {
        route @jazz_private {
            reverse_proxy 127.0.0.1:8768 {
                method GET
            }
        }
        handle_path /jazz/api/* {
            reverse_proxy {$JAZZ_API_URL}
        }
        # Trumpet notes inherit the private gateway.
        handle_path /trumpets/api/* {
            reverse_proxy {$JAZZ_API_URL}
        }
        file_server
    }
}
"""


class InstallRouteTest(unittest.TestCase):
    def test_adds_route_and_private_paths_once(self):
        once = installer.updated_caddyfile(LIVE_LIKE)
        self.assertIn('        handle_path /commonplace/api/* {', once)
        self.assertIn('header_up X-Jazz-User {http.request.header.X-Jazz-User}', once)
        self.assertIn('header_up X-Jazz-Gateway-Key {$JAZZ_GATEWAY_KEY}', once)
        self.assertIn('/assets/trumpets/* /commonplace /commonplace/* /assets/commonplace/*', once)
        # Placed before the trumpets block and its comment, inside the route.
        self.assertLess(once.index('handle_path /commonplace/api/*'), once.index('# Trumpet notes'))
        self.assertLess(once.index('handle_path /jazz/api/*'), once.index('handle_path /commonplace/api/*'))
        self.assertEqual(installer.updated_caddyfile(once), once, 'idempotent')
        self.assertEqual(once.count('{'), once.count('}'))

    def test_example_caddyfile_is_already_complete(self):
        example = (ROOT / 'deploy' / 'Caddyfile.jazz.example').read_text()
        self.assertEqual(installer.updated_caddyfile(example), example)

    def test_refuses_without_sign_in(self):
        with self.assertRaises(installer.InstallError):
            installer.updated_caddyfile('zachbednarke.com {\n    file_server\n}\n')

    def test_falls_back_to_the_jazz_api_block(self):
        text = re.sub(r'(?ms)^ +# Trumpet.*?\n +handle_path /trumpets/api/\* \{.*?\n +\}\n', '', LIVE_LIKE)
        self.assertNotIn('/trumpets/api', text)
        out = installer.updated_caddyfile(text)
        self.assertLess(out.index('handle_path /commonplace/api/*'), out.index('handle_path /jazz/api/*'))


if __name__ == '__main__':
    unittest.main()
