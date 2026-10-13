import unittest
import numpy as np
from audio_map import RATE, observations, note_events, summarize, candidates, select_plan
from model import validate_plan

class NoteMapTests(unittest.TestCase):
    def tone(self, hz, seconds=2):
        t=np.arange(int(RATE*seconds))/RATE
        return (.15*np.sin(2*np.pi*hz*t)+.08*np.sin(4*np.pi*hz*t)).astype(np.float32)

    def test_fundamental_notes_not_strongest_harmonics(self):
        for hz,midi in [(164.814,52),(261.626,60),(440,69),(932.328,82)]:
            frames=observations(self.tone(hz))
            voiced=[f['midi'] for f in frames if f['midi'] is not None]
            self.assertGreater(len(voiced),len(frames)*.95)
            self.assertAlmostEqual(float(np.median(voiced)),midi,delta=.08)
            self.assertGreater(len(note_events(frames)),0)

    def test_silence_and_noise_are_not_notes(self):
        for signal in [np.zeros(16000),np.random.default_rng(4).normal(0,.1,16000)]:
            self.assertLess(summarize(observations(signal))['voicedFraction'],.05)

    def test_absolute_clock_and_phrase_exclusions(self):
        frames=observations(self.tone(440,12),RATE*5)
        a=dict(frames=frames,notes=note_events(frames),durationMs=17000)
        r=dict(id='r',title='Long tones',durationMs=17000)
        found=candidates(r,a)
        self.assertTrue(found)
        self.assertGreaterEqual(found[0]['startMs'],4800)
        self.assertEqual(candidates(r,a,[(4000,18000)]),[])

    def test_balanced_plan_is_bounded_and_never_repeats_source(self):
        cs=[]
        for rid,title in [('a','Long tones'),('b','Training'),('c','Flexibility')]:
            for i in range(5):
                cs.append(dict(id=f'{rid}{i}',recordingId=rid,title=title,startMs=i*20000,endMs=i*20000+15000,score=.9,manual=False,liked=False))
        m=dict(date='2026-09-28',targetSeconds=120,candidates=cs,recordings=[{'id':x} for x in 'abc'])
        p=select_plan(m)
        self.assertEqual(validate_plan(p,m),120000)
        self.assertEqual({c['candidateId'][0] for c in p['clips']},set('abc'))

    def test_all_priority_candidates_get_review_even_if_overlap(self):
        c=dict(id='one',recordingId='a',title='Practice',startMs=0,endMs=15000,manual=True,liked=True)
        m=dict(date='2026-09-28',targetSeconds=60,candidates=[c,{**c,'id':'two'}],recordings=[{'id':'a'}])
        p=select_plan(m)
        self.assertEqual(validate_plan(p,m),15000)
        self.assertEqual(len(p['reviews']),2)

if __name__=='__main__':unittest.main()
