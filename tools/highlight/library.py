"""Private PDF library, independent of rendering jobs and static releases."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tempfile
import threading
from datetime import datetime, timezone
from urllib.parse import urlparse, parse_qs
from uuid import uuid4

MAX_PDF = 100 * 1024**2
LOCK = threading.Lock()

def validate_resources(resources):
    if resources is None:return []
    if not isinstance(resources,list) or len(resources)>12:raise ValueError('Choose up to 12 library resources')
    result=[]
    for resource in resources:
        if not isinstance(resource,dict) or set(resource)-{'title','artist','note','url'} or set(resource)<{'title','url'}:
            raise ValueError('Invalid library resource')
        title=resource['title'];artist=resource.get('artist','');note=resource.get('note','');url=resource['url']
        if not isinstance(title,str) or not title.strip() or len(title)>160:raise ValueError('Invalid library resource title')
        if not all(isinstance(v,str) for v in (artist,note,url)):raise ValueError('Invalid library resource')
        parsed=urlparse(url)
        if len(artist)>120 or len(note)>240 or len(url)>2048 or parsed.scheme!='https' or not parsed.netloc or parsed.username or parsed.password:
            raise ValueError('Library resource links must use HTTPS')
        result.append(dict(title=title.strip(),artist=artist.strip(),note=note.strip(),url=url))
    return result

class Library:
    def __init__(self, root):
        self.root = Path(root)
        self.root.mkdir(parents=True, exist_ok=True)

    def items(self):
        return sorted((json.loads(p.read_text()) for p in self.root.glob('*.json')), key=lambda x:x['addedAt'], reverse=True)

    def save(self, stream, length, title, author='', source=''):
        if not 5 <= length <= MAX_PDF: raise ValueError('Choose a PDF up to 100 MB')
        title = title.strip()[:240]
        if not title: raise ValueError('Add a title')
        if source and urlparse(source).scheme != 'https': raise ValueError('Source links must use HTTPS')
        with LOCK:
            if shutil.disk_usage(self.root).free < length + 2*1024**3: raise ValueError('Library storage is full; please try later')
            temp = None
            try:
                digest = hashlib.sha256()
                with tempfile.NamedTemporaryFile(dir=self.root, suffix='.upload', delete=False) as f:
                    temp=Path(f.name);remaining=length;first=True
                    while remaining:
                        chunk=stream.read(min(1024**2,remaining))
                        if not chunk: raise ValueError('Upload was incomplete; please retry')
                        if first and not chunk.startswith(b'%PDF-'): raise ValueError('This file is not a PDF')
                        first=False;f.write(chunk);digest.update(chunk);remaining-=len(chunk)
                    f.flush();os.fsync(f.fileno())
                checksum=digest.hexdigest()
                for item in self.items():
                    if item['sha256']==checksum:return item
                identity=str(uuid4())
                item=dict(id=identity,title=title,author=author.strip()[:160],source=source[:2048],sizeBytes=length,sha256=checksum,addedAt=datetime.now(timezone.utc).isoformat())
                target=self.root/(identity+'.pdf');os.replace(temp,target)
                meta=self.root/(identity+'.json.tmp');meta.write_text(json.dumps(item,ensure_ascii=False));os.replace(meta,self.root/(identity+'.json'))
                return item
            finally:
                if temp:temp.unlink(missing_ok=True)

    def update(self,h,data):
        match=re.fullmatch(r'/library/([a-f0-9-]{36})',urlparse(h.path).path)
        if not match:return h.send({'error':'Not found'},404)
        if not isinstance(data,dict) or set(data)!={'resources'}:return h.send({'error':'Expected library resources'},400)
        try:resources=validate_resources(data['resources'])
        except ValueError as e:return h.send({'error':str(e)},400)
        with LOCK:
            path=self.root/(match[1]+'.json')
            if not path.is_file():return h.send({'error':'Not found'},404)
            item=json.loads(path.read_text());item['resources']=resources
            temp=path.with_suffix('.json.tmp');temp.write_text(json.dumps(item,ensure_ascii=False));os.replace(temp,path)
        h.send(item)

    def get(self, h):
        path=urlparse(h.path).path
        if path=='/library':h.send(self.items());return
        match=re.fullmatch(r'/library/([a-f0-9-]{36})/file',path)
        if not match:return h.send({'error':'Not found'},404)
        file=self.root/(match[1]+'.pdf')
        if not file.is_file():return h.send({'error':'Not found'},404)
        size=file.stat().st_size;start=0;end=size-1;status=200
        range_header=h.headers.get('Range')
        if range_header:
            m=re.fullmatch(r'bytes=(\d*)-(\d*)',range_header)
            if not m or not any(m.groups()):return h.send({'error':'Unsupported range'},416)
            if m[1]:start=int(m[1]);end=min(end,int(m[2])) if m[2] else end
            else:start=max(0,size-int(m[2]))
            if start>end or start>=size:
                h.send_response(416);h.send_header('Content-Range',f'bytes */{size}');h.end_headers();return
            status=206
        h.send_response(status)
        for name,value in [('Content-Type','application/pdf'),('Content-Length',str(end-start+1)),('Accept-Ranges','bytes'),('Cache-Control','private, no-store'),('X-Content-Type-Options','nosniff'),('Content-Disposition',('attachment' if parse_qs(urlparse(h.path).query).get('download') else 'inline')+'; filename="'+match[1]+'.pdf"')]:h.send_header(name,value)
        if status==206:h.send_header('Content-Range',f'bytes {start}-{end}/{size}')
        h.end_headers()
        with file.open('rb') as f:
            f.seek(start);remaining=end-start+1
            while remaining:
                chunk=f.read(min(1024**2,remaining));h.wfile.write(chunk);remaining-=len(chunk)

    def upload(self,h):
        try:
            if urlparse(h.path).path!='/library':return h.send({'error':'Not found'},404)
            params=parse_qs(urlparse(h.path).query)
            length=int(h.headers.get('Content-Length','0'))
            if h.headers.get('Content-Type','').split(';')[0]!='application/pdf':raise ValueError('Choose a PDF file')
            item=self.save(h.rfile,length,params.get('title',[''])[0],params.get('author',[''])[0])
            h.send(item,201)
        except ValueError as e:h.send({'error':str(e)},400)
        except Exception:h.send({'error':'Could not save PDF. Please retry.'},500)
