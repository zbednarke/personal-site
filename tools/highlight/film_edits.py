"""Versioned, server-owned film edits. No source URLs enter persisted drafts."""
from copy import deepcopy
from datetime import datetime, timezone
import json
import math
from pathlib import Path
import re
import threading
from uuid import uuid4
from worker import write_json

ID = re.compile(r'[a-f0-9-]{36}')
FILM = re.compile(r'\d{4}-\d{2}-\d{2}-[a-f0-9-]{36}')
DEFAULT_APPEARANCE = dict(intro=True, introSeconds=4, pitchTrail=True, pitchConvention='written-bb')

def validate_graphics(value, start, end):
    if value is None: return None
    if not isinstance(value,dict) or set(value)-{'pitchTrail','chapter','subtitle','captions'}:
        raise EditError('Invalid passage graphics')
    result={}
    if 'pitchTrail' in value:
        if type(value['pitchTrail']) is not bool: raise EditError('Invalid passage pitch trail')
        result['pitchTrail']=value['pitchTrail']
    for key,limit in (('chapter',60),('subtitle',100)):
        if key in value:
            text=value[key]
            if not isinstance(text,str) or not text.strip() or len(text)>limit: raise EditError('Invalid passage title card')
            result[key]=text.strip()
    if 'captions' in value:
        if not isinstance(value['captions'],list) or len(value['captions'])>30: raise EditError('Invalid passage captions')
        captions=[]
        for caption in value['captions']:
            if not isinstance(caption,dict) or set(caption)!={'startMs','endMs','text'}: raise EditError('Invalid passage caption')
            a,b,text=caption['startMs'],caption['endMs'],caption['text']
            if any(type(n) not in (int,float) or not math.isfinite(n) for n in (a,b)) or not start<=a<b<=end:
                raise EditError('Passage captions must stay inside their source passage')
            if not isinstance(text,str) or not text.strip() or len(text)>160: raise EditError('Invalid passage caption')
            captions.append(dict(startMs=round(a),endMs=round(b),text=text.strip()))
        result['captions']=captions
    return result

class EditError(ValueError):
    def __init__(self, message, status=400):
        super().__init__(message)
        self.status = status

def now():
    return datetime.now(timezone.utc).isoformat()

def validate(content, sources, excluded=(), render=False):
    if not isinstance(content, dict) or set(content)-{'title','clips','appearance'}:
        raise EditError('Invalid edit fields')
    title = content.get('title')
    if not isinstance(title, str) or not title.strip() or len(title)>120:
        raise EditError('Choose a title of 1–120 characters')
    clips = content.get('clips')
    if not isinstance(clips,list) or len(clips)>60 or (render and not clips):
        raise EditError('Choose 1–60 passages to render')
    ids=set(); total=0; result=[]
    for c in clips:
        if not isinstance(c,dict) or set(c)-{'id','recordingId','startMs','endMs','originalPassage','title','graphics'}:
            raise EditError('Invalid passage fields')
        cid=c.get('id'); rid=c.get('recordingId'); start=c.get('startMs'); end=c.get('endMs')
        if not isinstance(cid,str) or not re.fullmatch(r'[\w-]{1,80}',cid) or cid in ids:
            raise EditError('Passage IDs must be unique')
        ids.add(cid)
        if rid not in sources: raise EditError('A source recording is unavailable; remove its passage or restore the source')
        if any(type(n) not in (int,float) or not math.isfinite(n) for n in (start,end)) or not 0<=start<end<=sources[rid]['durationMs'] or end-start<500:
            raise EditError('Each passage must be at least 0.5 seconds and stay inside its source')
        if any(x['recordingId']==rid and start<x['endMs'] and end>x['startMs'] for x in excluded):
            raise EditError('A passage overlaps a rejected region. Unreject that region separately first')
        if type(c.get('originalPassage')) is not int or not 1<=c['originalPassage']<=60:
            raise EditError('Invalid original passage label')
        item=dict(id=cid,recordingId=rid,startMs=round(start),endMs=round(end),originalPassage=c['originalPassage'],title=str(sources[rid].get('title','Practice'))[:200])
        graphics=validate_graphics(c.get('graphics'),start,end)
        if graphics is not None:item['graphics']=graphics
        result.append(item)
        total+=round(end)-round(start)
    if total>600000: raise EditError('An edit may contain at most ten minutes')
    ap=content.get('appearance')
    if not isinstance(ap,dict) or set(ap)!=set(DEFAULT_APPEARANCE) or type(ap['intro']) is not bool or type(ap['pitchTrail']) is not bool or type(ap['introSeconds']) is not int or ap['introSeconds'] not in (3,4,5) or ap['pitchConvention'] not in ('concert','written-bb'):
        raise EditError('Invalid film appearance')
    if render and ap['intro'] and total<ap['introSeconds']*1000:
        raise EditError('Turn off the intro or shorten it to fit this film')
    return dict(title=title.strip(),clips=result,appearance=deepcopy(ap))

class EditStore:
    def __init__(self, root, films, owner):
        self.root=Path(root); self.films=Path(films); self.owner=owner
        self.directory=self.root/'edits'; self.directory.mkdir(exist_ok=True)
        self.lock=threading.RLock()

    def path(self,eid):
        if not isinstance(eid,str) or not ID.fullmatch(eid): raise EditError('Edit not found',404)
        return self.directory/(eid+'.json')

    def get(self,eid):
        try: edit=json.loads(self.path(eid).read_text())
        except FileNotFoundError: raise EditError('Edit not found',404)
        if edit['owner']!=self.owner: raise EditError('Edit not found',404)
        return edit

    def list(self):
        return sorted([dict(id=e['id'],title=e['title'],date=e['date'],sourceFilmId=e['sourceFilmId'],revision=e['revision'],updatedAt=e['updatedAt']) for p in self.directory.glob('*.json') if (e:=json.loads(p.read_text()))['owner']==self.owner],key=lambda e:e['updatedAt'],reverse=True)

    def film(self,fid):
        if not isinstance(fid,str) or not FILM.fullmatch(fid): raise EditError('Film edit is unavailable',404)
        index=self.films/'index.json'
        entries=json.loads(index.read_text()) if index.exists() else []
        film=next((f for f in entries if f['id']==fid),None)
        if not film: raise EditError('Film not found',404)
        saved=self.films/(fid+'.edit.json')
        if saved.exists():
            manifest=json.loads(saved.read_text())
            if manifest.get('owner',self.owner)!=self.owner: raise EditError('Film not found',404)
            return dict(film=film,content=manifest['edit'],sources=manifest['sources'])
        project=self.root/fid[11:]/'project.json'
        analysis=self.films/(fid+'.analysis.json')
        if not project.exists() or not analysis.exists(): raise EditError('This older film has no recoverable edit; it can still be watched or downloaded',409)
        pr=json.loads(project.read_text()); maps=json.loads(analysis.read_text())
        sources={rid:dict(durationMs=v['durationMs'],title=v.get('title','Practice')) for rid,v in maps.items()}
        clips=[dict(id=c['id'],recordingId=c['recordingId'],startMs=c['startMs'],endMs=c['endMs'],originalPassage=i+1,title=c.get('title','Practice')) for i,c in enumerate(pr['clips'])]
        return dict(film=film,content=dict(title=pr['title'],clips=clips,appearance=deepcopy(DEFAULT_APPEARANCE)),sources=sources)

    def create(self,fid,content=None):
        base=self.film(fid)
        body=validate(content or base['content'],base['sources'])
        with self.lock:
            e=dict(schemaVersion=1,id=str(uuid4()),owner=self.owner,date=base['film']['date'],sourceFilmId=fid,revision=1,createdAt=now(),updatedAt=now(),sources=base['sources'],**body)
            write_json(self.path(e['id']),e)
        return e

    def create_day(self,date,content,sources):
        from datetime import date as Date
        try: Date.fromisoformat(date)
        except (TypeError,ValueError): raise EditError('Choose a valid practice date')
        if not sources: raise EditError('No uploaded recordings are available for this day')
        body=validate(content,sources)
        with self.lock:
            e=dict(schemaVersion=1,id=str(uuid4()),owner=self.owner,date=date,sourceFilmId=None,revision=1,createdAt=now(),updatedAt=now(),sources=sources,**body)
            write_json(self.path(e['id']),e)
        return e

    def save(self,eid,revision,content):
        with self.lock:
            e=self.get(eid)
            if type(revision) is not int or e['revision']!=revision: raise EditError('This draft changed in another browser. Reload it or save your changes as a separate edit',409)
            body=validate(content,e['sources'])
            e.update(body,revision=e['revision']+1,updatedAt=now())
            write_json(self.path(eid),e)
            return e
