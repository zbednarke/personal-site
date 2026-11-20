"""Tests for the /686 Caddy route rewrite."""
import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('install_686_route', ROOT / 'deploy' / 'install-686-route.py')
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)

LIVE_LIKE = """zachbednarke.com {
    @not_jazz {
        not path /jazz /jazz/*
    }
    @jazz_private {
        path /jazz /jazz/* /commonplace /commonplace/*
    }
    route {
        handle_path /jazz/api/* {
            reverse_proxy {$JAZZ_API_URL}
        }
        file_server
    }
}
"""


class Install686RouteTest(unittest.TestCase):
    def test_adds_route_and_protected_paths_once(self):
        once = installer.updated_caddyfile(LIVE_LIKE)
        self.assertIn('handle_path /686/api/*', once)
        self.assertIn('/686 /686/* /assets/686/*', once)
        self.assertEqual(installer.updated_caddyfile(once), once)
        self.assertEqual(once.count('{'), once.count('}'))

    def test_example_is_complete(self):
        example = (ROOT / 'deploy' / 'Caddyfile.jazz.example').read_text()
        self.assertEqual(installer.updated_caddyfile(example), example)

    def test_upgrades_existing_proxy(self):
        old = installer.updated_caddyfile(LIVE_LIKE).replace('            flush_interval -1\n', '')
        upgraded = installer.updated_caddyfile(old)
        self.assertIn('flush_interval -1', upgraded)
        self.assertEqual(installer.updated_caddyfile(upgraded), upgraded)

    def test_requires_private_matcher(self):
        with self.assertRaises(installer.InstallError):
            installer.updated_caddyfile('zachbednarke.com {\n file_server\n}\n')


if __name__ == '__main__':
    unittest.main()
