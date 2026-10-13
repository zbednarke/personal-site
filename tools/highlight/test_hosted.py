import json
import tempfile
from pathlib import Path
import threading
import unittest
import urllib.request
import urllib.error
from http.server import ThreadingHTTPServer
from unittest.mock import patch
from hosted import handler, day_manifest

class FakeWorker:
    active=None
    jobs={}
    def create(self,payload):
        self.payload=payload
        return dict(id='job',phase='preparing',date=payload['date'])
    def create_analysis(self,payload):
        self.analysis_payload=payload
        return dict(id='analysis',phase='preparing',kind='day-analysis',date=payload['date'])

class HostedTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.worker=FakeWorker();self.worker.root=Path(self.temp.name);self.server=ThreadingHTTPServer(('127.0.0.1',0),handler(self.worker,'owner'))
        self.thread=threading.Thread(target=self.server.serve_forever,daemon=True);self.thread.start()
        self.url='http://127.0.0.1:'+str(self.server.server_port)
    def tearDown(self):self.server.shutdown();self.server.server_close();self.thread.join()
    def request(self,path='/jobs',data=None,headers=None):
        req=urllib.request.Request(self.url+path,data=json.dumps(data).encode() if data is not None else None,headers=headers or {})
        try:
            with urllib.request.urlopen(req) as r:return r.status,json.load(r)
        except urllib.error.HTTPError as e:return e.code,json.load(e)
    def test_identity_and_cross_origin_requests_are_rejected(self):
        self.assertEqual(self.request()[0],401)
        self.assertEqual(self.request(headers={'X-Jazz-User':'other'})[0],401)
        self.assertEqual(self.request(data={},headers={'X-Jazz-User':'owner','Origin':'https://evil.test'})[0],403)
    def test_only_date_and_duration_cross_the_browser_boundary(self):
        headers={'X-Jazz-User':'owner','Origin':'https://zachbednarke.com'}
        with patch('hosted.day_manifest',return_value={'date':'2026-09-28'}) as manifest:
            self.assertEqual(self.request(data={'date':'2026-09-28','targetSeconds':120,'recordings':[]},headers=headers)[0],400)
            manifest.assert_not_called()
            self.assertEqual(self.request(data={'date':'2026-09-28','targetSeconds':120},headers=headers)[0],202)
            manifest.assert_called_once_with('2026-09-28',120,'owner')
    def test_busy_service_does_not_start_another_download(self):
        self.worker.active='active'
        with patch('hosted.day_manifest') as manifest:
            code,_=self.request(data={'date':'2026-09-28'},headers={'X-Jazz-User':'owner','Origin':'https://zachbednarke.com'})
            self.assertEqual(code,409);manifest.assert_not_called()
    def test_day_analysis_fetches_owned_recordings_without_render_settings(self):
        headers={'X-Jazz-User':'owner','Origin':'https://zachbednarke.com'}
        with patch('hosted.day_manifest',return_value={'date':'2026-10-01','recordings':[]}) as manifest:
            code,job=self.request('/day-analyses',{'date':'2026-10-01'},headers)
            self.assertEqual(code,202);self.assertEqual(job['kind'],'day-analysis')
            manifest.assert_called_once_with('2026-10-01',60,'owner')
            self.assertEqual(self.worker.analysis_payload['date'],'2026-10-01')
    def test_manifest_fetches_owned_sources_and_preserves_rejections(self):
        day={'recordings':[{'id':'r','durationMs':10000,'mediaKind':'video','practiceBlockTitle':'Long tones'}],'candidates':[{'reviewStatus':'rejected'}]}
        with patch('hosted.backend',side_effect=[day,{'url':'audio'},{'url':'video'}]) as call:
            m=day_manifest('2026-09-28',120,'owner')
            self.assertTrue(m['analyzeAll']);self.assertEqual(m['recordings'][0]['videoUrl'],'video')
            self.assertEqual(m['candidates'],day['candidates'])
            self.assertTrue(all(c.args[1]=='owner' for c in call.call_args_list))
        with self.assertRaises(ValueError):day_manifest('2026-02-31',120,'owner')
        with self.assertRaises(ValueError):day_manifest('2026-09-28',True,'owner')

class PreviewLifecycleTests(unittest.TestCase):
    def test_preview_keeps_movie_and_edit_but_does_not_publish_or_retain_full_analysis(self):
        from hosted import HostedWorker
        from worker import write_json
        with tempfile.TemporaryDirectory() as td:
            root=Path(td);worker=HostedWorker(root,root/'films',root/'seed.json')
            jid='33333333-3333-4333-8333-333333333333';folder=root/jid;folder.mkdir()
            write_json(folder/'edit-snapshot.json',{'title':'Preview'})
            worker.active=jid;worker.cancelled=threading.Event()
            job={'id':jid,'date':'2026-09-30','editId':'edit','preview':True};worker.jobs[jid]=job
            def render(*args):
                for name in ('highlight.mp4','analysis.json','graphics-analysis.json','note-map.html','edit.json'):(folder/name).write_text('fixture')
                return {'title':'Preview','clips':[]},6000
            with patch('exact_render.render_exact',side_effect=render),patch('hosted.install') as publish:
                worker.run(job,{})
                publish.assert_not_called()
            self.assertEqual(job['phase'],'complete');self.assertIsNone(worker.active)
            self.assertTrue((folder/'highlight.mp4').exists());self.assertTrue((folder/'edit.json').exists())
            self.assertFalse((folder/'analysis.json').exists());self.assertFalse((folder/'graphics-analysis.json').exists())

if __name__=='__main__':unittest.main()
