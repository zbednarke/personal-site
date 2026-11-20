import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('post_reboot', ROOT / 'deploy' / 'check-post-reboot.py')
reboot = importlib.util.module_from_spec(spec); spec.loader.exec_module(reboot)


class PostRebootTests(unittest.TestCase):
    def receipt(self):
        return {'boot_id': 'new-boot', 'reboot_required': False,
                'services_active': {'caddy': True, 'jazz-auth': True, 'jazz-highlights': True},
                'caddy_build': {'go_version': 'go1.27.3'}, 'jazz_auth': {'go_version': 'go1.27.3'},
                'highlights_runtime': {'pillow': '13.0.0'}, 'caddy_integrity': {'matches_managed_build': True}}

    def test_actual_new_boot_and_patches_are_required(self):
        self.assertEqual(reboot.validate({'boot_id': 'old-boot'}, self.receipt(), 'request_reboot')['boot_id'], 'new-boot')

    def test_same_boot_is_not_a_successful_reboot(self):
        with self.assertRaises(ValueError): reboot.validate({'boot_id': 'new-boot'}, self.receipt(), 'request_reboot')

    def test_unknown_boot_is_not_assumed_changed(self):
        with self.assertRaises(ValueError): reboot.validate({}, self.receipt(), 'request_reboot')

    def test_missing_service_or_unpatched_proxy_is_refused(self):
        for key in ('services_active', 'caddy_build', 'caddy_integrity', 'highlights_runtime'):
            report = self.receipt(); report.pop(key)
            with self.assertRaises(ValueError): reboot.validate({'boot_id': 'old-boot'}, report, 'request_reboot')

    def test_authorization_decision_cannot_be_silently_skipped(self):
        with self.assertRaises(ValueError): reboot.validate({'boot_id': 'old-boot'}, self.receipt(), 'unknown')

    def test_previous_fulfillment_does_not_require_a_second_boot(self):
        self.assertEqual(reboot.validate({'boot_id': 'new-boot'}, self.receipt(), 'already_completed')['boot_id'], 'new-boot')


if __name__ == '__main__': unittest.main()
