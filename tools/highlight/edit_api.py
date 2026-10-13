"""Small routing adapter for persisted film edits; hosted auth remains authoritative."""
import json
import re
from urllib.parse import urlparse
from film_edits import EditError, EditStore, validate

class EditAPI:
    def __init__(self,worker,owner,backend):
        self.worker,self.owner,self.backend=worker,owner,backend
        self.store=EditStore(worker.root,getattr(worker,'films',worker.root/'films'),owner)

    def request(self,h,method,data=None):
        path=urlparse(h.path).path
        if not (path.startswith('/edits') or path.startswith('/films/') or path.startswith('/jobs/')):return False
        try:
            if method=='GET' and path=='/edits':h.send(self.store.list());return True
            m=re.fullmatch(r'/films/([^/]+)/edit',path)
            if m and method=='GET':h.send(self.store.film(m[1]));return True
            if path=='/edits' and method=='POST':
                fields=set(data)
                if fields <= {'sourceFilmId','content'} and 'sourceFilmId' in fields:
                    h.send(self.store.create(data['sourceFilmId'],data.get('content')),201);return True
                if fields == {'date','content'}:
                    day=self.backend('/studio/days/'+data['date'],self.owner)
                    sources={r['id']:dict(durationMs=r['durationMs'],title=r.get('practiceBlockTitle') or 'Practice') for r in day['recordings']}
                    h.send(self.store.create_day(data['date'],data['content'],sources),201);return True
                raise EditError('Invalid draft fields')
            m=re.fullmatch(r'/edits/([^/]+)(?:/(renders|previews))?',path)
            if m:
                eid,action=m.groups()
                if method=='GET' and not action:h.send(self.store.get(eid));return True
                if method=='PATCH' and not action:
                    if set(data)!={'revision','content'}:raise EditError('Expected revision and content')
                    h.send(self.store.save(eid,data['revision'],data['content']));return True
                if method=='POST' and action:
                    if set(data)!={'revision','idempotencyKey'}:raise EditError('Expected revision and idempotency key')
                    key=data['idempotencyKey']
                    if not isinstance(key,str) or not re.fullmatch(r'[a-zA-Z0-9-]{1,80}',key):raise EditError('Invalid idempotency key')
                    # A retry returns the same immutable job even if the draft changed.
                    existing=next((j for j in self.worker.jobs.values() if j.get('editId')==eid and j.get('requestKey')==key),None)
                    if existing:
                        if existing.get('editRevision')!=data['revision'] or existing.get('preview')!=(action=='previews'):raise EditError('Idempotency key belongs to another request',409)
                        h.send(existing,202);return True
                    edit=self.store.get(eid)
                    if type(data['revision']) is not int or edit['revision']!=data['revision']:raise EditError('Draft changed; save and retry',409)
                    if self.worker.active:raise EditError('A film is already running. Wait or cancel it first',409)
                    day=self.backend('/studio/days/'+edit['date'],self.owner)
                    owned={r['id']:dict(durationMs=r['durationMs'],title=r.get('practiceBlockTitle') or 'Practice') for r in day['recordings']}
                    rejected=[c for c in day['candidates'] if c.get('reviewStatus')=='rejected']
                    validate({k:edit[k] for k in ('title','clips','appearance')},owned,rejected,render=True)
                    render_clips=edit['clips']
                    if action=='previews':
                        remaining=6000;render_clips=[]
                        for c in edit['clips']:
                            if remaining<=0:break
                            length=min(remaining,c['endMs']-c['startMs'])
                            render_clips.append(c);remaining-=length
                    needed={c['recordingId'] for c in render_clips};recordings=[]
                    for r in day['recordings']:
                        if r['id'] not in needed:continue
                        rid=r['id'];audio=self.backend('/recordings/'+rid+'/playback-url?asset=audio',self.owner,'POST');video=self.backend('/recordings/'+rid+'/playback-url?asset=video',self.owner,'POST') if r.get('mediaKind')=='video' else None
                        recordings.append(dict(id=rid,**owned[rid],section=owned[rid]['title'],audioUrl=audio['url'],videoUrl=video['url'] if video else None,sizeBytes=r.get('sizeBytes',0),videoSizeBytes=r.get('videoSizeBytes',0)))
                    payload=dict(date=edit['date'],targetSeconds=600,analyzeAll=False,recordings=recordings,candidates=[dict(id=c['id'],recordingId=c['recordingId'],startMs=c['startMs'],endMs=c['endMs'],source='manual',title=c['title']) for c in render_clips])
                    h.send(self.worker.create_exact(payload,edit,key,action=='previews'),202);return True
            m=re.fullmatch(r'/jobs/([a-f0-9-]{36})(/preview.mp4)?',path)
            if m:
                job=self.worker.jobs.get(m[1])
                if not job:raise EditError('Job not found',404)
                if not m[2]:h.send(job);return True
                file=self.worker.root/m[1]/'highlight.mp4'
                if not job.get('preview') or job['phase']!='complete' or not file.exists():raise EditError('Preview is not ready',404)
                from media_response import send_file
                send_file(h,file,'video/mp4');return True
            raise EditError('Not found',404)
        except EditError as e:h.send({'error':str(e)},e.status)
        except (TypeError,KeyError,ValueError):h.send({'error':'Invalid film edit request'},400)
        except Exception:
            h.send({'error':'Could not load or render this edit. Your saved draft is unchanged'},502)
        return True
