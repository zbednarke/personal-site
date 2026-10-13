"""Authenticated callers may serve local media with bounded byte ranges."""
import re

def send_file(h,file,content_type):
    size=file.stat().st_size;start=0;end=size-1;status=200
    value=h.headers.get('Range')
    if value:
        m=re.fullmatch(r'bytes=(\d*)-(\d*)',value)
        if not m or not any(m.groups()):return h.send({'error':'Unsupported range'},416)
        if m[1]:start=int(m[1]);end=min(end,int(m[2])) if m[2] else end
        else:start=max(0,size-int(m[2]))
        if start>end or start>=size:
            h.send_response(416);h.send_header('Content-Range',f'bytes */{size}');h.end_headers();return
        status=206
    h.send_response(status)
    for name,value in [('Content-Type',content_type),('Content-Length',str(end-start+1)),('Accept-Ranges','bytes'),('Cache-Control','private, no-store'),('X-Content-Type-Options','nosniff')]:h.send_header(name,value)
    if status==206:h.send_header('Content-Range',f'bytes {start}-{end}/{size}')
    h.end_headers()
    with file.open('rb') as f:
        f.seek(start);remaining=end-start+1
        while remaining:
            chunk=f.read(min(1024**2,remaining))
            if not chunk:break
            h.wfile.write(chunk);remaining-=len(chunk)
