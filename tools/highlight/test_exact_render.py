"""Real FFmpeg fixtures verify frame boundaries, source order and audio continuity."""
from copy import deepcopy
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import unittest
import numpy as np
from worker import Worker, write_json
from exact_render import render_exact, requires_composition
from film_edits import DEFAULT_APPEARANCE

@unittest.skipUnless(shutil.which('ffmpeg'),'FFmpeg required')
class ExactRenderTests(unittest.TestCase):
    def test_source_order_frames_audio_and_composition(self):
        with tempfile.TemporaryDirectory() as td:
            root=Path(td);folder=root/'job';folder.mkdir();source=root/'source.mp4'
            subprocess.run(['ffmpeg','-v','error','-y','-f','lavfi','-i','testsrc2=s=320x180:r=30:d=5','-f','lavfi','-i','aevalsrc=sin(2*PI*(440+220*floor(t))*t):s=48000:d=5','-c:v','libx264','-threads','1','-c:a','aac',str(source)],check=True)
            worker=Worker(root);worker.films=root/'films';worker.films.mkdir();worker.cancelled=threading.Event();worker.active='job';worker.jobs['job']={'id':'job'}
            maps={'r':dict(durationMs=5000,title='Study',section='Study',notes=[dict(startMs=0,endMs=5000,midi=69)])}
            def prepare(manifest,folder):write_json(folder/'analysis.json',maps);return {'r':{'audio':source,'video':source}},None
            worker.prepare=prepare
            commands=[];real_command=worker.command
            def command(args,*rest,**kwargs):
                commands.append(args);return real_command(args,*rest,**kwargs)
            worker.command=command
            edit=dict(id='e',owner='owner',date='2026-09-30',revision=3,sourceFilmId='old',title='Fixture',sources={'r':{'durationMs':5000}},appearance=deepcopy(DEFAULT_APPEARANCE),clips=[dict(id='a',recordingId='r',startMs=2000,endMs=4033,originalPassage=2,title='Study'),dict(id='b',recordingId='r',startMs=0,endMs=1967,originalPassage=1,title='Study')])
            project,duration=render_exact(worker,edit,{},folder,preview=True)
            self.assertLess(abs(duration-4000),100)
            cuts=[args for args in commands if str(args[-1]).endswith('.mov')]
            self.assertEqual(len(cuts),2)
            for args in cuts:
                self.assertLess(args.index('-t'),args.index('-i'),'Input duration must bound work on long source recordings')
            recipe=json.loads((folder/'edit.json').read_text());self.assertEqual([(s['startFrame'],s['endFrame']) for s in recipe['segments']],[(0,61),(61,120)])
            self.assertEqual([c['id'] for c in project['clips']],['a','b'])
            # Overlay copies the base AAC payload exactly; it adds no audio or time.
            def audio_hash(name):return subprocess.check_output(['ffmpeg','-v','error','-i',str(folder/name),'-map','0:a:0','-c','copy','-f','hash','-'])
            self.assertEqual(audio_hash('base.mp4'),audio_hash('highlight.mp4'))
            def pcm(file,start=0):return np.frombuffer(subprocess.check_output(['ffmpeg','-v','error','-ss',str(start),'-i',str(file),'-t','0.5','-vn','-ar','48000','-ac','1','-f','f32le','-']),dtype=np.float32)
            # Each half begins at the requested source clock, within AAC noise.
            for out_time,src_time in [(.2,2.2),(61/30+.2,.2)]:
                a,b=pcm(folder/'base.mp4',out_time),pcm(source,src_time);n=min(len(a),len(b));self.assertGreater(np.corrcoef(a[:n],b[:n])[0,1],.95)

    def test_audio_only_slate_missing_pitch_and_short_film_without_intro(self):
        with tempfile.TemporaryDirectory() as td:
            root=Path(td);folder=root/'job';folder.mkdir();source=root/'tone.wav'
            subprocess.run(['ffmpeg','-v','error','-y','-f','lavfi','-i','sine=frequency=330:duration=2','-ar','48000',str(source)],check=True)
            w=Worker(root);w.films=root/'films';w.films.mkdir();w.cancelled=threading.Event();w.active='job';w.jobs['job']={'id':'job'}
            def prepare(manifest,folder):write_json(folder/'analysis.json',{'r':dict(durationMs=2000,title='Audio only',notes=[])});return {'r':{'audio':source,'video':None}},None
            w.prepare=prepare
            edit=dict(id='e',owner='owner',date='2026-09-30',revision=1,sourceFilmId='old',title='Audio',sources={'r':{'durationMs':2000}},appearance={**DEFAULT_APPEARANCE,'intro':False},clips=[dict(id='a',recordingId='r',startMs=300,endMs=1800,originalPassage=1,title='Audio')])
            _,duration=render_exact(w,edit,{},folder,preview=True);self.assertLess(abs(duration-1500),100)
            self.assertEqual(json.loads((folder/'edit.json').read_text())['segments'][0]['endFrame'],45)

    def test_authored_cards_compose_when_global_pitch_trail_is_off(self):
        edit=dict(appearance={**DEFAULT_APPEARANCE,'intro':False,'pitchTrail':False})
        plain=[dict(id='a')]
        authored=[dict(id='a',graphics=dict(pitchTrail=False,chapter='Chapter one',captions=[dict(startMs=100,endMs=900,text='Hello')]))]
        self.assertFalse(requires_composition(edit,plain))
        self.assertTrue(requires_composition(edit,authored))

    def test_cancel_stops_encoder_and_restart_marks_job_failed(self):
        import sys,time
        with tempfile.TemporaryDirectory() as td:
            root=Path(td);folder=root/'job';folder.mkdir();write_json(folder/'status.json',dict(id='job',phase='rendering'))
            w=Worker(root);self.assertEqual(w.jobs['job']['phase'],'failed');w.cancelled=threading.Event()
            timer=threading.Timer(.15,w.cancelled.set);timer.start();start=time.monotonic()
            try:
                with self.assertRaises(InterruptedError):w.command([sys.executable,'-c','import time;time.sleep(30)'],folder)
                self.assertLess(time.monotonic()-start,3)
            finally:timer.cancel()
