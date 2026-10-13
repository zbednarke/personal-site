from copy import deepcopy
import json
from pathlib import Path
import tempfile
import threading
import unittest
import urllib.request
import urllib.error
from unittest.mock import patch
from http.server import ThreadingHTTPServer
from hosted import handler
from film_edits import DEFAULT_APPEARANCE
from test_film_edits import FID, content

class Worker:
    def __init__(self,root):self.root=root;self.films=root/'films';self.films.mkdir();self.active=None;self.jobs={};self.cancelled=threading.Event()
    def create_exact(self,payload,edit,key,preview):
        from model import snapshot
        snapshot(payload)
        self.snapshot=deepcopy(edit);self.payload=payload
        j=dict(id='22222222-2222-4222-8222-222222222222',editId=edit['id'],editRevision=edit['revision'],requestKey=key,preview=preview,phase='preparing');self.jobs[j['id']]=j;self.active=j['id'];return j

class EditAPITests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name);self.worker=Worker(self.root)
        (self.worker.films/'index.json').write_text(json.dumps([dict(id=FID,date='2026-09-30')]))
        (self.worker.films/(FID+'.edit.json')).write_text(json.dumps(dict(owner='owner',edit=content(),sources={'r':{'durationMs':10000,'title':'Study'}})))
        self.day=dict(recordings=[dict(id='r',durationMs=10000,mediaKind='video')],candidates=[])
        def backend(path,*args):return self.day if path.startswith('/studio') else dict(url='https://storage.googleapis.com/private')
        self.patch=patch('hosted.backend',side_effect=backend);self.patch.start()
        self.server=ThreadingHTTPServer(('127.0.0.1',0),handler(self.worker,'owner'));self.thread=threading.Thread(target=self.server.serve_forever,daemon=True);self.thread.start()
    def tearDown(self):self.server.shutdown();self.server.server_close();self.thread.join();self.patch.stop();self.tmp.cleanup()
    def req(self,path,body=None,method=None,origin='https://zachbednarke.com',user='owner',extra=None):
        h={'X-Jazz-User':user,'Origin':origin};h.update(extra or {})
        req=urllib.request.Request('http://127.0.0.1:'+str(self.server.server_port)+path,data=json.dumps(body).encode() if body is not None else None,method=method,headers=h)
        try:
            with urllib.request.urlopen(req) as r:return r.status,json.load(r)
        except urllib.error.HTTPError as e:return e.code,json.load(e)
    def draft(self):return self.req('/edits',{'sourceFilmId':FID})[1]
    def test_exact_request_is_idempotent_immutable_and_never_selects(self):
        e=self.draft();path='/edits/'+e['id'];request={'revision':1,'idempotencyKey':'once'}
        with patch('hosted.select_plan') as selector:
            code,j=self.req(path+'/renders',request);self.assertEqual(code,202)
            self.assertEqual(self.req(path+'/renders',request)[1]['id'],j['id']);self.assertEqual(len(self.worker.jobs),1);selector.assert_not_called()
        c=content();c['title']='New';self.assertEqual(self.req(path,{'revision':1,'content':c},'PATCH')[0],200)
        self.assertEqual(self.worker.snapshot['title'],'Film');self.assertEqual(self.req(path,{'revision':1,'content':c},'PATCH')[0],409)
        self.assertEqual(self.req(path+'/previews',request)[0],409)
    def test_authorization_busy_and_deleted_source(self):
        self.assertEqual(self.req('/edits',user='other')[0],401)
        self.assertEqual(self.req('/edits',{'sourceFilmId':FID},origin='https://evil.test')[0],403)
        e=self.draft();path='/edits/'+e['id']
        self.assertEqual(self.req(path,{'revision':1,'content':content()},'PATCH',origin='https://evil.test')[0],403)
        self.worker.active='busy';self.assertEqual(self.req(path+'/renders',{'revision':1,'idempotencyKey':'one'})[0],409)
        self.worker.active=None;self.day['recordings']=[];self.assertEqual(self.req(path+'/renders',{'revision':1,'idempotencyKey':'two'})[0],400)
        self.assertFalse(self.worker.jobs)
    def test_rejected_source_and_bad_fields_are_rejected(self):
        e=self.draft();self.day['candidates']=[dict(recordingId='r',reviewStatus='rejected',startMs=1000,endMs=2000)]
        self.assertEqual(self.req('/edits/'+e['id']+'/renders',{'revision':1,'idempotencyKey':'r'})[0],400)
        self.assertEqual(self.req('/edits',{'sourceFilmId':FID,'shell':'bad'})[0],400)

    def test_day_draft_can_render_without_a_parent_film(self):
        code,e=self.req('/edits',{'date':'2026-10-01','content':content()})
        self.assertEqual(code,201);self.assertIsNone(e['sourceFilmId']);self.assertEqual(e['date'],'2026-10-01')
        code,_=self.req('/edits/'+e['id']+'/renders',{'revision':1,'idempotencyKey':'day-cut'})
        self.assertEqual(code,202);self.assertIsNone(self.worker.snapshot['sourceFilmId'])

    def test_preview_can_end_with_less_than_half_second_of_next_passage(self):
        e=self.draft();c=content();c['clips'][0]['endMs']=5800
        c['clips'].append(dict(id='c2',recordingId='r',startMs=7000,endMs=8000,originalPassage=10,title='Study'))
        code,saved=self.req('/edits/'+e['id'],{'revision':1,'content':c},'PATCH');self.assertEqual(code,200)
        code,_=self.req('/edits/'+e['id']+'/previews',{'revision':2,'idempotencyKey':'short-tail'})
        self.assertEqual(code,202)
        self.assertEqual(self.worker.payload['candidates'][1]['endMs'],8000)
