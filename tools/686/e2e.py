"""Exercise the real 686 page and Go handlers in Chromium.

Build first: go build -C jazz-api -o /tmp/ember-api .
Run: python3 tools/686/e2e.py --api-binary /tmp/ember-api
Requires Python Playwright and Chromium. Optional --live-elevenlabs and
--live-gemini use the corresponding environment keys and incur provider usage.
The default uses delayed provider fixtures, not browser/API/renderer mocks.
"""
import argparse
import http.client
import json
import math
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import threading
import time
import urllib.request
import wave
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from playwright.sync_api import sync_playwright

ROOT = Path(__file__).resolve().parents[2]


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class Fixtures(SimpleHTTPRequestHandler):
    def log_message(self, *args): pass

    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        if self.path.startswith('/speech-to-text'):
            assert b'utterance' in raw and b'scribe_v2' in raw
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(b'{"text":"Hello monitor. Give me a short status report."}')
        elif self.path.startswith('/models/'):
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.end_headers()
            try:
                for text, finish in [('Hello, Reclaimer. ', False), ('Containment is stable.', True)]:
                    candidate = {'content': {'parts': [{'text': text}]}}
                    if finish: candidate['finishReason'] = 'STOP'
                    event = {'candidates': [candidate]}
                    self.wfile.write(('data: ' + json.dumps(event) + '\n\n').encode())
                    self.wfile.flush()
                    if not finish: time.sleep(2)
            except (BrokenPipeError, ConnectionResetError): pass
        elif self.path.startswith('/text-to-speech/'):
            assert json.loads(raw)['model_id'] == 'eleven_v4'
            self.send_response(200)
            self.send_header('Content-Type', 'application/octet-stream')
            self.end_headers()
            try:
                for chunk in range(15):
                    pcm = b''.join(struct.pack('<h', int(4000 * math.sin((chunk * 2400 + i) * 2 * math.pi * 440 / 24000))) for i in range(2400))
                    self.wfile.write(pcm)
                    self.wfile.flush()
                    time.sleep(.06)
            except (BrokenPipeError, ConnectionResetError): pass
        else:
            self.send_error(404)


class Site(SimpleHTTPRequestHandler):
    # Same-origin local relay; actual Go API remains responsible for auth,
    # provider calls, SSE parsing, PCM streaming, and error responses.
    protocol_version = 'HTTP/1.1'
    api_port = None
    events = []

    def log_message(self, *args): pass

    def do_POST(self):
        if not self.path.startswith('/686/api/'):
            self.send_error(404)
            return
        path = self.path.removeprefix('/686/api')
        body = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        event = {'path': path, 'started': time.time() * 1000}
        self.events.append(event)
        conn = http.client.HTTPConnection('127.0.0.1', self.api_port, timeout=45)
        try:
            conn.request('POST', path, body, {'Content-Type': self.headers.get('Content-Type', 'application/json')})
            response = conn.getresponse()
            event['status'] = response.status
            self.send_response(response.status)
            self.send_header('Content-Type', response.getheader('Content-Type'))
            self.send_header('Transfer-Encoding', 'chunked')
            self.send_header('Cache-Control', 'no-store')
            self.end_headers()
            while True:
                chunk = response.read1(8192)
                if not chunk: break
                event.setdefault('first_chunk', time.time() * 1000)
                self.wfile.write(f'{len(chunk):x}\r\n'.encode() + chunk + b'\r\n')
                self.wfile.flush()
            event['finished'] = time.time() * 1000
            self.wfile.write(b'0\r\n\r\n')
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError, http.client.HTTPException):
            event['aborted'] = True
            self.close_connection = True
        finally:
            conn.close()


INSTRUMENT = """
window.audioEvidence = {buffers: [], stopped: 0, active: 0};
const original = AudioContext.prototype.createBufferSource;
AudioContext.prototype.createBufferSource = function(...args) {
  const node = original.apply(this, args);
  const start = node.start.bind(node), stop = node.stop.bind(node);
  let counted = false;
  node.start = (at) => {
    const data = node.buffer.getChannelData(0);
    window.audioEvidence.buffers.push({
      time: performance.timeOrigin + performance.now(), at,
      samples: data.length, nonzero: data.some(n => Math.abs(n) > .0001)
    });
    counted = true;
    window.audioEvidence.active++;
    return start(at);
  };
  node.stop = (...a) => { window.audioEvidence.stopped++; return stop(...a); };
  node.addEventListener('ended', () => {
    if (counted) { counted = false; window.audioEvidence.active--; }
  });
  return node;
};
"""


def run(args):
    for live, key in [(args.live_elevenlabs, 'ELEVENLABS_API_KEY'), (args.live_gemini, 'GEMINI_API_KEY')]:
        if live and not os.environ.get(key): raise RuntimeError(f'{key} is required for live mode')
    fixture = ThreadingHTTPServer(('127.0.0.1', 0), Fixtures)
    threading.Thread(target=fixture.serve_forever, daemon=True).start()
    fixture_url = f'http://127.0.0.1:{fixture.server_port}'
    api_port = free_port()
    env = dict(os.environ, EMBER_DEV_ONLY='1', PORT=str(api_port))
    if not args.live_elevenlabs:
        env.update(ELEVENLABS_API_KEY='fixture', EMBER_ELEVENLABS_API_BASE=fixture_url)
    if not args.live_gemini:
        env.update(GEMINI_API_KEY='fixture', EMBER_GEMINI_API_BASE=fixture_url)
    Site.api_port = api_port
    Site.events = []
    site = ThreadingHTTPServer(('127.0.0.1', 0), partial(Site, directory=str(ROOT)))
    threading.Thread(target=site.serve_forever, daemon=True).start()
    with tempfile.TemporaryDirectory(prefix='ember-e2e-') as temporary:
        log = open(Path(temporary) / 'api.log', 'w+')
        api = subprocess.Popen([args.api_binary], env=env, stdout=log, stderr=log)
        try:
            for _ in range(100):
                try:
                    urllib.request.urlopen(f'http://127.0.0.1:{api_port}/health', timeout=.3).close()
                    break
                except OSError: time.sleep(.1)
            else: raise RuntimeError('Local Go API did not become ready')
            # Seed a spoken microphone recording using the same Go TTS path.
            req = urllib.request.Request(f'http://127.0.0.1:{api_port}/v1/686/speak?format=pcm',
                data=json.dumps({'text': 'Hello monitor. Give me a short status report.'}).encode(),
                headers={'Content-Type': 'application/json'})
            with urllib.request.urlopen(req, timeout=45) as response: pcm = response.read()
            microphone = Path(temporary) / 'microphone.wav'
            with wave.open(str(microphone), 'wb') as wav:
                wav.setnchannels(1); wav.setsampwidth(2); wav.setframerate(24000)
                wav.writeframes(pcm)
            with sync_playwright() as playwright:
                browser = playwright.chromium.launch(executable_path=args.chromium, headless=True, args=[
                    '--no-sandbox', '--enable-unsafe-swiftshader',
                    '--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream',
                    f'--use-file-for-fake-audio-capture={microphone}',
                ])
                page = browser.new_page(viewport={'width': 900, 'height': 700}, permissions=['microphone'])
                page.set_default_timeout(60000)
                def wait(condition):
                    try:
                        page.wait_for_function(condition)
                    except Exception:
                        print(json.dumps({'failed_condition': condition, 'state': page.locator('#state').inner_text(), 'errors': errors, 'requests': Site.events}), flush=True)
                        raise
                page.add_init_script(INSTRUMENT)
                errors = []
                page.on('pageerror', lambda e: errors.append(str(e)))
                page.goto(f'http://127.0.0.1:{site.server_port}/686/')
                # Use the real renderer, real Web Audio, and normal autoplay policy.
                page.evaluate('() => new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))')
                page.locator('#typed-input').fill('Give me a short status report.')
                page.locator('#typed-form button').click()
                wait("document.querySelector('#state').textContent.startsWith('Channel open')")
                typed_latency = page.locator('#latency').inner_text()
                evidence = page.evaluate('audioEvidence')
                assert evidence['buffers'] and any(b['nonzero'] for b in evidence['buffers']), evidence
                assert evidence['active'] == 0, evidence
                events = Site.events.copy()
                respond = next(e for e in events if '/respond' in e['path'])
                speech = [e for e in events if '/speak' in e['path']]
                assert speech and all(e['status'] == 200 for e in events), events
                if not args.live_gemini:
                    assert speech[0]['started'] < respond['finished'], 'TTS waited for complete Gemini response'
                if not args.live_elevenlabs:
                    assert evidence['buffers'][0]['time'] < speech[0]['finished'], 'Playback waited for complete TTS audio'
                print(json.dumps({'typed': 'PASS', 'latency': typed_latency, 'pcm_buffers': len(evidence['buffers']), 'speech_requests': len(speech)}), flush=True)

                # Exercise the browser's real MediaRecorder and multipart upload.
                page.locator('#talk').click()
                wait("document.querySelector('#talk-label').textContent === 'Hold to talk'")
                page.locator('#talk').focus()
                page.keyboard.down('v')
                wait("document.querySelector('#state').textContent.startsWith('Listening')")
                page.wait_for_timeout(max(5000, len(pcm) / 48 + 1000))
                page.keyboard.up('v')
                wait("document.querySelector('#state').textContent.startsWith('Channel open')")
                assert any('/transcribe' in e['path'] and e['status'] == 200 for e in Site.events)
                transcript = page.locator('.user-message p').last.inner_text()
                assert transcript.strip()
                print(json.dumps({'microphone': 'PASS', 'latency': page.locator('#latency').inner_text(), 'transcript': transcript}), flush=True)

                # One deliberate failure, followed by a successful real turn.
                pattern = '**/api/v1/686/respond?stream=1'
                page.route(pattern, lambda route: route.fulfill(status=502, content_type='application/json', body='{"error":"test provider unavailable"}'), times=1)
                page.locator('#typed-input').fill('Test failure recovery')
                page.locator('#typed-form button').click()
                wait("document.querySelector('#state').textContent === 'test provider unavailable'")
                wait("!document.querySelector('#talk').disabled")
                assert page.evaluate('audioEvidence.active') == 0
                page.locator('#typed-input').fill('Are you still there? Answer briefly.')
                page.locator('#typed-form button').click()
                wait("document.querySelector('#state').textContent.startsWith('Channel open')")
                assert page.evaluate('audioEvidence.active') == 0
                assert not errors, errors
                print(json.dumps({'failure_recovery': 'PASS', 'renderer': 'real WebGL', 'browser_errors': errors, 'live_elevenlabs': args.live_elevenlabs, 'live_gemini': args.live_gemini}), flush=True)
                browser.close()
        finally:
            api.terminate()
            try: api.wait(timeout=5)
            except subprocess.TimeoutExpired: api.kill(); api.wait()
            log.close()
            site.shutdown(); fixture.shutdown()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--api-binary', required=True)
    parser.add_argument('--chromium', default='/usr/bin/chromium')
    parser.add_argument('--live-elevenlabs', action='store_true')
    parser.add_argument('--live-gemini', action='store_true')
    run(parser.parse_args())
