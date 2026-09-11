import copy
import json
from pathlib import Path
import tempfile
import threading
import sys
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

from model import snapshot, validate_plan
from worker import Worker, handler


def fixture():
    return dict(date='2026-09-09', recordings=[dict(id='r1', durationMs=100000, audioUrl='https://storage.googleapis.com/bucket/a.wav')],
                candidates=[dict(id='manual', recordingId='r1', startMs=1000, endMs=20000, source='manual', reviewStatus='suggested'),
                            dict(id='liked', recordingId='r1', startMs=21000, endMs=40000, reviewStatus='kept'),
                            dict(id='rejected', recordingId='r1', startMs=42000, endMs=50000, source='manual', reviewStatus='rejected')])


class ValidationTests(unittest.TestCase):
    def setUp(self):
        self.manifest = snapshot(fixture())
        self.plan = dict(title='Practice', reviews=[dict(candidateId='manual', reason='Keep the selected phrase'), dict(candidateId='liked', reason='Contrast')],
                         clips=[dict(candidateId='manual', startMs=1000, endMs=20000, reason='Opening')])

    def test_excludes_rejected_manual_and_reviews_every_priority(self):
        self.assertEqual({c['id'] for c in self.manifest['candidates']}, {'manual', 'liked'})
        self.assertEqual(validate_plan(self.plan, self.manifest), 19000)
        self.plan['reviews'].pop()
        with self.assertRaises(ValueError):
            validate_plan(self.plan, self.manifest)

    def test_cuts_cannot_escape_candidate_or_repeat_source(self):
        self.plan['clips'][0]['startMs'] = 0
        with self.assertRaises(ValueError):
            validate_plan(self.plan, self.manifest)
        self.plan['clips'][0]['startMs'] = 1000
        self.plan['clips'].append(copy.deepcopy(self.plan['clips'][0]))
        with self.assertRaises(ValueError):
            validate_plan(self.plan, self.manifest)

    def test_invalid_urls_numbers_and_duplicate_ids(self):
        for url in ['http://127.0.0.1/x', 'file:///secret', 'https://storage.googleapis.com.evil.test/x', 'https://storage.googleapis.com@evil.test/x']:
            value = fixture()
            value['recordings'][0]['audioUrl'] = url
            with self.assertRaises(ValueError):
                snapshot(value)
        value = fixture()
        value['candidates'][0]['startMs'] = float('nan')
        with self.assertRaises(ValueError):
            snapshot(value)
        value = fixture()
        value['candidates'].append(value['candidates'][0])
        with self.assertRaises(ValueError):
            snapshot(value)

    def test_overlapping_candidates_still_cannot_repeat_footage(self):
        self.manifest['candidates'][1]['startMs'] = 10000
        self.plan['clips'].append(dict(candidateId='liked', startMs=10000, endMs=30000))
        with self.assertRaises(ValueError):
            validate_plan(self.plan, self.manifest)


class WorkerTests(unittest.TestCase):
    def test_cancel_stops_a_running_child(self):
        with tempfile.TemporaryDirectory() as temp:
            worker = Worker(temp)
            worker.cancelled = threading.Event()
            timer = threading.Timer(.2, worker.cancelled.set)
            timer.start()
            try:
                with self.assertRaises(InterruptedError):
                    worker.command([sys.executable, '-c', 'import time; time.sleep(30)'], temp)
            finally:
                timer.cancel()

    def test_only_one_agent_job_can_start(self):
        with tempfile.TemporaryDirectory() as temp:
            worker = Worker(temp)
            worker.active = 'already-running'
            with self.assertRaisesRegex(ValueError, 'already running'):
                worker.create(fixture())

    def test_restart_marks_unfinished_job_and_preserves_completed(self):
        with tempfile.TemporaryDirectory() as temp:
            p = Path(temp)/'job'
            p.mkdir()
            (p/'status.json').write_text(json.dumps(dict(id='job', phase='editing')))
            worker = Worker(temp)
            self.assertEqual(worker.jobs['job']['phase'], 'failed')

    def test_loopback_requires_token_origin_and_valid_host(self):
        with tempfile.TemporaryDirectory() as temp:
            worker = Worker(temp)
            server = ThreadingHTTPServer(('127.0.0.1', 0), handler(worker, 0))
            port = server.server_port
            server.RequestHandlerClass = handler(worker, port)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            url = f'http://127.0.0.1:{port}'
            try:
                with urllib.request.urlopen(url) as r:
                    self.assertIn(worker.token, r.read().decode())
                for headers in [{}, {'X-Highlight-Token':worker.token, 'Origin':'https://evil.test'}, {'X-Highlight-Token':worker.token, 'Host':'evil.test'}]:
                    request = urllib.request.Request(url+'/jobs', headers=headers, data=b'{}')
                    with self.assertRaises(urllib.error.HTTPError) as error:
                        urllib.request.urlopen(request)
                    self.assertEqual(error.exception.code, 403)
                req = urllib.request.Request(url+'/jobs', headers={'X-Highlight-Token':worker.token})
                with urllib.request.urlopen(req) as r:
                    self.assertEqual(json.load(r), [])
            finally:
                server.shutdown()
                server.server_close()


if __name__ == '__main__':
    unittest.main()

class TargetLengthTests(unittest.TestCase):
    def test_default_and_requested_length(self):
        self.assertEqual(snapshot(fixture())['targetSeconds'], 120)
        value = fixture(); value['targetSeconds'] = 300
        self.assertEqual(snapshot(value)['targetSeconds'], 300)
        for target in (True, 0, 601, '300', 120.5):
            value['targetSeconds'] = target
            with self.assertRaises(ValueError): snapshot(value)

    def test_five_minute_plan_allowed_only_for_longer_target(self):
        manifest = dict(targetSeconds=300, candidates=[dict(id='a', recordingId='r', startMs=0, endMs=400000, manual=True, liked=True)])
        plan = dict(title='Long film', reviews=[dict(candidateId='a',reason='User selection')], clips=[dict(candidateId='a',startMs=0,endMs=300000)])
        self.assertEqual(validate_plan(plan, manifest),300000)
        manifest['targetSeconds']=120
        with self.assertRaises(ValueError): validate_plan(plan,manifest)
        manifest['targetSeconds']=300;plan['clips'][0]['endMs']=316000
        with self.assertRaises(ValueError): validate_plan(plan,manifest)
