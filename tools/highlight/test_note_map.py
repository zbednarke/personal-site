import json
from pathlib import Path
import re
import tempfile
import unittest

from note_map import regenerate, render_note_map

TEMPLATE = '<script id="data" type="application/json">__ANALYSIS__</script><script id="metadata" type="application/json">__METADATA__</script>'
STEM = '2026-09-28-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'


def payload(html, identifier):
    return json.loads(re.search(r'<script id="' + identifier + r'"[^>]*>(.*?)</script>', html, re.S)[1])


class NoteMapRenderingTests(unittest.TestCase):
    def test_safe_json_preserves_labels_and_omits_frames_without_mutation(self):
        title = '</script><img src=x onerror=alert(1)> __METADATA__ & ♭'
        maps = {'a': {'title': title, 'notes': [], 'frames': [{'midi': 60}]}}
        html = render_note_map(maps, '2026-09-28', TEMPLATE)
        self.assertEqual(html.count('</script>'), 2)
        self.assertNotIn('<img', html)
        self.assertEqual(payload(html, 'data'), {'a': {'title': title, 'notes': []}})
        self.assertEqual(payload(html, 'metadata'), {'date': '2026-09-28'})
        self.assertIn('frames', maps['a'])

    def test_empty_maps_and_optional_date(self):
        html = render_note_map({}, template=TEMPLATE)
        self.assertEqual(payload(html, 'data'), {})
        self.assertEqual(payload(html, 'metadata'), {'date': None})

    def test_date_must_be_real_and_canonical(self):
        for value in ('2026-02-30', '26-09-28', '2026-9-28', '</script>', 42):
            with self.subTest(value=value), self.assertRaises(ValueError):
                render_note_map({}, value, TEMPLATE)

    def test_regeneration_changes_only_existing_known_map_and_keeps_backup(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            analysis = root / (STEM + '.analysis.json')
            analysis.write_text(json.dumps({'a': {'notes': [], 'frames': []}}))
            page = root / (STEM + '.notes.html')
            page.write_text('original page')
            page.chmod(0o640)
            video = root / (STEM + '.mp4')
            video.write_bytes(b'unchanged video')
            unrelated = root / 'unrelated.analysis.json'
            unrelated.write_text('{}')
            unrelated_page = root / 'unrelated.notes.html'
            unrelated_page.write_text('leave alone')
            invalid = root / (STEM.replace('2026-09-28', '2026-02-30') + '.analysis.json')
            invalid.write_text('{}')
            invalid_page = invalid.with_name(invalid.name.replace('.analysis.json', '.notes.html'))
            invalid_page.write_text('invalid date')
            before = analysis.read_bytes()
            self.assertEqual(regenerate(root, TEMPLATE), [page])
            self.assertEqual(payload(page.read_text(), 'metadata')['date'], '2026-09-28')
            self.assertEqual(page.stat().st_mode & 0o777, 0o640)
            self.assertEqual(page.with_name(page.name + '.bak').read_text(), 'original page')
            regenerate(root, TEMPLATE)
            self.assertEqual(page.with_name(page.name + '.bak').read_text(), 'original page')
            self.assertEqual(analysis.read_bytes(), before)
            self.assertEqual(video.read_bytes(), b'unchanged video')
            self.assertEqual(unrelated_page.read_text(), 'leave alone')
            self.assertEqual(invalid_page.read_text(), 'invalid date')
            self.assertFalse(list(root.glob('*.tmp')))

    def test_missing_page_is_not_created_and_symlink_is_not_replaced(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            analysis = root / (STEM + '.analysis.json')
            analysis.write_text('{}')
            self.assertEqual(regenerate(root, TEMPLATE), [])
            original = root / 'other.html'
            original.write_text('protected')
            page = root / (STEM + '.notes.html')
            page.symlink_to(original)
            self.assertEqual(regenerate(root, TEMPLATE), [])
            self.assertEqual(original.read_text(), 'protected')


if __name__ == '__main__':
    unittest.main()
