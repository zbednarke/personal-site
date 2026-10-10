import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import publish

class PublishTests(unittest.TestCase):
    def job(self):
        return dict(id='91838e50-ef16-4097-b5e8-12adb930ed0a', date='2026-09-10', phase='complete', durationMs=124000, project={'title': 'Practice'})

    def test_metadata_keeps_practice_date_and_job_identity(self):
        film = publish.metadata(self.job())
        self.assertEqual(film['date'], '2026-09-10')
        self.assertEqual(film['duration'], '2:04')
        self.assertIn(self.job()['id'], film['video'])
        bad = self.job(); bad['id'] = '../escape'
        with self.assertRaises(ValueError): publish.metadata(bad)

    def test_in_progress_and_published_jobs_are_not_uploaded(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); folder = root / self.job()['id']; folder.mkdir()
            job = self.job(); job['phase'] = 'rendering'
            publish.save(folder / 'status.json', job)
            with patch('publish.publish') as upload:
                publish.scan(root); upload.assert_not_called()
                job['phase'] = 'complete'; publish.save(folder / 'status.json', job)
                publish.save(folder / 'publication.json', {'phase': 'published'})
                publish.scan(root); upload.assert_not_called()

    def test_failure_retries_later_without_changing_worker_status(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); folder = root / self.job()['id']; folder.mkdir()
            publish.save(folder / 'status.json', self.job())
            before = (folder / 'status.json').read_bytes()
            with patch('publish.publish', side_effect=RuntimeError('offline')) as upload:
                publish.scan(root); publish.scan(root)
                self.assertEqual(upload.call_count, 1)
                self.assertEqual(json.loads((folder / 'publication.json').read_text())['phase'], 'failed')
                self.assertEqual((folder / 'status.json').read_bytes(), before)

if __name__ == '__main__': unittest.main()
