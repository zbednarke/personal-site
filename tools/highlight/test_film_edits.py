from copy import deepcopy
import json
from pathlib import Path
import tempfile
import unittest
from film_edits import EditStore, EditError, validate, DEFAULT_APPEARANCE

FID='2026-09-30-11111111-1111-4111-8111-111111111111'
def content():return dict(title='Film',clips=[dict(id='c1',recordingId='r',startMs=0,endMs=6000,originalPassage=9,title='Study')],appearance=deepcopy(DEFAULT_APPEARANCE))

class ContractTests(unittest.TestCase):
    def test_validation_rejects_unsafe_and_invalid_boundaries(self):
        for change in [dict(startMs=-1),dict(endMs=99),dict(endMs=float('nan')),dict(endMs=10001),dict(recordingId='foreign'),dict(originalPassage=True)]:
            c=content();c['clips'][0].update(change)
            with self.subTest(change=change),self.assertRaises(EditError):validate(c,{'r':{'durationMs':10000}},render=True)
        c=content();c['command']='ffmpeg'
        with self.assertRaises(EditError):validate(c,{'r':{'durationMs':10000}})
    def test_rejected_overlap_and_short_intro(self):
        with self.assertRaisesRegex(EditError,'rejected'):validate(content(),{'r':{'durationMs':10000}},[dict(recordingId='r',startMs=5000,endMs=7000)],True)
        c=content();c['clips'][0]['endMs']=2000
        with self.assertRaisesRegex(EditError,'intro'):validate(c,{'r':{'durationMs':10000}},render=True)
        c['clips']=[];validate(c,{'r':{'durationMs':10000}})
        with self.assertRaises(EditError):validate(c,{'r':{'durationMs':10000}},render=True)
    def test_preserves_safe_clip_graphics(self):
        c=content();c['clips'][0]['graphics']=dict(pitchTrail=False,chapter='Chapter one',subtitle='A subtitle',captions=[dict(startMs=1000,endMs=2000,text="A fixture caption.")])
        saved=validate(c,{'r':{'durationMs':10000}})['clips'][0]['graphics']
        self.assertEqual(saved['chapter'],'Chapter one');self.assertFalse(saved['pitchTrail'])
        c['clips'][0]['graphics']['captions'][0]['endMs']=7000
        with self.assertRaisesRegex(EditError,'inside'):validate(c,{'r':{'durationMs':10000}})
    def test_drafts_reload_and_conflict_preserves_server_copy(self):
        with tempfile.TemporaryDirectory() as td:
            root=Path(td);films=root/'films';films.mkdir();(films/'index.json').write_text(json.dumps([dict(id=FID,date='2026-09-30')]))
            (films/(FID+'.edit.json')).write_text(json.dumps(dict(owner='owner',edit=content(),sources={'r':{'durationMs':10000,'title':'Study'}})))
            store=EditStore(root,films,'owner');e=store.create(FID);first=deepcopy(e);c=content();c['title']='Changed';saved=store.save(e['id'],1,c)
            self.assertEqual(saved['revision'],2)
            with self.assertRaises(EditError):store.save(e['id'],1,content())
            self.assertEqual(EditStore(root,films,'owner').get(e['id'])['title'],'Changed')
            self.assertEqual(first['title'],'Film')
            with self.assertRaises(EditError):EditStore(root,films,'intruder').get(e['id'])
            with self.assertRaises(EditError):store.get('../escape')
            self.assertEqual(len(store.list()),1)

    def test_standalone_day_draft_validates_date_and_sources(self):
        with tempfile.TemporaryDirectory() as td:
            root=Path(td);films=root/'films';films.mkdir();store=EditStore(root,films,'owner')
            e=store.create_day('2026-10-01',content(),{'r':{'durationMs':10000,'title':'Study'}})
            self.assertIsNone(e['sourceFilmId']);self.assertEqual(e['date'],'2026-10-01')
            with self.assertRaises(EditError):store.create_day('bad-date',content(),{'r':{'durationMs':10000}})
            with self.assertRaises(EditError):store.create_day('2026-10-01',content(),{})
