"""Authenticated-site film service. Bind to loopback behind the Jazz auth proxy.

Browser requests contain a date and duration only. Source ownership, candidates
and signed URLs are resolved server-side using the verified Jazz identity.
"""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import threading
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse

from worker import Worker, write_json
from audio_map import select_plan
from model import validate_plan, snapshot
from copy import deepcopy
from uuid import uuid4
import time
from publish import metadata, checksum
from publish_remote import install


class HostedWorker(Worker):
    def __init__(self, root, films, seed):
        super().__init__(root)
        self.films, self.seed = Path(films), Path(seed)

    def create_exact(self, payload, edit, key, preview=False):
        manifest = snapshot(payload)
        with self.lock:
            if self.active:
                from film_edits import EditError
                raise EditError('A film is already running',409)
            jid=str(uuid4());folder=self.root/jid;folder.mkdir()
            job=dict(id=jid,date=edit['date'],createdAt=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),phase='preparing',message='Preparing saved edit',editId=edit['id'],editRevision=edit['revision'],requestKey=key,preview=preview)
            self.jobs[jid]=job;self.active=jid;self.cancelled=threading.Event()
            # Persist the immutable snapshot before returning; never store signed URLs.
            write_json(folder/'edit-snapshot.json',edit)
            self.status(job,'preparing','Preparing your saved passages')
            threading.Thread(target=self.run,args=(job,manifest),daemon=True).start()
            return deepcopy(job)

    def run(self, job, manifest):
        folder = self.root / job['id']
        try:
            if job.get('editId'):
                from exact_render import render_exact
                edit=json.loads((folder/'edit-snapshot.json').read_text())
                project,duration=render_exact(self,edit,manifest,folder,job.get('preview',False))
                plan=dict(summary='Your saved passages, in your chosen order',reviews=[])
                if job.get('preview'):
                    self.status(job,'complete','Intro preview ready',previewUrl='/jazz/highlight-api/jobs/'+job['id']+'/preview.mp4',durationMs=duration)
                    return
            else:
                sources, evidence = self.prepare(manifest, folder)
                if job.get('kind') == 'day-analysis':
                    maps=json.loads((folder/'analysis.json').read_text())
                    note_count=sum(len(item.get('notes',[])) for item in maps.values())
                    self.status(job,'complete',f'Note analysis ready for {len(maps)} recordings',recordingCount=len(maps),noteCount=note_count)
                    return
                self.status(job, 'editing', 'Selecting clear phrases across your practice day')
                plan = select_plan(manifest)
                validate_plan(plan, manifest)
                write_json(folder/'plan.json', plan)
                self.status(job, 'rendering', 'Rendering and checking your film on the server')
                project, duration = self.render(plan, manifest, sources, folder)
            self.status(job, 'publishing', 'Saving your film and note map', project=project,
                        summary=plan['summary'], reviews=plan['reviews'], durationMs=duration)
            self.command(['ffmpeg','-v','error','-y','-ss','1','-i',str(folder/'highlight.mp4'),
                          '-frames:v','1','-q:v','3',str(folder/'poster.jpg')], folder)
            if self.cancelled.is_set(): raise InterruptedError('Cancelled')
            film = metadata(job)
            film['noteMap'] = '/jazz/films/'+film['id']+'.notes.html'
            film['analysis'] = '/jazz/films/'+film['id']+'.analysis.json'
            names = dict(video='highlight.mp4',poster='poster.jpg',noteMap='note-map.html',analysis='analysis.json')
            if job.get('editId'):
                film.update(edit='/jazz/films/'+film['id']+'.edit.json',editId=edit['id'],editRevision=edit['revision'])
                if edit.get('sourceFilmId'): film['parentFilmId']=edit['sourceFilmId']
                names['edit']='edit.json'
            write_json(folder/'publish-manifest.json',dict(film=film,hashes={k:checksum(folder/v) for k,v in names.items()}))
            install(folder, self.films, self.seed)
            self.status(job, 'complete', 'Your film and note map are ready', film=film)
        except InterruptedError:
            self.status(job, 'cancelled', 'Film cancelled; original recordings are unchanged')
        except Exception:
            # Private URLs and backend errors must never be returned to browsers.
            stage=job.get('phase','preparing')
            self.status(job, 'failed', f'The server could not finish the {stage} step. Your saved edit and recordings are unchanged; please retry.',failedStage=stage)
            import traceback
            traceback.print_exc()
        finally:
            shutil.rmtree(folder/'media', ignore_errors=True)
            for pattern in ('cut-*.mp4','cut-*.mkv','cut-*.mov','base.mp4','graphics-analysis.json'):
                for cut in folder.glob(pattern):cut.unlink(missing_ok=True)
            if job.get('preview'):
                for name in ('analysis.json','note-map.html'):(folder/name).unlink(missing_ok=True)
            cache = sorted((self.root/'analysis-cache').glob('*.json'), key=lambda p:p.stat().st_mtime, reverse=True)
            size = 0
            for path in cache:
                size += path.stat().st_size
                if size > 500*1024**2: path.unlink(missing_ok=True)
            with self.lock: self.active = None


def backend(path, user, method='GET'):
    req = urllib.request.Request(os.environ['JAZZ_API_URL'].rstrip('/')+'/v1'+path,
        method=method, data=b'{}' if method=='POST' else None,
        headers={'X-Jazz-User':user,'X-Jazz-Gateway-Key':os.environ['JAZZ_GATEWAY_KEY'],'Content-Type':'application/json'})
    with urllib.request.urlopen(req,timeout=60) as response: return json.load(response)


def day_manifest(date, seconds, user):
    from datetime import date as Date
    if not isinstance(date,str) or not re.fullmatch(r'\d{4}-\d{2}-\d{2}',date): raise ValueError('Choose a practice date')
    Date.fromisoformat(date)
    if type(seconds) is not int or seconds not in (60,120,180,300,600): raise ValueError('Choose a supported film length')
    day = backend('/studio/days/'+date,user)
    recordings=[]
    for r in day['recordings']:
        rid=r['id']
        audio=backend('/recordings/'+rid+'/playback-url?asset=audio',user,'POST')
        video=backend('/recordings/'+rid+'/playback-url?asset=video',user,'POST') if r.get('mediaKind')=='video' else None
        title=r.get('practiceBlockTitle') or 'Practice'
        recordings.append(dict(id=rid,durationMs=r['durationMs'],title=title,section=title,
            audioUrl=audio['url'],videoUrl=video['url'] if video else None,
            sizeBytes=r.get('sizeBytes',0),videoSizeBytes=r.get('videoSizeBytes',0)))
    if not recordings: raise ValueError('No uploaded recordings for this day yet')
    return dict(date=date,targetSeconds=seconds,analyzeAll=True,recordings=recordings,candidates=day['candidates'])


def handler(worker, user):
    creation_lock=threading.Lock()
    from library import Library
    library = Library(worker.root / "library")
    from edit_api import EditAPI
    edits = EditAPI(worker,user,backend)
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*args): pass
        def send(self,data,status=200):
            body=json.dumps(data).encode();self.send_response(status)
            self.send_header('Content-Type','application/json');self.send_header('Cache-Control','private, no-store')
            self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
        def authorized(self, mutation=False):
            # Caddy authenticates and overwrites this header; service has no public listener.
            if self.headers.get('X-Jazz-User') != user: return False
            if mutation and (self.headers.get('Origin') != 'https://zachbednarke.com' or self.headers.get('Sec-Fetch-Site') not in (None,'same-origin')): return False
            return True
        def do_GET(self):
            if not self.authorized():return self.send({'error':'Authentication required'},401)
            if urlparse(self.path).path.startswith('/library'):return library.get(self)
            if edits.request(self,'GET'):return
            if urlparse(self.path).path != '/jobs':return self.send({'error':'Not found'},404)
            return self.send(sorted(worker.jobs.values(),key=lambda j:j.get('createdAt',''),reverse=True))
        def do_PATCH(self):
            if not self.authorized(True):return self.send({'error':'Same-origin authenticated request required'},403)
            try:
                size=int(self.headers.get('Content-Length','0'))
                if not 0<size<=65536:raise ValueError()
                data=json.loads(self.rfile.read(size))
                if not isinstance(data,dict):raise ValueError()
                if urlparse(self.path).path.startswith('/library/'):return library.update(self,data)
                if edits.request(self,'PATCH',data):return
                self.send({'error':'Not found'},404)
            except (ValueError,TypeError):self.send({'error':'Invalid request'},400)

        def do_POST(self):
            if not self.authorized(True):return self.send({'error':'Same-origin authenticated request required'},403)
            if urlparse(self.path).path.startswith('/library'):return library.upload(self)
            try:
                size=int(self.headers.get('Content-Length','0'))
                if not 0<size<=65536:raise ValueError('Invalid request size')
                data=json.loads(self.rfile.read(size))
                if not isinstance(data,dict):raise ValueError('Invalid request')
                if self.path.startswith('/edits'):
                    if not creation_lock.acquire(blocking=False):return self.send({'error':'A film request is being prepared; retry shortly'},409)
                    try:
                        if edits.request(self,'POST',data):return
                    finally:creation_lock.release()
                if self.path=='/cancel':
                    if worker.active==data.get('id'):worker.cancelled.set()
                    return self.send({'ok':True})
                if self.path=='/day-analyses':
                    if set(data)-{'date'}:raise ValueError('Only a practice date is accepted')
                    if not creation_lock.acquire(blocking=False):return self.send({'error':'An analysis request is being prepared'},409)
                    try:
                        payload=day_manifest(data.get('date'),60,user)
                        return self.send(worker.create_analysis(payload),202)
                    finally:creation_lock.release()
                if self.path!='/jobs':return self.send({'error':'Not found'},404)
                if set(data)-{'date','targetSeconds'}:raise ValueError('Only a practice date and film length are accepted')
                if not creation_lock.acquire(blocking=False):return self.send({'error':'A film is being prepared'},409)
                try:
                    if worker.active:return self.send({'error':'A film is already running. Wait or cancel it first.'},409)
                    payload=day_manifest(data.get('date'),data.get('targetSeconds',120),user)
                    return self.send(worker.create(payload),202)
                finally:creation_lock.release()
            except (ValueError,TypeError,KeyError):self.send({'error':'Invalid date, length, or no eligible uploaded recordings'},400)
            except Exception:self.send({'error':'Could not prepare the film from cloud storage. Please retry.'},502)
    return Handler


if __name__=='__main__':
    worker=HostedWorker(os.environ.get('JAZZ_HIGHLIGHT_DATA','/srv/jazz-highlights'),
        '/srv/zachbednarke.com/films','/srv/zachbednarke.com/current/assets/jazz/finished-films.json')
    ThreadingHTTPServer(('127.0.0.1',8769),handler(worker,os.environ['JAZZ_HIGHLIGHT_USER'])).serve_forever()
