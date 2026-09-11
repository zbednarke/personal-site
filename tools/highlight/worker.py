"""Loopback-only Clip Studio editing worker. Python standard library + Codex + FFmpeg."""
import argparse
import array
import hashlib
import hmac
import json
import math
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse
from uuid import uuid4

from model import snapshot, validate_plan, schema

HERE = Path(__file__).resolve().parent
MODEL = 'gpt-6-astra'


def write_json(path, value):
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(value, indent=2, ensure_ascii=False), encoding='utf-8')
    temp.replace(path)


class Worker:
    def __init__(self, root):
        self.root = Path(root).resolve()
        self.root.mkdir(parents=True, exist_ok=True)
        self.token = secrets.token_urlsafe(32)
        self.lock = threading.Lock()
        self.active = None
        self.jobs = {}
        for f in self.root.glob('*/status.json'):
            job = json.loads(f.read_text(encoding='utf-8'))
            if job['phase'] not in ('complete', 'failed', 'cancelled'):
                job.update(phase='failed', message='Worker restarted. Start a new draft from Clip Studio.')
                write_json(f, job)
            self.jobs[job['id']] = job

    def status(self, job, phase, message, **extra):
        job.update(phase=phase, message=message, **extra)
        write_json(self.root / job['id'] / 'status.json', job)

    def create(self, payload):
        manifest = snapshot(payload)
        with self.lock:
            if self.active:
                raise ValueError('An edit is already running. Finish or cancel it first.')
            jid = str(uuid4())
            folder = self.root / jid
            folder.mkdir()
            job = dict(id=jid, date=manifest['date'], targetSeconds=manifest.get('targetSeconds', 120), phase='preparing', message='Preparing candidate moments', model=MODEL, effort='medium')
            self.jobs[jid] = job
            self.active = jid
            self.cancelled = threading.Event()
            self.status(job, 'preparing', 'Downloading source media to this PC')
            threading.Thread(target=self.run, args=(job, manifest), daemon=True).start()
            return dict(job)

    def command(self, args, folder, timeout=900, stdin=None):
        if self.cancelled.is_set():
            raise InterruptedError('Cancelled')
        with subprocess.Popen(args, cwd=folder, stdin=subprocess.PIPE if stdin is not None else subprocess.DEVNULL,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0)) as proc:
            deadline = time.monotonic()+timeout
            first = True
            while True:
                try:
                    stdout, stderr = proc.communicate(input=stdin if first else None, timeout=.5)
                    break
                except subprocess.TimeoutExpired:
                    first = False
                    if self.cancelled.is_set() or time.monotonic() > deadline:
                        if os.name == 'nt':
                            subprocess.run(['taskkill', '/PID', str(proc.pid), '/T', '/F'], capture_output=True, creationflags=subprocess.CREATE_NO_WINDOW)
                        else:
                            proc.kill()
                        proc.communicate()
                        raise InterruptedError('Cancelled' if self.cancelled.is_set() else 'Job timed out; start a new draft')
            if proc.returncode:
                # Commands only receive local media paths; signed URLs never enter process logs.
                raise RuntimeError(stderr.decode('utf-8', errors='replace')[-2000:] or 'Editing process failed')
            return stdout

    def download(self, url, path):
        # Redirects could bypass the signed-storage host restriction.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args, **kwargs):
                return None
        try:
            with urllib.request.build_opener(NoRedirect).open(url, timeout=45) as response, path.open('wb') as output:
                size = 0
                while chunk := response.read(1024*1024):
                    if self.cancelled.is_set():
                        raise InterruptedError('Cancelled')
                    size += len(chunk)
                    if size > 8*1024**3:
                        raise ValueError('Source exceeds the 8 GB local download limit')
                    output.write(chunk)
        except InterruptedError:
            raise
        except Exception:
            raise RuntimeError('Could not download private media. Reopen Clip Studio and start again for fresh media links.') from None

    def prepare(self, manifest, folder):
        media = folder / 'media'
        media.mkdir()
        evidence = folder / 'editor'
        evidence.mkdir()
        sources = {}
        for r in manifest['recordings']:
            if shutil.disk_usage(folder).free < 10*1024**3:
                raise RuntimeError('Keep at least 10 GB free for local editing')
            audio = media / (r['id']+'.wav')
            self.download(r['audioUrl'], audio)
            video = None
            if r['videoUrl']:
                video = media / (r['id']+'.video')
                self.download(r['videoUrl'], video)
            sources[r['id']] = dict(audio=audio, video=video)
        for i, c in enumerate(manifest['candidates']):
            source = sources[c['recordingId']]
            duration = (c['endMs']-c['startMs'])/1000
            raw = self.command(['ffmpeg', '-v', 'error', '-ss', str(c['startMs']/1000), '-i', str(source['audio']),
                                '-t', str(duration), '-ac', '1', '-ar', '8000', '-f', 'f32le', '-'], folder)
            values = array.array('f', raw)
            hop = max(2000, math.ceil(len(values)/400))
            bins = [round(math.sqrt(sum(v*v for v in values[n:n+hop])/len(values[n:n+hop])), 5) for n in range(0, len(values), hop)]
            c['activity'] = dict(binMs=round(hop/8, 3), rms=bins, meaning='Signal energy only, not musical quality. Low-energy boundaries can help preserve phrase endings.')
            c['frames'] = []
            if source['video']:
                for index, fraction in enumerate((.15, .5, .85)):
                    frame = evidence / f'{i:03d}-{index}.jpg'
                    self.command(['ffmpeg', '-v', 'error', '-ss', str((c['startMs']+fraction*(c['endMs']-c['startMs']))/1000),
                                  '-i', str(source['video']), '-frames:v', '1', '-vf', 'scale=480:-2', '-y', str(frame)], folder)
                    if frame.exists():
                        c['frames'].append(frame.name)
        # No signed URLs or whole recordings in the agent's evidence manifest.
        write_json(evidence / 'candidates.json', dict(date=manifest['date'], candidates=manifest['candidates']))
        return sources, evidence

    def edit(self, manifest, evidence, folder):
        schema_path = folder / 'schema.json'
        write_json(schema_path, schema())
        bundled = Path(os.environ.get('JAZZ_CODEX_JS', str(HERE.parent.parent / '.local/highlight-tools/node_modules/@openai/codex/bin/codex.js')))
        codex = shutil.which('codex')
        if bundled.exists():
            executable = [shutil.which('node') or 'node', str(bundled)]
        elif not codex:
            raise RuntimeError('Install Codex CLI and run codex login first')
        elif os.name == 'nt':
            script = Path(codex).parent / 'node_modules/@openai/codex/bin/codex.js'
            if not script.exists():
                raise RuntimeError('Cannot locate the installed Codex CLI entry point')
            executable = [shutil.which('node') or 'node', str(script)]
        else:
            executable = [codex]
        prompt = (HERE / 'editor-prompt.md').read_text(encoding='utf-8')
        target = manifest.get('targetSeconds', 120)
        prompt = prompt.replace('{{TARGET_SECONDS}}', str(target)).replace('{{MIN_SECONDS}}', str(target-15)).replace('{{MAX_SECONDS}}', str(target+15))
        prompt += '\n\nThe full candidate manifest is attached below. Read this supplied data directly; filesystem access is not required.\n<candidate_manifest>\n'
        prompt += (evidence / 'candidates.json').read_text(encoding='utf-8') + '\n</candidate_manifest>\n'
        args = executable + ['exec', '--ignore-user-config', '--skip-git-repo-check', '--ephemeral',
                             '-m', MODEL, '-c', 'model_reasoning_effort="medium"', '-c', 'approval_policy="never"',
                             '--sandbox', 'read-only', '--output-schema', str(schema_path),
                             '--output-last-message', str(folder / 'plan.json'), '--json']
        # Attach representative frames directly so restrictive CLI shell policies
        # cannot prevent evidence access. All candidates' metadata is in stdin.
        for frame in sorted(evidence.glob('*-1.jpg'))[:12]:
            args += ['--image', str(frame)]
        args += ['-']
        events = self.command(args, evidence, timeout=1200, stdin=prompt.encode('utf-8'))
        (folder / 'agent-events.jsonl').write_bytes(events)
        plan = json.loads((folder / 'plan.json').read_text(encoding='utf-8-sig'))
        validate_plan(plan, manifest)
        return plan

    def render(self, plan, manifest, sources, folder):
        lookup = {c['id']: c for c in manifest['candidates']}
        clips = []
        for i, cut in enumerate(plan['clips']):
            c = lookup[cut['candidateId']]
            src = sources[c['recordingId']]
            duration = (cut['endMs']-cut['startMs'])/1000
            start = str(cut['startMs']/1000)
            # Keep video and its embedded audio together: paused takes can differ
            # slightly from the separately captured lossless WAV's clock.
            args = ['ffmpeg', '-v', 'error', '-y']
            if src['video']:
                args += ['-ss', start, '-i', str(src['video']), '-map', '0:v:0', '-map', '0:a:0']
            else:
                args += ['-f', 'lavfi', '-i', 'color=c=0x191c19:s=1920x1080:r=30', '-ss', start, '-i', str(src['audio']), '-map', '0:v:0', '-map', '1:a:0']
            vf = 'scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2,fps=30,format=yuv420p'
            if not src['video']:
                import textwrap
                (folder / f'slate-{i}.txt').write_text('\n'.join(textwrap.wrap(c['title'], 36))+'\n\n'+manifest['date']+' · Practice recording', encoding='utf-8')
                font = 'C\\:/Windows/Fonts/georgia.ttf' if os.name == 'nt' else '/usr/share/fonts/truetype/dejavu/DejaVuSerif.ttf'
                vf += f",drawtext=fontfile='{font}':textfile=slate-{i}.txt:expansion=none:fontcolor=0xe4edda:fontsize=54:line_spacing=18:x=(w-tw)/2:y=(h-th)/2"
            args += ['-t', str(duration), '-vf', vf,
                     '-af', 'aresample=48000', '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '18', '-threads', '2', '-c:a', 'aac', '-b:a', '320k', '-ac', '1',
                     '-movflags', '+faststart', str(folder / f'cut-{i:02d}.mp4')]
            self.command(args, folder)
            clips.append(dict(id=str(uuid4()), candidateId=c['id'], recordingId=c['recordingId'], startMs=cut['startMs'], endMs=cut['endMs'],
                              title=c['title'], liked=c['liked'], notes=cut['reason']))
        (folder / 'concat.txt').write_text('\n'.join(f"file 'cut-{i:02d}.mp4'" for i in range(len(clips))), encoding='utf-8')
        self.command(['ffmpeg', '-v', 'error', '-y', '-f', 'concat', '-safe', '1', '-i', 'concat.txt', '-c', 'copy', '-movflags', '+faststart', 'highlight.mp4'], folder)
        self.command(['ffmpeg', '-v', 'error', '-i', 'highlight.mp4', '-enc_time_base', '-1', '-fps_mode', 'passthrough', '-f', 'null', '-'], folder)
        probe = json.loads(self.command(['ffprobe', '-v', 'error', '-show_entries', 'format=duration:stream=codec_type', '-of', 'json', 'highlight.mp4'], folder))
        actual = float(probe['format']['duration'])*1000
        expected = validate_plan(plan, manifest)
        if abs(actual-expected) > max(1500, len(clips)*120) or not {'audio', 'video'} <= {s['codec_type'] for s in probe['streams']}:
            raise RuntimeError('Rendered media failed duration/audio/video verification')
        project = dict(version=1, date=manifest['date'], title=plan['title'][:120], clips=clips)
        write_json(folder / 'project.json', project)
        return project, actual

    def run(self, job, manifest):
        folder = self.root / job['id']
        try:
            sources, evidence = self.prepare(manifest, folder)
            self.status(job, 'editing', 'Astra · medium is reviewing manual and liked moments, then assembling the edit')
            plan = self.edit(manifest, evidence, folder)
            self.status(job, 'rendering', 'Rendering and checking your 1080p highlight on this PC')
            project, duration = self.render(plan, manifest, sources, folder)
            self.status(job, 'complete', 'Your draft is ready', project=project, summary=plan['summary'], reviews=plan['reviews'], durationMs=duration)
        except InterruptedError as error:
            self.status(job, 'cancelled', str(error))
        except Exception as error:
            self.status(job, 'failed', str(error)[-2000:])
        finally:
            with self.lock:
                self.active = None


def handler(worker, port):
    origin = f'http://127.0.0.1:{port}'
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def send(self, value, status=200):
            data = json.dumps(value).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Cache-Control', 'no-store')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def valid_host(self):
            return self.headers.get('Host') == f'127.0.0.1:{port}'

        def authorized(self):
            return self.valid_host() and self.headers.get('Origin', origin) == origin and hmac.compare_digest(self.headers.get('X-Highlight-Token', ''), worker.token)

        def do_POST(self):
            if not self.authorized():
                return self.send({'error': 'Local pairing required'}, 403)
            try:
                length = int(self.headers.get('Content-Length', '0'))
                if not 0 < length <= 2*1024*1024:
                    raise ValueError('Invalid request size')
                data = json.loads(self.rfile.read(length))
                if self.path == '/jobs':
                    return self.send(worker.create(data), 202)
                if self.path == '/cancel':
                    if worker.active == data.get('id'):
                        worker.cancelled.set()
                    return self.send({'ok': True})
                self.send({'error': 'Not found'}, 404)
            except (ValueError, TypeError, KeyError) as error:
                self.send({'error': str(error)}, 400)

        def do_GET(self):
            if not self.valid_host():
                return self.send({'error': 'Invalid host'}, 403)
            path = urlparse(self.path).path
            if path == '/':
                data = (HERE / 'local.html').read_text(encoding='utf-8').replace('__TOKEN__', worker.token).encode()
                self.send_response(200)
                self.send_header('Content-Type', 'text/html; charset=utf-8')
                self.send_header('Cache-Control', 'no-store')
                self.send_header('X-Frame-Options', 'DENY')
                self.send_header('Referrer-Policy', 'no-referrer')
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                return self.wfile.write(data)
            if path == '/jobs' and self.authorized():
                return self.send(list(worker.jobs.values()))
            parts = path.strip('/').split('/')
            if len(parts) == 3 and parts[0] == 'jobs' and parts[1] in worker.jobs and parts[2] in ('highlight.mp4', 'project.json'):
                if self.headers.get('Sec-Fetch-Site') not in (None, 'same-origin', 'none'):
                    return self.send({'error': 'Local access only'}, 403)
                if worker.jobs[parts[1]]['phase'] != 'complete':
                    return self.send({'error': 'Draft is not ready'}, 404)
                file = worker.root / parts[1] / parts[2]
                size = file.stat().st_size
                start, end = 0, size-1
                range_header = self.headers.get('Range')
                if range_header:
                    import re
                    match = re.fullmatch(r'bytes=(\d+)-(\d*)', range_header)
                    if not match:
                        return self.send({'error': 'Invalid range'}, 416)
                    start, end = int(match[1]), int(match[2]) if match[2] else end
                    if start > end or start >= size:
                        return self.send({'error': 'Invalid range'}, 416)
                    end = min(end, size-1)
                self.send_response(206 if range_header else 200)
                self.send_header('Content-Type', 'video/mp4' if file.suffix == '.mp4' else 'application/json')
                self.send_header('Accept-Ranges', 'bytes')
                self.send_header('Cache-Control', 'no-store')
                self.send_header('Content-Length', str(end-start+1))
                if range_header:
                    self.send_header('Content-Range', f'bytes {start}-{end}/{size}')
                self.end_headers()
                with file.open('rb') as f:
                    f.seek(start)
                    remaining = end-start+1
                    while remaining:
                        data = f.read(min(1024*1024, remaining))
                        if not data:
                            break
                        try:
                            self.wfile.write(data)
                        except (ConnectionAbortedError, BrokenPipeError, ConnectionResetError):
                            break
                        remaining -= len(data)
                return
            self.send({'error': 'Not found'}, 404)
    return Handler


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--port', type=int, default=8765)
    parser.add_argument('--data', default=str(HERE.parent.parent / '.local/highlights'))
    args = parser.parse_args()
    worker = Worker(args.data)
    server = ThreadingHTTPServer(('127.0.0.1', args.port), handler(worker, args.port))
    print(f'Clip Studio local editor: http://127.0.0.1:{args.port}', flush=True)
    server.serve_forever()
